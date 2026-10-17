// Package jwt issues and verifies the access and refresh tokens used by the
// ComicMark API.
//
// Both token kinds are HS256-signed JWTs carrying a "typ" claim so that one
// cannot be substituted for the other. Only HS256 is accepted when parsing:
// the signing method is taken from a fixed allowlist rather than from the
// token header, which is what prevents algorithm-confusion attacks.
//
// Refresh tokens are never stored. Only their SHA-256 fingerprint is persisted,
// so a database leak does not hand an attacker usable sessions.
package jwt

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

// SigningAlgorithm is the only algorithm this package issues or accepts.
const SigningAlgorithm = "HS256"

// MinSecretLength is the shortest signing key accepted. Shorter keys are
// brute-forceable offline.
const MinSecretLength = 32

// TokenType distinguishes the two kinds of token.
type TokenType string

const (
	// TypeAccess is a short-lived token presented on every request.
	TypeAccess TokenType = "access"
	// TypeRefresh is a long-lived token exchangeable for a new pair.
	TypeRefresh TokenType = "refresh"
)

// Errors returned when a token is unacceptable. Callers translate all of them
// into a generic 401.
var (
	ErrInvalidToken     = errors.New("jwt: invalid token")
	ErrWrongTokenType   = errors.New("jwt: wrong token type")
	ErrInvalidSecret    = errors.New("jwt: secret must be at least 32 bytes")
	ErrInvalidIssuer    = errors.New("jwt: issuer must not be empty")
	ErrInvalidAlgorithm = errors.New("jwt: unexpected signing algorithm")
)

// Claims is the validated content of a token.
type Claims struct {
	// UserID is the token subject.
	UserID string
	// SessionID identifies the device session the token belongs to.
	SessionID string
	// TokenID is the JWT id, used to look up a refresh token row.
	TokenID string
	// Type is the "typ" claim.
	Type TokenType
	// IssuedAt and ExpiresAt are the standard time claims.
	IssuedAt  time.Time
	ExpiresAt time.Time
}

// RefreshToken bundles a freshly signed refresh token with the values that must
// be persisted alongside it.
type RefreshToken struct {
	// Token is the value handed to the client.
	Token string
	// ID is the JWT id, which becomes the refresh_tokens row id.
	ID string
	// Hash is the SHA-256 fingerprint of Token, which is what gets stored.
	Hash string
	// ExpiresAt is when the token stops being valid.
	ExpiresAt time.Time
}

// Manager signs and verifies tokens with a fixed secret, issuer and lifetime.
type Manager struct {
	secret     []byte
	issuer     string
	accessTTL  time.Duration
	refreshTTL time.Duration
}

// NewManager returns a Manager. It rejects secrets that are too short to be
// worth attacking less than a day of guessing.
func NewManager(secret []byte, issuer string, accessTTL, refreshTTL time.Duration) (*Manager, error) {
	if len(secret) < MinSecretLength {
		return nil, fmt.Errorf("%w (got %d bytes)", ErrInvalidSecret, len(secret))
	}
	if issuer == "" {
		return nil, ErrInvalidIssuer
	}
	if accessTTL <= 0 {
		return nil, fmt.Errorf("jwt: access token TTL must be positive (got %s)", accessTTL)
	}
	if refreshTTL <= 0 {
		return nil, fmt.Errorf("jwt: refresh token TTL must be positive (got %s)", refreshTTL)
	}
	return &Manager{
		secret:     secret,
		issuer:     issuer,
		accessTTL:  accessTTL,
		refreshTTL: refreshTTL,
	}, nil
}

// AccessTTL returns how long an access token lives.
func (m *Manager) AccessTTL() time.Duration { return m.accessTTL }

// RefreshTTL returns how long a refresh token lives.
func (m *Manager) RefreshTTL() time.Duration { return m.refreshTTL }

// tokenClaims is the on-the-wire claim set. The embedded registered claims give
// us iss/sub/jti/iat/exp handling for free.
type tokenClaims struct {
	jwt.RegisteredClaims
	Type      TokenType `json:"typ"`
	SessionID string    `json:"sid,omitempty"`
}

// SignAccess issues a short-lived access token for a user and session.
func (m *Manager) SignAccess(userID, sessionID string) (string, time.Time, error) {
	if userID == "" {
		return "", time.Time{}, fmt.Errorf("jwt: user id is required")
	}
	now := time.Now()
	expiresAt := now.Add(m.accessTTL)

	claims := tokenClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    m.issuer,
			Subject:   userID,
			IssuedAt:  jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(expiresAt),
		},
		Type:      TypeAccess,
		SessionID: sessionID,
	}

	signed, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(m.secret)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("jwt: sign access token: %w", err)
	}
	return signed, expiresAt, nil
}

// SignRefresh issues a long-lived refresh token, generating the id that the
// caller must persist as the token's row id.
func (m *Manager) SignRefresh(userID, sessionID string) (*RefreshToken, error) {
	if userID == "" {
		return nil, fmt.Errorf("jwt: user id is required")
	}

	id := uuid.Must(uuid.NewV7()).String()
	now := time.Now()
	expiresAt := now.Add(m.refreshTTL)

	claims := tokenClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    m.issuer,
			Subject:   userID,
			ID:        id,
			IssuedAt:  jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(expiresAt),
		},
		Type:      TypeRefresh,
		SessionID: sessionID,
	}

	signed, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(m.secret)
	if err != nil {
		return nil, fmt.Errorf("jwt: sign refresh token: %w", err)
	}

	return &RefreshToken{
		Token:     signed,
		ID:        id,
		Hash:      HashToken(signed),
		ExpiresAt: expiresAt,
	}, nil
}

// Parse verifies a token's signature, issuer and expiry, and returns its
// claims. It does not constrain the token type; prefer ParseAccess or
// ParseRefresh.
func (m *Manager) Parse(token string) (*Claims, error) {
	if token == "" {
		return nil, fmt.Errorf("%w: empty", ErrInvalidToken)
	}

	var claims tokenClaims
	// The keyfunc returns the secret unconditionally: the algorithm is
	// constrained by WithValidMethods, not by the token header.
	_, err := jwt.ParseWithClaims(token, &claims, func(*jwt.Token) (any, error) {
		return m.secret, nil
	},
		jwt.WithValidMethods([]string{SigningAlgorithm}),
		jwt.WithIssuer(m.issuer),
		jwt.WithExpirationRequired(),
	)
	if err != nil {
		// Wrap everything as ErrInvalidToken so the API never has to reason
		// about library-specific error types, while the detail stays available
		// for logs.
		return nil, fmt.Errorf("%w: %v", ErrInvalidToken, err)
	}

	if claims.Subject == "" {
		return nil, fmt.Errorf("%w: missing subject", ErrInvalidToken)
	}

	out := &Claims{
		UserID:    claims.Subject,
		SessionID: claims.SessionID,
		TokenID:   claims.ID,
		Type:      claims.Type,
	}
	if claims.IssuedAt != nil {
		out.IssuedAt = claims.IssuedAt.Time
	}
	if claims.ExpiresAt != nil {
		out.ExpiresAt = claims.ExpiresAt.Time
	}
	return out, nil
}

// ParseAccess verifies a token and requires it to be an access token.
func (m *Manager) ParseAccess(token string) (*Claims, error) {
	return m.parseTyped(token, TypeAccess)
}

// ParseRefresh verifies a token and requires it to be a refresh token.
func (m *Manager) ParseRefresh(token string) (*Claims, error) {
	return m.parseTyped(token, TypeRefresh)
}

func (m *Manager) parseTyped(token string, want TokenType) (*Claims, error) {
	claims, err := m.Parse(token)
	if err != nil {
		return nil, err
	}
	if claims.Type != want {
		return nil, fmt.Errorf("%w: expected %q, got %q", ErrWrongTokenType, want, claims.Type)
	}
	return claims, nil
}

// HashToken returns the hex-encoded SHA-256 fingerprint of a token. This is what
// gets stored for refresh tokens.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
