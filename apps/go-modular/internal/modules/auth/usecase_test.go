package auth

import (
	"context"
	"log/slog"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"comicmark/go-modular/internal/pkg/database"
	"comicmark/go-modular/internal/pkg/jwt"
	"comicmark/go-modular/internal/pkg/password"
)

// --- shared test scaffolding ------------------------------------------------

const testSecret = "test-secret-that-is-at-least-32-bytes-long"

// recordingNotifier captures the emails the service tries to send.
type recordingNotifier struct {
	mu           sync.Mutex
	verification []*User
	reset        []*User
	err          error
}

func (n *recordingNotifier) SendEmailVerification(_ context.Context, user *User) error {
	if n.err != nil {
		return n.err
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	n.verification = append(n.verification, user)
	return nil
}

func (n *recordingNotifier) SendPasswordReset(_ context.Context, user *User) error {
	if n.err != nil {
		return n.err
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	n.reset = append(n.reset, user)
	return nil
}

func (n *recordingNotifier) verificationCount() int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return len(n.verification)
}

// testHasher is far cheaper than production; the production cost is verified by
// the password package's own tests.
func testHasher() *password.Hasher {
	return password.NewHasherWithParams(password.Params{
		Memory: 8, Iterations: 1, Parallelism: 1, SaltLength: 8, KeyLength: 16,
	})
}

// discardLoggerForTest keeps service logs out of the test output.
func discardLoggerForTest() *slog.Logger { return slog.New(slog.DiscardHandler) }

func newTestJWT(t *testing.T) *jwt.Manager {
	t.Helper()

	m, err := jwt.NewManager([]byte(testSecret), "comicmark", 15*time.Minute, 30*24*time.Hour)
	require.NoError(t, err)
	return m
}

func newTestRepository(t *testing.T) *Repository {
	t.Helper()

	db, err := database.Open(context.Background(), filepath.Join(t.TempDir(), "auth.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })

	_, err = database.NewMigrator(db).Up(context.Background())
	require.NoError(t, err)

	return NewRepository(db)
}

// testService bundles the service with its collaborators so tests can inspect
// both sides.
type testService struct {
	*Service
	repo     *Repository
	notifier *recordingNotifier
	jwt      *jwt.Manager
}

func newTestService(t *testing.T) *testService {
	t.Helper()
	return newTestServiceWith(t, Options{})
}

// newTestServiceWith lets a test override individual options.
func newTestServiceWith(t *testing.T, overrides Options) *testService {
	t.Helper()

	repo := newTestRepository(t)
	notifier := &recordingNotifier{}
	jwtManager := newTestJWT(t)

	opts := Options{
		Repository:            repo,
		JWT:                   jwtManager,
		Passwords:             testHasher(),
		Notifier:              notifier,
		Logger:                discardLoggerForTest(),
		BaseURL:               "http://localhost:5173",
		SessionIdleTTL:        30 * 24 * time.Hour,
		SessionTouchInterval:  5 * time.Minute,
		VerificationTokenTTL:  24 * time.Hour,
		PasswordResetTokenTTL: time.Hour,
	}

	// Apply overrides for the fields a test may want to change.
	if overrides.RequireEmailVerification {
		opts.RequireEmailVerification = true
	}
	if overrides.SessionIdleTTL != 0 {
		opts.SessionIdleTTL = overrides.SessionIdleTTL
	}
	if overrides.SessionTouchInterval != 0 {
		opts.SessionTouchInterval = overrides.SessionTouchInterval
	}
	if overrides.Logger != nil {
		opts.Logger = overrides.Logger
	}

	svc, err := NewService(opts)
	require.NoError(t, err)

	return &testService{Service: svc, repo: repo, notifier: notifier, jwt: jwtManager}
}

// testDevice is a stable device description for session assertions.
func testDevice() DeviceInfo {
	return DeviceInfo{
		UserAgent: "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/155.0.0.0 Safari/537.36",
		Platform:  "web",
		IP:        "203.0.113.7",
	}
}

func registerInput(email, pw string) RegisterInput {
	return RegisterInput{
		Email:       email,
		Password:    pw,
		DisplayName: "Test Reader",
		Device:      testDevice(),
	}
}

// --- construction -----------------------------------------------------------

func TestNewServiceRequiresItsCollaborators(t *testing.T) {
	repo := newTestRepository(t)
	valid := Options{
		Repository:            repo,
		JWT:                   newTestJWT(t),
		Passwords:             testHasher(),
		Notifier:              &recordingNotifier{},
		BaseURL:               "http://localhost:5173",
		VerificationTokenTTL:  24 * time.Hour,
		PasswordResetTokenTTL: time.Hour,
	}

	t.Run("repository", func(t *testing.T) {
		o := valid
		o.Repository = nil
		_, err := NewService(o)
		require.Error(t, err)
	})

	t.Run("jwt manager", func(t *testing.T) {
		o := valid
		o.JWT = nil
		_, err := NewService(o)
		require.Error(t, err)
	})

	t.Run("password hasher", func(t *testing.T) {
		o := valid
		o.Passwords = nil
		_, err := NewService(o)
		require.Error(t, err)
	})

	// A nil notifier would silently break verification and reset in production,
	// so it is rejected at construction rather than at first use.
	t.Run("notifier", func(t *testing.T) {
		o := valid
		o.Notifier = nil
		_, err := NewService(o)
		require.Error(t, err)
	})

	t.Run("base URL", func(t *testing.T) {
		o := valid
		o.BaseURL = "   "
		_, err := NewService(o)
		require.Error(t, err)
	})

	t.Run("verification token TTL", func(t *testing.T) {
		o := valid
		o.VerificationTokenTTL = 0
		_, err := NewService(o)
		require.Error(t, err)
	})

	t.Run("password reset token TTL", func(t *testing.T) {
		o := valid
		o.PasswordResetTokenTTL = 0
		_, err := NewService(o)
		require.Error(t, err)
	})

	t.Run("valid options", func(t *testing.T) {
		_, err := NewService(valid)
		require.NoError(t, err)
	})
}

// --- registration -----------------------------------------------------------

func TestRegisterCreatesAccountSessionAndTokens(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	result, err := svc.Register(ctx, registerInput("reader@example.com", "correct horse battery"))
	require.NoError(t, err)

	require.NotNil(t, result.User)
	require.NotNil(t, result.Tokens)

	// The user is signed in immediately, so the app can show "check your inbox"
	// while already authenticated.
	assert := result.Tokens
	require.NotEmpty(t, assert.AccessToken)
	require.NotEmpty(t, assert.RefreshToken)
	require.NotEmpty(t, assert.SessionID)
	require.Equal(t, "Bearer", assert.TokenType)
	require.Positive(t, assert.ExpiresIn)

	// A device session was recorded with a readable label.
	session, err := svc.repo.GetSession(ctx, result.Tokens.SessionID)
	require.NoError(t, err)
	require.Equal(t, "Chrome on macOS", session.DeviceLabel)
	require.Equal(t, "web", session.Platform)
	require.Equal(t, "203.0.113.7", session.IP)

	// A verification email was sent.
	require.Equal(t, 1, svc.notifier.verificationCount())

	// The password identity exists.
	identity, err := svc.repo.GetIdentity(ctx, ProviderPassword, result.User.ID)
	require.NoError(t, err)
	require.Equal(t, result.User.ID, identity.UserID)
}

func TestRegisterStartsUnverified(t *testing.T) {
	svc := newTestService(t)

	result, err := svc.Register(context.Background(), registerInput("reader@example.com", "correct horse battery"))
	require.NoError(t, err)
	require.False(t, result.User.EmailVerified())
}

func TestRegisterIsCaseInsensitiveOnEmail(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	_, err := svc.Register(ctx, registerInput("Reader@Example.com", "correct horse battery"))
	require.NoError(t, err)

	_, err = svc.Register(ctx, registerInput("reader@example.com", "another password"))
	require.ErrorIs(t, err, ErrEmailTaken)
}

func TestRegisterRejectsBadInput(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	t.Run("short password", func(t *testing.T) {
		_, err := svc.Register(ctx, registerInput("a@example.com", "short"))
		require.ErrorIs(t, err, ErrPasswordTooShort)

		var fieldErr *FieldError
		require.ErrorAs(t, err, &fieldErr)
		require.Equal(t, "password", fieldErr.Field)
	})

	t.Run("password equal to email", func(t *testing.T) {
		_, err := svc.Register(ctx, registerInput("same@example.com", "same@example.com"))
		require.ErrorIs(t, err, ErrPasswordMatchesEmail)
	})

	t.Run("invalid email", func(t *testing.T) {
		_, err := svc.Register(ctx, registerInput("not-an-email", "correct horse battery"))
		require.ErrorIs(t, err, ErrInvalidEmail)

		var fieldErr *FieldError
		require.ErrorAs(t, err, &fieldErr)
		require.Equal(t, "email", fieldErr.Field)
	})

	t.Run("empty email", func(t *testing.T) {
		_, err := svc.Register(ctx, registerInput("  ", "correct horse battery"))
		require.ErrorIs(t, err, ErrInvalidEmail)
	})
}

// A mail outage must not lose the account: the user can ask for another link.
func TestRegisterSucceedsEvenWhenTheVerificationEmailFails(t *testing.T) {
	svc := newTestService(t)
	svc.notifier.err = context.DeadlineExceeded

	result, err := svc.Register(context.Background(), registerInput("reader@example.com", "correct horse battery"))
	require.NoError(t, err)
	require.NotNil(t, result.Tokens)
}

// --- sign-in ----------------------------------------------------------------

func TestLoginSucceedsWithCorrectCredentials(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	registered, err := svc.Register(ctx, registerInput("reader@example.com", "correct horse battery"))
	require.NoError(t, err)

	// Sign in from a second device.
	second := testDevice()
	second.Platform = "android"
	second.UserAgent = "Dart/3.13.5 (dart:io) Android"

	result, err := svc.Login(ctx, LoginInput{
		Email: "reader@example.com", Password: "correct horse battery", Device: second,
	})
	require.NoError(t, err)
	require.NotNil(t, result.Tokens)

	require.NotEqual(t, registered.Tokens.SessionID, result.Tokens.SessionID,
		"signing in on another device must create a separate session")

	// Both sessions are listed.
	sessions, err := svc.repo.ListSessions(ctx, registered.User.ID, false)
	require.NoError(t, err)
	require.Len(t, sessions, 2)
}

func TestLoginAcceptsEmailInAnyCase(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	_, err := svc.Register(ctx, registerInput("reader@example.com", "correct horse battery"))
	require.NoError(t, err)

	_, err = svc.Login(ctx, LoginInput{
		Email: "  READER@example.COM  ", Password: "correct horse battery", Device: testDevice(),
	})
	require.NoError(t, err)
}

func TestLoginRejectsBadCredentials(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	_, err := svc.Register(ctx, registerInput("reader@example.com", "correct horse battery"))
	require.NoError(t, err)

	t.Run("wrong password", func(t *testing.T) {
		_, err := svc.Login(ctx, LoginInput{
			Email: "reader@example.com", Password: "wrong password", Device: testDevice(),
		})
		require.ErrorIs(t, err, ErrInvalidCredentials)
	})

	// An unknown address must be indistinguishable from a wrong password,
	// otherwise sign-in becomes an account-enumeration oracle.
	t.Run("unknown email looks identical", func(t *testing.T) {
		_, err := svc.Login(ctx, LoginInput{
			Email: "nobody@example.com", Password: "correct horse battery", Device: testDevice(),
		})
		require.ErrorIs(t, err, ErrInvalidCredentials)
	})
}

func TestLoginRejectsASocialOnlyAccount(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	user := &User{Email: "social@example.com"}
	require.NoError(t, svc.repo.CreateUser(ctx, user, ""))

	_, err := svc.Login(ctx, LoginInput{
		Email: "social@example.com", Password: "whatever password", Device: testDevice(),
	})
	require.ErrorIs(t, err, ErrInvalidCredentials,
		"revealing that the account exists would leak registration status")
}

func TestLoginEnforcesVerificationOnlyWhenConfigured(t *testing.T) {
	ctx := context.Background()

	t.Run("verification not required", func(t *testing.T) {
		svc := newTestService(t)
		_, err := svc.Register(ctx, registerInput("reader@example.com", "correct horse battery"))
		require.NoError(t, err)

		_, err = svc.Login(ctx, LoginInput{
			Email: "reader@example.com", Password: "correct horse battery", Device: testDevice(),
		})
		require.NoError(t, err, "development default allows sign-in before verification")
	})

	t.Run("verification required", func(t *testing.T) {
		svc := newTestServiceWith(t, Options{RequireEmailVerification: true})
		_, err := svc.Register(ctx, registerInput("reader@example.com", "correct horse battery"))
		require.NoError(t, err)

		_, err = svc.Login(ctx, LoginInput{
			Email: "reader@example.com", Password: "correct horse battery", Device: testDevice(),
		})
		require.ErrorIs(t, err, ErrEmailNotVerified)
	})
}

// --- refresh rotation -------------------------------------------------------

func TestRotateRefreshIssuesANewPairAndRetiresTheOldToken(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	registered, err := svc.Register(ctx, registerInput("reader@example.com", "correct horse battery"))
	require.NoError(t, err)

	rotated, err := svc.RotateRefresh(ctx, registered.Tokens.RefreshToken)
	require.NoError(t, err)

	require.NotEqual(t, registered.Tokens.RefreshToken, rotated.Tokens.RefreshToken)
	require.Equal(t, registered.Tokens.SessionID, rotated.Tokens.SessionID,
		"rotation must stay within the same device session")
	require.Equal(t, registered.User.ID, rotated.User.ID)
	require.NotEmpty(t, rotated.Tokens.AccessToken)
}

func TestRotateRefreshDetectsReplayAndRevokesTheFamily(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	registered, err := svc.Register(ctx, registerInput("reader@example.com", "correct horse battery"))
	require.NoError(t, err)

	first, err := svc.RotateRefresh(ctx, registered.Tokens.RefreshToken)
	require.NoError(t, err)

	// Replay the original token, as an attacker with a copy would.
	_, err = svc.RotateRefresh(ctx, registered.Tokens.RefreshToken)
	require.ErrorIs(t, err, ErrTokenReused)

	// The legitimate holder's current token must now be dead too: there is no
	// way to tell which party is the thief, so both are signed out.
	_, err = svc.RotateRefresh(ctx, first.Tokens.RefreshToken)
	require.Error(t, err, "the whole family must be revoked after a replay")
}

// A replayed refresh token means a thief was handed a matching access token by the
// successful first rotation. Killing only the rotation family would leave that
// access token working until it expired, so the whole session is revoked.
func TestReplayRevokesTheSessionNotJustTheTokenFamily(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	registered, err := svc.Register(ctx, registerInput("reader@example.com", "correct horse battery"))
	require.NoError(t, err)

	// The thief rotates first and receives a usable access token.
	stolen, err := svc.RotateRefresh(ctx, registered.Tokens.RefreshToken)
	require.NoError(t, err)

	// The legitimate client then presents the same token, exposing the replay.
	_, err = svc.RotateRefresh(ctx, registered.Tokens.RefreshToken)
	require.ErrorIs(t, err, ErrTokenReused)

	// The thief's access token must be dead at once, not in fifteen minutes.
	_, err = svc.Authenticate(ctx, stolen.Tokens.AccessToken)
	require.ErrorIs(t, err, ErrSessionRevoked,
		"a detected replay must cut off the access token the thief received")

	// So must the legitimate client's, and the session row itself.
	_, err = svc.Authenticate(ctx, registered.Tokens.AccessToken)
	require.ErrorIs(t, err, ErrSessionRevoked)

	session, err := svc.repo.GetSession(ctx, registered.Tokens.SessionID)
	require.NoError(t, err)
	require.False(t, session.Active())

	// The user is told to sign in again rather than being left to retry.
	_, err = svc.Login(ctx, LoginInput{
		Email: "reader@example.com", Password: "correct horse battery", Device: testDevice(),
	})
	require.NoError(t, err, "signing in again must still work")
}

func TestRotateRefreshRejectsAnExpiredToken(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	registered, err := svc.Register(ctx, registerInput("reader@example.com", "correct horse battery"))
	require.NoError(t, err)

	// Pretend the token expired by moving the clock past it.
	svc.now = func() time.Time { return time.Now().Add(31 * 24 * time.Hour) }

	_, err = svc.RotateRefresh(ctx, registered.Tokens.RefreshToken)
	require.Error(t, err)
}

func TestRotateRefreshRejectsAnAccessToken(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	registered, err := svc.Register(ctx, registerInput("reader@example.com", "correct horse battery"))
	require.NoError(t, err)

	_, err = svc.RotateRefresh(ctx, registered.Tokens.AccessToken)
	require.ErrorIs(t, err, ErrTokenInvalid, "an access token is not exchangeable")
}

func TestRotateRefreshRejectsARubbishToken(t *testing.T) {
	svc := newTestService(t)

	_, err := svc.RotateRefresh(context.Background(), "not-a-jwt")
	require.ErrorIs(t, err, ErrTokenInvalid)
}

func TestRotateRefreshRejectsARevokedSession(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	registered, err := svc.Register(ctx, registerInput("reader@example.com", "correct horse battery"))
	require.NoError(t, err)

	require.NoError(t, svc.repo.RevokeSession(ctx, registered.Tokens.SessionID, time.Now()))

	_, err = svc.RotateRefresh(ctx, registered.Tokens.RefreshToken)
	require.ErrorIs(t, err, ErrSessionRevoked)
}

func TestRotateRefreshRejectsAnIdleSession(t *testing.T) {
	svc := newTestServiceWith(t, Options{SessionIdleTTL: time.Hour})
	ctx := context.Background()

	registered, err := svc.Register(ctx, registerInput("reader@example.com", "correct horse battery"))
	require.NoError(t, err)

	// Move the clock beyond the idle window.
	svc.now = func() time.Time { return time.Now().Add(2 * time.Hour) }

	_, err = svc.RotateRefresh(ctx, registered.Tokens.RefreshToken)
	require.ErrorIs(t, err, ErrSessionRevoked)

	// The session is revoked as a side effect, so it stops appearing as active.
	session, err := svc.repo.GetSession(ctx, registered.Tokens.SessionID)
	require.NoError(t, err)
	require.False(t, session.Active())
}

// --- logout -----------------------------------------------------------------

func TestLogoutRevokesTheSession(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	registered, err := svc.Register(ctx, registerInput("reader@example.com", "correct horse battery"))
	require.NoError(t, err)

	require.NoError(t, svc.Logout(ctx, registered.Tokens.RefreshToken))

	_, err = svc.Authenticate(ctx, registered.Tokens.AccessToken)
	require.ErrorIs(t, err, ErrSessionRevoked,
		"signing out must take effect immediately, not when the access token expires")

	_, err = svc.RotateRefresh(ctx, registered.Tokens.RefreshToken)
	require.Error(t, err)
}

func TestLogoutIsIdempotent(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	registered, err := svc.Register(ctx, registerInput("reader@example.com", "correct horse battery"))
	require.NoError(t, err)

	require.NoError(t, svc.Logout(ctx, registered.Tokens.RefreshToken))
	require.NoError(t, svc.Logout(ctx, registered.Tokens.RefreshToken),
		"signing out twice must not be an error for the client")
}

func TestLogoutToleratesAnUnknownToken(t *testing.T) {
	svc := newTestService(t)

	require.NoError(t, svc.Logout(context.Background(), "not-a-jwt"))
}

// --- authenticate and Me ----------------------------------------------------

func TestAuthenticateReturnsTheCaller(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	registered, err := svc.Register(ctx, registerInput("reader@example.com", "correct horse battery"))
	require.NoError(t, err)

	principal, err := svc.Authenticate(ctx, registered.Tokens.AccessToken)
	require.NoError(t, err)
	require.Equal(t, registered.User.ID, principal.UserID)
	require.Equal(t, registered.Tokens.SessionID, principal.SessionID)
}

func TestAuthenticateRejectsBadTokens(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	registered, err := svc.Register(ctx, registerInput("reader@example.com", "correct horse battery"))
	require.NoError(t, err)

	t.Run("garbage", func(t *testing.T) {
		_, err := svc.Authenticate(ctx, "not-a-jwt")
		require.ErrorIs(t, err, ErrTokenInvalid)
	})

	t.Run("a refresh token is not an access token", func(t *testing.T) {
		_, err := svc.Authenticate(ctx, registered.Tokens.RefreshToken)
		require.ErrorIs(t, err, ErrTokenInvalid)
	})

	t.Run("a token signed with another secret", func(t *testing.T) {
		other, err := jwt.NewManager([]byte("another-secret-that-is-32-bytes-long-ok"), "comicmark", time.Minute, time.Hour)
		require.NoError(t, err)

		forged, _, err := other.SignAccess(registered.User.ID, registered.Tokens.SessionID)
		require.NoError(t, err)

		_, err = svc.Authenticate(ctx, forged)
		require.ErrorIs(t, err, ErrTokenInvalid)
	})

	t.Run("a token for a session that no longer exists", func(t *testing.T) {
		orphan, _, err := svc.jwt.SignAccess(registered.User.ID, "no-such-session")
		require.NoError(t, err)

		_, err = svc.Authenticate(ctx, orphan)
		require.ErrorIs(t, err, ErrTokenInvalid)
	})
}

func TestMeReturnsTheUser(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	registered, err := svc.Register(ctx, registerInput("reader@example.com", "correct horse battery"))
	require.NoError(t, err)

	user, err := svc.Me(ctx, registered.User.ID)
	require.NoError(t, err)
	require.Equal(t, "reader@example.com", user.Email)

	_, err = svc.Me(ctx, "no-such-user")
	require.ErrorIs(t, err, ErrNotFound)
}

// --- change password --------------------------------------------------------

func TestChangePasswordRequiresTheCurrentPassword(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	registered, err := svc.Register(ctx, registerInput("reader@example.com", "correct horse battery"))
	require.NoError(t, err)

	err = svc.ChangePassword(ctx, registered.User.ID, "wrong current", "a brand new password")
	require.ErrorIs(t, err, ErrPasswordMismatch)

	require.NoError(t, svc.ChangePassword(ctx, registered.User.ID, "correct horse battery", "a brand new password"))

	// The new password works; the old one does not.
	_, err = svc.Login(ctx, LoginInput{
		Email: "reader@example.com", Password: "a brand new password", Device: testDevice(),
	})
	require.NoError(t, err)

	_, err = svc.Login(ctx, LoginInput{
		Email: "reader@example.com", Password: "correct horse battery", Device: testDevice(),
	})
	require.ErrorIs(t, err, ErrInvalidCredentials)
}

// A user who proved they know the current password keeps their other devices;
// signing them all out is what the reset flow is for.
func TestChangePasswordLeavesOtherSessionsSignedIn(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	registered, err := svc.Register(ctx, registerInput("reader@example.com", "correct horse battery"))
	require.NoError(t, err)
	other, err := svc.Login(ctx, LoginInput{
		Email: "reader@example.com", Password: "correct horse battery", Device: testDevice(),
	})
	require.NoError(t, err)

	require.NoError(t, svc.ChangePassword(ctx, registered.User.ID, "correct horse battery", "a brand new password"))

	_, err = svc.Authenticate(ctx, other.Tokens.AccessToken)
	require.NoError(t, err, "changing a password should not sign other devices out")
}

func TestChangePasswordRejectsAWeakNewPassword(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	registered, err := svc.Register(ctx, registerInput("reader@example.com", "correct horse battery"))
	require.NoError(t, err)

	err = svc.ChangePassword(ctx, registered.User.ID, "correct horse battery", "short")
	require.ErrorIs(t, err, ErrPasswordTooShort)
}

func TestChangePasswordRejectsASocialOnlyAccount(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	user := &User{Email: "social@example.com"}
	require.NoError(t, svc.repo.CreateUser(ctx, user, ""))

	err := svc.ChangePassword(ctx, user.ID, "anything", "a brand new password")
	require.ErrorIs(t, err, ErrNoPasswordSet)
}

// --- session activity -------------------------------------------------------

func TestAuthenticateThrottlesSessionTouches(t *testing.T) {
	svc := newTestServiceWith(t, Options{SessionTouchInterval: time.Hour})
	ctx := context.Background()

	registered, err := svc.Register(ctx, registerInput("reader@example.com", "correct horse battery"))
	require.NoError(t, err)

	before, err := svc.repo.GetSession(ctx, registered.Tokens.SessionID)
	require.NoError(t, err)

	for range 3 {
		_, err := svc.Authenticate(ctx, registered.Tokens.AccessToken)
		require.NoError(t, err)
	}

	after, err := svc.repo.GetSession(ctx, registered.Tokens.SessionID)
	require.NoError(t, err)
	require.Equal(t, before.LastSeenAt, after.LastSeenAt,
		"repeated requests inside the throttle window must not write to the database")
}

func TestAuthenticateTouchesTheSessionAfterTheThrottleWindow(t *testing.T) {
	svc := newTestServiceWith(t, Options{SessionTouchInterval: time.Nanosecond})
	ctx := context.Background()

	registered, err := svc.Register(ctx, registerInput("reader@example.com", "correct horse battery"))
	require.NoError(t, err)

	before, err := svc.repo.GetSession(ctx, registered.Tokens.SessionID)
	require.NoError(t, err)

	time.Sleep(2 * time.Millisecond)
	_, err = svc.Authenticate(ctx, registered.Tokens.AccessToken)
	require.NoError(t, err)

	after, err := svc.repo.GetSession(ctx, registered.Tokens.SessionID)
	require.NoError(t, err)
	require.True(t, after.LastSeenAt.After(before.LastSeenAt), "activity should be recorded")
}
