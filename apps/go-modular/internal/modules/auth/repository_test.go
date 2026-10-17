package auth

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"comicmark/go-modular/internal/pkg/database"
)

func newTestRepo(t *testing.T) *Repository {
	t.Helper()

	db, err := database.Open(context.Background(), filepath.Join(t.TempDir(), "auth.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })

	_, err = database.NewMigrator(db).Up(context.Background())
	require.NoError(t, err)

	return NewRepository(db)
}

// createUser inserts a user with a password and returns it.
func createUser(t *testing.T, repo *Repository, email, passwordHash string) *User {
	t.Helper()

	u := &User{Email: email, DisplayName: "Test Reader"}
	require.NoError(t, repo.CreateUser(context.Background(), u, passwordHash))
	return u
}

func createSession(t *testing.T, repo *Repository, userID string) *Session {
	t.Helper()

	s := &Session{
		UserID:      userID,
		DeviceLabel: "Chrome on macOS",
		Platform:    "web",
		UserAgent:   "test-agent",
		IP:          "203.0.113.7",
	}
	require.NoError(t, repo.CreateSession(context.Background(), s))
	return s
}

func TestCreateUserRejectsADuplicateEmailRegardlessOfCase(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()

	createUser(t, repo, "Reader@Example.com", "hash")

	err := repo.CreateUser(ctx, &User{Email: "reader@example.com"}, "hash")
	require.ErrorIs(t, err, ErrEmailTaken)

	exists, err := repo.EmailExists(ctx, "READER@EXAMPLE.COM")
	require.NoError(t, err)
	assert.True(t, exists)
}

func TestGetUserByEmailIsCaseInsensitive(t *testing.T) {
	repo := newTestRepo(t)

	created := createUser(t, repo, "reader@example.com", "hash")

	got, err := repo.GetUserByEmail(context.Background(), "Reader@Example.COM")
	require.NoError(t, err)
	assert.Equal(t, created.ID, got.ID)
	assert.False(t, got.EmailVerified(), "a new account starts unverified")
}

func TestGetUserByEmailReturnsNotFound(t *testing.T) {
	repo := newTestRepo(t)

	_, err := repo.GetUserByEmail(context.Background(), "nobody@example.com")
	require.ErrorIs(t, err, ErrNotFound)
}

func TestGetUserWithPasswordReturnsTheHashSeparately(t *testing.T) {
	repo := newTestRepo(t)
	created := createUser(t, repo, "reader@example.com", "$argon2id$stored")

	user, hash, err := repo.GetUserWithPassword(context.Background(), "reader@example.com")
	require.NoError(t, err)
	assert.Equal(t, created.ID, user.ID)
	assert.Equal(t, "$argon2id$stored", hash)
}

func TestGetUserWithPasswordReportsAnAbsentHashForSocialAccounts(t *testing.T) {
	repo := newTestRepo(t)
	createUser(t, repo, "social@example.com", "")

	_, hash, err := repo.GetUserWithPassword(context.Background(), "social@example.com")
	require.NoError(t, err)
	assert.Empty(t, hash, "a social-only account has no password")
}

func TestMarkEmailVerifiedIsIdempotent(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	user := createUser(t, repo, "reader@example.com", "hash")

	first := time.Now()
	require.NoError(t, repo.MarkEmailVerified(ctx, user.ID, first))

	got, err := repo.GetUserByID(ctx, user.ID)
	require.NoError(t, err)
	require.NotNil(t, got.EmailVerifiedAt)
	assert.WithinDuration(t, first, *got.EmailVerifiedAt, time.Second)

	// Verifying twice must not fail or move the timestamp.
	require.NoError(t, repo.MarkEmailVerified(ctx, user.ID, first.Add(time.Hour)))

	got, err = repo.GetUserByID(ctx, user.ID)
	require.NoError(t, err)
	require.NotNil(t, got.EmailVerifiedAt)
	assert.WithinDuration(t, first, *got.EmailVerifiedAt, time.Second,
		"the original verification time should be preserved")
}

func TestMarkEmailVerifiedReportsAMissingUser(t *testing.T) {
	repo := newTestRepo(t)

	err := repo.MarkEmailVerified(context.Background(), "no-such-user", time.Now())
	require.ErrorIs(t, err, ErrNotFound)
}

func TestUpdatePasswordHash(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	user := createUser(t, repo, "reader@example.com", "old-hash")

	require.NoError(t, repo.UpdatePasswordHash(ctx, user.ID, "new-hash", time.Now()))

	hash, err := repo.PasswordHash(ctx, user.ID)
	require.NoError(t, err)
	assert.Equal(t, "new-hash", hash)

	err = repo.UpdatePasswordHash(ctx, "no-such-user", "new-hash", time.Now())
	require.ErrorIs(t, err, ErrNotFound)
}

func TestIdentityRoundTrip(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	user := createUser(t, repo, "reader@example.com", "hash")

	identity := &AuthIdentity{
		UserID:          user.ID,
		Provider:        ProviderFirebase,
		ProviderSubject: "firebase-uid-1",
		Email:           "reader@example.com",
	}
	require.NoError(t, repo.CreateIdentity(ctx, identity))

	got, err := repo.GetIdentity(ctx, ProviderFirebase, "firebase-uid-1")
	require.NoError(t, err)
	assert.Equal(t, user.ID, got.UserID)
	assert.Equal(t, "reader@example.com", got.Email)

	_, err = repo.GetIdentity(ctx, ProviderFirebase, "someone-else")
	require.ErrorIs(t, err, ErrNotFound)
}

// --- sessions ---------------------------------------------------------------

func TestListSessionsReturnsMostRecentlyUsedFirst(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	user := createUser(t, repo, "reader@example.com", "hash")

	old := createSession(t, repo, user.ID)
	recent := createSession(t, repo, user.ID)

	// Make the second session the more recent one.
	require.NoError(t, repo.TouchSession(ctx, recent.ID, time.Now().Add(time.Hour)))

	sessions, err := repo.ListSessions(ctx, user.ID, false)
	require.NoError(t, err)
	require.Len(t, sessions, 2)
	assert.Equal(t, recent.ID, sessions[0].ID)
	assert.Equal(t, old.ID, sessions[1].ID)
}

func TestListSessionsHidesRevokedOnesByDefault(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	user := createUser(t, repo, "reader@example.com", "hash")

	keep := createSession(t, repo, user.ID)
	drop := createSession(t, repo, user.ID)
	require.NoError(t, repo.RevokeSession(ctx, drop.ID, time.Now()))

	sessions, err := repo.ListSessions(ctx, user.ID, false)
	require.NoError(t, err)
	require.Len(t, sessions, 1)
	assert.Equal(t, keep.ID, sessions[0].ID)

	// The revoked row still exists for auditing.
	all, err := repo.ListSessions(ctx, user.ID, true)
	require.NoError(t, err)
	assert.Len(t, all, 2)
}

func TestListSessionsIsScopedToTheUser(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	mine := createUser(t, repo, "mine@example.com", "hash")
	theirs := createUser(t, repo, "theirs@example.com", "hash")

	createSession(t, repo, mine.ID)
	createSession(t, repo, theirs.ID)

	sessions, err := repo.ListSessions(ctx, mine.ID, false)
	require.NoError(t, err)
	require.Len(t, sessions, 1)
	assert.Equal(t, mine.ID, sessions[0].UserID)
}

func TestRevokeSessionAlsoRevokesItsRefreshTokens(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	user := createUser(t, repo, "reader@example.com", "hash")
	session := createSession(t, repo, user.ID)

	token := &RefreshToken{
		UserID:    user.ID,
		SessionID: session.ID,
		TokenHash: "hash-1",
		ExpiresAt: time.Now().Add(time.Hour),
	}
	require.NoError(t, repo.CreateRefreshToken(ctx, token))

	require.NoError(t, repo.RevokeSession(ctx, session.ID, time.Now()))

	got, err := repo.GetRefreshTokenByHash(ctx, "hash-1")
	require.NoError(t, err)
	assert.False(t, got.Active(), "revoking a device must also kill its refresh tokens")

	stored, err := repo.GetSession(ctx, session.ID)
	require.NoError(t, err)
	assert.False(t, stored.Active())
}

func TestRevokeSessionIsIdempotent(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	user := createUser(t, repo, "reader@example.com", "hash")
	session := createSession(t, repo, user.ID)

	require.NoError(t, repo.RevokeSession(ctx, session.ID, time.Now()))
	require.NoError(t, repo.RevokeSession(ctx, session.ID, time.Now()),
		"revoking an already revoked session should not be an error")
}

func TestRevokeSessionReportsAnUnknownSession(t *testing.T) {
	repo := newTestRepo(t)

	err := repo.RevokeSession(context.Background(), "no-such-session", time.Now())
	require.ErrorIs(t, err, ErrNotFound)
}

func TestRevokeAllSessionsAndTokens(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	user := createUser(t, repo, "reader@example.com", "hash")

	first := createSession(t, repo, user.ID)
	second := createSession(t, repo, user.ID)
	for i, s := range []*Session{first, second} {
		require.NoError(t, repo.CreateRefreshToken(ctx, &RefreshToken{
			UserID:    user.ID,
			SessionID: s.ID,
			TokenHash: string(rune('a' + i)),
			ExpiresAt: time.Now().Add(time.Hour),
		}))
	}

	require.NoError(t, repo.RevokeAllSessions(ctx, user.ID, time.Now()))

	active, err := repo.ListSessions(ctx, user.ID, false)
	require.NoError(t, err)
	assert.Empty(t, active, "a password reset must sign every device out")

	for _, hash := range []string{"a", "b"} {
		token, err := repo.GetRefreshTokenByHash(ctx, hash)
		require.NoError(t, err)
		assert.False(t, token.Active())
	}
}

func TestRevokeOtherSessionsKeepsTheCaller(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	user := createUser(t, repo, "reader@example.com", "hash")

	current := createSession(t, repo, user.ID)
	other := createSession(t, repo, user.ID)

	require.NoError(t, repo.RevokeOtherSessions(ctx, user.ID, current.ID, time.Now()))

	active, err := repo.ListSessions(ctx, user.ID, false)
	require.NoError(t, err)
	require.Len(t, active, 1)
	assert.Equal(t, current.ID, active[0].ID)

	stored, err := repo.GetSession(ctx, other.ID)
	require.NoError(t, err)
	assert.False(t, stored.Active())
}

func TestRenameSession(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	user := createUser(t, repo, "reader@example.com", "hash")
	session := createSession(t, repo, user.ID)

	require.NoError(t, repo.RenameSession(ctx, session.ID, "My laptop"))

	got, err := repo.GetSession(ctx, session.ID)
	require.NoError(t, err)
	assert.Equal(t, "My laptop", got.DeviceLabel)

	err = repo.RenameSession(ctx, "no-such-session", "x")
	require.ErrorIs(t, err, ErrNotFound)
}

// --- refresh token rotation -------------------------------------------------

func TestRotateRefreshTokenSupersedesTheOldRow(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	user := createUser(t, repo, "reader@example.com", "hash")
	session := createSession(t, repo, user.ID)

	current := &RefreshToken{
		UserID: user.ID, SessionID: session.ID,
		TokenHash: "old-hash", ExpiresAt: time.Now().Add(time.Hour),
	}
	require.NoError(t, repo.CreateRefreshToken(ctx, current))

	next := &RefreshToken{
		ID: newID(), UserID: user.ID, SessionID: session.ID,
		TokenHash: "new-hash", FamilyID: current.FamilyID,
		ExpiresAt: time.Now().Add(time.Hour),
	}
	require.NoError(t, repo.RotateRefreshToken(ctx, current, next, time.Now()))

	old, err := repo.GetRefreshTokenByHash(ctx, "old-hash")
	require.NoError(t, err)
	assert.False(t, old.Active())
	require.NotNil(t, old.ReplacedBy)
	assert.Equal(t, next.ID, *old.ReplacedBy)

	fresh, err := repo.GetRefreshTokenByHash(ctx, "new-hash")
	require.NoError(t, err)
	assert.True(t, fresh.Active())
	assert.Equal(t, current.FamilyID, fresh.FamilyID, "the chain must stay intact")
}

// Rotating the same token twice is the signature of a stolen credential being
// replayed, and must be reported rather than silently tolerated.
func TestRotateRefreshTokenDetectsReplay(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	user := createUser(t, repo, "reader@example.com", "hash")
	session := createSession(t, repo, user.ID)

	current := &RefreshToken{
		UserID: user.ID, SessionID: session.ID,
		TokenHash: "old-hash", ExpiresAt: time.Now().Add(time.Hour),
	}
	require.NoError(t, repo.CreateRefreshToken(ctx, current))

	next := &RefreshToken{
		ID: newID(), UserID: user.ID, SessionID: session.ID,
		TokenHash: "new-hash", FamilyID: current.FamilyID, ExpiresAt: time.Now().Add(time.Hour),
	}
	require.NoError(t, repo.RotateRefreshToken(ctx, current, next, time.Now()))

	// A replay of the already-rotated token.
	replay := &RefreshToken{
		ID: newID(), UserID: user.ID, SessionID: session.ID,
		TokenHash: "replay-hash", FamilyID: current.FamilyID, ExpiresAt: time.Now().Add(time.Hour),
	}
	err := repo.RotateRefreshToken(ctx, current, replay, time.Now())
	require.ErrorIs(t, err, ErrTokenReused)

	// The replay must not have inserted a usable token.
	_, err = repo.GetRefreshTokenByHash(ctx, "replay-hash")
	require.ErrorIs(t, err, ErrNotFound, "a rejected rotation must roll back its insert")
}

func TestRevokeFamilyKillsEveryLinkInTheChain(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	user := createUser(t, repo, "reader@example.com", "hash")

	const family = "family-1"
	for _, hash := range []string{"h1", "h2", "h3"} {
		session := createSession(t, repo, user.ID)
		require.NoError(t, repo.CreateRefreshToken(ctx, &RefreshToken{
			UserID: user.ID, SessionID: session.ID, TokenHash: hash,
			FamilyID: family, ExpiresAt: time.Now().Add(time.Hour),
		}))
	}

	require.NoError(t, repo.RevokeFamily(ctx, family, time.Now()))

	for _, hash := range []string{"h1", "h2", "h3"} {
		token, err := repo.GetRefreshTokenByHash(ctx, hash)
		require.NoError(t, err)
		assert.False(t, token.Active(), "%s should be revoked with its family", hash)
	}
}

func TestDeleteExpiredRefreshTokens(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	user := createUser(t, repo, "reader@example.com", "hash")
	session := createSession(t, repo, user.ID)

	require.NoError(t, repo.CreateRefreshToken(ctx, &RefreshToken{
		UserID: user.ID, SessionID: session.ID, TokenHash: "expired",
		ExpiresAt: time.Now().Add(-time.Hour),
	}))
	require.NoError(t, repo.CreateRefreshToken(ctx, &RefreshToken{
		UserID: user.ID, SessionID: session.ID, TokenHash: "valid",
		ExpiresAt: time.Now().Add(time.Hour),
	}))

	deleted, err := repo.DeleteExpiredRefreshTokens(ctx, time.Now())
	require.NoError(t, err)
	assert.Equal(t, int64(1), deleted)

	_, err = repo.GetRefreshTokenByHash(ctx, "valid")
	require.NoError(t, err, "the valid token must survive pruning")
}

// --- one-time tokens --------------------------------------------------------

func TestOneTimeTokenIsSingleUse(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	user := createUser(t, repo, "reader@example.com", "hash")

	token := &OneTimeToken{
		UserID: user.ID, Purpose: PurposeEmailVerification,
		TokenHash: "ot-1", ExpiresAt: time.Now().Add(time.Hour),
	}
	require.NoError(t, repo.CreateOneTimeToken(ctx, token))

	consumed, err := repo.ConsumeOneTimeToken(ctx, PurposeEmailVerification, "ot-1", time.Now())
	require.NoError(t, err)
	assert.Equal(t, user.ID, consumed.UserID)
	assert.True(t, consumed.Consumed())

	_, err = repo.ConsumeOneTimeToken(ctx, PurposeEmailVerification, "ot-1", time.Now())
	require.ErrorIs(t, err, ErrTokenInvalid, "a verification link must work exactly once")
}

func TestConsumeOneTimeTokenRejectsWrongPurposeAndExpiry(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()

	// Separate accounts, because issuing a second token for the same user and
	// purpose would supersede the first (covered by its own test below).
	withToken := createUser(t, repo, "with-token@example.com", "hash")
	withExpired := createUser(t, repo, "with-expired@example.com", "hash")

	require.NoError(t, repo.CreateOneTimeToken(ctx, &OneTimeToken{
		UserID: withToken.ID, Purpose: PurposeEmailVerification,
		TokenHash: "ot-purpose", ExpiresAt: time.Now().Add(time.Hour),
	}))
	require.NoError(t, repo.CreateOneTimeToken(ctx, &OneTimeToken{
		UserID: withExpired.ID, Purpose: PurposeEmailVerification,
		TokenHash: "ot-expired", ExpiresAt: time.Now().Add(-time.Minute),
	}))

	// A verification token must not be usable as a password reset token.
	_, err := repo.ConsumeOneTimeToken(ctx, PurposePasswordReset, "ot-purpose", time.Now())
	require.ErrorIs(t, err, ErrTokenInvalid)

	_, err = repo.ConsumeOneTimeToken(ctx, PurposeEmailVerification, "ot-expired", time.Now())
	require.ErrorIs(t, err, ErrTokenInvalid, "an expired token must not be accepted")

	// The wrong-purpose attempt must not have burned the token.
	_, err = repo.ConsumeOneTimeToken(ctx, PurposeEmailVerification, "ot-purpose", time.Now())
	require.NoError(t, err)
}

func TestCreateOneTimeTokenSupersedesTheOutstandingOne(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	user := createUser(t, repo, "reader@example.com", "hash")

	require.NoError(t, repo.CreateOneTimeToken(ctx, &OneTimeToken{
		UserID: user.ID, Purpose: PurposeEmailVerification,
		TokenHash: "first", ExpiresAt: time.Now().Add(time.Hour),
	}))
	require.NoError(t, repo.CreateOneTimeToken(ctx, &OneTimeToken{
		UserID: user.ID, Purpose: PurposeEmailVerification,
		TokenHash: "second", ExpiresAt: time.Now().Add(time.Hour),
	}))

	_, err := repo.ConsumeOneTimeToken(ctx, PurposeEmailVerification, "first", time.Now())
	require.ErrorIs(t, err, ErrTokenInvalid, "only the newest link should work")

	_, err = repo.ConsumeOneTimeToken(ctx, PurposeEmailVerification, "second", time.Now())
	require.NoError(t, err)
}

func TestCreateOneTimeTokenKeepsOtherPurposes(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	user := createUser(t, repo, "reader@example.com", "hash")

	require.NoError(t, repo.CreateOneTimeToken(ctx, &OneTimeToken{
		UserID: user.ID, Purpose: PurposePasswordReset,
		TokenHash: "reset", ExpiresAt: time.Now().Add(time.Hour),
	}))
	require.NoError(t, repo.CreateOneTimeToken(ctx, &OneTimeToken{
		UserID: user.ID, Purpose: PurposeEmailVerification,
		TokenHash: "verify", ExpiresAt: time.Now().Add(time.Hour),
	}))

	// Issuing a verification token must not disturb a pending reset.
	_, err := repo.ConsumeOneTimeToken(ctx, PurposePasswordReset, "reset", time.Now())
	require.NoError(t, err)
}

// --- transactions -----------------------------------------------------------

func TestWithTxRollsBackOnError(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()

	sentinel := assert.AnError
	err := repo.WithTx(ctx, func(tx *Repository) error {
		require.NoError(t, tx.CreateUser(ctx, &User{Email: "rolled-back@example.com"}, "hash"))
		return sentinel
	})
	require.ErrorIs(t, err, sentinel)

	exists, err := repo.EmailExists(ctx, "rolled-back@example.com")
	require.NoError(t, err)
	assert.False(t, exists, "a failed transaction must leave nothing behind")
}

func TestWithTxRejectsNesting(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()

	err := repo.WithTx(ctx, func(tx *Repository) error {
		return tx.WithTx(ctx, func(*Repository) error { return nil })
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nested")
}

// --- timestamps -------------------------------------------------------------

// Timestamps are stored as text and compared lexicographically, so the encoding
// has to sort in the same order as the instants it represents.
func TestStoredTimestampsSortLexicographically(t *testing.T) {
	base := time.Date(2026, 10, 17, 10, 0, 0, 0, time.UTC)

	var previous string
	for i, instant := range []time.Time{
		base,
		base.Add(time.Millisecond),
		base.Add(time.Second),
		base.Add(time.Minute),
		base.Add(24 * time.Hour),
	} {
		encoded := formatTime(instant)
		assert.Len(t, encoded, len(formatTime(base)), "fixed width keeps comparisons correct")
		if i > 0 {
			assert.Greater(t, encoded, previous, "later instants must sort after earlier ones")
		}
		previous = encoded
	}
}

func TestParseTimeRoundTrips(t *testing.T) {
	original := time.Date(2026, 10, 17, 10, 30, 45, 123_000_000, time.UTC)

	parsed, err := parseTime(formatTime(original))
	require.NoError(t, err)
	assert.True(t, original.Equal(parsed))
}
