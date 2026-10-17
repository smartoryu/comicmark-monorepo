package auth

import (
	"context"
	"errors"
	"fmt"

	"comicmark/go-modular/internal/pkg/password"
)

// QueryVerified is the parameter the app reads after following an email
// confirmation link. Its value is "1" on success and "0" when the link was
// invalid or had expired, so the app can explain the failure instead of showing
// a raw API error in the browser.
const QueryVerified = "verified"

// passwordLimit mirrors the hasher's own bound, so the cheap pre-checks in
// ResetPassword and the full policy check always agree.
const passwordLimit = password.MaxPasswordLength

// InitiateEmailVerification sends a confirmation link when the address belongs
// to an account that still needs confirming.
//
// It deliberately reports nothing about whether the address exists: the caller
// responds identically either way, so this endpoint cannot be used to discover
// which addresses are registered. To make asking again useful, issuing a new
// link supersedes the previous one.
func (s *Service) InitiateEmailVerification(ctx context.Context, email string) error {
	normalized := NormalizeEmail(email)

	user, err := s.repo.GetUserByEmail(ctx, normalized)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			s.logger.DebugContext(ctx, "verification requested for an unknown address")
			return nil
		}
		return err
	}

	if user.EmailVerified() {
		s.logger.DebugContext(ctx, "verification requested for an already verified account",
			"user_id", user.ID)
		return nil
	}

	return s.notifier.SendEmailVerification(ctx, user)
}

// VerifyEmail consumes a confirmation token and marks the address as verified.
func (s *Service) VerifyEmail(ctx context.Context, token string) error {
	consumed, err := s.tokens.consume(ctx, PurposeEmailVerification, token)
	if err != nil {
		return err
	}

	if err := s.repo.MarkEmailVerified(ctx, consumed.UserID, s.now()); err != nil {
		return err
	}

	s.logger.InfoContext(ctx, "email verified", "user_id", consumed.UserID)
	return nil
}

// EmailVerificationRedirectURL is where the browser is sent after following a
// confirmation link.
//
// The link is clicked in a browser, so answering with JSON would leave the user
// staring at an API response. Redirecting into the app keeps the whole flow in
// one place, and the app reads the outcome from the query string.
func (s *Service) EmailVerificationRedirectURL(verified bool) string {
	u, err := parseAppURL(s.baseURL)
	if err != nil {
		// baseURL was validated at construction, so this is unreachable.
		return s.baseURL
	}

	q := u.Query()
	if verified {
		q.Set(QueryVerified, "1")
	} else {
		q.Set(QueryVerified, "0")
	}
	u.RawQuery = q.Encode()
	return u.String()
}

// --- password reset ---------------------------------------------------------

// ForgotPassword sends a reset link when the address belongs to an account that
// can actually use one.
//
// Like verification, it reveals nothing about whether the address exists. An
// account without a password is skipped: it signs in through a provider, and
// quietly minting a password for it would be a surprise.
func (s *Service) ForgotPassword(ctx context.Context, email string) error {
	normalized := NormalizeEmail(email)

	user, _, err := s.repo.GetUserWithPassword(ctx, normalized)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			s.logger.DebugContext(ctx, "password reset requested for an unknown address")
			return nil
		}
		return err
	}

	hash, err := s.repo.PasswordHash(ctx, user.ID)
	if err != nil {
		return err
	}
	if hash == "" {
		s.logger.DebugContext(ctx, "password reset requested for an account with no password",
			"user_id", user.ID)
		return nil
	}

	return s.notifier.SendPasswordReset(ctx, user)
}

// ResetPassword consumes a reset token, sets a new password and signs every
// device out.
//
// Revoking everything is the point of a reset: if the account was taken over,
// the attacker's sessions must die with the old password. The email is also
// marked verified, because receiving the link proves control of the inbox.
//
// The token is consumed inside the same transaction that applies the change, so
// a password that fails the policy leaves the link usable. Consuming first and
// validating afterwards would let anyone holding the link burn it with a
// deliberately weak password, locking the legitimate user out of their own reset.
func (s *Service) ResetPassword(ctx context.Context, token, newPassword string) error {
	// Cheap checks before spending argon2 time on input we already know is bad.
	if len(newPassword) < MinPasswordLength {
		return newFieldError("password",
			fmt.Sprintf("Use at least %d characters.", MinPasswordLength), ErrPasswordTooShort)
	}
	if len(newPassword) > passwordLimit {
		return newFieldError("password",
			fmt.Sprintf("Passwords cannot be longer than %d characters.", passwordLimit), ErrPasswordTooLong)
	}

	hash, err := s.passwords.Hash(newPassword)
	if err != nil {
		return fmt.Errorf("auth: hash password: %w", err)
	}

	now := s.now()
	var userID string

	err = s.repo.WithTx(ctx, func(tx *Repository) error {
		consumed, err := (&tokenStore{repo: tx, now: s.now}).consume(ctx, PurposePasswordReset, token)
		if err != nil {
			return err
		}

		user, err := tx.GetUserByID(ctx, consumed.UserID)
		if err != nil {
			return err
		}
		// The full policy check needs the address, so it happens here, where a
		// failure rolls the consumption back.
		if err := validatePassword(user.Email, newPassword); err != nil {
			return err
		}
		userID = user.ID

		if err := tx.UpdatePasswordHash(ctx, user.ID, hash, now); err != nil {
			return err
		}
		if err := tx.MarkEmailVerified(ctx, user.ID, now); err != nil {
			return err
		}
		// Same transaction as the new hash, so a failure cannot leave the new
		// password live alongside the old sessions.
		return tx.revokeAllSessions(ctx, user.ID, now)
	})
	if err != nil {
		return err
	}

	s.logger.InfoContext(ctx, "password reset", "user_id", userID)
	return nil
}
