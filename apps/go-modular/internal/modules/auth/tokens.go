package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"time"

	"comicmark/go-modular/internal/pkg/jwt"
	"comicmark/go-modular/internal/pkg/mailer"
)

// tokenEntropyBytes is the amount of randomness in a one-time token. 32 bytes
// puts guessing a live token out of reach even at high request rates.
const tokenEntropyBytes = 32

// Query parameters carrying one-time tokens.
const (
	// QueryToken is the parameter on the verification link endpoint, which is
	// hit by a browser rather than by the app.
	QueryToken = "token"
	// QueryVerifyToken is the parameter the app reads from the emailed link.
	QueryVerifyToken = "verify_token"
	// QueryResetToken is the password reset equivalent.
	QueryResetToken = "reset_token"
)

// generateOneTimeToken returns a fresh token and its storage fingerprint.
//
// The raw value is handed to the user and never stored; only the SHA-256 hash
// goes into the database, so a leaked database does not yield working links.
func generateOneTimeToken() (raw, hash string, err error) {
	buf := make([]byte, tokenEntropyBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", "", fmt.Errorf("auth: generate token: %w", err)
	}
	// Raw URL encoding keeps the token safe to place in a query string.
	raw = base64.RawURLEncoding.EncodeToString(buf)
	return raw, jwt.HashToken(raw), nil
}

// tokenStore reads and consumes one-time tokens.
//
// Issuing lives with the notifier, which is what knows the configured lifetimes;
// consuming only needs to check what was stored.
type tokenStore struct {
	repo *Repository
	now  func() time.Time
}

func newTokenStore(repo *Repository) *tokenStore {
	return &tokenStore{repo: repo, now: time.Now}
}

// consume atomically claims a token for a purpose.
func (ts *tokenStore) consume(ctx context.Context, purpose, raw string) (*OneTimeToken, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, ErrTokenInvalid
	}
	return ts.repo.ConsumeOneTimeToken(ctx, purpose, jwt.HashToken(raw), ts.now())
}

// invalidateOtherTokens burns any outstanding token of the same purpose, used
// when a flow wants to make a previous link stop working immediately.
func (ts *tokenStore) invalidateOtherTokens(ctx context.Context, userID, purpose string) error {
	return ts.repo.InvalidateOneTimeTokens(ctx, userID, purpose, ts.now())
}

// --- email notifier ---------------------------------------------------------

// NotifierOptions configures the transactional email notifier.
type NotifierOptions struct {
	Repository *Repository
	Mailer     mailer.Mailer
	Logger     *slog.Logger

	// BaseURL is the app origin that links point at.
	BaseURL string
	// VerificationTokenTTL bounds how long a confirmation link works.
	VerificationTokenTTL time.Duration
	// PasswordResetTokenTTL bounds how long a reset link works.
	PasswordResetTokenTTL time.Duration
}

// notifier issues one-time tokens and emails the links that carry them. It is the
// production implementation of Notifier.
type notifier struct {
	repo            *Repository
	mailer          mailer.Mailer
	logger          *slog.Logger
	baseURL         string
	verificationTTL time.Duration
	resetTTL        time.Duration
	now             func() time.Time
}

var _ Notifier = (*notifier)(nil)

// NewNotifier validates its options and returns the email notifier.
func NewNotifier(opts NotifierOptions) (Notifier, error) {
	switch {
	case opts.Repository == nil:
		return nil, errors.New("auth: repository is required")
	case opts.Mailer == nil:
		return nil, errors.New("auth: mailer is required")
	case opts.VerificationTokenTTL <= 0:
		return nil, errors.New("auth: verification token TTL must be positive")
	case opts.PasswordResetTokenTTL <= 0:
		return nil, errors.New("auth: password reset token TTL must be positive")
	}

	base := strings.TrimRight(strings.TrimSpace(opts.BaseURL), "/")
	if base == "" {
		return nil, errors.New("auth: base URL is required")
	}
	if _, err := parseAppURL(base); err != nil {
		return nil, fmt.Errorf("auth: base URL %q is not usable: %w", opts.BaseURL, err)
	}

	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}

	return &notifier{
		repo:            opts.Repository,
		mailer:          opts.Mailer,
		logger:          logger,
		baseURL:         base,
		verificationTTL: opts.VerificationTokenTTL,
		resetTTL:        opts.PasswordResetTokenTTL,
		now:             time.Now,
	}, nil
}

// SendEmailVerification sends the confirmation link.
//
// An already-verified account is left alone: there is nothing to confirm, and
// issuing a token would invalidate a link the user may still be using.
func (n *notifier) SendEmailVerification(ctx context.Context, user *User) error {
	if user.EmailVerified() {
		n.logger.DebugContext(ctx, "skipping verification email for a verified account",
			"user_id", user.ID)
		return nil
	}

	token, err := n.issue(ctx, user.ID, PurposeEmailVerification, n.verificationTTL)
	if err != nil {
		return err
	}

	msg, err := mailer.VerificationMessage(user.Email, mailer.ActionData{
		DisplayName: user.DisplayName,
		Email:       user.Email,
		Link:        actionLink(n.baseURL, QueryVerifyToken, token),
		ExpiresIn:   humanDuration(n.verificationTTL),
	})
	if err != nil {
		return err
	}

	return n.send(ctx, msg, "verification", user.ID)
}

// SendPasswordReset sends the reset link.
func (n *notifier) SendPasswordReset(ctx context.Context, user *User) error {
	token, err := n.issue(ctx, user.ID, PurposePasswordReset, n.resetTTL)
	if err != nil {
		return err
	}

	msg, err := mailer.PasswordResetMessage(user.Email, mailer.ActionData{
		DisplayName: user.DisplayName,
		Email:       user.Email,
		Link:        actionLink(n.baseURL, QueryResetToken, token),
		ExpiresIn:   humanDuration(n.resetTTL),
	})
	if err != nil {
		return err
	}

	return n.send(ctx, msg, "password reset", user.ID)
}

// issue mints a token and stores its fingerprint, superseding any outstanding
// token of the same purpose so only the newest link works.
func (n *notifier) issue(ctx context.Context, userID, purpose string, ttl time.Duration) (string, error) {
	raw, hash, err := generateOneTimeToken()
	if err != nil {
		return "", err
	}

	now := n.now()
	if err := n.repo.CreateOneTimeToken(ctx, &OneTimeToken{
		UserID:    userID,
		Purpose:   purpose,
		TokenHash: hash,
		ExpiresAt: now.Add(ttl),
		CreatedAt: now,
	}); err != nil {
		return "", err
	}
	return raw, nil
}

func (n *notifier) send(ctx context.Context, msg mailer.Message, kind, userID string) error {
	if err := n.mailer.Send(ctx, msg); err != nil {
		return fmt.Errorf("auth: send %s email: %w", kind, err)
	}
	n.logger.InfoContext(ctx, "sent account email", "kind", kind, "user_id", userID)
	return nil
}

// parseAppURL parses the configured app origin, requiring an absolute URL and
// defaulting an empty path to "/".
//
// The absolute check matters: a relative base URL would produce links that cannot
// be clicked in an email. The path default matters too, because a base URL of
// "https://app.example.com" would otherwise yield
// "https://app.example.com?verify_token=…", which browsers accept but which
// reads as a mistake and which some clients mishandle.
func parseAppURL(base string) (*url.URL, error) {
	u, err := url.Parse(base)
	if err != nil {
		return nil, err
	}
	if !u.IsAbs() || u.Host == "" {
		return nil, fmt.Errorf("auth: %q must be an absolute URL including scheme and host", base)
	}
	if u.Path == "" {
		u.Path = "/"
	}
	return u, nil
}

// actionLink builds a link to the app with the token in the query string.
//
// The app reads the parameter and calls the matching API endpoint, which keeps
// the emailed address independent of the API host.
func actionLink(baseURL, param, token string) string {
	u, err := parseAppURL(baseURL)
	if err != nil {
		// baseURL is validated at construction, so this is unreachable.
		return baseURL
	}

	q := u.Query()
	q.Set(param, token)
	u.RawQuery = q.Encode()
	return u.String()
}

// humanDuration renders a TTL the way the email copy needs it. The templates say
// "This link expires in {{.ExpiresIn}}".
func humanDuration(d time.Duration) string {
	switch {
	case d >= 24*time.Hour:
		days := int(d.Hours() / 24)
		if days == 1 {
			return "24 hours"
		}
		return fmt.Sprintf("%d days", days)
	case d >= time.Hour:
		hours := int(d.Hours())
		if hours == 1 {
			return "1 hour"
		}
		return fmt.Sprintf("%d hours", hours)
	case d >= time.Minute:
		minutes := int(d.Minutes())
		if minutes == 1 {
			return "1 minute"
		}
		return fmt.Sprintf("%d minutes", minutes)
	default:
		return "a few seconds"
	}
}
