package auth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/mail"
	"strings"
	"time"

	"comicmark/go-modular/internal/pkg/jwt"
	"comicmark/go-modular/internal/pkg/password"
	"comicmark/go-modular/internal/pkg/useragent"
)

// Notifier sends the account emails. It is an interface so that registration and
// password flows can be tested without a mail server, and so the concrete
// implementation can live alongside the token code it shares.
type Notifier interface {
	// SendEmailVerification sends the "confirm your address" email.
	SendEmailVerification(ctx context.Context, user *User) error
	// SendPasswordReset sends the "choose a new password" email.
	SendPasswordReset(ctx context.Context, user *User) error
}

// Options configures the auth service.
type Options struct {
	Repository *Repository
	JWT        *jwt.Manager
	Passwords  *password.Hasher
	Notifier   Notifier
	Logger     *slog.Logger

	// BaseURL is the app origin used to build links in emails.
	BaseURL string
	// RequireEmailVerification blocks sign-in until the address is confirmed.
	RequireEmailVerification bool

	// SessionIdleTTL is how long a device may go unused before its refresh
	// tokens stop working.
	SessionIdleTTL time.Duration
	// SessionTouchInterval throttles writes of sessions.last_seen_at.
	SessionTouchInterval time.Duration

	// VerificationTokenTTL and PasswordResetTokenTTL bound how long the emailed
	// links stay usable.
	VerificationTokenTTL  time.Duration
	PasswordResetTokenTTL time.Duration
}

// Service implements the auth use cases.
type Service struct {
	repo      *Repository
	jwt       *jwt.Manager
	passwords *password.Hasher
	notifier  Notifier
	logger    *slog.Logger

	baseURL                  string
	requireEmailVerification bool
	sessionIdleTTL           time.Duration
	sessionTouchInterval     time.Duration
	verificationTokenTTL     time.Duration
	passwordResetTokenTTL    time.Duration

	// tokens consumes the one-time tokens the notifier issues.
	tokens *tokenStore

	// dummyHash is compared against when the account does not exist, so that a
	// failed sign-in costs roughly the same either way and cannot be used to
	// enumerate addresses by timing.
	dummyHash string

	// now is injectable for tests.
	now func() time.Time
}

// NewService validates the options and returns the auth service.
func NewService(opts Options) (*Service, error) {
	switch {
	case opts.Repository == nil:
		return nil, errors.New("auth: repository is required")
	case opts.JWT == nil:
		return nil, errors.New("auth: jwt manager is required")
	case opts.Passwords == nil:
		return nil, errors.New("auth: password hasher is required")
	case opts.Notifier == nil:
		return nil, errors.New("auth: notifier is required")
	case strings.TrimSpace(opts.BaseURL) == "":
		return nil, errors.New("auth: base URL is required")
	case opts.VerificationTokenTTL <= 0:
		return nil, errors.New("auth: verification token TTL must be positive")
	case opts.PasswordResetTokenTTL <= 0:
		return nil, errors.New("auth: password reset token TTL must be positive")
	}

	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}

	dummyHash, err := opts.Passwords.Hash("comicmark-dummy-password-for-timing")
	if err != nil {
		return nil, fmt.Errorf("auth: prepare dummy hash: %w", err)
	}

	return &Service{
		repo:                     opts.Repository,
		jwt:                      opts.JWT,
		passwords:                opts.Passwords,
		notifier:                 opts.Notifier,
		logger:                   logger,
		baseURL:                  strings.TrimRight(opts.BaseURL, "/"),
		requireEmailVerification: opts.RequireEmailVerification,
		sessionIdleTTL:           opts.SessionIdleTTL,
		sessionTouchInterval:     opts.SessionTouchInterval,
		verificationTokenTTL:     opts.VerificationTokenTTL,
		passwordResetTokenTTL:    opts.PasswordResetTokenTTL,
		tokens:                   newTokenStore(opts.Repository),
		dummyHash:                dummyHash,
		now:                      time.Now,
	}, nil
}

// --- request and result types -----------------------------------------------

// DeviceInfo describes the client making a request, used to create a session.
type DeviceInfo struct {
	UserAgent string
	// Platform is the raw X-Client-Platform header, which may be empty.
	Platform string
	IP       string
}

// TokenPair is what a successful authentication hands back.
type TokenPair struct {
	AccessToken      string    `json:"access_token"`
	AccessExpiresAt  time.Time `json:"access_expires_at"`
	RefreshToken     string    `json:"refresh_token"`
	RefreshExpiresAt time.Time `json:"refresh_expires_at"`
	// TokenType is always "Bearer".
	TokenType string `json:"token_type"`
	// ExpiresIn is the access token lifetime in seconds, for clients that
	// prefer it to an absolute timestamp.
	ExpiresIn int `json:"expires_in"`
	// SessionID identifies the device session these tokens belong to.
	SessionID string `json:"session_id"`
}

// AuthResult is a user plus freshly issued tokens.
type AuthResult struct {
	User   *User
	Tokens *TokenPair
}

// Principal is the authenticated caller behind a request.
type Principal struct {
	UserID    string
	SessionID string
}

// RegisterInput is a sign-up request.
type RegisterInput struct {
	Email       string
	Password    string
	DisplayName string
	Device      DeviceInfo
}

// LoginInput is a sign-in request.
type LoginInput struct {
	Email    string
	Password string
	Device   DeviceInfo
}

// --- validation -------------------------------------------------------------

// NormalizeEmail trims surrounding space. The local part is technically
// case-sensitive, but every real provider treats it as insensitive, and the
// database enforces uniqueness case-insensitively to match.
func NormalizeEmail(email string) string { return strings.TrimSpace(email) }

// validateEmail performs the checks the use case depends on. Stricter format
// validation belongs in the request DTO.
func validateEmail(email string) error {
	switch {
	case email == "":
		return newFieldError("email", "Enter your email address.", ErrInvalidEmail)
	case len(email) > MaxEmailLength:
		return newFieldError("email", fmt.Sprintf("Email addresses cannot be longer than %d characters.", MaxEmailLength), ErrInvalidEmail)
	}

	addr, err := mail.ParseAddress(email)
	if err != nil || addr.Address != email {
		return newFieldError("email", "Enter a valid email address.", ErrInvalidEmail)
	}
	return nil
}

// validatePassword enforces the password policy. The email is compared so that
// the password is not simply the address.
func validatePassword(email, pw string) error {
	switch {
	case len(pw) < MinPasswordLength:
		return newFieldError("password",
			fmt.Sprintf("Use at least %d characters.", MinPasswordLength), ErrPasswordTooShort)
	case len(pw) > password.MaxPasswordLength:
		return newFieldError("password",
			fmt.Sprintf("Passwords cannot be longer than %d characters.", password.MaxPasswordLength), ErrPasswordTooLong)
	case strings.EqualFold(strings.TrimSpace(pw), strings.TrimSpace(email)):
		return newFieldError("password", "Choose a password that is not your email address.", ErrPasswordMatchesEmail)
	}
	return nil
}

// --- registration -----------------------------------------------------------

// Register creates an account, signs the new user in on their current device and
// sends a verification email.
//
// The account, its password identity, the device session and the first refresh
// token are written in one transaction: a partial sign-up would leave an account
// that cannot sign in.
func (s *Service) Register(ctx context.Context, in RegisterInput) (*AuthResult, error) {
	email := NormalizeEmail(in.Email)
	if err := validateEmail(email); err != nil {
		return nil, err
	}
	if err := validatePassword(email, in.Password); err != nil {
		return nil, err
	}

	hash, err := s.passwords.Hash(in.Password)
	if err != nil {
		return nil, fmt.Errorf("auth: hash password: %w", err)
	}

	user := &User{
		Email:       email,
		DisplayName: strings.TrimSpace(in.DisplayName),
	}

	var tokens *TokenPair
	err = s.repo.WithTx(ctx, func(tx *Repository) error {
		if err := tx.CreateUser(ctx, user, hash); err != nil {
			return err
		}
		if err := tx.CreateIdentity(ctx, &AuthIdentity{
			UserID:          user.ID,
			Provider:        ProviderPassword,
			ProviderSubject: user.ID,
			Email:           user.Email,
		}); err != nil {
			return err
		}

		tokens, err = s.issueTokens(ctx, tx, user.ID, in.Device)
		return err
	})
	if err != nil {
		return nil, err
	}

	// Sending mail is deliberately outside the transaction: a slow mail server
	// must not hold the single SQLite writer, and a failure here is recoverable
	// because the user can request another link.
	if err := s.notifier.SendEmailVerification(ctx, user); err != nil {
		s.logger.ErrorContext(ctx, "failed to send verification email",
			"user_id", user.ID, "error", err)
	}

	s.logger.InfoContext(ctx, "account registered", "user_id", user.ID)
	return &AuthResult{User: user, Tokens: tokens}, nil
}

// --- sign-in ----------------------------------------------------------------

// Login verifies credentials and starts a new device session.
func (s *Service) Login(ctx context.Context, in LoginInput) (*AuthResult, error) {
	email := NormalizeEmail(in.Email)

	user, hash, err := s.repo.GetUserWithPassword(ctx, email)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			// Spend the same effort as a real verification, then fail
			// identically, so sign-in cannot reveal which addresses exist.
			_, _ = s.passwords.Validate(in.Password, s.dummyHash)
			return nil, ErrInvalidCredentials
		}
		return nil, err
	}

	if hash == "" {
		// A social-only account. Reported as bad credentials so the endpoint
		// does not disclose that the address is registered.
		_, _ = s.passwords.Validate(in.Password, s.dummyHash)
		return nil, ErrInvalidCredentials
	}

	ok, err := s.passwords.Validate(in.Password, hash)
	if err != nil {
		return nil, fmt.Errorf("auth: verify password: %w", err)
	}
	if !ok {
		return nil, ErrInvalidCredentials
	}

	if s.requireEmailVerification && !user.EmailVerified() {
		return nil, ErrEmailNotVerified
	}

	var tokens *TokenPair
	err = s.repo.WithTx(ctx, func(tx *Repository) error {
		tokens, err = s.issueTokens(ctx, tx, user.ID, in.Device)
		return err
	})
	if err != nil {
		return nil, err
	}

	// Opportunistic housekeeping: sign-in is the one endpoint every returning
	// user hits, which keeps the table from growing without a scheduled job.
	if n, err := s.repo.DeleteExpiredRefreshTokens(ctx, s.now()); err != nil {
		s.logger.WarnContext(ctx, "failed to prune expired refresh tokens", "error", err)
	} else if n > 0 {
		s.logger.DebugContext(ctx, "pruned expired refresh tokens", "count", n)
	}

	s.logger.InfoContext(ctx, "signed in", "user_id", user.ID, "session_id", tokens.SessionID)
	return &AuthResult{User: user, Tokens: tokens}, nil
}

// --- tokens -----------------------------------------------------------------

// IssueTokens creates a new device session and the first token pair of a new
// rotation family. Used by sign-in flows that have already established the
// user's identity.
func (s *Service) IssueTokens(ctx context.Context, userID string, device DeviceInfo) (*TokenPair, error) {
	var tokens *TokenPair
	err := s.repo.WithTx(ctx, func(tx *Repository) error {
		var err error
		tokens, err = s.issueTokens(ctx, tx, userID, device)
		return err
	})
	if err != nil {
		return nil, err
	}
	return tokens, nil
}

// issueTokens creates the session and its first refresh token using the given
// repository, which may be a transaction.
func (s *Service) issueTokens(ctx context.Context, repo *Repository, userID string, device DeviceInfo) (*TokenPair, error) {
	parsed := useragent.Describe(device.UserAgent, device.Platform)

	session := &Session{
		UserID:      userID,
		DeviceLabel: parsed.Label,
		Platform:    parsed.Platform,
		UserAgent:   device.UserAgent,
		IP:          device.IP,
	}
	if err := repo.CreateSession(ctx, session); err != nil {
		return nil, err
	}

	refresh, err := s.jwt.SignRefresh(userID, session.ID)
	if err != nil {
		return nil, fmt.Errorf("auth: sign refresh token: %w", err)
	}

	if err := repo.CreateRefreshToken(ctx, &RefreshToken{
		ID:        refresh.ID,
		UserID:    userID,
		SessionID: session.ID,
		TokenHash: refresh.Hash,
		ExpiresAt: refresh.ExpiresAt,
	}); err != nil {
		return nil, err
	}

	access, accessExpiresAt, err := s.jwt.SignAccess(userID, session.ID)
	if err != nil {
		return nil, fmt.Errorf("auth: sign access token: %w", err)
	}

	return &TokenPair{
		AccessToken:      access,
		AccessExpiresAt:  accessExpiresAt,
		RefreshToken:     refresh.Token,
		RefreshExpiresAt: refresh.ExpiresAt,
		TokenType:        "Bearer",
		ExpiresIn:        int(s.jwt.AccessTTL().Seconds()),
		SessionID:        session.ID,
	}, nil
}

// RotateRefresh exchanges a refresh token for a new access token and a new
// refresh token, retiring the one presented.
//
// Presenting a token that was already rotated means two parties hold it. There
// is no way to tell the legitimate client from a thief, so the entire rotation
// family is revoked and both are signed out.
func (s *Service) RotateRefresh(ctx context.Context, refreshToken string) (*AuthResult, error) {
	claims, err := s.jwt.ParseRefresh(refreshToken)
	if err != nil {
		return nil, ErrTokenInvalid
	}

	stored, err := s.repo.GetRefreshTokenByHash(ctx, jwt.HashToken(refreshToken))
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, ErrTokenInvalid
		}
		return nil, err
	}

	// The token row and the token's own claims must agree, or a valid token for
	// one row could be paired with another's jti.
	if stored.ID != claims.TokenID || stored.UserID != claims.UserID {
		return nil, ErrTokenInvalid
	}

	if !stored.Active() {
		// replaced_by is only set when a rotation superseded this token, so its
		// presence means the token is being replayed.
		if stored.ReplacedBy != nil {
			s.revokeAfterReuse(ctx, stored)
			return nil, ErrTokenReused
		}
		// Otherwise it was revoked deliberately: a sign-out, an explicit session
		// revocation, or a family cut off after an earlier replay. The client's
		// remedy is the same as any other dead session, which is to sign in
		// again.
		return nil, ErrSessionRevoked
	}
	if !stored.ExpiresAt.After(s.now()) {
		return nil, ErrTokenInvalid
	}

	session, err := s.repo.GetSession(ctx, stored.SessionID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, ErrTokenInvalid
		}
		return nil, err
	}
	if !session.Active() {
		return nil, ErrSessionRevoked
	}
	if s.idleExpired(session) {
		// An idle device must not be able to keep refreshing indefinitely.
		if err := s.repo.RevokeSession(ctx, session.ID, s.now()); err != nil {
			s.logger.WarnContext(ctx, "failed to revoke idle session", "session_id", session.ID, "error", err)
		}
		return nil, ErrSessionRevoked
	}

	next, err := s.jwt.SignRefresh(stored.UserID, stored.SessionID)
	if err != nil {
		return nil, fmt.Errorf("auth: sign refresh token: %w", err)
	}

	if err := s.repo.RotateRefreshToken(ctx, stored, &RefreshToken{
		ID:        next.ID,
		UserID:    stored.UserID,
		SessionID: stored.SessionID,
		TokenHash: next.Hash,
		FamilyID:  stored.FamilyID,
		ExpiresAt: next.ExpiresAt,
	}, s.now()); err != nil {
		if errors.Is(err, ErrTokenReused) {
			s.revokeAfterReuse(ctx, stored)
			return nil, ErrTokenReused
		}
		return nil, err
	}

	access, accessExpiresAt, err := s.jwt.SignAccess(stored.UserID, stored.SessionID)
	if err != nil {
		return nil, fmt.Errorf("auth: sign access token: %w", err)
	}

	user, err := s.repo.GetUserByID(ctx, stored.UserID)
	if err != nil {
		return nil, err
	}

	s.touchSession(ctx, session)

	return &AuthResult{
		User: user,
		Tokens: &TokenPair{
			AccessToken:      access,
			AccessExpiresAt:  accessExpiresAt,
			RefreshToken:     next.Token,
			RefreshExpiresAt: next.ExpiresAt,
			TokenType:        "Bearer",
			ExpiresIn:        int(s.jwt.AccessTTL().Seconds()),
			SessionID:        stored.SessionID,
		},
	}, nil
}

// revokeAfterReuse cuts a session off after a replay.
//
// Revoking the *session* rather than only the rotation family is deliberate.
// A replay means two parties hold the refresh token, and the successful first
// rotation handed the thief a matching access token. Revoking the family alone
// would leave that access token working until it expired, so the session is
// revoked instead: it cascades to every refresh token the session owns and takes
// effect on the very next request.
func (s *Service) revokeAfterReuse(ctx context.Context, token *RefreshToken) {
	s.logger.WarnContext(ctx, "refresh token replay detected; revoking the session",
		"user_id", token.UserID, "session_id", token.SessionID, "family_id", token.FamilyID)

	err := s.repo.RevokeSession(ctx, token.SessionID, s.now())
	switch {
	case err == nil:
		return
	case errors.Is(err, ErrNotFound):
		// The session row is already gone, so revoke the family directly rather
		// than leaving the tokens usable.
		if familyErr := s.repo.RevokeFamily(ctx, token.FamilyID, s.now()); familyErr != nil {
			s.logger.ErrorContext(ctx, "failed to revoke token family after replay",
				"family_id", token.FamilyID, "error", familyErr)
		}
		return
	default:
		s.logger.ErrorContext(ctx, "failed to revoke session after replay",
			"session_id", token.SessionID, "error", err)
	}
}

// Logout ends the session a refresh token belongs to.
//
// It succeeds even when the token is unknown, so signing out twice is not an
// error for the client.
func (s *Service) Logout(ctx context.Context, refreshToken string) error {
	claims, err := s.jwt.ParseRefresh(refreshToken)
	if err != nil {
		return nil
	}

	stored, err := s.repo.GetRefreshTokenByHash(ctx, jwt.HashToken(refreshToken))
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil
		}
		return err
	}

	if err := s.repo.RevokeSession(ctx, stored.SessionID, s.now()); err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil
		}
		return err
	}

	s.logger.InfoContext(ctx, "signed out", "user_id", claims.UserID, "session_id", stored.SessionID)
	return nil
}

// --- authentication ---------------------------------------------------------

// Authenticate validates an access token and returns the caller behind it.
//
// The session is re-checked on every request: that is what makes signing a
// device out effective immediately rather than only when its access token
// happens to expire.
func (s *Service) Authenticate(ctx context.Context, accessToken string) (*Principal, error) {
	claims, err := s.jwt.ParseAccess(accessToken)
	if err != nil {
		return nil, ErrTokenInvalid
	}
	if claims.SessionID == "" {
		// Tokens issued before sessions existed, or a malformed token.
		return nil, ErrTokenInvalid
	}

	session, err := s.repo.GetSession(ctx, claims.SessionID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, ErrTokenInvalid
		}
		return nil, err
	}
	if !session.Active() {
		return nil, ErrSessionRevoked
	}
	if session.UserID != claims.UserID {
		return nil, ErrTokenInvalid
	}
	if s.idleExpired(session) {
		if err := s.repo.RevokeSession(ctx, session.ID, s.now()); err != nil {
			s.logger.WarnContext(ctx, "failed to revoke idle session", "session_id", session.ID, "error", err)
		}
		return nil, ErrSessionRevoked
	}

	s.touchSession(ctx, session)

	return &Principal{UserID: claims.UserID, SessionID: claims.SessionID}, nil
}

// Me returns the signed-in user.
func (s *Service) Me(ctx context.Context, userID string) (*User, error) {
	return s.repo.GetUserByID(ctx, userID)
}

// --- provider sign-in -------------------------------------------------------

// IdentityInput is a verified third-party identity. The caller is responsible for
// having validated the token; by the time this is built, the provider has already
// confirmed who the person is.
type IdentityInput struct {
	// Provider is ProviderFirebase.
	Provider string
	// Subject is the provider's stable user id.
	Subject string
	Email   string
	// EmailVerified comes from the provider. A verified provider email is
	// treated as proving control of the inbox, so the account does not need a
	// second confirmation round.
	EmailVerified bool
	DisplayName   string
	AvatarURL     string
	Device        DeviceInfo
}

// SignInWithIdentity signs in through a third-party provider, creating the
// account on first use.
//
// The identity is matched by (provider, subject) rather than by email: a
// provider's subject is stable, whereas an address can be reassigned, and
// matching on email alone would let anyone who controls a recycled address take
// over an existing account.
//
// When the address already belongs to a local account, the identity is linked to
// it, but only if the provider says the address is verified.
func (s *Service) SignInWithIdentity(ctx context.Context, in IdentityInput) (*AuthResult, error) {
	if strings.TrimSpace(in.Subject) == "" {
		return nil, errors.New("auth: provider subject is required")
	}
	if in.Provider == "" {
		return nil, errors.New("auth: provider is required")
	}

	// An existing link is the common case after the first sign-in.
	identity, err := s.repo.GetIdentity(ctx, in.Provider, in.Subject)
	if err == nil {
		user, err := s.repo.GetUserByID(ctx, identity.UserID)
		if err != nil {
			return nil, err
		}
		return s.finishProviderSignIn(ctx, user, in)
	}
	if !errors.Is(err, ErrNotFound) {
		return nil, err
	}

	email := NormalizeEmail(in.Email)
	if err := validateEmail(email); err != nil {
		return nil, err
	}

	var user *User
	err = s.repo.WithTx(ctx, func(tx *Repository) error {
		existing, err := tx.GetUserByEmail(ctx, email)
		switch {
		case err == nil:
			if !in.EmailVerified {
				// Without a verified provider email there is no proof the person
				// owns the address, so linking would hand them somebody's account.
				return ErrEmailNotVerified
			}
			user = existing
		case errors.Is(err, ErrNotFound):
			user = &User{
				Email:       email,
				DisplayName: strings.TrimSpace(in.DisplayName),
				AvatarURL:   strings.TrimSpace(in.AvatarURL),
			}
			if in.EmailVerified {
				now := s.now()
				user.EmailVerifiedAt = &now
			}
			if err := tx.CreateUser(ctx, user, ""); err != nil {
				return err
			}
		default:
			return err
		}

		return tx.CreateIdentity(ctx, &AuthIdentity{
			UserID:          user.ID,
			Provider:        in.Provider,
			ProviderSubject: in.Subject,
			Email:           email,
		})
	})
	if err != nil {
		return nil, err
	}

	s.logger.InfoContext(ctx, "account linked to a provider",
		"user_id", user.ID, "provider", in.Provider)
	return s.finishProviderSignIn(ctx, user, in)
}

// finishProviderSignIn issues a new device session for a verified identity.
func (s *Service) finishProviderSignIn(ctx context.Context, user *User, in IdentityInput) (*AuthResult, error) {
	tokens, err := s.IssueTokens(ctx, user.ID, in.Device)
	if err != nil {
		return nil, err
	}

	s.logger.InfoContext(ctx, "signed in with a provider",
		"user_id", user.ID, "provider", in.Provider, "session_id", tokens.SessionID)
	return &AuthResult{User: user, Tokens: tokens}, nil
}

// ChangePassword replaces the password of the signed-in user.
//
// Other devices keep working: the user proved they know the current password,
// and signing them out everywhere is what the reset flow is for.
func (s *Service) ChangePassword(ctx context.Context, userID, currentPassword, newPassword string) error {
	user, err := s.repo.GetUserByID(ctx, userID)
	if err != nil {
		return err
	}
	if err := validatePassword(user.Email, newPassword); err != nil {
		return err
	}

	hash, err := s.repo.PasswordHash(ctx, userID)
	if err != nil {
		return err
	}
	if hash == "" {
		return ErrNoPasswordSet
	}

	ok, err := s.passwords.Validate(currentPassword, hash)
	if err != nil {
		return fmt.Errorf("auth: verify current password: %w", err)
	}
	if !ok {
		return ErrPasswordMismatch
	}

	newHash, err := s.passwords.Hash(newPassword)
	if err != nil {
		return fmt.Errorf("auth: hash password: %w", err)
	}
	if err := s.repo.UpdatePasswordHash(ctx, userID, newHash, s.now()); err != nil {
		return err
	}

	s.logger.InfoContext(ctx, "password changed", "user_id", userID)
	return nil
}
