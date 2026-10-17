package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// MaxDeviceLabelLength bounds the user-visible device name.
const MaxDeviceLabelLength = 64

// ListSessions returns the caller's active devices, most recently used first.
//
// The session making the request is flagged, so the app can label it "This
// device" and refuse to offer a revoke action that would sign the user out
// mid-request.
func (s *Service) ListSessions(ctx context.Context, principal *Principal) ([]SessionView, error) {
	sessions, err := s.repo.ListSessions(ctx, principal.UserID, false)
	if err != nil {
		return nil, err
	}

	views := make([]SessionView, 0, len(sessions))
	for _, session := range sessions {
		views = append(views, SessionView{
			Session: *session,
			Current: session.ID == principal.SessionID,
		})
	}
	return views, nil
}

// RenameSession updates the user-visible name of one of the caller's devices.
// Returns the updated session.
func (s *Service) RenameSession(ctx context.Context, principal *Principal, sessionID, label string) (*Session, error) {
	label = strings.TrimSpace(label)
	switch {
	case label == "":
		return nil, newFieldError("device_label", "Give this device a name.", ErrInvalidDeviceLabel)
	case len(label) > MaxDeviceLabelLength:
		return nil, newFieldError("device_label",
			fmt.Sprintf("Device names cannot be longer than %d characters.", MaxDeviceLabelLength),
			ErrInvalidDeviceLabel)
	}

	session, err := s.ownedSession(ctx, principal, sessionID)
	if err != nil {
		return nil, err
	}

	if err := s.repo.RenameSession(ctx, session.ID, label); err != nil {
		return nil, err
	}
	session.DeviceLabel = label

	s.logger.InfoContext(ctx, "session renamed", "user_id", principal.UserID, "session_id", session.ID)
	return session, nil
}

// RevokeSession signs one of the caller's other devices out.
//
// Revoking the calling session is refused rather than performed: the response to
// this request has not been sent yet, and silently invalidating the caller's own
// credentials mid-flight is exactly the sort of thing that produces confusing
// errors. The client calls the sign-out endpoint for that, which is a deliberate
// action it can handle.
func (s *Service) RevokeSession(ctx context.Context, principal *Principal, sessionID string) error {
	if sessionID == principal.SessionID {
		return ErrCannotRevokeCurrent
	}

	session, err := s.ownedSession(ctx, principal, sessionID)
	if err != nil {
		return err
	}

	if err := s.repo.RevokeSession(ctx, session.ID, s.now()); err != nil {
		return err
	}

	s.logger.InfoContext(ctx, "session revoked", "user_id", principal.UserID, "session_id", session.ID)
	return nil
}

// RevokeOtherSessions signs out every device except the calling one.
func (s *Service) RevokeOtherSessions(ctx context.Context, principal *Principal) error {
	if err := s.repo.RevokeOtherSessions(ctx, principal.UserID, principal.SessionID, s.now()); err != nil {
		return err
	}

	s.logger.InfoContext(ctx, "all other sessions revoked",
		"user_id", principal.UserID, "kept_session_id", principal.SessionID)
	return nil
}

// ownedSession loads a session and confirms it belongs to the caller.
//
// Another user's session id is reported as not found, so the endpoint cannot be
// used to discover which session ids exist.
func (s *Service) ownedSession(ctx context.Context, principal *Principal, sessionID string) (*Session, error) {
	if strings.TrimSpace(sessionID) == "" {
		return nil, ErrNotFound
	}

	session, err := s.repo.GetSession(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	if session.UserID != principal.UserID {
		s.logger.WarnContext(ctx, "session access attempted by a different user",
			"user_id", principal.UserID, "session_id", sessionID)
		return nil, ErrNotFound
	}
	return session, nil
}

// --- session upkeep ---------------------------------------------------------

// touchSession records activity, skipping the write when the session was already
// touched recently. Without the throttle every authenticated request would take
// the single SQLite write lock, and last_seen_at would be written far more often
// than any UI could display it.
func (s *Service) touchSession(ctx context.Context, session *Session) {
	if s.sessionTouchInterval > 0 && s.now().Sub(session.LastSeenAt) < s.sessionTouchInterval {
		return
	}
	if err := s.repo.TouchSession(ctx, session.ID, s.now()); err != nil {
		// Activity tracking is not worth failing a request over.
		s.logger.WarnContext(ctx, "failed to update session activity",
			"session_id", session.ID, "error", err)
	}
}

// idleExpired reports whether a session has gone unused for too long. A device
// that has not been seen in weeks should not be able to keep refreshing its
// tokens indefinitely.
func (s *Service) idleExpired(session *Session) bool {
	if s.sessionIdleTTL <= 0 {
		return false
	}
	return s.now().Sub(session.LastSeenAt) > s.sessionIdleTTL
}

// PruneExpiredTokens deletes refresh tokens that can no longer be used and
// reports how many rows went away.
//
// This runs on sign-in, which every returning user triggers, so no scheduled job
// is needed to stop the table growing without bound.
func (s *Service) PruneExpiredTokens(ctx context.Context) (int64, error) {
	deleted, err := s.repo.DeleteExpiredRefreshTokens(ctx, s.now())
	if err != nil {
		return 0, err
	}
	if deleted > 0 {
		s.logger.DebugContext(ctx, "pruned expired refresh tokens", "count", deleted)
	}
	return deleted, nil
}

// SessionBelongsToUser reports whether a session id belongs to the user, used by
// other modules that need to scope data to a device.
func (s *Service) SessionBelongsToUser(ctx context.Context, userID, sessionID string) (bool, error) {
	session, err := s.repo.GetSession(ctx, sessionID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return false, nil
		}
		return false, err
	}
	return session.UserID == userID, nil
}
