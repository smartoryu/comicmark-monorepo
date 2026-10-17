package httpx

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.DiscardHandler)
}

// newRequest builds a request with a real RemoteAddr so rate limiting has
// something to key on.
func newRequest(method, target string, body io.Reader) *http.Request {
	r := httptest.NewRequest(method, target, body)
	r.RemoteAddr = "203.0.113.7:54321"
	return r
}

// serve runs a handler through the middleware under test.
func serve(t *testing.T, h http.Handler, r *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func decodeEnvelope(t *testing.T, w *httptest.ResponseRecorder) ErrorEnvelope {
	t.Helper()
	var env ErrorEnvelope
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &env), "body: %s", w.Body.String())
	return env
}

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
}

// --- RequestID --------------------------------------------------------------

func TestRequestIDGeneratesWhenAbsent(t *testing.T) {
	w := serve(t, RequestID()(okHandler()), newRequest(http.MethodGet, "/", nil))

	assert.NotEmpty(t, w.Header().Get(HeaderRequestID),
		"every response should carry a correlation id")
}

func TestRequestIDEchoesAClientSuppliedID(t *testing.T) {
	r := newRequest(http.MethodGet, "/", nil)
	r.Header.Set(HeaderRequestID, "client-supplied-id")

	w := serve(t, RequestID()(okHandler()), r)

	assert.Equal(t, "client-supplied-id", w.Header().Get(HeaderRequestID),
		"a supplied id is what lets a user quote something we can find in the log")
}

func TestRequestIDReplacesAnAbsurdlyLongValue(t *testing.T) {
	r := newRequest(http.MethodGet, "/", nil)
	r.Header.Set(HeaderRequestID, strings.Repeat("x", 500))

	w := serve(t, RequestID()(okHandler()), r)

	id := w.Header().Get(HeaderRequestID)
	require.NotEmpty(t, id)
	assert.LessOrEqual(t, len(id), maxRequestIDLength)
	assert.NotEqual(t, strings.Repeat("x", 500), id)
}

func TestRequestIDReachesTheHandlerContext(t *testing.T) {
	var seen string
	h := RequestID()(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		seen = RequestIDFromContext(r.Context())
	}))

	r := newRequest(http.MethodGet, "/", nil)
	r.Header.Set(HeaderRequestID, "abc-123")
	w := serve(t, h, r)

	assert.Equal(t, "abc-123", seen)
	assert.Equal(t, "abc-123", w.Header().Get(HeaderRequestID))
}

func TestRequestIDFromContextIsEmptyOutsideARequest(t *testing.T) {
	assert.Empty(t, RequestIDFromContext(t.Context()))
}

// --- Recover ----------------------------------------------------------------

func TestRecoverTurnsAPanicIntoAnEnvelopedFiveHundred(t *testing.T) {
	h := Recover(discardLogger())(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom")
	}))

	w := serve(t, RequestID()(h), newRequest(http.MethodGet, "/", nil))

	assert.Equal(t, http.StatusInternalServerError, w.Code)
	env := decodeEnvelope(t, w)
	assert.Equal(t, CodeInternal, env.Error.Code)
	assert.NotContains(t, env.Error.Message, "boom", "the panic message must not leak to the client")
	assert.NotEmpty(t, env.Error.RequestID, "the id lets an operator find this in the log")
}

func TestRecoverLeavesNormalResponsesAlone(t *testing.T) {
	h := Recover(discardLogger())(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	}))

	w := serve(t, h, newRequest(http.MethodGet, "/", nil))
	assert.Equal(t, http.StatusTeapot, w.Code)
}

// --- AccessLog --------------------------------------------------------------

func TestAccessLogRecordsStatusAndSize(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))

	h := AccessLog(logger)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte("hello"))
	}))

	serve(t, h, newRequest(http.MethodPost, "/api/v1/bookmarks", nil))

	out := buf.String()
	assert.Contains(t, out, "status=201")
	assert.Contains(t, out, "bytes=5")
	assert.Contains(t, out, "path=/api/v1/bookmarks")
	assert.Contains(t, out, "method=POST")
}

func TestAccessLogReportsTheImplicitOKStatus(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))

	h := AccessLog(logger)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// A handler that only writes a body leaves the status implicit.
		_, _ = w.Write([]byte("ok"))
	}))

	w := serve(t, h, newRequest(http.MethodGet, "/", nil))

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, buf.String(), "status=200")
}

// --- CORS -------------------------------------------------------------------

func TestCORSAllowsAConfiguredOrigin(t *testing.T) {
	h := CORS([]string{"http://localhost:5173"})(okHandler())

	r := newRequest(http.MethodGet, "/api/v1/bookmarks", nil)
	r.Header.Set("Origin", "http://localhost:5173")
	w := serve(t, h, r)

	assert.Equal(t, "http://localhost:5173", w.Header().Get("Access-Control-Allow-Origin"))
	assert.Equal(t, "true", w.Header().Get("Access-Control-Allow-Credentials"))
	assert.Contains(t, w.Header().Values("Vary"), "Origin")
}

func TestCORSOmitsAllowHeadersForAnUnknownOrigin(t *testing.T) {
	h := CORS([]string{"http://localhost:5173"})(okHandler())

	r := newRequest(http.MethodGet, "/api/v1/bookmarks", nil)
	r.Header.Set("Origin", "https://evil.example")
	w := serve(t, h, r)

	assert.Empty(t, w.Header().Get("Access-Control-Allow-Origin"),
		"the browser must not be told it may read this response")
	assert.Equal(t, http.StatusOK, w.Code, "CORS is a browser concern; the handler still runs")
}

func TestCORSAnswersPreflight(t *testing.T) {
	h := CORS([]string{"http://localhost:5173"})(okHandler())

	r := newRequest(http.MethodOptions, "/api/v1/bookmarks", nil)
	r.Header.Set("Origin", "http://localhost:5173")
	r.Header.Set("Access-Control-Request-Method", "PATCH")
	r.Header.Set("Access-Control-Request-Headers", "authorization, content-type")
	w := serve(t, h, r)

	assert.Equal(t, http.StatusNoContent, w.Code)
	allowed := w.Header().Get("Access-Control-Allow-Headers")
	for _, header := range []string{"Authorization", "Content-Type", HeaderClientPlatform, HeaderRequestID} {
		assert.Contains(t, allowed, header)
	}
	assert.Contains(t, w.Header().Get("Access-Control-Allow-Methods"), http.MethodPatch)
	assert.Contains(t, w.Header().Values("Vary"), "Access-Control-Request-Headers")
}

func TestCORSRejectsPreflightFromAnUnknownOrigin(t *testing.T) {
	h := CORS([]string{"http://localhost:5173"})(okHandler())

	r := newRequest(http.MethodOptions, "/api/v1/bookmarks", nil)
	r.Header.Set("Origin", "https://evil.example")
	w := serve(t, h, r)

	assert.Equal(t, http.StatusForbidden, w.Code)
	assert.Empty(t, w.Header().Get("Access-Control-Allow-Origin"))
}

// --- rate limiting ----------------------------------------------------------

func TestRateLimiterAllowsUpToBurstThenRejects(t *testing.T) {
	rl := NewRateLimiter(1, 2)
	t.Cleanup(rl.Stop)

	h := rl.Middleware()(okHandler())

	assert.Equal(t, http.StatusOK, serve(t, h, newRequest(http.MethodGet, "/", nil)).Code)
	assert.Equal(t, http.StatusOK, serve(t, h, newRequest(http.MethodGet, "/", nil)).Code)

	w := serve(t, h, newRequest(http.MethodGet, "/", nil))
	assert.Equal(t, http.StatusTooManyRequests, w.Code)
	assert.Equal(t, "1", w.Header().Get("Retry-After"))
	assert.Equal(t, CodeRateLimited, decodeEnvelope(t, w).Error.Code)
}

func TestRateLimiterIsIndependentPerClient(t *testing.T) {
	rl := NewRateLimiter(1, 1)
	t.Cleanup(rl.Stop)
	h := rl.Middleware()(okHandler())

	first := newRequest(http.MethodGet, "/", nil)
	first.RemoteAddr = "203.0.113.7:1000"
	assert.Equal(t, http.StatusOK, serve(t, h, first).Code)

	again := newRequest(http.MethodGet, "/", nil)
	again.RemoteAddr = "203.0.113.7:2000"
	assert.Equal(t, http.StatusTooManyRequests, serve(t, h, again).Code,
		"a different source port is still the same client")

	other := newRequest(http.MethodGet, "/", nil)
	other.RemoteAddr = "198.51.100.9:3000"
	assert.Equal(t, http.StatusOK, serve(t, h, other).Code)
}

// Honouring a spoofable header here would let any client mint a fresh identity
// per request and bypass every limit.
func TestRateLimiterIgnoresForwardedForHeaders(t *testing.T) {
	rl := NewRateLimiter(1, 1)
	t.Cleanup(rl.Stop)
	h := rl.Middleware()(okHandler())

	first := newRequest(http.MethodGet, "/", nil)
	first.Header.Set("X-Forwarded-For", "1.1.1.1")
	assert.Equal(t, http.StatusOK, serve(t, h, first).Code)

	second := newRequest(http.MethodGet, "/", nil)
	second.Header.Set("X-Forwarded-For", "2.2.2.2")
	assert.Equal(t, http.StatusTooManyRequests, serve(t, h, second).Code,
		"a spoofed X-Forwarded-For must not create a new bucket")
}

func TestPerMinuteLimiterRejectsTheFourthRequest(t *testing.T) {
	rl := NewPerMinuteLimiter(3, 3)
	t.Cleanup(rl.Stop)

	for i := range 3 {
		require.True(t, rl.Allow("client"), "request %d should be allowed", i+1)
	}
	assert.False(t, rl.Allow("client"), "the fourth request should be limited")
}

func TestRateLimiterForgetsIdleVisitors(t *testing.T) {
	rl := NewRateLimiter(1, 1)
	t.Cleanup(rl.Stop)

	// Drive the clock forward so a collection pass sees the visitor as idle.
	base := time.Now()
	rl.now = func() time.Time { return base }
	rl.Allow("client")

	rl.mu.Lock()
	require.Len(t, rl.visitors, 1)
	rl.mu.Unlock()

	rl.now = func() time.Time { return base.Add(visitorTTL + time.Minute) }
	rl.collectOnce()

	rl.mu.Lock()
	assert.Empty(t, rl.visitors, "idle buckets must be released")
	rl.mu.Unlock()
}

func TestRateLimiterStopIsIdempotent(t *testing.T) {
	rl := NewRateLimiter(1, 1)
	rl.Stop()
	rl.Stop()
}

// --- ClientIP ---------------------------------------------------------------

func TestClientIPUsesTheConnectionAddress(t *testing.T) {
	r := newRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "198.51.100.9:41234"
	assert.Equal(t, "198.51.100.9", ClientIP(r))

	// A request with no port (as in some tests) is returned unchanged.
	r.RemoteAddr = "198.51.100.9"
	assert.Equal(t, "198.51.100.9", ClientIP(r))
}

// --- Chain ------------------------------------------------------------------

func TestChainAppliesMiddlewareOutsideIn(t *testing.T) {
	var order []string

	mark := func(name string) Middleware {
		return func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				order = append(order, name)
				next.ServeHTTP(w, r)
			})
		}
	}

	h := Chain(okHandler(), mark("first"), mark("second"), mark("third"))
	serve(t, h, newRequest(http.MethodGet, "/", nil))

	assert.Equal(t, []string{"first", "second", "third"}, order)
}
