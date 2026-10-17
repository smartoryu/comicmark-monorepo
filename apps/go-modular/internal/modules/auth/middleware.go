package auth

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"comicmark/go-modular/internal/httpx"
)

// principalKey is the context key carrying the authenticated caller. It is
// unexported so nothing outside this package can forge one.
type principalKey struct{}

// PrincipalFrom returns the authenticated caller attached by RequireAuth.
//
// Other modules use this to scope their own data to the signed-in user.
func PrincipalFrom(ctx context.Context) (*Principal, bool) {
	principal, ok := ctx.Value(principalKey{}).(*Principal)
	return principal, ok
}

// RequireAuth rejects requests without a valid access token and attaches the
// caller to the request context.
//
// The session behind the token is re-checked on every request, which is what
// makes revoking a device take effect immediately rather than whenever the
// access token happens to expire.
func (s *Service) RequireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, ok := bearerToken(r)
		if !ok {
			httpx.WriteError(w, r, http.StatusUnauthorized, httpx.CodeUnauthorized, httpx.MsgUnauthorized)
			return
		}

		principal, err := s.Authenticate(r.Context(), token)
		if err != nil {
			s.writeAuthError(w, r, err)
			return
		}

		ctx := context.WithValue(r.Context(), principalKey{}, principal)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// bearerToken extracts the credential from an Authorization header.
func bearerToken(r *http.Request) (string, bool) {
	header := r.Header.Get("Authorization")
	if header == "" {
		return "", false
	}

	// The scheme is case-insensitive per RFC 7235.
	scheme, value, found := strings.Cut(header, " ")
	if !found || !strings.EqualFold(scheme, "Bearer") {
		return "", false
	}

	token := strings.TrimSpace(value)
	if token == "" {
		return "", false
	}
	return token, true
}

// writeAuthError maps an authentication failure to a response.
//
// Everything that is not an explicit, actionable state is reported as a plain
// 401, so the endpoint cannot be used to probe token internals.
func (s *Service) writeAuthError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, ErrSessionRevoked):
		// Distinguishable only in wording; the client's remedy is the same.
		httpx.WriteError(w, r, http.StatusUnauthorized, "session_revoked", "This session has ended. Please sign in again.")
	case errors.Is(err, ErrTokenInvalid):
		httpx.WriteError(w, r, http.StatusUnauthorized, httpx.CodeUnauthorized, httpx.MsgUnauthorized)
	default:
		s.logger.ErrorContext(r.Context(), "authentication failed unexpectedly",
			"error", err, "path", r.URL.Path)
		httpx.WriteError(w, r, http.StatusInternalServerError, httpx.CodeInternal, httpx.MsgInternal)
	}
}

// withUserID is a small helper for handlers that need the caller id.
func withUserID(principal *Principal) slog.Attr {
	return slog.String("user_id", principal.UserID)
}
