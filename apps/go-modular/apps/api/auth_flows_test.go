package main

// The account-lifecycle cases the plan calls out for this step, and where each is
// proven. Anything already covered elsewhere is referenced instead of duplicated.
//
//	verification token single-use        TestEmailVerificationFlowThroughTheMailedLink
//	                                     TestResendSupersedesThePreviousLink
//	verification token expiry            (auth package) TestVerifyEmailRejectsExpiredAndUnknownTokens
//	resend works without a token         TestResendWithoutATokenSupersedesThePreviousLink
//	resend invalidates the previous link TestResendSupersedesThePreviousLink
//	reset revokes every session          TestResetRevokesEverySession
//	forgot-password 202 for unknown      TestForgotPasswordHidesWhetherTheAddressExists
//	session revoke kills the family      TestRevokingADeviceKillsItsRefreshToken
//	DELETE /sessions/{current} -> 409    TestSessionsEndpoints/the_calling_device_cannot_revoke_itself
//	verification required -> 403         TestVerificationRequiredBlocksSignInUntilConfirmed

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"comicmark/go-modular/internal/config"
	"comicmark/go-modular/internal/httpx"
	"comicmark/go-modular/internal/modules/auth"
)

// --- verification gating ----------------------------------------------------

// With verification enforced, an unconfirmed account cannot sign in, and the app
// is told which state the account is in so it can offer a resend rather than an
// unexplained failure.
func TestVerificationRequiredBlocksSignInUntilConfirmed(t *testing.T) {
	srv, rec := newAPIServerWithVerificationRequired(t)

	// Registration itself succeeds and hands back tokens, so the app can show
	// "check your inbox" while signed in.
	registered := registerAccount(t, srv, "reader@example.com", "correct horse battery")
	require.False(t, registered.User.EmailVerified)

	var env httpx.ErrorEnvelope
	w := callJSON(t, srv, http.MethodPost, "/api/v1/auth/login", map[string]string{
		"email": "reader@example.com", "password": "correct horse battery",
	}, &env, nil)

	assert.Equal(t, http.StatusForbidden, w.Code)
	assert.Equal(t, "email_not_verified", env.Error.Code,
		"the client needs a distinct code to route to the verify screen")

	// Confirming the address unblocks sign-in.
	token := queryParam(t, rec.lastLink(t), auth.QueryVerifyToken)
	require.Equal(t, http.StatusOK, call(t, srv, http.MethodPost,
		"/api/v1/auth/verification/email/validate",
		map[string]string{"token": token}, nil).Code)

	w = call(t, srv, http.MethodPost, "/api/v1/auth/login", map[string]string{
		"email": "reader@example.com", "password": "correct horse battery",
	}, nil)
	assert.Equal(t, http.StatusOK, w.Code)
}

// newAPIServerWithVerificationRequired builds a server configured the way
// production is: an unconfirmed address may not sign in.
func newAPIServerWithVerificationRequired(t *testing.T) (*apiServer, *recordingMailer) {
	t.Helper()

	rec := &recordingMailer{}
	srv := newTestServerWithConfig(t, func(cfg *config.Config) {
		cfg.App.RequireEmailVerification = true
	}, rec)
	return srv, rec
}

// --- resending --------------------------------------------------------------

// The resend endpoint is deliberately unauthenticated: when verification is
// enforced, an unverified user has no access token to authenticate with, so
// asking for another link has to work without one.
func TestResendWithoutATokenSupersedesThePreviousLink(t *testing.T) {
	srv, rec := newAPIServerWithVerificationRequired(t)
	registerAccount(t, srv, "reader@example.com", "correct horse battery")

	first := queryParam(t, rec.lastLink(t), auth.QueryVerifyToken)

	// No Authorization header at all.
	var body map[string]string
	w := callJSON(t, srv, http.MethodPost, "/api/v1/auth/verification/email/initiate",
		map[string]string{"email": "reader@example.com"}, &body, nil)
	require.Equal(t, http.StatusAccepted, w.Code, "body: %s", w.Body.String())

	second := queryParam(t, rec.lastLink(t), auth.QueryVerifyToken)
	require.NotEqual(t, first, second, "resending must issue a fresh token")

	// Only the newest link works, so an old email cannot be replayed later.
	var env httpx.ErrorEnvelope
	w = callJSON(t, srv, http.MethodPost, "/api/v1/auth/verification/email/validate",
		map[string]string{"token": first}, &env, nil)
	assert.Equal(t, http.StatusUnauthorized, w.Code)
	assert.Equal(t, "token_invalid", env.Error.Code)

	w = call(t, srv, http.MethodPost, "/api/v1/auth/verification/email/validate",
		map[string]string{"token": second}, nil)
	assert.Equal(t, http.StatusOK, w.Code)
}

// --- password reset breadth -------------------------------------------------

// A reset is the response to a possible takeover, so it must end every session,
// not merely the one that asked for it.
func TestResetRevokesEverySession(t *testing.T) {
	srv, rec := newAPIServerWithMailer(t)

	desktop := registerAccount(t, srv, "reader@example.com", "correct horse battery")

	var phone authEnvelope
	require.Equal(t, http.StatusOK, callJSON(t, srv, http.MethodPost, "/api/v1/auth/login",
		map[string]string{"email": "reader@example.com", "password": "correct horse battery"},
		&phone, map[string]string{"X-Client-Platform": "android"}).Code)

	require.Len(t, mustSessions(t, srv, phone.AccessToken), 2)

	require.Equal(t, http.StatusAccepted, call(t, srv, http.MethodPost, "/api/v1/auth/password/forgot",
		map[string]string{"email": "reader@example.com"}, nil).Code)

	token := queryParam(t, rec.lastLink(t), auth.QueryResetToken)
	require.Equal(t, http.StatusNoContent, call(t, srv, http.MethodPost, "/api/v1/auth/password/reset",
		map[string]string{"token": token, "new_password": "a brand new password"}, nil).Code)

	// Both sessions are gone, and neither device can refresh its way back in.
	for name, session := range map[string]authEnvelope{"desktop": desktop, "phone": phone} {
		w := call(t, srv, http.MethodGet, "/api/v1/auth/me", nil, bearer(session.AccessToken))
		assert.Equal(t, http.StatusUnauthorized, w.Code, "%s access token should be dead", name)

		w = call(t, srv, http.MethodPost, "/api/v1/auth/refresh",
			map[string]string{"refresh_token": session.RefreshToken}, nil)
		assert.Equal(t, http.StatusUnauthorized, w.Code, "%s refresh token should be dead", name)
	}
}

// --- revocation breadth -----------------------------------------------------

// Revoking a device must stop it refreshing as well as using its access token,
// or it would simply mint a new session every fifteen minutes.
func TestRevokingADeviceKillsItsRefreshToken(t *testing.T) {
	srv := newTestServer(t, testMailer(t))

	desktop := registerAccount(t, srv, "reader@example.com", "correct horse battery")

	var phone authEnvelope
	require.Equal(t, http.StatusOK, callJSON(t, srv, http.MethodPost, "/api/v1/auth/login",
		map[string]string{"email": "reader@example.com", "password": "correct horse battery"},
		&phone, nil).Code)

	// The phone revokes the desktop.
	require.Equal(t, http.StatusNoContent, call(t, srv, http.MethodDelete,
		"/api/v1/auth/sessions/"+desktop.SessionID, nil, bearer(phone.AccessToken)).Code)

	// Access token dead...
	w := call(t, srv, http.MethodGet, "/api/v1/auth/me", nil, bearer(desktop.AccessToken))
	assert.Equal(t, http.StatusUnauthorized, w.Code)

	// ...and it cannot buy a new one.
	w = call(t, srv, http.MethodPost, "/api/v1/auth/refresh",
		map[string]string{"refresh_token": desktop.RefreshToken}, nil)
	assert.Equal(t, http.StatusUnauthorized, w.Code)

	// The phone is unaffected.
	assert.Equal(t, http.StatusOK,
		call(t, srv, http.MethodGet, "/api/v1/auth/me", nil, bearer(phone.AccessToken)).Code)

	// And the list reflects reality.
	assert.Len(t, mustSessions(t, srv, phone.AccessToken), 1)
}

// --- helpers ----------------------------------------------------------------

// mustSessions returns the caller's active sessions.
func mustSessions(t *testing.T, srv *apiServer, token string) []struct {
	ID string `json:"id"`
} {
	t.Helper()

	var out struct {
		Sessions []struct {
			ID string `json:"id"`
		} `json:"sessions"`
	}
	w := callJSON(t, srv, http.MethodGet, "/api/v1/auth/sessions", nil, &out, bearer(token))
	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
	return out.Sessions
}
