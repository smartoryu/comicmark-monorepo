package httpx

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWriteJSONSetsTheContentTypeAndStatus(t *testing.T) {
	w := httptest.NewRecorder()
	WriteJSON(w, newRequest(http.MethodGet, "/", nil), http.StatusCreated, map[string]string{"id": "abc"})

	assert.Equal(t, http.StatusCreated, w.Code)
	assert.Equal(t, "application/json; charset=utf-8", w.Header().Get("Content-Type"))
	assert.JSONEq(t, `{"id":"abc"}`, w.Body.String())
}

func TestWriteErrorUsesTheSingleEnvelope(t *testing.T) {
	r := newRequest(http.MethodGet, "/", nil)
	r.Header.Set(HeaderRequestID, "trace-1")

	w := httptest.NewRecorder()
	// Run through the middleware that populates the context, which is where the
	// error envelope reads the id from.
	RequestID()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		WriteError(w, r, http.StatusConflict, CodeConflict, MsgConflict)
	})).ServeHTTP(w, r)

	assert.Equal(t, http.StatusConflict, w.Code)

	env := decodeEnvelope(t, w)
	assert.Equal(t, CodeConflict, env.Error.Code)
	assert.Equal(t, MsgConflict, env.Error.Message)
	assert.Equal(t, "trace-1", env.Error.RequestID,
		"the request id is what lets a user quote something searchable")
	assert.Nil(t, env.Error.Details)
}

func TestWriteErrorIncludesDetailsWhenGiven(t *testing.T) {
	w := httptest.NewRecorder()
	WriteError(w, newRequest(http.MethodPost, "/", nil), http.StatusConflict, "chapter_not_numeric",
		"That chapter has no number.", map[string]any{"chapter": "Prologue"})

	env := decodeEnvelope(t, w)
	require.NotNil(t, env.Error.Details)
	assert.Equal(t, "Prologue", env.Error.Details["chapter"])
}

func TestWriteFieldErrorsListsEveryOffendingField(t *testing.T) {
	w := httptest.NewRecorder()
	WriteFieldErrors(w, newRequest(http.MethodPost, "/", nil), map[string]string{
		"email":    "Enter a valid email address.",
		"password": "Use at least 8 characters.",
	})

	assert.Equal(t, http.StatusBadRequest, w.Code)

	env := decodeEnvelope(t, w)
	assert.Equal(t, CodeValidation, env.Error.Code)
	require.Len(t, env.Error.Details, 2)
	assert.Equal(t, "Use at least 8 characters.", env.Error.Details["password"])
}

func TestWriteFieldErrorReportsASingleField(t *testing.T) {
	w := httptest.NewRecorder()
	WriteFieldError(w, newRequest(http.MethodPost, "/", nil), "device_label", "Give this device a name.")

	env := decodeEnvelope(t, w)
	require.Len(t, env.Error.Details, 1)
	assert.Equal(t, "Give this device a name.", env.Error.Details["device_label"])
}

func TestNoContentWritesABareTwoOhFour(t *testing.T) {
	w := httptest.NewRecorder()
	NoContent(w)

	assert.Equal(t, http.StatusNoContent, w.Code)
	assert.Empty(t, w.Body.String())
}

// --- decoding ---------------------------------------------------------------

type decodeTarget struct {
	Name string `json:"name"`
}

func TestDecodeJSONReadsASingleObject(t *testing.T) {
	r := newRequest(http.MethodPost, "/", strings.NewReader(`{"name":"Risf"}`))

	var dst decodeTarget
	require.NoError(t, DecodeJSON(httptest.NewRecorder(), r, &dst))
	assert.Equal(t, "Risf", dst.Name)
}

func TestDecodeJSONRejectsUnknownFields(t *testing.T) {
	// A typo in a client payload should fail loudly rather than be ignored.
	r := newRequest(http.MethodPost, "/", strings.NewReader(`{"nmae":"Risf"}`))

	var dst decodeTarget
	err := DecodeJSON(httptest.NewRecorder(), r, &dst)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nmae")
}

func TestDecodeJSONRejectsTrailingContent(t *testing.T) {
	r := newRequest(http.MethodPost, "/", strings.NewReader(`{"name":"Risf"}{"name":"again"}`))

	var dst decodeTarget
	err := DecodeJSON(httptest.NewRecorder(), r, &dst)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "single JSON object")
}

func TestDecodeJSONRejectsAnOversizedBody(t *testing.T) {
	// A single long string value pushes the body past the limit.
	body := `{"name":"` + strings.Repeat("x", MaxBodyBytes) + `"}`
	r := newRequest(http.MethodPost, "/", strings.NewReader(body))

	var dst decodeTarget
	err := DecodeJSON(httptest.NewRecorder(), r, &dst)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exceeds")
}

func TestDecodeJSONOrFailWritesTheStandardErrorResponse(t *testing.T) {
	r := newRequest(http.MethodPost, "/", strings.NewReader("not json"))

	var dst decodeTarget
	w := httptest.NewRecorder()

	require.False(t, DecodeJSONOrFail(w, r, &dst), "the caller must not continue")

	assert.Equal(t, http.StatusBadRequest, w.Code)
	env := decodeEnvelope(t, w)
	assert.Equal(t, CodeBadRequest, env.Error.Code)
	assert.Equal(t, MsgInvalidPayload, env.Error.Message)
}

func TestDecodeJSONOrFailReportsSuccess(t *testing.T) {
	r := newRequest(http.MethodPost, "/", strings.NewReader(`{"name":"Risf"}`))

	var dst decodeTarget
	require.True(t, DecodeJSONOrFail(httptest.NewRecorder(), r, &dst))
	assert.Equal(t, "Risf", dst.Name)
}

func TestErrorEnvelopeRoundTripsAsOneParserShape(t *testing.T) {
	// The whole point of a single envelope is that a client needs one parser.
	raw := `{"error":{"code":"unauthorized","message":"Sign in to continue.","request_id":"abc"}}`

	var env ErrorEnvelope
	require.NoError(t, json.Unmarshal([]byte(raw), &env))
	assert.Equal(t, CodeUnauthorized, env.Error.Code)
	assert.Equal(t, "abc", env.Error.RequestID)
}
