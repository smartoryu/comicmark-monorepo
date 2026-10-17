package auth

import (
	"context"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newVerifiableService returns a service plus a notifier that actually issues
// tokens and emails links, so the full flows can be exercised.
func newVerifiableService(t *testing.T) (*testService, *fakeMailer) {
	t.Helper()

	repo := newTestRepository(t)
	m := &fakeMailer{}

	concreteNotifier := newTestNotifier(t, repo, m, 24*time.Hour, time.Hour)

	svc, err := NewService(Options{
		Repository:            repo,
		JWT:                   newTestJWT(t),
		Passwords:             testHasher(),
		Notifier:              concreteNotifier,
		Logger:                discardLoggerForTest(),
		BaseURL:               "http://localhost:5173",
		SessionIdleTTL:        30 * 24 * time.Hour,
		SessionTouchInterval:  5 * time.Minute,
		VerificationTokenTTL:  24 * time.Hour,
		PasswordResetTokenTTL: time.Hour,
	})
	require.NoError(t, err)

	return &testService{Service: svc, repo: repo, notifier: &recordingNotifier{}, jwt: svc.jwt}, m
}

// --- email verification -----------------------------------------------------

func TestInitiateEmailVerificationSendsALink(t *testing.T) {
	svc, m := newVerifiableService(t)
	ctx := context.Background()

	registered, err := svc.Register(ctx, registerInput("reader@example.com", "correct horse battery"))
	require.NoError(t, err)

	// Registration already sent one; count from here.
	require.NoError(t, svc.InitiateEmailVerification(ctx, "reader@example.com"))

	msg := m.last(t)
	assert.Equal(t, "reader@example.com", msg.To)
	tokenFromLink(t, msg.Link, QueryVerifyToken)

	// Resend must not have changed who the account is.
	user, err := svc.Me(ctx, registered.User.ID)
	require.NoError(t, err)
	assert.False(t, user.EmailVerified())
}

// The endpoint must not be usable to discover which addresses are registered.
func TestInitiateEmailVerificationSaysNothingAboutUnknownAddresses(t *testing.T) {
	svc, m := newVerifiableService(t)

	require.NoError(t, svc.InitiateEmailVerification(context.Background(), "nobody@example.com"))
	assert.Empty(t, m.all(), "no email for an address that has no account")
}

func TestInitiateEmailVerificationSkipsAVerifiedAccount(t *testing.T) {
	svc, m := newVerifiableService(t)
	ctx := context.Background()

	registered, err := svc.Register(ctx, registerInput("reader@example.com", "correct horse battery"))
	require.NoError(t, err)

	token := tokenFromLink(t, m.last(t).Link, QueryVerifyToken)
	require.NoError(t, svc.VerifyEmail(ctx, token))

	before := len(m.all())
	require.NoError(t, svc.InitiateEmailVerification(ctx, "reader@example.com"))
	assert.Len(t, m.all(), before, "a verified account needs no further links")

	user, err := svc.Me(ctx, registered.User.ID)
	require.NoError(t, err)
	assert.True(t, user.EmailVerified())
}

func TestVerifyEmailMarksTheAccountVerified(t *testing.T) {
	svc, m := newVerifiableService(t)
	ctx := context.Background()

	registered, err := svc.Register(ctx, registerInput("reader@example.com", "correct horse battery"))
	require.NoError(t, err)

	token := tokenFromLink(t, m.last(t).Link, QueryVerifyToken)
	require.NoError(t, svc.VerifyEmail(ctx, token))

	user, err := svc.Me(ctx, registered.User.ID)
	require.NoError(t, err)
	require.True(t, user.EmailVerified())
	require.NotNil(t, user.EmailVerifiedAt)
}

func TestVerifyEmailIsSingleUse(t *testing.T) {
	svc, m := newVerifiableService(t)
	ctx := context.Background()

	_, err := svc.Register(ctx, registerInput("reader@example.com", "correct horse battery"))
	require.NoError(t, err)

	token := tokenFromLink(t, m.last(t).Link, QueryVerifyToken)
	require.NoError(t, svc.VerifyEmail(ctx, token))

	// Clicking the same link twice is a normal user action, but only one
	// request may succeed; the second must not silently pass.
	require.ErrorIs(t, svc.VerifyEmail(ctx, token), ErrTokenInvalid)
}

func TestVerifyEmailRejectsExpiredAndUnknownTokens(t *testing.T) {
	svc, m := newVerifiableService(t)
	ctx := context.Background()

	_, err := svc.Register(ctx, registerInput("reader@example.com", "correct horse battery"))
	require.NoError(t, err)

	token := tokenFromLink(t, m.last(t).Link, QueryVerifyToken)

	// Move past the 24 hour validity window.
	svc.now = func() time.Time { return time.Now().Add(25 * time.Hour) }
	// The token store has its own clock; align it with the service clock so the
	// expiry check and the consumption agree.
	svc.tokens.now = svc.now

	require.ErrorIs(t, svc.VerifyEmail(ctx, token), ErrTokenInvalid)
	require.ErrorIs(t, svc.VerifyEmail(ctx, "never-issued"), ErrTokenInvalid)
}

func TestVerifyEmailRejectsAResetToken(t *testing.T) {
	svc, m := newVerifiableService(t)
	ctx := context.Background()

	_, err := svc.Register(ctx, registerInput("reader@example.com", "correct horse battery"))
	require.NoError(t, err)
	require.NoError(t, svc.ForgotPassword(ctx, "reader@example.com"))

	resetToken := tokenFromLink(t, m.last(t).Link, QueryResetToken)

	require.ErrorIs(t, svc.VerifyEmail(ctx, resetToken), ErrTokenInvalid,
		"a password reset link must not confirm an email address")
}

func TestEmailVerificationRedirectURL(t *testing.T) {
	svc, _ := newVerifiableService(t)

	okURL, err := url.Parse(svc.EmailVerificationRedirectURL(true))
	require.NoError(t, err)
	assert.Equal(t, "http://localhost:5173", okURL.Scheme+"://"+okURL.Host)
	assert.Equal(t, "1", okURL.Query().Get(QueryVerified))

	failedURL, err := url.Parse(svc.EmailVerificationRedirectURL(false))
	require.NoError(t, err)
	assert.Equal(t, "0", failedURL.Query().Get(QueryVerified))
}

// --- password reset ---------------------------------------------------------

func TestForgotPasswordSendsAResetLink(t *testing.T) {
	svc, m := newVerifiableService(t)
	ctx := context.Background()

	_, err := svc.Register(ctx, registerInput("reader@example.com", "correct horse battery"))
	require.NoError(t, err)

	require.NoError(t, svc.ForgotPassword(ctx, "READER@example.com"))

	msg := m.last(t)
	assert.Contains(t, msg.Subject, "Reset your ComicMark password")
	tokenFromLink(t, msg.Link, QueryResetToken)
}

func TestForgotPasswordSaysNothingAboutUnknownAddresses(t *testing.T) {
	svc, m := newVerifiableService(t)

	require.NoError(t, svc.ForgotPassword(context.Background(), "nobody@example.com"))
	assert.Empty(t, m.all())
}

// An account that signs in through a provider has no password, so silently
// giving it one would be a surprise rather than a recovery.
func TestForgotPasswordSkipsSocialOnlyAccounts(t *testing.T) {
	svc, m := newVerifiableService(t)
	ctx := context.Background()

	user := &User{Email: "social@example.com"}
	require.NoError(t, svc.repo.CreateUser(ctx, user, ""))

	require.NoError(t, svc.ForgotPassword(ctx, "social@example.com"))
	assert.Empty(t, m.all())
}

func TestResetPasswordChangesThePasswordAndSignsEveryDeviceOut(t *testing.T) {
	svc, m := newVerifiableService(t)
	ctx := context.Background()

	// Two devices signed in.
	first, err := svc.Register(ctx, registerInput("reader@example.com", "correct horse battery"))
	require.NoError(t, err)
	second, err := svc.Login(ctx, LoginInput{
		Email: "reader@example.com", Password: "correct horse battery", Device: testDevice(),
	})
	require.NoError(t, err)

	require.NoError(t, svc.ForgotPassword(ctx, "reader@example.com"))
	token := tokenFromLink(t, m.last(t).Link, QueryResetToken)

	require.NoError(t, svc.ResetPassword(ctx, token, "a brand new password"))

	// The old password no longer works, the new one does.
	_, err = svc.Login(ctx, LoginInput{
		Email: "reader@example.com", Password: "correct horse battery", Device: testDevice(),
	})
	require.ErrorIs(t, err, ErrInvalidCredentials)

	_, err = svc.Login(ctx, LoginInput{
		Email: "reader@example.com", Password: "a brand new password", Device: testDevice(),
	})
	require.NoError(t, err)

	// Every session that existed before the reset is dead.
	for _, tokens := range []*TokenPair{first.Tokens, second.Tokens} {
		_, err := svc.Authenticate(ctx, tokens.AccessToken)
		require.ErrorIs(t, err, ErrSessionRevoked,
			"a reset must sign every device out so a stolen session dies with the old password")
	}
}

func TestResetPasswordMarksTheEmailVerified(t *testing.T) {
	svc, m := newVerifiableService(t)
	ctx := context.Background()

	registered, err := svc.Register(ctx, registerInput("reader@example.com", "correct horse battery"))
	require.NoError(t, err)
	require.False(t, registered.User.EmailVerified())

	require.NoError(t, svc.ForgotPassword(ctx, "reader@example.com"))
	token := tokenFromLink(t, m.last(t).Link, QueryResetToken)
	require.NoError(t, svc.ResetPassword(ctx, token, "a brand new password"))

	user, err := svc.Me(ctx, registered.User.ID)
	require.NoError(t, err)
	require.True(t, user.EmailVerified(),
		"receiving the reset link proves control of the inbox")
}

func TestResetPasswordIsSingleUse(t *testing.T) {
	svc, m := newVerifiableService(t)
	ctx := context.Background()

	_, err := svc.Register(ctx, registerInput("reader@example.com", "correct horse battery"))
	require.NoError(t, err)
	require.NoError(t, svc.ForgotPassword(ctx, "reader@example.com"))

	token := tokenFromLink(t, m.last(t).Link, QueryResetToken)
	require.NoError(t, svc.ResetPassword(ctx, token, "a brand new password"))

	require.ErrorIs(t, svc.ResetPassword(ctx, token, "another password entirely"), ErrTokenInvalid)
}

func TestResetPasswordRejectsBadTokensAndWeakPasswords(t *testing.T) {
	svc, m := newVerifiableService(t)
	ctx := context.Background()

	_, err := svc.Register(ctx, registerInput("reader@example.com", "correct horse battery"))
	require.NoError(t, err)
	require.NoError(t, svc.ForgotPassword(ctx, "reader@example.com"))

	token := tokenFromLink(t, m.last(t).Link, QueryResetToken)

	t.Run("weak password", func(t *testing.T) {
		err := svc.ResetPassword(ctx, token, "short")
		require.ErrorIs(t, err, ErrPasswordTooShort)
	})

	// A rejected password must not burn the token, or the user would have to
	// request a new link just for mistyping.
	t.Run("the token survives a rejected password", func(t *testing.T) {
		require.NoError(t, svc.ResetPassword(ctx, token, "a brand new password"))
	})

	t.Run("unknown token", func(t *testing.T) {
		require.ErrorIs(t, svc.ResetPassword(ctx, "never-issued", "a brand new password"), ErrTokenInvalid)
	})
}

func TestResetPasswordDoesNotAcceptAVerificationToken(t *testing.T) {
	svc, m := newVerifiableService(t)
	ctx := context.Background()

	_, err := svc.Register(ctx, registerInput("reader@example.com", "correct horse battery"))
	require.NoError(t, err)

	verifyToken := tokenFromLink(t, m.last(t).Link, QueryVerifyToken)

	err = svc.ResetPassword(ctx, verifyToken, "a brand new password")
	require.ErrorIs(t, err, ErrTokenInvalid)
}

func TestResendingAResetSupersedesThePreviousLink(t *testing.T) {
	svc, m := newVerifiableService(t)
	ctx := context.Background()

	_, err := svc.Register(ctx, registerInput("reader@example.com", "correct horse battery"))
	require.NoError(t, err)

	require.NoError(t, svc.ForgotPassword(ctx, "reader@example.com"))
	oldToken := tokenFromLink(t, m.last(t).Link, QueryResetToken)

	require.NoError(t, svc.ForgotPassword(ctx, "reader@example.com"))
	newToken := tokenFromLink(t, m.last(t).Link, QueryResetToken)

	require.NotEqual(t, oldToken, newToken)

	err = svc.ResetPassword(ctx, oldToken, "a brand new password")
	require.ErrorIs(t, err, ErrTokenInvalid, "only the newest link may work")

	require.NoError(t, svc.ResetPassword(ctx, newToken, "a brand new password"))
}

// A user who completes the reset flow must be able to sign in again.
func TestResetPasswordAllowsSigningInAfterwards(t *testing.T) {
	svc, m := newVerifiableService(t)
	ctx := context.Background()

	_, err := svc.Register(ctx, registerInput("reader@example.com", "correct horse battery"))
	require.NoError(t, err)
	require.NoError(t, svc.ForgotPassword(ctx, "reader@example.com"))

	token := tokenFromLink(t, m.last(t).Link, QueryResetToken)
	require.NoError(t, svc.ResetPassword(ctx, token, "a brand new password"))

	result, err := svc.Login(ctx, LoginInput{
		Email: "reader@example.com", Password: "a brand new password", Device: testDevice(),
	})
	require.NoError(t, err)
	require.NotNil(t, result.Tokens)

	// And the fresh session works.
	_, err = svc.Authenticate(ctx, result.Tokens.AccessToken)
	require.NoError(t, err)
}

func TestResetPasswordRejectsATokenForAMissingUser(t *testing.T) {
	svc, _ := newVerifiableService(t)
	ctx := context.Background()

	// The schema enforces the relationship, so a token cannot exist without an
	// account. This guards against a future migration dropping the constraint.
	_, hash, err := generateOneTimeToken()
	require.NoError(t, err)

	err = svc.repo.CreateOneTimeToken(ctx, &OneTimeToken{
		UserID: "no-such-user", Purpose: PurposePasswordReset,
		TokenHash: hash, ExpiresAt: time.Now().Add(time.Hour),
	})
	require.Error(t, err, "a token for a non-existent account must be rejected")
}
