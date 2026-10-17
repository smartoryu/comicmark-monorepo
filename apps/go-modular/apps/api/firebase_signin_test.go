package main

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"comicmark/go-modular/internal/config"
	"comicmark/go-modular/internal/httpx"
	"comicmark/go-modular/internal/modules/auth"
)

// Provider sign-in, driven through the real HTTP layer with a stubbed verifier.
//
// The Firebase Authentication emulator covers the SDK wiring
// (scripts/firebase-smoke.sh), but it reports password accounts as unverified —
// unlike real Google, which always reports a verified address. That verified path
// is the one that decides whether an identity may be linked to an existing
// account, so it is exercised here where the claim can be controlled.

// stubVerifier stands in for the Firebase SDK.
type stubVerifier struct {
	identity  auth.FirebaseIdentity
	err       error
	calls     int
	lastToken string
}

func (v *stubVerifier) VerifyIDToken(_ context.Context, idToken string) (auth.FirebaseIdentity, error) {
	v.calls++
	v.lastToken = idToken
	if v.err != nil {
		return auth.FirebaseIdentity{}, v.err
	}
	return v.identity, nil
}

// serverWithFirebase builds a server whose provider route is mounted.
func serverWithFirebase(t *testing.T, verifier auth.FirebaseVerifier) *apiServer {
	t.Helper()

	cfg := testConfig(t)
	cfg.Firebase.ProjectID = "comicmark-test"

	db := newTestDB(t)
	srv, err := newAPIServer(cfg, discardLogger(), db, testJWTManager(t), testHasher(), testMailer(t), verifier, testFetcher(t))
	require.NoError(t, err)
	t.Cleanup(srv.close)

	return srv
}

func TestFirebaseRouteIsMountedWhenConfigured(t *testing.T) {
	verifier := &stubVerifier{identity: auth.FirebaseIdentity{
		UID: "uid-1", Email: "reader@example.com", EmailVerified: true,
	}}
	srv := serverWithFirebase(t, verifier)

	var providers struct {
		Password bool `json:"password"`
		Firebase bool `json:"firebase"`
	}
	w := callJSON(t, srv, http.MethodGet, "/api/v1/auth/providers", nil, &providers, nil)
	require.Equal(t, http.StatusOK, w.Code)
	assert.True(t, providers.Password)
	assert.True(t, providers.Firebase, "the app needs this to know whether to show the Google button")
}

func TestFirebaseSignInCreatesAnAccount(t *testing.T) {
	verifier := &stubVerifier{identity: auth.FirebaseIdentity{
		UID:           "firebase-uid-1",
		Email:         "google-reader@example.com",
		EmailVerified: true,
		DisplayName:   "Risf",
		PhotoURL:      "https://cdn.example/avatar.png",
	}}
	srv := serverWithFirebase(t, verifier)

	var out authEnvelope
	w := callJSON(t, srv, http.MethodPost, "/api/v1/auth/firebase",
		map[string]string{"id_token": "emulator-token"}, &out, nil)

	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
	assert.Equal(t, "google-reader@example.com", out.User.Email)
	assert.True(t, out.User.EmailVerified,
		"a provider that vouches for the address should not need a second confirmation")
	assert.NotEmpty(t, out.AccessToken)
	assert.NotEmpty(t, out.SessionID)

	// The token reached the verifier, and the session works.
	assert.Equal(t, "emulator-token", verifier.lastToken)
	w = call(t, srv, http.MethodGet, "/api/v1/auth/me", nil, bearer(out.AccessToken))
	assert.Equal(t, http.StatusOK, w.Code)
}

// A provider identity is matched by its stable subject, so signing in again must
// land on the same account rather than accumulating empty duplicates.
func TestFirebaseSignInReusesTheAccount(t *testing.T) {
	verifier := &stubVerifier{identity: auth.FirebaseIdentity{
		UID: "firebase-uid-1", Email: "google-reader@example.com", EmailVerified: true,
	}}
	srv := serverWithFirebase(t, verifier)

	var first authEnvelope
	require.Equal(t, http.StatusOK, callJSON(t, srv, http.MethodPost, "/api/v1/auth/firebase",
		map[string]string{"id_token": "token-1"}, &first, nil).Code)

	var second authEnvelope
	require.Equal(t, http.StatusOK, callJSON(t, srv, http.MethodPost, "/api/v1/auth/firebase",
		map[string]string{"id_token": "token-2"}, &second, nil).Code)

	assert.Equal(t, first.User.ID, second.User.ID, "the same identity is the same account")
	assert.NotEqual(t, first.SessionID, second.SessionID, "each sign-in is a new device session")
}

// Linking to an existing account is only safe when the provider vouches for the
// address. Without that, anyone able to add a victim's address to their own
// provider account could take it over.
func TestFirebaseSignInRefusesToLinkAnUnverifiedAddress(t *testing.T) {
	srv := serverWithFirebase(t, &stubVerifier{identity: auth.FirebaseIdentity{
		UID: "firebase-uid-1", Email: "reader@example.com", EmailVerified: false,
	}})

	// An account already exists with that address.
	registerAccount(t, srv, "reader@example.com", "correct horse battery")

	var env httpx.ErrorEnvelope
	w := callJSON(t, srv, http.MethodPost, "/api/v1/auth/firebase",
		map[string]string{"id_token": "unverified-token"}, &env, nil)

	assert.Equal(t, http.StatusForbidden, w.Code, "body: %s", w.Body.String())
	assert.Equal(t, "email_not_verified", env.Error.Code)

	// The existing account is untouched and still needs its own password.
	w = call(t, srv, http.MethodPost, "/api/v1/auth/login",
		map[string]string{"email": "reader@example.com", "password": "correct horse battery"}, nil)
	assert.Equal(t, http.StatusOK, w.Code)
}

// A verified address belonging to an existing password account is linked rather
// than duplicated: the reader keeps one library.
func TestFirebaseSignInLinksAVerifiedAddressToAnExistingAccount(t *testing.T) {
	srv := serverWithFirebase(t, &stubVerifier{identity: auth.FirebaseIdentity{
		UID: "firebase-uid-1", Email: "reader@example.com", EmailVerified: true,
	}})

	existing := registerAccount(t, srv, "reader@example.com", "correct horse battery")

	var linked authEnvelope
	w := callJSON(t, srv, http.MethodPost, "/api/v1/auth/firebase",
		map[string]string{"id_token": "verified-token"}, &linked, nil)

	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
	assert.Equal(t, existing.User.ID, linked.User.ID, "the account is linked, not duplicated")

	// Both ways in still work: the password they already had, and the provider.
	w = call(t, srv, http.MethodPost, "/api/v1/auth/login",
		map[string]string{"email": "reader@example.com", "password": "correct horse battery"}, nil)
	assert.Equal(t, http.StatusOK, w.Code, "linking must not break the existing sign-in")

	var again authEnvelope
	w = callJSON(t, srv, http.MethodPost, "/api/v1/auth/firebase",
		map[string]string{"id_token": "verified-token-2"}, &again, nil)
	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, existing.User.ID, again.User.ID)
}

func TestFirebaseSignInRejectsTokensTheVerifierRefuses(t *testing.T) {
	srv := serverWithFirebase(t, &stubVerifier{err: errors.New("signature mismatch")})

	var env httpx.ErrorEnvelope
	w := callJSON(t, srv, http.MethodPost, "/api/v1/auth/firebase",
		map[string]string{"id_token": "forged"}, &env, nil)

	assert.Equal(t, http.StatusUnauthorized, w.Code)
	assert.Equal(t, "invalid_provider_token", env.Error.Code)
	// The verifier's own message is not echoed: it can describe internals.
	assert.NotContains(t, env.Error.Message, "signature")
}

func TestFirebaseSignInRequiresAToken(t *testing.T) {
	srv := serverWithFirebase(t, &stubVerifier{})

	var env httpx.ErrorEnvelope
	w := callJSON(t, srv, http.MethodPost, "/api/v1/auth/firebase",
		map[string]string{"id_token": ""}, &env, nil)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Equal(t, httpx.CodeValidation, env.Error.Code)
}

// A provider session is a normal session: it can be revoked like any other.
func TestFirebaseSessionCanBeRevoked(t *testing.T) {
	verifier := &stubVerifier{identity: auth.FirebaseIdentity{
		UID: "firebase-uid-1", Email: "google-reader@example.com", EmailVerified: true,
	}}
	srv := serverWithFirebase(t, verifier)

	var out authEnvelope
	require.Equal(t, http.StatusOK, callJSON(t, srv, http.MethodPost, "/api/v1/auth/firebase",
		map[string]string{"id_token": "token"}, &out, nil).Code)

	var sessions struct {
		Sessions []struct {
			ID      string `json:"id"`
			Current bool   `json:"current"`
		} `json:"sessions"`
	}
	w := callJSON(t, srv, http.MethodGet, "/api/v1/auth/sessions", nil, &sessions, bearer(out.AccessToken))
	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
	require.Len(t, sessions.Sessions, 1)
	assert.True(t, sessions.Sessions[0].Current)
}

// The route must be absent, not broken, when Firebase is not configured.
func TestFirebaseRouteIsAbsentWithoutConfiguration(t *testing.T) {
	srv := newTestServer(t, testMailer(t))

	var providers struct {
		Firebase bool `json:"firebase"`
	}
	require.Equal(t, http.StatusOK,
		callJSON(t, srv, http.MethodGet, "/api/v1/auth/providers", nil, &providers, nil).Code)
	assert.False(t, providers.Firebase)

	w := call(t, srv, http.MethodPost, "/api/v1/auth/firebase", map[string]string{"id_token": "x"}, nil)
	assert.Equal(t, http.StatusNotFound, w.Code)
}

// The configuration decides this, so the two agree by construction.
func TestFirebaseEnabledReflectsTheProjectID(t *testing.T) {
	cfg := config.Defaults()
	assert.False(t, cfg.FirebaseEnabled())

	cfg.Firebase.ProjectID = "comicmark-demo"
	assert.True(t, cfg.FirebaseEnabled())
}
