package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"comicmark/go-modular/internal/httpx"
	"comicmark/go-modular/internal/pkg/jwt"
)

// --- helpers ----------------------------------------------------------------

// capturedCaller records what the pipeline saw once it was let through.
type capturedCaller struct {
	principal *Principal
	ok        bool
	called    bool
}

// protectedHandler wraps RequireAuth around a handler that reports the caller it
// was given. It returns the middleware and the capture, so a test can assert on
// both the response and what the downstream handler observed.
func protectedHandler(svc *testService) (http.Handler, *capturedCaller) {
	caller := &capturedCaller{}

	run := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		caller.called = true
		caller.principal, caller.ok = PrincipalFrom(r.Context())
		w.WriteHeader(http.StatusOK)
	})

	return svc.RequireAuth(run), caller
}

// callAuth issues a request through the middleware and returns the recorder.
func callAuth(h http.Handler, headers map[string]string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodGet, "/api/v1/auth/me", nil)
	r.RemoteAddr = "203.0.113.7:12345"
	for k, v := range headers {
		r.Header.Set(k, v)
	}

	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func errorCode(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()

	var env httpx.ErrorEnvelope
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &env), "body: %s", w.Body.String())
	return env.Error.Code
}

// --- the happy path ---------------------------------------------------------

func TestRequireAuthPassesTheCallerDownstream(t *testing.T) {
	svc := newTestService(t)
	session := registerOnDevice(t, svc)

	h, caller := protectedHandler(svc)
	w := callAuth(h, map[string]string{"Authorization": "Bearer " + session.Tokens.AccessToken})

	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
	require.True(t, caller.called, "the wrapped handler should have run")
	require.True(t, caller.ok, "the caller should be reachable from the context")

	// Both identifiers are what downstream code scopes data by: the user id for
	// ownership, and the session id so the device making the request can be
	// told apart from the others.
	assert.Equal(t, session.User.ID, caller.principal.UserID)
	assert.Equal(t, session.Tokens.SessionID, caller.principal.SessionID)
}

func TestRequireAuthAcceptsALowercaseBearerScheme(t *testing.T) {
	svc := newTestService(t)
	session := registerOnDevice(t, svc)

	h, caller := protectedHandler(svc)
	// RFC 7235 makes the scheme case-insensitive.
	w := callAuth(h, map[string]string{"Authorization": "bearer " + session.Tokens.AccessToken})

	require.Equal(t, http.StatusOK, w.Code)
	require.True(t, caller.ok)
}

// --- rejected credentials ---------------------------------------------------

func TestRequireAuthRejectsMalformedHeaders(t *testing.T) {
	svc := newTestService(t)
	h, caller := protectedHandler(svc)

	for name, headers := range map[string]map[string]string{
		"absent":       nil,
		"empty":        {"Authorization": ""},
		"scheme only":  {"Authorization": "Bearer"},
		"empty token":  {"Authorization": "Bearer "},
		"wrong scheme": {"Authorization": "Basic dXNlcjpwYXNz"},
		"raw token":    {"Authorization": "abc.def.ghi"},
	} {
		t.Run(name, func(t *testing.T) {
			w := callAuth(h, headers)

			assert.Equal(t, http.StatusUnauthorized, w.Code)
			assert.False(t, caller.called, "the wrapped handler must not run")
		})
	}
}

func TestRequireAuthRejectsBadTokens(t *testing.T) {
	svc := newTestService(t)
	session := registerOnDevice(t, svc)
	h, caller := protectedHandler(svc)

	t.Run("garbage", func(t *testing.T) {
		w := callAuth(h, map[string]string{"Authorization": "Bearer not-a-jwt"})
		assert.Equal(t, http.StatusUnauthorized, w.Code)
		assert.False(t, caller.called)
	})

	// A refresh token is the credential a client stores longest, so it must not
	// double as an access token.
	t.Run("a refresh token", func(t *testing.T) {
		w := callAuth(h, map[string]string{"Authorization": "Bearer " + session.Tokens.RefreshToken})
		assert.Equal(t, http.StatusUnauthorized, w.Code)
		assert.False(t, caller.called)
	})

	t.Run("a token signed with another secret", func(t *testing.T) {
		other, err := jwt.NewManager([]byte("a-different-secret-that-is-32-bytes"), "comicmark", time.Minute, time.Hour)
		require.NoError(t, err)

		forged, _, err := other.SignAccess(session.User.ID, session.Tokens.SessionID)
		require.NoError(t, err)

		w := callAuth(h, map[string]string{"Authorization": "Bearer " + forged})
		assert.Equal(t, http.StatusUnauthorized, w.Code)
		assert.False(t, caller.called)
	})

	t.Run("a token for a session that does not exist", func(t *testing.T) {
		orphan, _, err := svc.jwt.SignAccess(session.User.ID, "no-such-session")
		require.NoError(t, err)

		w := callAuth(h, map[string]string{"Authorization": "Bearer " + orphan})
		assert.Equal(t, http.StatusUnauthorized, w.Code)
		assert.False(t, caller.called)
	})

	t.Run("a token whose user does not own the session", func(t *testing.T) {
		// A token that names a real session but the wrong subject must not pass.
		mismatch, _, err := svc.jwt.SignAccess("someone-else", session.Tokens.SessionID)
		require.NoError(t, err)

		w := callAuth(h, map[string]string{"Authorization": "Bearer " + mismatch})
		assert.Equal(t, http.StatusUnauthorized, w.Code)
		assert.False(t, caller.called)
	})
}

// A revoked device must be refused at once, not when its access token happens to
// expire. This is the property that makes the sessions screen trustworthy.
func TestRequireAuthRejectsARevokedSessionImmediately(t *testing.T) {
	svc := newTestService(t)
	session := registerOnDevice(t, svc)
	h, caller := protectedHandler(svc)

	require.NoError(t, svc.repo.RevokeSession(context.Background(), session.Tokens.SessionID, time.Now()))

	w := callAuth(h, map[string]string{"Authorization": "Bearer " + session.Tokens.AccessToken})

	assert.Equal(t, http.StatusUnauthorized, w.Code)
	assert.Equal(t, "session_revoked", errorCode(t, w))
	assert.False(t, caller.called)
}

func TestRequireAuthRevokesAnIdleSession(t *testing.T) {
	svc := newTestServiceWith(t, Options{SessionIdleTTL: time.Hour})
	session := registerOnDevice(t, svc)
	h, caller := protectedHandler(svc)

	// Move beyond the idle window.
	svc.now = func() time.Time { return time.Now().Add(2 * time.Hour) }

	w := callAuth(h, map[string]string{"Authorization": "Bearer " + session.Tokens.AccessToken})

	assert.Equal(t, http.StatusUnauthorized, w.Code)
	assert.Equal(t, "session_revoked", errorCode(t, w))
	assert.False(t, caller.called)

	// The refusal is not merely cosmetic: the session is now really revoked.
	stored, err := svc.repo.GetSession(context.Background(), session.Tokens.SessionID)
	require.NoError(t, err)
	assert.False(t, stored.Active())
}

// --- context plumbing -------------------------------------------------------

// The context key is unexported, so nothing outside this package can present
// itself as an already-authenticated caller.
func TestPrincipalFromIsEmptyWithoutTheMiddleware(t *testing.T) {
	principal, ok := PrincipalFrom(context.Background())

	assert.False(t, ok)
	assert.Nil(t, principal)
}

func TestRequireAuthReplacesAPreExistingPrincipal(t *testing.T) {
	svc := newTestService(t)
	session := registerOnDevice(t, svc)

	var seen *Principal
	inner := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		seen, _ = PrincipalFrom(r.Context())
	})

	// A request that arrives with a context of its own still ends up carrying
	// the authenticated principal, not whatever it started with.
	handler := svc.RequireAuth(inner)
	r := httptest.NewRequest(http.MethodGet, "/api/v1/auth/me", nil)
	r = r.WithContext(context.WithValue(r.Context(), "some-other-key", "ignored"))
	r.Header.Set("Authorization", "Bearer "+session.Tokens.AccessToken)

	handler.ServeHTTP(httptest.NewRecorder(), r)

	require.NotNil(t, seen)
	assert.Equal(t, session.User.ID, seen.UserID)
	assert.Equal(t, session.Tokens.SessionID, seen.SessionID)
}

// A revoked session is reported with a code the client can branch on, so the app
// can sign the user out rather than retrying.
func TestAuthFailuresUseDistinguishableCodes(t *testing.T) {
	svc := newTestService(t)
	session := registerOnDevice(t, svc)
	h, _ := protectedHandler(svc)

	missing := callAuth(h, nil)
	assert.Equal(t, httpx.CodeUnauthorized, errorCode(t, missing))

	garbage := callAuth(h, map[string]string{"Authorization": "Bearer nonsense"})
	assert.Equal(t, httpx.CodeUnauthorized, errorCode(t, garbage))

	require.NoError(t, svc.repo.RevokeSession(context.Background(), session.Tokens.SessionID, time.Now()))
	revoked := callAuth(h, map[string]string{"Authorization": "Bearer " + session.Tokens.AccessToken})
	assert.Equal(t, "session_revoked", errorCode(t, revoked))
}

// --- bearerToken ------------------------------------------------------------

func TestBearerTokenParsing(t *testing.T) {
	cases := map[string]struct {
		header string
		want   string
		ok     bool
	}{
		"standard":     {"Bearer abc", "abc", true},
		"lowercase":    {"bearer abc", "abc", true},
		"mixed case":   {"BeArEr abc", "abc", true},
		"padded":       {"Bearer   abc  ", "abc", true},
		"empty":        {"", "", false},
		"scheme only":  {"Bearer", "", false},
		"blank token":  {"Bearer   ", "", false},
		"other scheme": {"Token abc", "", false},
		"basic auth":   {"Basic abc", "", false},
		"no scheme":    {"abc", "", false},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			if tc.header != "" {
				r.Header.Set("Authorization", tc.header)
			}

			got, ok := bearerToken(r)
			assert.Equal(t, tc.ok, ok)
			assert.Equal(t, tc.want, got)
		})
	}
}

// A token containing spaces would be a malformed credential; it must not be
// trimmed into something that looks valid.
func TestBearerTokenKeepsTheTokenIntact(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("Authorization", "Bearer token.with.dots-and_base64url~chars")

	got, ok := bearerToken(r)
	require.True(t, ok)
	assert.Equal(t, "token.with.dots-and_base64url~chars", got)
	assert.False(t, strings.Contains(got, " "))
}

// registerOnDevice registers an account and returns the resulting session, which
// is what the middleware tests need.
func registerOnDevice(t *testing.T, svc *testService) *AuthResult {
	t.Helper()

	result, err := svc.Register(context.Background(), registerInput("reader@example.com", "correct horse battery"))
	require.NoError(t, err)
	return result
}
