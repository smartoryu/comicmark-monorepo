package main

// The cases the plan calls out for this step, and where each one is proven.
// Anything already covered by an earlier test is referenced rather than
// duplicated, so there is one authoritative test per behaviour.
//
//	register                       TestRegisterReturnsTokensAndAUser
//	login                          TestLoginAndMe
//	duplicate email -> 409         TestRegisterRejectsADuplicateEmail
//	weak password -> 400           TestRegisterRejectsInvalidInputWithFieldErrors
//	                               TestPasswordBoundaries
//	refresh rotation               TestRefreshRotatesAndDetectsReplay
//	reuse revokes the family       TestRefreshRotatesAndDetectsReplay
//	                               TestReplayedRefreshTokenRevokesTheWholeFamily
//	expired access token           TestExpiredAccessTokenIsRejected
//	expired refresh token          TestExpiredRefreshTokenIsRejected
//	alg:none rejected              TestForgedAlgorithmTokensAreRejected/alg_none
//	RS256 confusion rejected       TestForgedAlgorithmTokensAreRejected/rs256
//	tampered payload rejected      TestTamperedAccessTokenIsRejected

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"comicmark/go-modular/internal/httpx"
)

// --- password policy boundaries ---------------------------------------------

func TestPasswordBoundaries(t *testing.T) {
	srv := newTestServer(t, testMailer(t))

	t.Run("one character short is rejected", func(t *testing.T) {
		var env httpxEnvelope
		w := callJSON(t, srv, http.MethodPost, "/api/v1/auth/register", map[string]string{
			"email": "short@example.com", "password": strings.Repeat("a", 7),
		}, &env, nil)
		assert.Equal(t, http.StatusBadRequest, w.Code)
	})

	t.Run("exactly the minimum is accepted", func(t *testing.T) {
		w := call(t, srv, http.MethodPost, "/api/v1/auth/register", map[string]string{
			"email": "exact@example.com", "password": strings.Repeat("a", 8),
		}, nil)
		assert.Equal(t, http.StatusCreated, w.Code, "body: %s", w.Body.String())
	})

	// A password that is just the email address is the most guessable choice
	// available, and the use case rejects it explicitly.
	t.Run("the email address is rejected even when long enough", func(t *testing.T) {
		const email = "longenough@example.com"
		var env httpxEnvelope
		w := callJSON(t, srv, http.MethodPost, "/api/v1/auth/register", map[string]string{
			"email": email, "password": email,
		}, &env, nil)

		assert.Equal(t, http.StatusBadRequest, w.Code)
		require.NotNil(t, env.Error.Details)
		assert.Contains(t, env.Error.Details, "password")
	})
}

// --- token forgery ----------------------------------------------------------

// The signing method must come from a fixed allowlist rather than from the token
// header. Otherwise a forged payload can be presented with "none", or signed with
// an algorithm the verifier was never meant to accept.
func TestForgedAlgorithmTokensAreRejected(t *testing.T) {
	srv := newTestServer(t, testMailer(t))
	session := registerAccount(t, srv, "reader@example.com", "correct horse battery")

	// Read the subject and session out of a legitimate token so the forged one
	// claims exactly the same identity.
	claims := decodeUnverifiedClaims(t, session.AccessToken)

	forgedClaims := jwt.MapClaims{
		"iss": "comicmark",
		"sub": claims["sub"],
		"sid": claims["sid"],
		"typ": "access",
		"iat": time.Now().Unix(),
		"exp": time.Now().Add(time.Hour).Unix(),
	}

	t.Run("alg none", func(t *testing.T) {
		unsigned, err := jwt.NewWithClaims(jwt.SigningMethodNone, forgedClaims).
			SignedString(jwt.UnsafeAllowNoneSignatureType)
		require.NoError(t, err)

		w := call(t, srv, http.MethodGet, "/api/v1/auth/me", nil,
			map[string]string{"Authorization": "Bearer " + unsigned})
		assert.Equal(t, http.StatusUnauthorized, w.Code)
	})

	t.Run("rs256 signed with an attacker's own key", func(t *testing.T) {
		// The classic confusion attack: present a token whose header claims an
		// asymmetric algorithm, signed with a key the attacker controls, hoping
		// the verifier trusts the header. Only HS256 is allowed, so it must be
		// refused before the signature is even considered.
		key, err := rsa.GenerateKey(rand.Reader, 2048)
		require.NoError(t, err)

		signed, err := jwt.NewWithClaims(jwt.SigningMethodRS256, forgedClaims).
			SignedString(key)
		require.NoError(t, err)

		w := call(t, srv, http.MethodGet, "/api/v1/auth/me", nil,
			map[string]string{"Authorization": "Bearer " + signed})
		assert.Equal(t, http.StatusUnauthorized, w.Code,
			"an asymmetric token must be rejected, not verified")
	})

	t.Run("hs512 instead of hs256", func(t *testing.T) {
		// Same secret, different HMAC variant: still not the allowlisted
		// algorithm, so still refused.
		hs512, err := jwt.NewWithClaims(jwt.SigningMethodHS512, forgedClaims).
			SignedString([]byte(testSecret))
		require.NoError(t, err)

		w := call(t, srv, http.MethodGet, "/api/v1/auth/me", nil,
			map[string]string{"Authorization": "Bearer " + hs512})
		assert.Equal(t, http.StatusUnauthorized, w.Code)
	})
}

func TestTamperedAccessTokenIsRejected(t *testing.T) {
	srv := newTestServer(t, testMailer(t))
	session := registerAccount(t, srv, "reader@example.com", "correct horse battery")

	// Flip the last character of the payload, leaving the signature untouched.
	parts := strings.Split(session.AccessToken, ".")
	require.Len(t, parts, 3)

	payload := []byte(parts[1])
	if payload[len(payload)-1] == 'A' {
		payload[len(payload)-1] = 'B'
	} else {
		payload[len(payload)-1] = 'A'
	}
	tampered := parts[0] + "." + string(payload) + "." + parts[2]

	w := call(t, srv, http.MethodGet, "/api/v1/auth/me", nil,
		map[string]string{"Authorization": "Bearer " + tampered})

	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

// --- expiry -----------------------------------------------------------------

func TestExpiredAccessTokenIsRejected(t *testing.T) {
	srv := newTestServer(t, testMailer(t))
	session := registerAccount(t, srv, "reader@example.com", "correct horse battery")

	claims := decodeUnverifiedClaims(t, session.AccessToken)

	// The same token, backdated so it expired an hour ago.
	forged := jwt.MapClaims{
		"iss": claims["iss"],
		"sub": claims["sub"],
		"sid": claims["sid"],
		"typ": "access",
		"iat": time.Now().Add(-2 * time.Hour).Unix(),
		"exp": time.Now().Add(-time.Hour).Unix(),
	}
	signed, err := jwt.NewWithClaims(jwt.SigningMethodHS256, forged).SignedString([]byte(testSecret))
	require.NoError(t, err)

	w := call(t, srv, http.MethodGet, "/api/v1/auth/me", nil,
		map[string]string{"Authorization": "Bearer " + signed})

	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestExpiredRefreshTokenIsRejected(t *testing.T) {
	srv := newTestServer(t, testMailer(t))
	session := registerAccount(t, srv, "reader@example.com", "correct horse battery")

	claims := decodeUnverifiedClaims(t, session.RefreshToken)

	// The refresh jti must match a real row, so only the expiry is forged.
	forged := jwt.MapClaims{
		"iss": claims["iss"],
		"sub": claims["sub"],
		"sid": claims["sid"],
		"typ": "refresh",
		"jti": claims["jti"],
		"iat": time.Now().Add(-2 * time.Hour).Unix(),
		"exp": time.Now().Add(-time.Hour).Unix(),
	}
	signed, err := jwt.NewWithClaims(jwt.SigningMethodHS256, forged).SignedString([]byte(testSecret))
	require.NoError(t, err)

	var env httpxEnvelope
	w := callJSON(t, srv, http.MethodPost, "/api/v1/auth/refresh",
		map[string]string{"refresh_token": signed}, &env, nil)

	assert.Equal(t, http.StatusUnauthorized, w.Code)
	assert.Equal(t, "token_invalid", env.Error.Code)
}

// A refresh token must be presented exactly as issued: replaying an earlier link
// in the chain is what the family revocation exists for.
func TestReplayedRefreshTokenRevokesTheWholeFamily(t *testing.T) {
	srv := newTestServer(t, testMailer(t))
	session := registerAccount(t, srv, "reader@example.com", "correct horse battery")

	// Rotate three times, keeping every token in the chain.
	chain := []string{session.RefreshToken}
	var latest authEnvelope
	for range 3 {
		require.Equal(t, http.StatusOK, callJSON(t, srv, http.MethodPost, "/api/v1/auth/refresh",
			map[string]string{"refresh_token": chain[len(chain)-1]}, &latest, nil).Code)
		chain = append(chain, latest.RefreshToken)
	}

	// Replay the very first token.
	var env httpxEnvelope
	w := callJSON(t, srv, http.MethodPost, "/api/v1/auth/refresh",
		map[string]string{"refresh_token": chain[0]}, &env, nil)
	require.Equal(t, http.StatusUnauthorized, w.Code,
		"replaying chain[0] must be refused; got body: %s", w.Body.String())
	assert.Equal(t, "token_reused", env.Error.Code)

	// Every token in the chain, including the current one, is now dead.
	for i, token := range chain {
		w := call(t, srv, http.MethodPost, "/api/v1/auth/refresh",
			map[string]string{"refresh_token": token}, nil)
		assert.Equal(t, http.StatusUnauthorized, w.Code,
			"chain[%d] should be dead after a replay revokes its family; body: %s", i, w.Body.String())
	}

	// The access token for the revoked session stops working too.
	w = call(t, srv, http.MethodGet, "/api/v1/auth/me", nil, bearer(latest.AccessToken))
	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestRotationKeepsTheSessionAndIssuesAUsableAccessToken(t *testing.T) {
	srv := newTestServer(t, testMailer(t))
	session := registerAccount(t, srv, "reader@example.com", "correct horse battery")

	var rotated authEnvelope
	require.Equal(t, http.StatusOK, callJSON(t, srv, http.MethodPost, "/api/v1/auth/refresh",
		map[string]string{"refresh_token": session.RefreshToken}, &rotated, nil).Code)

	assert.Equal(t, session.SessionID, rotated.SessionID, "rotation stays in the same device session")
	assert.Equal(t, session.User.ID, rotated.User.ID)

	// The new access token works straight away.
	w := call(t, srv, http.MethodGet, "/api/v1/auth/me", nil, bearer(rotated.AccessToken))
	assert.Equal(t, http.StatusOK, w.Code)

	// And the session has not multiplied.
	var out struct {
		Sessions []struct {
			ID string `json:"id"`
		} `json:"sessions"`
	}
	require.Equal(t, http.StatusOK, callJSON(t, srv, http.MethodGet, "/api/v1/auth/sessions",
		nil, &out, bearer(rotated.AccessToken)).Code)
	assert.Len(t, out.Sessions, 1, "refreshing must not create another device session")
}

// --- helpers ----------------------------------------------------------------

// httpxEnvelope aliases the shared error envelope for readability here.
type httpxEnvelope = httpx.ErrorEnvelope

// decodeUnverifiedClaims reads a JWT's claims without checking its signature, so
// a test can forge a variant that looks identical to the real thing.
func decodeUnverifiedClaims(t *testing.T, token string) map[string]any {
	t.Helper()

	parts := strings.Split(token, ".")
	require.Len(t, parts, 3)

	payload, err := jwt.NewParser().DecodeSegment(parts[1])
	require.NoError(t, err)

	var claims map[string]any
	require.NoError(t, json.Unmarshal(payload, &claims))

	// Sanity: the fields forged tokens rely on must be present.
	require.Contains(t, claims, "sub")
	require.Contains(t, claims, "sid")
	require.Contains(t, claims, "iss")
	return claims
}
