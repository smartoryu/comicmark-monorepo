package jwt

import (
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testSecret = "test-secret-that-is-at-least-32-bytes-long"
	testIssuer = "comicmark"
)

func newTestManager(t *testing.T) *Manager {
	t.Helper()
	m, err := NewManager([]byte(testSecret), testIssuer, 15*time.Minute, 30*24*time.Hour)
	require.NoError(t, err)
	return m
}

func TestNewManagerRejectsUnsafeConfiguration(t *testing.T) {
	t.Run("short secret", func(t *testing.T) {
		_, err := NewManager([]byte("too-short"), testIssuer, time.Minute, time.Hour)
		require.ErrorIs(t, err, ErrInvalidSecret)
	})

	t.Run("empty issuer", func(t *testing.T) {
		_, err := NewManager([]byte(testSecret), "", time.Minute, time.Hour)
		require.ErrorIs(t, err, ErrInvalidIssuer)
	})

	t.Run("non-positive TTLs", func(t *testing.T) {
		_, err := NewManager([]byte(testSecret), testIssuer, 0, time.Hour)
		require.Error(t, err)

		_, err = NewManager([]byte(testSecret), testIssuer, time.Minute, 0)
		require.Error(t, err)
	})
}

func TestAccessTokenRoundTrip(t *testing.T) {
	m := newTestManager(t)

	token, expiresAt, err := m.SignAccess("user-1", "session-1")
	require.NoError(t, err)
	assert.WithinDuration(t, time.Now().Add(15*time.Minute), expiresAt, time.Minute)

	claims, err := m.ParseAccess(token)
	require.NoError(t, err)
	assert.Equal(t, "user-1", claims.UserID)
	assert.Equal(t, "session-1", claims.SessionID)
	assert.Equal(t, TypeAccess, claims.Type)
	assert.Empty(t, claims.TokenID, "access tokens carry no jti")
}

func TestRefreshTokenRoundTrip(t *testing.T) {
	m := newTestManager(t)

	rt, err := m.SignRefresh("user-1", "session-1")
	require.NoError(t, err)
	require.NotEmpty(t, rt.Token)
	require.NotEmpty(t, rt.ID)
	assert.WithinDuration(t, time.Now().Add(30*24*time.Hour), rt.ExpiresAt, time.Minute)

	// The stored hash must be the token's fingerprint, never the token itself.
	assert.NotContains(t, rt.Token, rt.Hash)
	assert.Equal(t, HashToken(rt.Token), rt.Hash)

	claims, err := m.ParseRefresh(rt.Token)
	require.NoError(t, err)
	assert.Equal(t, "user-1", claims.UserID)
	assert.Equal(t, "session-1", claims.SessionID)
	assert.Equal(t, rt.ID, claims.TokenID, "jti must round-trip so the row can be located")
	assert.Equal(t, TypeRefresh, claims.Type)
}

func TestTokenTypesAreNotInterchangeable(t *testing.T) {
	m := newTestManager(t)

	access, _, err := m.SignAccess("user-1", "session-1")
	require.NoError(t, err)

	refresh, err := m.SignRefresh("user-1", "session-1")
	require.NoError(t, err)

	_, err = m.ParseAccess(refresh.Token)
	require.ErrorIs(t, err, ErrWrongTokenType, "a refresh token must not authenticate a request")

	_, err = m.ParseRefresh(access)
	require.ErrorIs(t, err, ErrWrongTokenType, "an access token must not be exchangeable for a new pair")

	// Parse itself is type-agnostic, which is what the two wrappers build on.
	claims, err := m.Parse(refresh.Token)
	require.NoError(t, err)
	assert.Equal(t, TypeRefresh, claims.Type)
}

func TestParseRejectsExpiredToken(t *testing.T) {
	m := newTestManager(t)

	// Craft a token that expired an hour ago. NewManager refuses non-positive
	// TTLs, so this is built directly with the same secret, issuer and algorithm.
	expired := tokenClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    testIssuer,
			Subject:   "user-1",
			IssuedAt:  jwt.NewNumericDate(time.Now().Add(-2 * time.Hour)),
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(-time.Hour)),
		},
		Type: TypeAccess,
	}
	signed, err := jwt.NewWithClaims(jwt.SigningMethodHS256, expired).SignedString([]byte(testSecret))
	require.NoError(t, err)

	_, err = m.ParseAccess(signed)
	require.ErrorIs(t, err, ErrInvalidToken)
	assert.Contains(t, err.Error(), "expired")
}

func TestParseRejectsWrongSecret(t *testing.T) {
	signer := newTestManager(t)
	verifier, err := NewManager([]byte("a-completely-different-secret-value-32+"), testIssuer, time.Minute, time.Hour)
	require.NoError(t, err)

	token, _, err := signer.SignAccess("user-1", "session-1")
	require.NoError(t, err)

	_, err = verifier.ParseAccess(token)
	require.ErrorIs(t, err, ErrInvalidToken)
}

func TestParseRejectsWrongIssuer(t *testing.T) {
	signer := newTestManager(t)
	verifier, err := NewManager([]byte(testSecret), "someone-else", time.Minute, time.Hour)
	require.NoError(t, err)

	token, _, err := signer.SignAccess("user-1", "session-1")
	require.NoError(t, err)

	_, err = verifier.ParseAccess(token)
	require.ErrorIs(t, err, ErrInvalidToken)
}

// The signing algorithm must come from a fixed allowlist, not from the token
// header. Otherwise an attacker can re-sign a forged payload with "none" or
// swap HS256 for a public-key algorithm and have it verified.
func TestParseRejectsUnexpectedAlgorithms(t *testing.T) {
	m := newTestManager(t)

	claims := tokenClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    testIssuer,
			Subject:   "user-1",
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
		},
		Type: TypeAccess,
	}

	t.Run("alg none", func(t *testing.T) {
		unsigned, err := jwt.NewWithClaims(jwt.SigningMethodNone, claims).
			SignedString(jwt.UnsafeAllowNoneSignatureType)
		require.NoError(t, err)

		_, err = m.ParseAccess(unsigned)
		require.ErrorIs(t, err, ErrInvalidToken)
	})

	t.Run("HS512 instead of HS256", func(t *testing.T) {
		other, err := jwt.NewWithClaims(jwt.SigningMethodHS512, claims).SignedString([]byte(testSecret))
		require.NoError(t, err)

		_, err = m.ParseAccess(other)
		require.ErrorIs(t, err, ErrInvalidToken)
		assert.Contains(t, err.Error(), "signing method")
	})
}

func TestParseRejectsTamperedToken(t *testing.T) {
	m := newTestManager(t)

	token, _, err := m.SignAccess("user-1", "session-1")
	require.NoError(t, err)

	// Flip the last character of the payload segment.
	parts := strings.Split(token, ".")
	require.Len(t, parts, 3)
	payload := []byte(parts[1])
	if payload[len(payload)-1] == 'A' {
		payload[len(payload)-1] = 'B'
	} else {
		payload[len(payload)-1] = 'A'
	}
	tampered := parts[0] + "." + string(payload) + "." + parts[2]

	_, err = m.ParseAccess(tampered)
	require.ErrorIs(t, err, ErrInvalidToken)
	assert.NotEqual(t, token, tampered)
}

func TestParseRejectsEmptyToken(t *testing.T) {
	m := newTestManager(t)

	_, err := m.Parse("")
	require.ErrorIs(t, err, ErrInvalidToken)
}

func TestHashTokenIsStableAndUnique(t *testing.T) {
	// Stable, so a stored hash can be looked up by token.
	assert.Equal(t, HashToken("abc"), HashToken("abc"))
	// Different per token, so one leaked hash does not reveal another session.
	assert.NotEqual(t, HashToken("abc"), HashToken("abd"))
	// 64 hex characters is 32 bytes of SHA-256.
	assert.Len(t, HashToken("abc"), 64)
}
