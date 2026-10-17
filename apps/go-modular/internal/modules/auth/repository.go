package auth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// querier is satisfied by both *sql.DB and *sql.Tx, which lets the repository
// methods be reused inside a transaction without duplicating SQL.
type querier interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// Repository is the data access layer for the auth module.
type Repository struct {
	q querier
}

// NewRepository returns a repository backed by a connection pool.
func NewRepository(db *sql.DB) *Repository {
	return &Repository{q: db}
}

// WithTx runs fn inside a transaction and hands it a repository bound to that
// transaction. Returning an error from fn rolls the whole thing back.
//
// Nested transactions are rejected rather than silently deadlocking, which is
// what would happen with the single connection SQLite is configured with.
func (r *Repository) WithTx(ctx context.Context, fn func(*Repository) error) error {
	db, ok := r.q.(*sql.DB)
	if !ok {
		return errors.New("auth: nested transactions are not supported")
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("auth: begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if err := fn(&Repository{q: tx}); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("auth: commit transaction: %w", err)
	}
	return nil
}

// newID returns a time-sortable identifier.
func newID() string { return uuid.Must(uuid.NewV7()).String() }

// --- users ------------------------------------------------------------------

const userColumns = `id, email, display_name, avatar_url, email_verified_at, created_at, updated_at`

// CreateUser inserts a user, assigning an id and creation time when unset.
//
// passwordHash is stored on the row and may be empty for social-only accounts.
func (r *Repository) CreateUser(ctx context.Context, u *User, passwordHash string) error {
	if u.ID == "" {
		u.ID = newID()
	}
	if u.CreatedAt.IsZero() {
		u.CreatedAt = time.Now()
	}

	_, err := r.q.ExecContext(ctx, `
		INSERT INTO users (id, email, display_name, avatar_url, password_hash, email_verified_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		u.ID,
		u.Email,
		nullString(u.DisplayName),
		nullString(u.AvatarURL),
		nullString(passwordHash),
		nullTimeToSQL(u.EmailVerifiedAt),
		formatTime(u.CreatedAt),
		nullTimeToSQL(u.UpdatedAt),
	)
	if err != nil {
		if isUniqueViolation(err) {
			return ErrEmailTaken
		}
		return fmt.Errorf("auth: create user: %w", err)
	}
	return nil
}

// GetUserByID looks a user up by primary key.
func (r *Repository) GetUserByID(ctx context.Context, id string) (*User, error) {
	row := r.q.QueryRowContext(ctx,
		`SELECT `+userColumns+` FROM users WHERE id = ?`, id)
	return scanUser(row)
}

// GetUserByEmail looks a user up by address, ignoring case.
func (r *Repository) GetUserByEmail(ctx context.Context, email string) (*User, error) {
	row := r.q.QueryRowContext(ctx,
		`SELECT `+userColumns+` FROM users WHERE LOWER(email) = LOWER(?)`, email)
	return scanUser(row)
}

// GetUserWithPassword returns the user and their password hash.
//
// The hash is returned separately rather than as a field so that it cannot be
// serialised by accident when a User is handed to the HTTP layer.
func (r *Repository) GetUserWithPassword(ctx context.Context, email string) (*User, string, error) {
	var (
		u          User
		display    sql.NullString
		avatar     sql.NullString
		verifiedAt sql.NullString
		updatedAt  sql.NullString
		createdAt  string
		hash       sql.NullString
	)

	row := r.q.QueryRowContext(ctx, `
		SELECT `+userColumns+`, password_hash
		FROM users WHERE LOWER(email) = LOWER(?)`, email)

	if err := row.Scan(&u.ID, &u.Email, &display, &avatar, &verifiedAt, &createdAt, &updatedAt, &hash); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, "", ErrNotFound
		}
		return nil, "", fmt.Errorf("auth: get user with password: %w", err)
	}

	u.DisplayName = stringOrEmpty(display)
	u.AvatarURL = stringOrEmpty(avatar)

	var err error
	if u.EmailVerifiedAt, err = nullableTime(verifiedAt); err != nil {
		return nil, "", err
	}
	if u.UpdatedAt, err = nullableTime(updatedAt); err != nil {
		return nil, "", err
	}
	if u.CreatedAt, err = parseTime(createdAt); err != nil {
		return nil, "", err
	}

	return &u, hash.String, nil
}

// PasswordHash returns the stored hash for a user, or "" when none is set.
func (r *Repository) PasswordHash(ctx context.Context, userID string) (string, error) {
	var hash sql.NullString
	err := r.q.QueryRowContext(ctx,
		`SELECT password_hash FROM users WHERE id = ?`, userID).Scan(&hash)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("auth: get password hash: %w", err)
	}
	return hash.String, nil
}

// EmailExists reports whether an address is already registered.
func (r *Repository) EmailExists(ctx context.Context, email string) (bool, error) {
	var one int
	err := r.q.QueryRowContext(ctx,
		`SELECT 1 FROM users WHERE LOWER(email) = LOWER(?) LIMIT 1`, email).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("auth: check email exists: %w", err)
	}
	return true, nil
}

// MarkEmailVerified records that the address has been confirmed.
func (r *Repository) MarkEmailVerified(ctx context.Context, userID string, at time.Time) error {
	res, err := r.q.ExecContext(ctx, `
		UPDATE users SET email_verified_at = ?, updated_at = ?
		WHERE id = ? AND email_verified_at IS NULL`,
		formatTime(at), formatTime(at), userID)
	if err != nil {
		return fmt.Errorf("auth: mark email verified: %w", err)
	}

	if n, err := res.RowsAffected(); err == nil && n == 0 {
		// Either the user is gone or it was already verified; distinguish so a
		// missing user is not silently treated as success.
		if _, err := r.GetUserByID(ctx, userID); err != nil {
			return err
		}
	}
	return nil
}

// UpdatePasswordHash replaces the stored password hash.
func (r *Repository) UpdatePasswordHash(ctx context.Context, userID, passwordHash string, at time.Time) error {
	res, err := r.q.ExecContext(ctx, `
		UPDATE users SET password_hash = ?, updated_at = ? WHERE id = ?`,
		passwordHash, formatTime(at), userID)
	if err != nil {
		return fmt.Errorf("auth: update password hash: %w", err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return ErrNotFound
	}
	return nil
}

// --- auth identities --------------------------------------------------------

const identityColumns = `id, user_id, provider, provider_subject, email, created_at`

// CreateIdentity links a user to a sign-in provider.
func (r *Repository) CreateIdentity(ctx context.Context, identity *AuthIdentity) error {
	if identity.ID == "" {
		identity.ID = newID()
	}
	if identity.CreatedAt.IsZero() {
		identity.CreatedAt = time.Now()
	}

	_, err := r.q.ExecContext(ctx, `
		INSERT INTO auth_identities (id, user_id, provider, provider_subject, email, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		identity.ID, identity.UserID, identity.Provider, identity.ProviderSubject,
		nullString(identity.Email), formatTime(identity.CreatedAt),
	)
	if err != nil {
		return fmt.Errorf("auth: create identity: %w", err)
	}
	return nil
}

// GetIdentity looks up the account linked to a provider subject.
func (r *Repository) GetIdentity(ctx context.Context, provider, subject string) (*AuthIdentity, error) {
	row := r.q.QueryRowContext(ctx,
		`SELECT `+identityColumns+` FROM auth_identities WHERE provider = ? AND provider_subject = ?`,
		provider, subject)

	var (
		identity  AuthIdentity
		email     sql.NullString
		createdAt string
	)
	if err := row.Scan(&identity.ID, &identity.UserID, &identity.Provider,
		&identity.ProviderSubject, &email, &createdAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("auth: get identity: %w", err)
	}

	identity.Email = stringOrEmpty(email)

	var err error
	if identity.CreatedAt, err = parseTime(createdAt); err != nil {
		return nil, err
	}
	return &identity, nil
}

// --- sessions ---------------------------------------------------------------

const sessionColumns = `id, user_id, device_label, platform, user_agent, ip, last_seen_at, revoked_at, created_at`

// CreateSession records a new signed-in device.
func (r *Repository) CreateSession(ctx context.Context, s *Session) error {
	if s.ID == "" {
		s.ID = newID()
	}
	now := time.Now()
	if s.CreatedAt.IsZero() {
		s.CreatedAt = now
	}
	if s.LastSeenAt.IsZero() {
		s.LastSeenAt = s.CreatedAt
	}
	if s.Platform == "" {
		s.Platform = "unknown"
	}

	_, err := r.q.ExecContext(ctx, `
		INSERT INTO sessions (id, user_id, device_label, platform, user_agent, ip, last_seen_at, revoked_at, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		s.ID, s.UserID, s.DeviceLabel, s.Platform,
		nullString(s.UserAgent), nullString(s.IP),
		formatTime(s.LastSeenAt), nullTimeToSQL(s.RevokedAt), formatTime(s.CreatedAt),
	)
	if err != nil {
		return fmt.Errorf("auth: create session: %w", err)
	}
	return nil
}

// GetSession looks a session up by id.
func (r *Repository) GetSession(ctx context.Context, id string) (*Session, error) {
	row := r.q.QueryRowContext(ctx,
		`SELECT `+sessionColumns+` FROM sessions WHERE id = ?`, id)
	return scanSession(row)
}

// ListSessions returns a user's sessions, most recently used first.
//
// Revoked sessions are included so the client can show that a device was signed
// out rather than have rows silently vanish; callers filter on Active.
func (r *Repository) ListSessions(ctx context.Context, userID string, includeRevoked bool) ([]*Session, error) {
	query := `SELECT ` + sessionColumns + ` FROM sessions WHERE user_id = ?`
	if !includeRevoked {
		query += ` AND revoked_at IS NULL`
	}
	query += ` ORDER BY last_seen_at DESC, created_at DESC`

	rows, err := r.q.QueryContext(ctx, query, userID)
	if err != nil {
		return nil, fmt.Errorf("auth: list sessions: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var sessions []*Session
	for rows.Next() {
		s, err := scanSession(rows)
		if err != nil {
			return nil, err
		}
		sessions = append(sessions, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("auth: iterate sessions: %w", err)
	}
	return sessions, nil
}

// RenameSession updates the user-visible device label.
func (r *Repository) RenameSession(ctx context.Context, id, label string) error {
	res, err := r.q.ExecContext(ctx,
		`UPDATE sessions SET device_label = ? WHERE id = ?`, label, id)
	if err != nil {
		return fmt.Errorf("auth: rename session: %w", err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return ErrNotFound
	}
	return nil
}

// TouchSession records activity. Callers throttle this; it runs on every
// authenticated request otherwise.
func (r *Repository) TouchSession(ctx context.Context, id string, at time.Time) error {
	_, err := r.q.ExecContext(ctx,
		`UPDATE sessions SET last_seen_at = ? WHERE id = ? AND revoked_at IS NULL`,
		formatTime(at), id)
	if err != nil {
		return fmt.Errorf("auth: touch session: %w", err)
	}
	return nil
}

// RevokeSession signs one device out and revokes its refresh tokens.
func (r *Repository) RevokeSession(ctx context.Context, id string, at time.Time) error {
	return r.WithTx(ctx, func(tx *Repository) error {
		res, err := tx.q.ExecContext(ctx,
			`UPDATE sessions SET revoked_at = ? WHERE id = ? AND revoked_at IS NULL`,
			formatTime(at), id)
		if err != nil {
			return fmt.Errorf("auth: revoke session: %w", err)
		}
		if n, err := res.RowsAffected(); err == nil && n == 0 {
			// Already revoked, or never existed.
			if _, err := tx.GetSession(ctx, id); err != nil {
				return err
			}
		}
		return tx.revokeSessionTokens(ctx, id, at)
	})
}

// RevokeAllSessions signs every device out. Used after a password reset, where
// the point is that no old session survives.
func (r *Repository) RevokeAllSessions(ctx context.Context, userID string, at time.Time) error {
	return r.WithTx(ctx, func(tx *Repository) error {
		return tx.revokeAllSessions(ctx, userID, at)
	})
}

// revokeAllSessions is the transaction-composable body of RevokeAllSessions, so
// a password reset can fold revocation into the same transaction as the new
// password hash.
func (r *Repository) revokeAllSessions(ctx context.Context, userID string, at time.Time) error {
	if _, err := r.q.ExecContext(ctx, `
		UPDATE sessions SET revoked_at = ?
		WHERE user_id = ? AND revoked_at IS NULL`,
		formatTime(at), userID); err != nil {
		return fmt.Errorf("auth: revoke all sessions: %w", err)
	}
	if _, err := r.q.ExecContext(ctx, `
		UPDATE refresh_tokens SET revoked_at = ?
		WHERE user_id = ? AND revoked_at IS NULL`,
		formatTime(at), userID); err != nil {
		return fmt.Errorf("auth: revoke all refresh tokens: %w", err)
	}
	return nil
}

// RevokeOtherSessions signs out every device except keepSessionID.
func (r *Repository) RevokeOtherSessions(ctx context.Context, userID, keepSessionID string, at time.Time) error {
	return r.WithTx(ctx, func(tx *Repository) error {
		if _, err := tx.q.ExecContext(ctx, `
			UPDATE sessions SET revoked_at = ?
			WHERE user_id = ? AND id <> ? AND revoked_at IS NULL`,
			formatTime(at), userID, keepSessionID); err != nil {
			return fmt.Errorf("auth: revoke other sessions: %w", err)
		}
		if _, err := tx.q.ExecContext(ctx, `
			UPDATE refresh_tokens SET revoked_at = ?
			WHERE user_id = ? AND session_id <> ? AND revoked_at IS NULL`,
			formatTime(at), userID, keepSessionID); err != nil {
			return fmt.Errorf("auth: revoke other refresh tokens: %w", err)
		}
		return nil
	})
}

// --- refresh tokens ---------------------------------------------------------

const refreshTokenColumns = `id, user_id, session_id, token_hash, family_id, expires_at, revoked_at, replaced_by, created_at`

// CreateRefreshToken inserts a new token row.
func (r *Repository) CreateRefreshToken(ctx context.Context, t *RefreshToken) error {
	if t.ID == "" {
		t.ID = newID()
	}
	if t.FamilyID == "" {
		t.FamilyID = t.ID
	}
	if t.CreatedAt.IsZero() {
		t.CreatedAt = time.Now()
	}

	_, err := r.q.ExecContext(ctx, `
		INSERT INTO refresh_tokens (id, user_id, session_id, token_hash, family_id, expires_at, revoked_at, replaced_by, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		t.ID, t.UserID, t.SessionID, t.TokenHash, t.FamilyID,
		formatTime(t.ExpiresAt), nullTimeToSQL(t.RevokedAt), t.ReplacedBy, formatTime(t.CreatedAt),
	)
	if err != nil {
		return fmt.Errorf("auth: create refresh token: %w", err)
	}
	return nil
}

// GetRefreshTokenByHash locates a token by its fingerprint.
func (r *Repository) GetRefreshTokenByHash(ctx context.Context, hash string) (*RefreshToken, error) {
	row := r.q.QueryRowContext(ctx,
		`SELECT `+refreshTokenColumns+` FROM refresh_tokens WHERE token_hash = ?`, hash)
	return scanRefreshToken(row)
}

// RotateRefreshToken marks current as superseded and inserts next in one
// transaction.
//
// The revoke is a conditional update: if it affects no rows then another
// request already rotated this token, which means the token is being replayed.
// ErrTokenReused is returned in that case and the caller revokes the family.
func (r *Repository) RotateRefreshToken(ctx context.Context, current, next *RefreshToken, at time.Time) error {
	return r.WithTx(ctx, func(tx *Repository) error {
		res, err := tx.q.ExecContext(ctx, `
			UPDATE refresh_tokens SET revoked_at = ?, replaced_by = ?
			WHERE id = ? AND revoked_at IS NULL`,
			formatTime(at), next.ID, current.ID)
		if err != nil {
			return fmt.Errorf("auth: rotate refresh token: %w", err)
		}

		n, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("auth: rotate refresh token: %w", err)
		}
		if n == 0 {
			return ErrTokenReused
		}

		return tx.CreateRefreshToken(ctx, next)
	})
}

// RevokeFamily revokes every token in a rotation chain. This is the response to
// detecting a replayed token: either the legitimate client or an attacker is
// holding a copy, and there is no way to tell which, so both are cut off.
func (r *Repository) RevokeFamily(ctx context.Context, familyID string, at time.Time) error {
	_, err := r.q.ExecContext(ctx, `
		UPDATE refresh_tokens SET revoked_at = ?
		WHERE family_id = ? AND revoked_at IS NULL`,
		formatTime(at), familyID)
	if err != nil {
		return fmt.Errorf("auth: revoke token family: %w", err)
	}
	return nil
}

func (r *Repository) revokeSessionTokens(ctx context.Context, sessionID string, at time.Time) error {
	_, err := r.q.ExecContext(ctx, `
		UPDATE refresh_tokens SET revoked_at = ?
		WHERE session_id = ? AND revoked_at IS NULL`,
		formatTime(at), sessionID)
	if err != nil {
		return fmt.Errorf("auth: revoke session tokens: %w", err)
	}
	return nil
}

// DeleteExpiredRefreshTokens removes rows that can no longer be used, so the
// table does not grow forever. Runs on sign-in.
func (r *Repository) DeleteExpiredRefreshTokens(ctx context.Context, before time.Time) (int64, error) {
	res, err := r.q.ExecContext(ctx,
		`DELETE FROM refresh_tokens WHERE expires_at < ?`, formatTime(before))
	if err != nil {
		return 0, fmt.Errorf("auth: delete expired refresh tokens: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("auth: delete expired refresh tokens: %w", err)
	}
	return n, nil
}

// --- one-time tokens --------------------------------------------------------

const oneTimeTokenColumns = `id, user_id, purpose, token_hash, expires_at, consumed_at, created_at`

// CreateOneTimeToken issues a token, invalidating any outstanding token for the
// same purpose so that only the most recent link works.
func (r *Repository) CreateOneTimeToken(ctx context.Context, t *OneTimeToken) error {
	if t.ID == "" {
		t.ID = newID()
	}
	if t.CreatedAt.IsZero() {
		t.CreatedAt = time.Now()
	}

	return r.WithTx(ctx, func(tx *Repository) error {
		if err := tx.InvalidateOneTimeTokens(ctx, t.UserID, t.Purpose, t.CreatedAt); err != nil {
			return err
		}
		_, err := tx.q.ExecContext(ctx, `
			INSERT INTO one_time_tokens (id, user_id, purpose, token_hash, expires_at, consumed_at, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)`,
			t.ID, t.UserID, t.Purpose, t.TokenHash,
			formatTime(t.ExpiresAt), nullTimeToSQL(t.ConsumedAt), formatTime(t.CreatedAt),
		)
		if err != nil {
			return fmt.Errorf("auth: create one-time token: %w", err)
		}
		return nil
	})
}

// InvalidateOneTimeTokens consumes any outstanding token of a purpose for a
// user, so superseded links stop working immediately.
func (r *Repository) InvalidateOneTimeTokens(ctx context.Context, userID, purpose string, at time.Time) error {
	_, err := r.q.ExecContext(ctx, `
		UPDATE one_time_tokens SET consumed_at = ?
		WHERE user_id = ? AND purpose = ? AND consumed_at IS NULL`,
		formatTime(at), userID, purpose)
	if err != nil {
		return fmt.Errorf("auth: invalidate one-time tokens: %w", err)
	}
	return nil
}

// ConsumeOneTimeToken atomically claims a token.
//
// The claim is a single conditional update, so two concurrent requests cannot
// both succeed: exactly one sees a row affected. Unknown, expired and
// already-consumed all return ErrTokenInvalid, which keeps the failure modes
// indistinguishable to a caller probing the endpoint.
func (r *Repository) ConsumeOneTimeToken(ctx context.Context, purpose, hash string, at time.Time) (*OneTimeToken, error) {
	res, err := r.q.ExecContext(ctx, `
		UPDATE one_time_tokens SET consumed_at = ?
		WHERE token_hash = ? AND purpose = ? AND consumed_at IS NULL AND expires_at > ?`,
		formatTime(at), hash, purpose, formatTime(at))
	if err != nil {
		return nil, fmt.Errorf("auth: consume one-time token: %w", err)
	}

	n, err := res.RowsAffected()
	if err != nil {
		return nil, fmt.Errorf("auth: consume one-time token: %w", err)
	}
	if n == 0 {
		return nil, ErrTokenInvalid
	}

	var (
		t         OneTimeToken
		expiresAt string
		consumed  sql.NullString
		createdAt string
	)
	row := r.q.QueryRowContext(ctx,
		`SELECT `+oneTimeTokenColumns+` FROM one_time_tokens WHERE token_hash = ?`, hash)
	if err := row.Scan(&t.ID, &t.UserID, &t.Purpose, &t.TokenHash,
		&expiresAt, &consumed, &createdAt); err != nil {
		return nil, fmt.Errorf("auth: read consumed token: %w", err)
	}

	if t.ExpiresAt, err = parseTime(expiresAt); err != nil {
		return nil, err
	}
	if t.ConsumedAt, err = nullableTime(consumed); err != nil {
		return nil, err
	}
	if t.CreatedAt, err = parseTime(createdAt); err != nil {
		return nil, err
	}
	return &t, nil
}

// --- scanning helpers -------------------------------------------------------

// rowScanner is implemented by both *sql.Row and *sql.Rows.
type rowScanner interface {
	Scan(dest ...any) error
}

func scanUser(row rowScanner) (*User, error) {
	var (
		u          User
		display    sql.NullString
		avatar     sql.NullString
		verifiedAt sql.NullString
		createdAt  string
		updatedAt  sql.NullString
	)
	if err := row.Scan(&u.ID, &u.Email, &display, &avatar, &verifiedAt, &createdAt, &updatedAt); err != nil {
		return nil, wrapNoRows(err, "get user")
	}

	u.DisplayName = stringOrEmpty(display)
	u.AvatarURL = stringOrEmpty(avatar)

	var err error
	if u.EmailVerifiedAt, err = nullableTime(verifiedAt); err != nil {
		return nil, err
	}
	if u.UpdatedAt, err = nullableTime(updatedAt); err != nil {
		return nil, err
	}
	if u.CreatedAt, err = parseTime(createdAt); err != nil {
		return nil, err
	}
	return &u, nil
}

func scanSession(row rowScanner) (*Session, error) {
	var (
		s         Session
		userAgent sql.NullString
		ip        sql.NullString
		lastSeen  string
		revokedAt sql.NullString
		createdAt string
	)
	if err := row.Scan(&s.ID, &s.UserID, &s.DeviceLabel, &s.Platform,
		&userAgent, &ip, &lastSeen, &revokedAt, &createdAt); err != nil {
		return nil, wrapNoRows(err, "get session")
	}

	s.UserAgent = stringOrEmpty(userAgent)
	s.IP = stringOrEmpty(ip)

	var err error
	if s.LastSeenAt, err = parseTime(lastSeen); err != nil {
		return nil, err
	}
	if s.RevokedAt, err = nullableTime(revokedAt); err != nil {
		return nil, err
	}
	if s.CreatedAt, err = parseTime(createdAt); err != nil {
		return nil, err
	}
	return &s, nil
}

func scanRefreshToken(row rowScanner) (*RefreshToken, error) {
	var (
		t          RefreshToken
		expiresAt  string
		revokedAt  sql.NullString
		replacedBy sql.NullString
		createdAt  string
	)
	if err := row.Scan(&t.ID, &t.UserID, &t.SessionID, &t.TokenHash, &t.FamilyID,
		&expiresAt, &revokedAt, &replacedBy, &createdAt); err != nil {
		return nil, wrapNoRows(err, "get refresh token")
	}

	t.ReplacedBy = optionalString(replacedBy)

	var err error
	if t.ExpiresAt, err = parseTime(expiresAt); err != nil {
		return nil, err
	}
	if t.RevokedAt, err = nullableTime(revokedAt); err != nil {
		return nil, err
	}
	if t.CreatedAt, err = parseTime(createdAt); err != nil {
		return nil, err
	}
	return &t, nil
}

func wrapNoRows(err error, action string) error {
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return fmt.Errorf("auth: %s: %w", action, err)
}

// optionalString reads a nullable text column as a pointer.
func optionalString(ns sql.NullString) *string {
	if !ns.Valid {
		return nil
	}
	return &ns.String
}

// isUniqueViolation reports whether an error is a SQLite uniqueness failure.
//
// The driver exposes no typed error, so this matches the message. The only
// unique constraint a user can trip is the case-insensitive email index.
func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "UNIQUE constraint failed") ||
		strings.Contains(msg, "constraint failed: UNIQUE")
}
