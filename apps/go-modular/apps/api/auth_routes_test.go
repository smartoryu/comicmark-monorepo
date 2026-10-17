package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"comicmark/go-modular/internal/httpx"
	"comicmark/go-modular/internal/modules/auth"
)

// These tests drive the real router with real HTTP requests, so they cover the
// route table, the middleware chain, status codes, the error envelope and the
// use cases together.

// --- request helpers --------------------------------------------------------

// call issues a request against the server's handler and returns the recorder.
func call(t *testing.T, srv *apiServer, method, path string, body any, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()

	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		require.NoError(t, err)
		reader = bytes.NewReader(encoded)
	}

	r := newRequest(method, path, reader)
	if body != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		r.Header.Set(k, v)
	}

	w := httptest.NewRecorder()
	srv.handler().ServeHTTP(w, r)
	return w
}

// callJSON issues a request and decodes the JSON response into out.
func callJSON(t *testing.T, srv *apiServer, method, path string, body any, out any, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()

	w := call(t, srv, method, path, body, headers)
	if out != nil && w.Body.Len() > 0 {
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), out), "body: %s", w.Body.String())
	}
	return w
}

func bearer(token string) map[string]string {
	return map[string]string{"Authorization": "Bearer " + token}
}

// authEnvelope mirrors the response shape the API documents.
type authEnvelope struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
	SessionID    string `json:"session_id"`
	User         struct {
		ID            string `json:"id"`
		Email         string `json:"email"`
		DisplayName   string `json:"display_name"`
		EmailVerified bool   `json:"email_verified"`
	} `json:"user"`
}

// registerAccount registers a user and returns the decoded response.
func registerAccount(t *testing.T, srv *apiServer, email, password string) authEnvelope {
	t.Helper()

	var out authEnvelope
	w := callJSON(t, srv, http.MethodPost, "/api/v1/auth/register", map[string]string{
		"email": email, "password": password, "display_name": "Risf",
	}, &out, nil)
	require.Equal(t, http.StatusCreated, w.Code, "body: %s", w.Body.String())
	return out
}

// --- registration and sign-in ----------------------------------------------

func TestRegisterReturnsTokensAndAUser(t *testing.T) {
	srv := newTestServer(t, testMailer(t))

	var out authEnvelope
	w := callJSON(t, srv, http.MethodPost, "/api/v1/auth/register", map[string]string{
		"email": "reader@example.com", "password": "correct horse battery", "display_name": "Risf",
	}, &out, map[string]string{"X-Client-Platform": "web"})

	require.Equal(t, http.StatusCreated, w.Code, "body: %s", w.Body.String())
	assert.NotEmpty(t, out.AccessToken)
	assert.NotEmpty(t, out.RefreshToken)
	assert.NotEmpty(t, out.SessionID)
	assert.Equal(t, "Bearer", out.TokenType)
	assert.Positive(t, out.ExpiresIn)
	assert.Equal(t, "reader@example.com", out.User.Email)
	assert.Equal(t, "Risf", out.User.DisplayName)
	assert.False(t, out.User.EmailVerified, "a new account starts unverified")
	assert.NotContains(t, w.Body.String(), "password", "the response must never echo credentials")
}

func TestRegisterRejectsInvalidInputWithFieldErrors(t *testing.T) {
	srv := newTestServer(t, testMailer(t))

	cases := map[string]struct {
		body   map[string]string
		field  string
		status int
	}{
		"bad email": {
			body:   map[string]string{"email": "not-an-email", "password": "correct horse battery"},
			field:  "email",
			status: http.StatusBadRequest,
		},
		"short password": {
			body:   map[string]string{"email": "reader@example.com", "password": "short"},
			field:  "password",
			status: http.StatusBadRequest,
		},
		"password equal to email": {
			body:   map[string]string{"email": "same@example.com", "password": "same@example.com"},
			field:  "password",
			status: http.StatusBadRequest,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			var env httpx.ErrorEnvelope
			w := callJSON(t, srv, http.MethodPost, "/api/v1/auth/register", tc.body, &env, nil)

			assert.Equal(t, tc.status, w.Code)
			assert.Equal(t, httpx.CodeValidation, env.Error.Code)
			require.NotNil(t, env.Error.Details)
			assert.Contains(t, env.Error.Details, tc.field)
			assert.NotEmpty(t, env.Error.RequestID)
		})
	}
}

func TestRegisterRejectsADuplicateEmail(t *testing.T) {
	srv := newTestServer(t, testMailer(t))
	registerAccount(t, srv, "reader@example.com", "correct horse battery")

	var env httpx.ErrorEnvelope
	w := callJSON(t, srv, http.MethodPost, "/api/v1/auth/register", map[string]string{
		"email": "READER@example.com", "password": "another password",
	}, &env, nil)

	assert.Equal(t, http.StatusConflict, w.Code)
	assert.Equal(t, "email_taken", env.Error.Code)
}

func TestRegisterRejectsUnknownFields(t *testing.T) {
	srv := newTestServer(t, testMailer(t))

	w := call(t, srv, http.MethodPost, "/api/v1/auth/register", map[string]string{
		"email": "reader@example.com", "password": "correct horse battery", "is_admin": "true",
	}, nil)

	assert.Equal(t, http.StatusBadRequest, w.Code, "a typo or a guess must fail loudly")
}

func TestLoginAndMe(t *testing.T) {
	srv := newTestServer(t, testMailer(t))
	registered := registerAccount(t, srv, "reader@example.com", "correct horse battery")

	var login authEnvelope
	w := callJSON(t, srv, http.MethodPost, "/api/v1/auth/login", map[string]string{
		"email": "reader@example.com", "password": "correct horse battery",
	}, &login, nil)
	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
	require.Equal(t, registered.User.ID, login.User.ID)
	assert.NotEqual(t, registered.SessionID, login.SessionID,
		"signing in again is a new device session")

	var me struct {
		ID            string `json:"id"`
		Email         string `json:"email"`
		EmailVerified bool   `json:"email_verified"`
	}
	w = callJSON(t, srv, http.MethodGet, "/api/v1/auth/me", nil, &me, bearer(login.AccessToken))
	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
	assert.Equal(t, registered.User.ID, me.ID)
	assert.Equal(t, "reader@example.com", me.Email)
}

func TestLoginRejectsBadCredentialsWithoutRevealingWhich(t *testing.T) {
	srv := newTestServer(t, testMailer(t))
	registerAccount(t, srv, "reader@example.com", "correct horse battery")

	for name, body := range map[string]map[string]string{
		"wrong password": {"email": "reader@example.com", "password": "not the password"},
		"unknown email":  {"email": "nobody@example.com", "password": "correct horse battery"},
	} {
		t.Run(name, func(t *testing.T) {
			var env httpx.ErrorEnvelope
			w := callJSON(t, srv, http.MethodPost, "/api/v1/auth/login", body, &env, nil)

			assert.Equal(t, http.StatusUnauthorized, w.Code)
			assert.Equal(t, "invalid_credentials", env.Error.Code)
		})
	}
}

// --- authenticated access ---------------------------------------------------

func TestProtectedRoutesRejectMissingOrBrokenTokens(t *testing.T) {
	srv := newTestServer(t, testMailer(t))

	for name, headers := range map[string]map[string]string{
		"no header":     nil,
		"empty bearer":  {"Authorization": "Bearer "},
		"wrong scheme":  {"Authorization": "Basic abc"},
		"garbage token": {"Authorization": "Bearer not-a-jwt"},
		"scheme only":   {"Authorization": "Bearer"},
		"header junk":   {"Authorization": "Bearer aaa.bbb.ccc"},
	} {
		t.Run(name, func(t *testing.T) {
			w := call(t, srv, http.MethodGet, "/api/v1/auth/me", nil, headers)
			assert.Equal(t, http.StatusUnauthorized, w.Code)
		})
	}
}

func TestLogoutEndsTheSessionImmediately(t *testing.T) {
	srv := newTestServer(t, testMailer(t))
	session := registerAccount(t, srv, "reader@example.com", "correct horse battery")

	var env httpx.ErrorEnvelope
	w := callJSON(t, srv, http.MethodPost, "/api/v1/auth/logout",
		map[string]string{"refresh_token": session.RefreshToken}, &env, bearer(session.AccessToken))
	require.Equal(t, http.StatusNoContent, w.Code, "body: %s", w.Body.String())

	// The access token stops working at once, not when it happens to expire.
	w = call(t, srv, http.MethodGet, "/api/v1/auth/me", nil, bearer(session.AccessToken))
	assert.Equal(t, http.StatusUnauthorized, w.Code)
	assert.Equal(t, "session_revoked", mustEnvelope(t, w).Error.Code)
}

// --- refresh rotation -------------------------------------------------------

func TestRefreshRotatesAndDetectsReplay(t *testing.T) {
	srv := newTestServer(t, testMailer(t))
	session := registerAccount(t, srv, "reader@example.com", "correct horse battery")

	var rotated authEnvelope
	w := callJSON(t, srv, http.MethodPost, "/api/v1/auth/refresh",
		map[string]string{"refresh_token": session.RefreshToken}, &rotated, nil)
	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
	require.NotEqual(t, session.RefreshToken, rotated.RefreshToken)
	require.Equal(t, session.SessionID, rotated.SessionID)

	// Replaying the original is treated as a stolen credential.
	var env httpx.ErrorEnvelope
	w = callJSON(t, srv, http.MethodPost, "/api/v1/auth/refresh",
		map[string]string{"refresh_token": session.RefreshToken}, &env, nil)
	require.Equal(t, http.StatusUnauthorized, w.Code)
	assert.Equal(t, "token_reused", env.Error.Code)

	// The replayed-for family is dead too.
	w = call(t, srv, http.MethodPost, "/api/v1/auth/refresh",
		map[string]string{"refresh_token": rotated.RefreshToken}, nil)
	assert.Equal(t, http.StatusUnauthorized, w.Code,
		"both parties are signed out because there is no way to tell them apart")
}

func TestRefreshRejectsAnAccessToken(t *testing.T) {
	srv := newTestServer(t, testMailer(t))
	session := registerAccount(t, srv, "reader@example.com", "correct horse battery")

	w := call(t, srv, http.MethodPost, "/api/v1/auth/refresh",
		map[string]string{"refresh_token": session.AccessToken}, nil)

	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

// --- email verification -----------------------------------------------------

func TestEmailVerificationFlowThroughTheMailedLink(t *testing.T) {
	srv, rec := newAPIServerWithMailer(t)
	session := registerAccount(t, srv, "reader@example.com", "correct horse battery")

	// The registration email carries a link into the app, with the token as a
	// parameter. Keeping the address in the app origin means the emailed link
	// does not depend on the API host.
	link := rec.lastLink(t)
	assert.Equal(t, "localhost:5173", mustHost(t, link))
	token := queryParam(t, link, auth.QueryVerifyToken)

	// The app then validates the token, which is what actually marks the account
	// verified.
	var out struct {
		EmailVerified bool `json:"email_verified"`
	}
	var env httpx.ErrorEnvelope
	w := callJSON(t, srv, http.MethodPost, "/api/v1/auth/verification/email/validate",
		map[string]string{"token": token}, &out, nil)
	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
	assert.True(t, out.EmailVerified)

	var me struct {
		EmailVerified bool `json:"email_verified"`
	}
	w = callJSON(t, srv, http.MethodGet, "/api/v1/auth/me", nil, &me, bearer(session.AccessToken))
	require.Equal(t, http.StatusOK, w.Code)
	assert.True(t, me.EmailVerified)

	// The token cannot be reused.
	w = callJSON(t, srv, http.MethodPost, "/api/v1/auth/verification/email/validate",
		map[string]string{"token": token}, &env, nil)
	assert.Equal(t, http.StatusUnauthorized, w.Code)
	assert.Equal(t, "token_invalid", env.Error.Code)
}

// A client that would rather not handle the token itself can send the browser
// straight at the API, which validates and then redirects back into the app.
func TestEmailVerificationByLinkRedirectsIntoTheApp(t *testing.T) {
	srv, rec := newAPIServerWithMailer(t)
	registerAccount(t, srv, "reader@example.com", "correct horse battery")

	token := queryParam(t, rec.lastLink(t), auth.QueryVerifyToken)

	w := get(t, srv.handler(), "/api/v1/auth/verification/email/verify?token="+token)
	require.Equal(t, http.StatusFound, w.Code, "body: %s", w.Body.String())

	location := w.Header().Get("Location")
	assert.Equal(t, "1", queryParam(t, location, auth.QueryVerified))
	assert.Equal(t, "no-referrer", w.Header().Get("Referrer-Policy"),
		"the token must not leak onward through a Referer header")

	// The account is verified.
	var me struct {
		EmailVerified bool `json:"email_verified"`
	}
	session := registeredSession(t, srv)
	w = callJSON(t, srv, http.MethodGet, "/api/v1/auth/me", nil, &me, bearer(session))
	require.Equal(t, http.StatusOK, w.Code)
	assert.True(t, me.EmailVerified)
}

func TestEmailVerificationLinkRejectsAnInvalidToken(t *testing.T) {
	srv := newTestServer(t, testMailer(t))

	// A bad link still redirects into the app, so the user sees an explanation
	// instead of a raw API error page.
	w := get(t, srv.handler(), "/api/v1/auth/verification/email/verify?token=nonsense")
	require.Equal(t, http.StatusFound, w.Code)
	assert.Equal(t, "0", queryParam(t, w.Header().Get("Location"), auth.QueryVerified))
}

func TestEmailVerificationLinkRejectsAReusedToken(t *testing.T) {
	srv, rec := newAPIServerWithMailer(t)
	registerAccount(t, srv, "reader@example.com", "correct horse battery")
	token := queryParam(t, rec.lastLink(t), auth.QueryVerifyToken)
	target := "/api/v1/auth/verification/email/verify?token=" + token

	require.Equal(t, http.StatusFound, get(t, srv.handler(), target).Code)

	// Clicking the same link twice is a normal user action; the second attempt
	// must not report success.
	second := get(t, srv.handler(), target)
	require.Equal(t, http.StatusFound, second.Code)
	assert.Equal(t, "0", queryParam(t, second.Header().Get("Location"), auth.QueryVerified))
}

func TestInitiateVerificationAlwaysAcceptsAndRevealsNothing(t *testing.T) {
	srv, rec := newAPIServerWithMailer(t)
	registerAccount(t, srv, "reader@example.com", "correct horse battery")
	before := len(rec.all())

	for name, email := range map[string]string{
		"registered and unverified": "reader@example.com",
		"unknown address":           "nobody@example.com",
	} {
		t.Run(name, func(t *testing.T) {
			var body map[string]string
			w := callJSON(t, srv, http.MethodPost, "/api/v1/auth/verification/email/initiate",
				map[string]string{"email": email}, &body, nil)

			assert.Equal(t, http.StatusAccepted, w.Code, "the status must not depend on the address")
			assert.NotContains(t, strings.ToLower(w.Body.String()), "no such")
		})
	}

	// Only the real address produced mail.
	assert.Greater(t, len(rec.all()), before)
}

// --- password reset ---------------------------------------------------------

func TestPasswordResetFlowThroughTheMailedLink(t *testing.T) {
	srv, rec := newAPIServerWithMailer(t)
	session := registerAccount(t, srv, "reader@example.com", "correct horse battery")

	var body map[string]string
	w := callJSON(t, srv, http.MethodPost, "/api/v1/auth/password/forgot",
		map[string]string{"email": "reader@example.com"}, &body, nil)
	require.Equal(t, http.StatusAccepted, w.Code, "body: %s", w.Body.String())

	token := queryParam(t, rec.lastLink(t), auth.QueryResetToken)
	require.NotEmpty(t, token)

	// A weak new password is rejected without burning the link.
	var env httpx.ErrorEnvelope
	w = callJSON(t, srv, http.MethodPost, "/api/v1/auth/password/reset",
		map[string]string{"token": token, "new_password": "short"}, &env, nil)
	require.Equal(t, http.StatusBadRequest, w.Code)
	require.NotNil(t, env.Error.Details)
	assert.Contains(t, env.Error.Details, "new_password")

	// A valid one succeeds.
	w = call(t, srv, http.MethodPost, "/api/v1/auth/password/reset",
		map[string]string{"token": token, "new_password": "a brand new password"}, nil)
	require.Equal(t, http.StatusNoContent, w.Code, "body: %s", w.Body.String())

	// Every pre-existing session is dead.
	w = call(t, srv, http.MethodGet, "/api/v1/auth/me", nil, bearer(session.AccessToken))
	assert.Equal(t, http.StatusUnauthorized, w.Code, "a reset must sign every device out")

	// The new password works.
	w = call(t, srv, http.MethodPost, "/api/v1/auth/login",
		map[string]string{"email": "reader@example.com", "password": "a brand new password"}, nil)
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestForgotPasswordHidesWhetherTheAddressExists(t *testing.T) {
	srv, rec := newAPIServerWithMailer(t)

	for _, email := range []string{"nobody@example.com", "also-nobody@example.com"} {
		var body map[string]string
		w := callJSON(t, srv, http.MethodPost, "/api/v1/auth/password/forgot",
			map[string]string{"email": email}, &body, nil)
		assert.Equal(t, http.StatusAccepted, w.Code)
	}

	assert.Empty(t, rec.all(), "no mail for addresses that have no account")
}

func TestChangePasswordRequiresTheCurrentOne(t *testing.T) {
	srv := newTestServer(t, testMailer(t))
	session := registerAccount(t, srv, "reader@example.com", "correct horse battery")

	var env httpx.ErrorEnvelope
	w := callJSON(t, srv, http.MethodPut, "/api/v1/auth/password", map[string]string{
		"current_password": "wrong", "new_password": "a brand new password",
	}, &env, bearer(session.AccessToken))
	require.Equal(t, http.StatusBadRequest, w.Code)
	require.NotNil(t, env.Error.Details)
	assert.Contains(t, env.Error.Details, "current_password")

	w = call(t, srv, http.MethodPut, "/api/v1/auth/password", map[string]string{
		"current_password": "correct horse battery", "new_password": "a brand new password",
	}, bearer(session.AccessToken))
	require.Equal(t, http.StatusNoContent, w.Code, "body: %s", w.Body.String())

	// The caller stays signed in; other devices are a separate concern.
	w = call(t, srv, http.MethodGet, "/api/v1/auth/me", nil, bearer(session.AccessToken))
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestChangePasswordRequiresAuthentication(t *testing.T) {
	srv := newTestServer(t, testMailer(t))

	w := call(t, srv, http.MethodPut, "/api/v1/auth/password", map[string]string{
		"current_password": "whatever", "new_password": "a brand new password",
	}, nil)
	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

// --- device sessions --------------------------------------------------------

func TestSessionsEndpoints(t *testing.T) {
	srv := newTestServer(t, testMailer(t))
	desktop := registerAccount(t, srv, "reader@example.com", "correct horse battery")

	// A second device.
	var phone authEnvelope
	w := callJSON(t, srv, http.MethodPost, "/api/v1/auth/login", map[string]string{
		"email": "reader@example.com", "password": "correct horse battery",
	}, &phone, map[string]string{"X-Client-Platform": "android"})
	require.Equal(t, http.StatusOK, w.Code)

	t.Run("list flags the calling device", func(t *testing.T) {
		var out struct {
			Sessions []struct {
				ID          string `json:"id"`
				DeviceLabel string `json:"device_label"`
				Platform    string `json:"platform"`
				Current     bool   `json:"current"`
			} `json:"sessions"`
		}
		w := callJSON(t, srv, http.MethodGet, "/api/v1/auth/sessions", nil, &out, bearer(phone.AccessToken))
		require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
		require.Len(t, out.Sessions, 2)

		current := 0
		for _, s := range out.Sessions {
			if s.Current {
				current++
				assert.Equal(t, phone.SessionID, s.ID)
			}
		}
		assert.Equal(t, 1, current)
	})

	t.Run("rename a device", func(t *testing.T) {
		var out struct {
			DeviceLabel string `json:"device_label"`
		}
		w := callJSON(t, srv, http.MethodPatch, "/api/v1/auth/sessions/"+desktop.SessionID,
			map[string]string{"device_label": "Work laptop"}, &out, bearer(phone.AccessToken))
		require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
		assert.Equal(t, "Work laptop", out.DeviceLabel)
	})

	t.Run("the calling device cannot revoke itself", func(t *testing.T) {
		var env httpx.ErrorEnvelope
		w := callJSON(t, srv, http.MethodDelete, "/api/v1/auth/sessions/"+phone.SessionID,
			nil, &env, bearer(phone.AccessToken))

		assert.Equal(t, http.StatusConflict, w.Code)
		assert.Equal(t, "cannot_revoke_current", env.Error.Code)
	})

	t.Run("another device can be revoked", func(t *testing.T) {
		w := call(t, srv, http.MethodDelete, "/api/v1/auth/sessions/"+desktop.SessionID,
			nil, bearer(phone.AccessToken))
		require.Equal(t, http.StatusNoContent, w.Code, "body: %s", w.Body.String())

		// And it stops working straight away.
		w = call(t, srv, http.MethodGet, "/api/v1/auth/me", nil, bearer(desktop.AccessToken))
		assert.Equal(t, http.StatusUnauthorized, w.Code)
	})

	t.Run("an unknown session is not found", func(t *testing.T) {
		w := call(t, srv, http.MethodDelete, "/api/v1/auth/sessions/no-such-session",
			nil, bearer(phone.AccessToken))
		assert.Equal(t, http.StatusNotFound, w.Code)
	})
}

func TestRevokeOtherSessionsEndpoint(t *testing.T) {
	srv := newTestServer(t, testMailer(t))
	desktop := registerAccount(t, srv, "reader@example.com", "correct horse battery")

	var phone authEnvelope
	require.Equal(t, http.StatusOK, callJSON(t, srv, http.MethodPost, "/api/v1/auth/login",
		map[string]string{"email": "reader@example.com", "password": "correct horse battery"},
		&phone, nil).Code)

	w := call(t, srv, http.MethodDelete, "/api/v1/auth/sessions", nil, bearer(phone.AccessToken))
	require.Equal(t, http.StatusNoContent, w.Code, "body: %s", w.Body.String())

	assert.Equal(t, http.StatusUnauthorized,
		call(t, srv, http.MethodGet, "/api/v1/auth/me", nil, bearer(desktop.AccessToken)).Code)
	assert.Equal(t, http.StatusOK,
		call(t, srv, http.MethodGet, "/api/v1/auth/me", nil, bearer(phone.AccessToken)).Code,
		"the calling device stays signed in")
}

// --- providers and optional Firebase ---------------------------------------

func TestProvidersReportsEmailPasswordOnlyByDefault(t *testing.T) {
	srv := newTestServer(t, testMailer(t))

	var out struct {
		Password bool `json:"password"`
		Firebase bool `json:"firebase"`
	}
	w := callJSON(t, srv, http.MethodGet, "/api/v1/auth/providers", nil, &out, nil)
	require.Equal(t, http.StatusOK, w.Code)
	assert.True(t, out.Password)
	assert.False(t, out.Firebase, "Firebase is not configured in tests, so the app hides the button")
}

func TestFirebaseRouteIsAbsentWhenUnconfigured(t *testing.T) {
	srv := newTestServer(t, testMailer(t))

	// Not mounted at all, so it does not exist. The app learns to hide the
	// Google button from /auth/providers instead of probing this route.
	w := call(t, srv, http.MethodPost, "/api/v1/auth/firebase", map[string]string{"id_token": "x"}, nil)
	assert.Equal(t, http.StatusNotFound, w.Code)
}

// --- helpers ----------------------------------------------------------------

func mustEnvelope(t *testing.T, w *httptest.ResponseRecorder) httpx.ErrorEnvelope {
	t.Helper()
	var env httpx.ErrorEnvelope
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &env), "body: %s", w.Body.String())
	return env
}

// mustHost returns the host of a URL, asserting it parsed.
func mustHost(t *testing.T, rawURL string) string {
	t.Helper()

	u, err := url.Parse(rawURL)
	require.NoError(t, err)
	return u.Host
}

// registeredSession signs in and returns an access token, for tests that need a
// fresh session after the registration one has been consumed.
func registeredSession(t *testing.T, srv *apiServer) string {
	t.Helper()

	var out authEnvelope
	w := callJSON(t, srv, http.MethodPost, "/api/v1/auth/login",
		map[string]string{"email": "reader@example.com", "password": "correct horse battery"}, &out, nil)
	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
	return out.AccessToken
}

// queryParam reads a single query parameter from a URL, failing the test when it
// is absent.
func queryParam(t *testing.T, rawURL, key string) string {
	t.Helper()

	u, err := url.Parse(rawURL)
	require.NoError(t, err)

	value := u.Query().Get(key)
	require.NotEmpty(t, value, "%s should be present in %q", key, rawURL)
	return value
}
