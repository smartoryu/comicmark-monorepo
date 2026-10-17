package auth

import (
	"context"
	"errors"
	"log/slog"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"comicmark/go-modular/internal/pkg/jwt"
	"comicmark/go-modular/internal/pkg/mailer"
)

// --- test doubles -----------------------------------------------------------

// fakeMailer records sent messages and can be made to fail.
type fakeMailer struct {
	mu       sync.Mutex
	messages []mailer.Message
	err      error
}

func (m *fakeMailer) Send(_ context.Context, msg mailer.Message) error {
	if m.err != nil {
		return m.err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.messages = append(m.messages, msg)
	return nil
}

func (m *fakeMailer) all() []mailer.Message {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]mailer.Message(nil), m.messages...)
}

func (m *fakeMailer) last(t *testing.T) mailer.Message {
	t.Helper()

	msgs := m.all()
	require.NotEmpty(t, msgs, "expected an email to have been sent")
	return msgs[len(msgs)-1]
}

func newTestNotifier(t *testing.T, repo *Repository, m *fakeMailer, verifyTTL, resetTTL time.Duration) *notifier {
	t.Helper()

	built, err := NewNotifier(NotifierOptions{
		Repository:            repo,
		Mailer:                m,
		Logger:                slog.New(slog.DiscardHandler),
		BaseURL:               "http://localhost:5173",
		VerificationTokenTTL:  verifyTTL,
		PasswordResetTokenTTL: resetTTL,
	})
	require.NoError(t, err)

	concrete, ok := built.(*notifier)
	require.True(t, ok)
	return concrete
}

// tokenFromLink extracts a query parameter from an emailed link.
func tokenFromLink(t *testing.T, link, param string) string {
	t.Helper()

	u, err := url.Parse(link)
	require.NoError(t, err)
	token := u.Query().Get(param)
	require.NotEmpty(t, token, "link %q should carry %s", link, param)
	return token
}

// --- token generation -------------------------------------------------------

func TestGenerateOneTimeTokenIsUnpredictableAndHashedForStorage(t *testing.T) {
	seen := make(map[string]bool, 100)

	for range 100 {
		raw, hash, err := generateOneTimeToken()
		require.NoError(t, err)

		require.False(t, seen[raw], "tokens must not repeat")
		seen[raw] = true

		// 32 bytes in raw base64url is 43 characters.
		assert.Len(t, raw, 43)
		// The stored value must be the fingerprint, never the raw token.
		assert.Equal(t, jwt.HashToken(raw), hash)
		assert.NotEqual(t, raw, hash)
		assert.Len(t, hash, 64)
	}
}

func TestGenerateOneTimeTokenIsURLSafe(t *testing.T) {
	raw, _, err := generateOneTimeToken()
	require.NoError(t, err)

	// The token travels in a query string, so it must survive round-tripping.
	assert.Equal(t, raw, url.QueryEscape(raw))
	assert.NotContains(t, raw, "+")
	assert.NotContains(t, raw, "/")
	assert.NotContains(t, raw, "=")
}

// --- notifier ---------------------------------------------------------------

func TestNewNotifierValidatesItsOptions(t *testing.T) {
	repo := newTestRepository(t)
	valid := NotifierOptions{
		Repository:            repo,
		Mailer:                &fakeMailer{},
		BaseURL:               "http://localhost:5173",
		VerificationTokenTTL:  24 * time.Hour,
		PasswordResetTokenTTL: time.Hour,
	}

	t.Run("repository", func(t *testing.T) {
		o := valid
		o.Repository = nil
		_, err := NewNotifier(o)
		require.Error(t, err)
	})

	t.Run("mailer", func(t *testing.T) {
		o := valid
		o.Mailer = nil
		_, err := NewNotifier(o)
		require.Error(t, err)
	})

	t.Run("verification TTL", func(t *testing.T) {
		o := valid
		o.VerificationTokenTTL = 0
		_, err := NewNotifier(o)
		require.Error(t, err)
	})

	t.Run("reset TTL", func(t *testing.T) {
		o := valid
		o.PasswordResetTokenTTL = 0
		_, err := NewNotifier(o)
		require.Error(t, err)
	})

	t.Run("relative base URL", func(t *testing.T) {
		o := valid
		o.BaseURL = "/relative"
		_, err := NewNotifier(o)
		require.Error(t, err)
	})

	t.Run("valid", func(t *testing.T) {
		_, err := NewNotifier(valid)
		require.NoError(t, err)
	})
}

func TestSendEmailVerificationStoresAHashedTokenAndEmailsTheLink(t *testing.T) {
	repo := newTestRepository(t)
	m := &fakeMailer{}
	n := newTestNotifier(t, repo, m, 24*time.Hour, time.Hour)
	ctx := context.Background()

	user := &User{Email: "reader@example.com", DisplayName: "Risf"}
	require.NoError(t, repo.CreateUser(ctx, user, "hash"))

	require.NoError(t, n.SendEmailVerification(ctx, user))

	msg := m.last(t)
	assert.Equal(t, "reader@example.com", msg.To)
	assert.Contains(t, msg.Subject, "Confirm your ComicMark email address")
	// Both body alternatives must carry the link.
	assert.Contains(t, msg.HTML, msg.Link)
	assert.Contains(t, msg.Text, msg.Link)

	token := tokenFromLink(t, msg.Link, QueryVerifyToken)

	// The raw token must not be in the database, only its hash.
	var stored string
	require.NoError(t, repo.q.QueryRowContext(ctx,
		`SELECT token_hash FROM one_time_tokens WHERE user_id = ?`, user.ID).Scan(&stored))
	assert.Equal(t, jwt.HashToken(token), stored)
	assert.NotContains(t, stored, token)

	// And it must actually work.
	consumed, err := newTokenStore(repo).consume(ctx, PurposeEmailVerification, token)
	require.NoError(t, err)
	require.Equal(t, user.ID, consumed.UserID)
}

func TestSendPasswordResetUsesItsOwnParameterAndSubject(t *testing.T) {
	repo := newTestRepository(t)
	m := &fakeMailer{}
	n := newTestNotifier(t, repo, m, 24*time.Hour, time.Hour)
	ctx := context.Background()

	user := &User{Email: "reader@example.com"}
	require.NoError(t, repo.CreateUser(ctx, user, "hash"))

	require.NoError(t, n.SendPasswordReset(ctx, user))

	msg := m.last(t)
	assert.Contains(t, msg.Subject, "Reset your ComicMark password")
	tokenFromLink(t, msg.Link, QueryResetToken)

	// A reset token must not be usable as a verification token.
	raw := tokenFromLink(t, msg.Link, QueryResetToken)
	_, err := newTokenStore(repo).consume(ctx, PurposeEmailVerification, raw)
	require.ErrorIs(t, err, ErrTokenInvalid)
}

// Sending a verification email to an account that is already verified would
// invalidate a link the user may still be using, for no benefit.
func TestSendEmailVerificationSkipsAVerifiedAccount(t *testing.T) {
	repo := newTestRepository(t)
	m := &fakeMailer{}
	n := newTestNotifier(t, repo, m, 24*time.Hour, time.Hour)
	ctx := context.Background()

	user := &User{Email: "reader@example.com"}
	require.NoError(t, repo.CreateUser(ctx, user, "hash"))
	require.NoError(t, repo.MarkEmailVerified(ctx, user.ID, time.Now()))

	user, err := repo.GetUserByID(ctx, user.ID)
	require.NoError(t, err)

	require.NoError(t, n.SendEmailVerification(ctx, user))
	assert.Empty(t, m.all(), "no email should be sent for an already verified account")
}

func TestNotifierReportsMailerFailures(t *testing.T) {
	repo := newTestRepository(t)
	m := &fakeMailer{err: errors.New("smtp is down")}
	n := newTestNotifier(t, repo, m, 24*time.Hour, time.Hour)
	ctx := context.Background()

	user := &User{Email: "reader@example.com"}
	require.NoError(t, repo.CreateUser(ctx, user, "hash"))

	err := n.SendEmailVerification(ctx, user)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "verification email")
}

func TestNotifierExpiryMatchesTheConfiguredTTL(t *testing.T) {
	repo := newTestRepository(t)
	m := &fakeMailer{}
	n := newTestNotifier(t, repo, m, 24*time.Hour, time.Hour)
	ctx := context.Background()

	user := &User{Email: "reader@example.com"}
	require.NoError(t, repo.CreateUser(ctx, user, "hash"))

	before := time.Now()
	require.NoError(t, n.SendEmailVerification(ctx, user))

	var expiresAt string
	require.NoError(t, repo.q.QueryRowContext(ctx,
		`SELECT expires_at FROM one_time_tokens WHERE user_id = ?`, user.ID).Scan(&expiresAt))

	expiry, err := parseTime(expiresAt)
	require.NoError(t, err)
	// 24 hours from now, within a second of tolerance.
	assert.WithinDuration(t, before.Add(24*time.Hour), expiry, 2*time.Second)
}

func TestResendingInvalidatesThePreviousLink(t *testing.T) {
	repo := newTestRepository(t)
	m := &fakeMailer{}
	n := newTestNotifier(t, repo, m, 24*time.Hour, time.Hour)
	ctx := context.Background()

	user := &User{Email: "reader@example.com"}
	require.NoError(t, repo.CreateUser(ctx, user, "hash"))

	require.NoError(t, n.SendEmailVerification(ctx, user))
	first := tokenFromLink(t, m.last(t).Link, QueryVerifyToken)

	require.NoError(t, n.SendEmailVerification(ctx, user))
	second := tokenFromLink(t, m.last(t).Link, QueryVerifyToken)

	require.NotEqual(t, first, second)

	_, err := newTokenStore(repo).consume(ctx, PurposeEmailVerification, first)
	require.ErrorIs(t, err, ErrTokenInvalid, "the superseded link must stop working")

	_, err = newTokenStore(repo).consume(ctx, PurposeEmailVerification, second)
	require.NoError(t, err)
}

// --- links ------------------------------------------------------------------

func TestActionLinkEncodesTheTokenAndKeepsExistingQueryParameters(t *testing.T) {
	link := actionLink("http://localhost:5173", QueryResetToken, "abc+/=def")

	u, err := url.Parse(link)
	require.NoError(t, err)
	assert.Equal(t, "http://localhost:5173", u.Scheme+"://"+u.Host)
	assert.Equal(t, "abc+/=def", u.Query().Get(QueryResetToken),
		"the token must survive encoding and decoding")

	// A base URL that already carries parameters keeps them.
	link = actionLink("https://app.example.com/?utm_source=email", QueryVerifyToken, "tok")
	u, err = url.Parse(link)
	require.NoError(t, err)
	assert.Equal(t, "email", u.Query().Get("utm_source"))
	assert.Equal(t, "tok", u.Query().Get(QueryVerifyToken))
}

func TestHumanDurationReadsNaturallyInEmailCopy(t *testing.T) {
	for d, want := range map[time.Duration]string{
		24 * time.Hour:   "24 hours",
		48 * time.Hour:   "2 days",
		time.Hour:        "1 hour",
		3 * time.Hour:    "3 hours",
		time.Minute:      "1 minute",
		15 * time.Minute: "15 minutes",
		30 * time.Second: "a few seconds",
	} {
		assert.Equal(t, want, humanDuration(d), "for %s", d)
	}
}

// --- token store ------------------------------------------------------------

func TestTokenStoreRejectsEmptyAndUnknownTokens(t *testing.T) {
	repo := newTestRepository(t)
	store := newTokenStore(repo)
	ctx := context.Background()

	_, err := store.consume(ctx, PurposeEmailVerification, "")
	require.ErrorIs(t, err, ErrTokenInvalid)

	_, err = store.consume(ctx, PurposeEmailVerification, "   ")
	require.ErrorIs(t, err, ErrTokenInvalid)

	_, err = store.consume(ctx, PurposeEmailVerification, "never-issued")
	require.ErrorIs(t, err, ErrTokenInvalid)
}

func TestTokenStoreInvalidateBurnsOutstandingTokens(t *testing.T) {
	repo := newTestRepository(t)
	m := &fakeMailer{}
	n := newTestNotifier(t, repo, m, 24*time.Hour, time.Hour)
	store := newTokenStore(repo)
	ctx := context.Background()

	user := &User{Email: "reader@example.com"}
	require.NoError(t, repo.CreateUser(ctx, user, "hash"))

	require.NoError(t, n.SendEmailVerification(ctx, user))
	token := tokenFromLink(t, m.last(t).Link, QueryVerifyToken)

	require.NoError(t, store.invalidateOtherTokens(ctx, user.ID, PurposeEmailVerification))

	_, err := store.consume(ctx, PurposeEmailVerification, token)
	require.ErrorIs(t, err, ErrTokenInvalid)
}

func TestTokenStoreIsolatesPurposes(t *testing.T) {
	repo := newTestRepository(t)
	m := &fakeMailer{}
	n := newTestNotifier(t, repo, m, 24*time.Hour, time.Hour)
	store := newTokenStore(repo)
	ctx := context.Background()

	user := &User{Email: "reader@example.com"}
	require.NoError(t, repo.CreateUser(ctx, user, "hash"))

	require.NoError(t, n.SendPasswordReset(ctx, user))
	resetToken := tokenFromLink(t, m.last(t).Link, QueryResetToken)

	_, err := store.consume(ctx, PurposeEmailVerification, resetToken)
	require.ErrorIs(t, err, ErrTokenInvalid, "a reset link must not verify an email")
}

func TestNotifierSendsADistinctTokenEachTimeAndOnlyTheNewestWorks(t *testing.T) {
	repo := newTestRepository(t)
	m := &fakeMailer{}
	n := newTestNotifier(t, repo, m, 24*time.Hour, time.Hour)
	ctx := context.Background()

	user := &User{Email: "reader@example.com"}
	require.NoError(t, repo.CreateUser(ctx, user, "hash"))

	var links []string
	for range 3 {
		require.NoError(t, n.SendPasswordReset(ctx, user))
		links = append(links, m.last(t).Link)
	}

	require.Len(t, links, 3)
	assert.NotEqual(t, links[0], links[1])
	assert.NotEqual(t, links[1], links[2])
	assert.NotEqual(t, links[0], links[2])

	store := newTokenStore(repo)
	tokens := make([]string, 0, len(links))
	for _, link := range links {
		tokens = append(tokens, tokenFromLink(t, link, QueryResetToken))
	}

	// Superseded links must not work.
	for _, superseded := range tokens[:2] {
		_, err := store.consume(ctx, PurposePasswordReset, superseded)
		require.ErrorIs(t, err, ErrTokenInvalid)
	}

	// Exactly the newest one does.
	_, err := store.consume(ctx, PurposePasswordReset, tokens[2])
	require.NoError(t, err)

	// And only one token row is left outstanding, so the table does not
	// accumulate one row per resend.
	var outstanding int
	require.NoError(t, repo.q.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM one_time_tokens
		WHERE user_id = ? AND purpose = ? AND consumed_at IS NULL`,
		user.ID, PurposePasswordReset).Scan(&outstanding))
	assert.Equal(t, 0, outstanding, "consuming the newest token leaves none outstanding")
}
