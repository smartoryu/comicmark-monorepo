// Package auth implements ComicMark accounts: registration and sign-in,
// email verification, password reset, refresh-token rotation and per-device
// session management.
//
// The package is deliberately self-contained. It owns its tables, its HTTP
// routes and its JWT middleware, and the only things it needs from the rest of
// the application are injected through Deps.
package auth

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Identity providers recorded in auth_identities.
const (
	// ProviderPassword is the local email and password identity.
	ProviderPassword = "password"
	// ProviderFirebase is a Firebase Authentication identity (Google sign-in).
	ProviderFirebase = "firebase"
)

// Purposes recorded in one_time_tokens.
const (
	PurposeEmailVerification = "email_verification"
	PurposePasswordReset     = "password_reset"
)

// Domain errors. The HTTP layer maps these to status codes; nothing else should
// need to inspect them.
var (
	// ErrNotFound means the requested row does not exist.
	ErrNotFound = errors.New("auth: not found")

	// ErrEmailTaken means an account already exists for the address.
	ErrEmailTaken = errors.New("auth: email is already registered")

	// ErrInvalidCredentials covers both an unknown email and a wrong password,
	// so a caller cannot use sign-in to discover which addresses exist.
	ErrInvalidCredentials = errors.New("auth: invalid email or password")

	// ErrEmailNotVerified is returned by sign-in when verification is required
	// and the account has not confirmed its address.
	ErrEmailNotVerified = errors.New("auth: email is not verified")

	// ErrTokenInvalid means a one-time token is unknown, expired or already
	// used. The three cases are deliberately indistinguishable to the caller.
	ErrTokenInvalid = errors.New("auth: token is invalid or has expired")

	// ErrTokenReused means a refresh token that had already been rotated was
	// presented again. The whole rotation family is revoked as a result.
	ErrTokenReused = errors.New("auth: refresh token was already used")

	// ErrSessionRevoked means the session behind a token has been revoked.
	ErrSessionRevoked = errors.New("auth: session has been revoked")

	// ErrCannotRevokeCurrent is returned when a request tries to revoke the
	// session it is authenticating with; the client should sign out instead.
	ErrCannotRevokeCurrent = errors.New("auth: cannot revoke the current session")

	// ErrPasswordMismatch means the supplied current password is wrong.
	ErrPasswordMismatch = errors.New("auth: current password is incorrect")

	// ErrNoPasswordSet means the account has no password (a social-only
	// account) and therefore cannot sign in or change a password.
	ErrNoPasswordSet = errors.New("auth: this account has no password set")

	// Input problems, reported to the caller as field errors.
	ErrPasswordTooShort     = errors.New("auth: password is too short")
	ErrPasswordTooLong      = errors.New("auth: password is too long")
	ErrPasswordMatchesEmail = errors.New("auth: password must not be the email address")
	ErrInvalidEmail         = errors.New("auth: email address is not valid")
	ErrInvalidDeviceLabel   = errors.New("auth: device label is not valid")
)

// Password and email policy.
const (
	// MinPasswordLength is the shortest accepted password. Length is the only
	// complexity rule: it is what actually resists guessing, and character-class
	// rules mostly push users towards predictable substitutions.
	MinPasswordLength = 8

	// MaxEmailLength is the longest accepted address, per RFC 5321.
	MaxEmailLength = 254
)

// FieldError is a single invalid input, reported to the client as one field in
// the validation response.
type FieldError struct {
	Field   string
	Message string
	cause   error
}

func (e *FieldError) Error() string { return e.Field + ": " + e.Message }

// Unwrap lets errors.Is match the underlying sentinel, so a caller can check for
// ErrPasswordTooShort without also inspecting the field name.
func (e *FieldError) Unwrap() error { return e.cause }

// newFieldError builds a FieldError carrying a sentinel for errors.Is.
func newFieldError(field, message string, cause error) *FieldError {
	return &FieldError{Field: field, Message: message, cause: cause}
}

// timeFormat is RFC 3339 with milliseconds and a fixed width.
//
// Timestamps round-trip through SQLite as TEXT and are compared
// lexicographically, so the layout must be fixed width. time.RFC3339Nano is not
// usable here: it drops trailing zeros, which makes "…:00Z" sort after
// "…:00.5Z".
const timeFormat = "2006-01-02T15:04:05.000Z07:00"

// formatTime renders a timestamp for storage.
func formatTime(t time.Time) string { return t.UTC().Format(timeFormat) }

// parseTime reads a stored timestamp.
func parseTime(s string) (time.Time, error) {
	t, err := time.Parse(timeFormat, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("auth: parse time %q: %w", s, err)
	}
	return t.UTC(), nil
}

// nullString converts an empty string to SQL NULL. Optional text columns treat
// "" and NULL as the same thing, which keeps comparisons and uniqueness sane.
func nullString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// stringOrEmpty reads a nullable text column.
func stringOrEmpty(ns sql.NullString) string { return ns.String }

// nullableTime reads a nullable timestamp column.
func nullableTime(ns sql.NullString) (*time.Time, error) {
	if !ns.Valid || ns.String == "" {
		return nil, nil
	}
	t, err := parseTime(ns.String)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// User is an account.
type User struct {
	ID          string
	Email       string
	DisplayName string
	AvatarURL   string
	// EmailVerifiedAt is nil until the address is confirmed.
	EmailVerifiedAt *time.Time
	CreatedAt       time.Time
	UpdatedAt       *time.Time
}

// EmailVerified reports whether the address has been confirmed.
func (u *User) EmailVerified() bool { return u.EmailVerifiedAt != nil }

// AuthIdentity links a user to a sign-in provider.
type AuthIdentity struct {
	ID string
	// UserID is the owning account.
	UserID string
	// Provider is ProviderPassword or ProviderFirebase.
	Provider string
	// ProviderSubject is the Firebase uid, or the user id for the password
	// provider.
	ProviderSubject string
	Email           string
	CreatedAt       time.Time
}

// Session is one signed-in device.
type Session struct {
	ID string
	// UserID is the owning account.
	UserID string
	// DeviceLabel is the user-visible description, e.g. "Chrome on macOS".
	DeviceLabel string
	// Platform is web, android or ios.
	Platform  string
	UserAgent string
	// IP is recorded on creation so the user can recognise the device.
	IP         string
	LastSeenAt time.Time
	RevokedAt  *time.Time
	CreatedAt  time.Time
}

// Active reports whether the session is still usable.
func (s *Session) Active() bool { return s.RevokedAt == nil }

// SessionView is a session plus the caller-specific "is this me" flag returned
// by the sessions endpoint.
type SessionView struct {
	Session
	// Current is true for the session making the request.
	Current bool
}

// RefreshToken is one link in a rotation chain. Only the hash of the token is
// stored.
type RefreshToken struct {
	ID string
	// UserID is the owning account.
	UserID string
	// SessionID is the device session the chain belongs to.
	SessionID string
	// TokenHash is the SHA-256 fingerprint of the token string.
	TokenHash string
	// FamilyID groups a rotation chain so replay can revoke all of it.
	FamilyID  string
	ExpiresAt time.Time
	RevokedAt *time.Time
	// ReplacedBy is the id of the token that superseded this one.
	ReplacedBy *string
	CreatedAt  time.Time
}

// Active reports whether the token may still be exchanged.
func (t *RefreshToken) Active() bool { return t.RevokedAt == nil }

// OneTimeToken is a single-use token for email verification or password reset.
type OneTimeToken struct {
	ID string
	// UserID is the owning account.
	UserID string
	// Purpose is PurposeEmailVerification or PurposePasswordReset.
	Purpose string
	// TokenHash is the SHA-256 fingerprint of the token string.
	TokenHash  string
	ExpiresAt  time.Time
	ConsumedAt *time.Time
	CreatedAt  time.Time
}

// Consumed reports whether the token has been used.
func (t *OneTimeToken) Consumed() bool { return t.ConsumedAt != nil }
