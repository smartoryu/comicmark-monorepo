package auth

import (
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"comicmark/go-modular/internal/httpx"
	"comicmark/go-modular/internal/pkg/validation"
)

// handler turns HTTP requests into use case calls and domain errors back into
// responses.
type handler struct {
	service   *Service
	logger    *slog.Logger
	validator *validation.Validator

	// firebase, when non-nil, verifies Firebase ID tokens. It is nil unless
	// FIREBASE_PROJECT_ID is configured, and the route is not mounted at all in
	// that case, so the Google button disappears rather than failing at runtime.
	firebase FirebaseVerifier
	// providers reports which sign-in methods are available.
	providers providersResponse
}

func newHandler(service *Service, logger *slog.Logger, firebase FirebaseVerifier) *handler {
	return &handler{
		service:   service,
		logger:    logger,
		validator: validation.New(),
		firebase:  firebase,
		providers: providersResponse{
			Password: true,
			Firebase: firebase != nil,
		},
	}
}

// --- account ----------------------------------------------------------------

// Register handles POST /api/v1/auth/register.
func (h *handler) Register(w http.ResponseWriter, r *http.Request) {
	var req registerRequest
	if !httpx.DecodeJSONOrFail(w, r, &req) {
		return
	}
	if fields := h.validator.FieldErrors(req); fields != nil {
		httpx.WriteFieldErrors(w, r, fields)
		return
	}

	result, err := h.service.Register(r.Context(), RegisterInput{
		Email:       req.Email,
		Password:    req.Password,
		DisplayName: req.DisplayName,
		Device:      deviceFromRequest(r),
	})
	if err != nil {
		h.writeError(w, r, err)
		return
	}

	httpx.WriteJSON(w, r, http.StatusCreated, newAuthResponse(result))
}

// Login handles POST /api/v1/auth/login.
func (h *handler) Login(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if !httpx.DecodeJSONOrFail(w, r, &req) {
		return
	}
	if fields := h.validator.FieldErrors(req); fields != nil {
		httpx.WriteFieldErrors(w, r, fields)
		return
	}

	result, err := h.service.Login(r.Context(), LoginInput{
		Email:    req.Email,
		Password: req.Password,
		Device:   deviceFromRequest(r),
	})
	if err != nil {
		h.writeError(w, r, err)
		return
	}

	httpx.WriteJSON(w, r, http.StatusOK, newAuthResponse(result))
}

// Refresh handles POST /api/v1/auth/refresh.
func (h *handler) Refresh(w http.ResponseWriter, r *http.Request) {
	var req refreshRequest
	if !httpx.DecodeJSONOrFail(w, r, &req) {
		return
	}
	if fields := h.validator.FieldErrors(req); fields != nil {
		httpx.WriteFieldErrors(w, r, fields)
		return
	}

	result, err := h.service.RotateRefresh(r.Context(), req.RefreshToken)
	if err != nil {
		h.writeError(w, r, err)
		return
	}

	httpx.WriteJSON(w, r, http.StatusOK, newAuthResponse(result))
}

// Logout handles POST /api/v1/auth/logout.
func (h *handler) Logout(w http.ResponseWriter, r *http.Request) {
	var req logoutRequest
	if !httpx.DecodeJSONOrFail(w, r, &req) {
		return
	}

	// Signing out is idempotent and never reveals whether the token was real.
	if err := h.service.Logout(r.Context(), req.RefreshToken); err != nil {
		h.writeError(w, r, err)
		return
	}

	httpx.NoContent(w)
}

// Me handles GET /api/v1/auth/me.
func (h *handler) Me(w http.ResponseWriter, r *http.Request) {
	principal, ok := PrincipalFrom(r.Context())
	if !ok {
		httpx.WriteError(w, r, http.StatusUnauthorized, httpx.CodeUnauthorized, httpx.MsgUnauthorized)
		return
	}

	user, err := h.service.Me(r.Context(), principal.UserID)
	if err != nil {
		h.writeError(w, r, err)
		return
	}

	httpx.WriteJSON(w, r, http.StatusOK, newUserResponse(user))
}

// Providers handles GET /api/v1/auth/providers.
func (h *handler) Providers(w http.ResponseWriter, r *http.Request) {
	httpx.WriteJSON(w, r, http.StatusOK, h.providers)
}

// --- email verification -----------------------------------------------------

// InitiateEmailVerification handles POST /api/v1/auth/verification/email/initiate.
//
// Deliberately unauthenticated: when verification is required an unverified user
// has no token to authenticate with, so asking for another link has to work
// without one. The answer is always 202, whether or not the address exists.
func (h *handler) InitiateEmailVerification(w http.ResponseWriter, r *http.Request) {
	var req emailRequest
	if !httpx.DecodeJSONOrFail(w, r, &req) {
		return
	}
	if fields := h.validator.FieldErrors(req); fields != nil {
		httpx.WriteFieldErrors(w, r, fields)
		return
	}

	if err := h.service.InitiateEmailVerification(r.Context(), req.Email); err != nil {
		// Logged rather than surfaced: telling the caller that delivery failed
		// for this particular address would leak that the address exists.
		h.logger.ErrorContext(r.Context(), "failed to send verification email", "error", err)
	}

	httpx.WriteJSON(w, r, http.StatusAccepted, map[string]string{
		"message": "If that address belongs to an unverified account, a confirmation link is on its way.",
	})
}

// ValidateEmailVerification handles POST /api/v1/auth/verification/email/validate.
func (h *handler) ValidateEmailVerification(w http.ResponseWriter, r *http.Request) {
	var req tokenRequest
	if !httpx.DecodeJSONOrFail(w, r, &req) {
		return
	}
	if fields := h.validator.FieldErrors(req); fields != nil {
		httpx.WriteFieldErrors(w, r, fields)
		return
	}

	if err := h.service.VerifyEmail(r.Context(), req.Token); err != nil {
		h.writeError(w, r, err)
		return
	}

	httpx.WriteJSON(w, r, http.StatusOK, map[string]bool{"email_verified": true})
}

// VerifyEmailByLink handles GET /api/v1/auth/verification/email/verify?token=….
//
// This is the server-side alternative to handing the token to the app: a browser
// is sent straight here, the token is validated, and the browser is redirected
// back into the app. It always redirects, with an outcome the app can read,
// because answering a clicked link with JSON would strand the user on an API
// response. The Referer header is suppressed so the token cannot leak onward
// through a third-party resource on the page it lands on.
func (h *handler) VerifyEmailByLink(w http.ResponseWriter, r *http.Request) {
	token := strings.TrimSpace(queryValue(r, QueryToken))

	verified := false
	if token != "" {
		if err := h.service.VerifyEmail(r.Context(), token); err != nil {
			h.logger.InfoContext(r.Context(), "email verification link was rejected", "error", err)
		} else {
			verified = true
		}
	}

	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, h.service.EmailVerificationRedirectURL(verified), http.StatusFound)
}

// --- password ---------------------------------------------------------------

// ChangePassword handles PUT /api/v1/auth/password.
func (h *handler) ChangePassword(w http.ResponseWriter, r *http.Request) {
	principal, ok := PrincipalFrom(r.Context())
	if !ok {
		httpx.WriteError(w, r, http.StatusUnauthorized, httpx.CodeUnauthorized, httpx.MsgUnauthorized)
		return
	}

	var req changePasswordRequest
	if !httpx.DecodeJSONOrFail(w, r, &req) {
		return
	}
	if fields := h.validator.FieldErrors(req); fields != nil {
		httpx.WriteFieldErrors(w, r, fields)
		return
	}

	err := h.service.ChangePassword(r.Context(), principal.UserID, req.CurrentPassword, req.NewPassword)
	if err != nil {
		h.writeError(w, r, err)
		return
	}

	httpx.NoContent(w)
}

// ForgotPassword handles POST /api/v1/auth/password/forgot.
//
// Always 202: the response must not reveal whether the address has an account.
func (h *handler) ForgotPassword(w http.ResponseWriter, r *http.Request) {
	var req emailRequest
	if !httpx.DecodeJSONOrFail(w, r, &req) {
		return
	}
	if fields := h.validator.FieldErrors(req); fields != nil {
		httpx.WriteFieldErrors(w, r, fields)
		return
	}

	if err := h.service.ForgotPassword(r.Context(), req.Email); err != nil {
		h.logger.ErrorContext(r.Context(), "failed to send password reset email", "error", err)
	}

	httpx.WriteJSON(w, r, http.StatusAccepted, map[string]string{
		"message": "If that address has an account, a reset link is on its way.",
	})
}

// ResetPassword handles POST /api/v1/auth/password/reset.
func (h *handler) ResetPassword(w http.ResponseWriter, r *http.Request) {
	var req resetPasswordRequest
	if !httpx.DecodeJSONOrFail(w, r, &req) {
		return
	}
	if fields := h.validator.FieldErrors(req); fields != nil {
		httpx.WriteFieldErrors(w, r, fields)
		return
	}

	if err := h.service.ResetPassword(r.Context(), req.Token, req.NewPassword); err != nil {
		h.writeError(w, r, err)
		return
	}

	httpx.NoContent(w)
}

// --- sessions ---------------------------------------------------------------

// ListSessions handles GET /api/v1/auth/sessions.
func (h *handler) ListSessions(w http.ResponseWriter, r *http.Request) {
	principal, ok := PrincipalFrom(r.Context())
	if !ok {
		httpx.WriteError(w, r, http.StatusUnauthorized, httpx.CodeUnauthorized, httpx.MsgUnauthorized)
		return
	}

	views, err := h.service.ListSessions(r.Context(), principal)
	if err != nil {
		h.writeError(w, r, err)
		return
	}

	out := make([]sessionResponse, 0, len(views))
	for _, view := range views {
		out = append(out, newSessionResponse(view))
	}

	httpx.WriteJSON(w, r, http.StatusOK, map[string]any{"sessions": out})
}

// RenameSession handles PATCH /api/v1/auth/sessions/{id}.
func (h *handler) RenameSession(w http.ResponseWriter, r *http.Request) {
	principal, ok := PrincipalFrom(r.Context())
	if !ok {
		httpx.WriteError(w, r, http.StatusUnauthorized, httpx.CodeUnauthorized, httpx.MsgUnauthorized)
		return
	}

	var req renameSessionRequest
	if !httpx.DecodeJSONOrFail(w, r, &req) {
		return
	}
	if fields := h.validator.FieldErrors(req); fields != nil {
		httpx.WriteFieldErrors(w, r, fields)
		return
	}

	session, err := h.service.RenameSession(r.Context(), principal, r.PathValue("id"), req.DeviceLabel)
	if err != nil {
		h.writeError(w, r, err)
		return
	}

	httpx.WriteJSON(w, r, http.StatusOK, newSessionResponse(SessionView{
		Session: *session,
		Current: session.ID == principal.SessionID,
	}))
}

// RevokeSession handles DELETE /api/v1/auth/sessions/{id}.
func (h *handler) RevokeSession(w http.ResponseWriter, r *http.Request) {
	principal, ok := PrincipalFrom(r.Context())
	if !ok {
		httpx.WriteError(w, r, http.StatusUnauthorized, httpx.CodeUnauthorized, httpx.MsgUnauthorized)
		return
	}

	if err := h.service.RevokeSession(r.Context(), principal, r.PathValue("id")); err != nil {
		h.writeError(w, r, err)
		return
	}

	httpx.NoContent(w)
}

// RevokeOtherSessions handles DELETE /api/v1/auth/sessions.
func (h *handler) RevokeOtherSessions(w http.ResponseWriter, r *http.Request) {
	principal, ok := PrincipalFrom(r.Context())
	if !ok {
		httpx.WriteError(w, r, http.StatusUnauthorized, httpx.CodeUnauthorized, httpx.MsgUnauthorized)
		return
	}

	if err := h.service.RevokeOtherSessions(r.Context(), principal); err != nil {
		h.writeError(w, r, err)
		return
	}

	httpx.NoContent(w)
}

// --- provider sign-in -------------------------------------------------------

// SignInWithFirebase handles POST /api/v1/auth/firebase.
//
// The route is only mounted when a verifier is configured, so this handler can
// assume one exists.
func (h *handler) SignInWithFirebase(w http.ResponseWriter, r *http.Request) {
	var req firebaseRequest
	if !httpx.DecodeJSONOrFail(w, r, &req) {
		return
	}
	if fields := h.validator.FieldErrors(req); fields != nil {
		httpx.WriteFieldErrors(w, r, fields)
		return
	}

	identity, err := h.firebase.VerifyIDToken(r.Context(), req.IDToken)
	if err != nil {
		h.logger.InfoContext(r.Context(), "firebase id token was rejected", "error", err)
		httpx.WriteError(w, r, http.StatusUnauthorized, "invalid_provider_token",
			"We could not verify that sign-in. Please try again.")
		return
	}

	result, err := h.service.SignInWithIdentity(r.Context(), IdentityInput{
		Provider:      ProviderFirebase,
		Subject:       identity.UID,
		Email:         identity.Email,
		EmailVerified: identity.EmailVerified,
		DisplayName:   identity.DisplayName,
		AvatarURL:     identity.PhotoURL,
		Device:        deviceFromRequest(r),
	})
	if err != nil {
		h.writeError(w, r, err)
		return
	}

	httpx.WriteJSON(w, r, http.StatusOK, newAuthResponse(result))
}

// --- helpers ----------------------------------------------------------------

// deviceFromRequest describes the client for the session being created.
//
// The platform header is preferred over User-Agent sniffing because a Dart HTTP
// client does not disclose its operating system, and a wrong device label is
// exactly what makes a security screen useless.
func deviceFromRequest(r *http.Request) DeviceInfo {
	return DeviceInfo{
		UserAgent: r.UserAgent(),
		Platform:  r.Header.Get(httpx.HeaderClientPlatform),
		IP:        httpx.ClientIP(r),
	}
}

// queryValue reads a query parameter, tolerating a malformed query string.
func queryValue(r *http.Request, key string) string {
	values, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return ""
	}
	return values.Get(key)
}

// writeError maps a domain error onto the API's error envelope.
//
// The mapping lives here rather than in the use case so that the transport
// decision (status code, machine-readable code) has exactly one home.
func (h *handler) writeError(w http.ResponseWriter, r *http.Request, err error) {
	var fieldErr *FieldError

	switch {
	case errors.As(err, &fieldErr):
		httpx.WriteFieldError(w, r, fieldErr.Field, fieldErr.Message)

	case errors.Is(err, ErrEmailTaken):
		httpx.WriteError(w, r, http.StatusConflict, "email_taken",
			"An account already exists for that email address.")

	case errors.Is(err, ErrInvalidCredentials), errors.Is(err, ErrNoPasswordSet):
		// A social-only account is reported as bad credentials, so the endpoint
		// does not disclose that the address is registered.
		httpx.WriteError(w, r, http.StatusUnauthorized, "invalid_credentials",
			"The email or password you entered is incorrect.")

	case errors.Is(err, ErrEmailNotVerified):
		httpx.WriteError(w, r, http.StatusForbidden, "email_not_verified",
			"Confirm your email address before signing in.")

	case errors.Is(err, ErrPasswordMismatch):
		httpx.WriteFieldError(w, r, "current_password", "That is not your current password.")

	case errors.Is(err, ErrTokenInvalid):
		httpx.WriteError(w, r, http.StatusUnauthorized, "token_invalid",
			"That link is invalid or has expired.")

	case errors.Is(err, ErrTokenReused):
		httpx.WriteError(w, r, http.StatusUnauthorized, "token_reused",
			"This session has ended. Please sign in again.")

	case errors.Is(err, ErrSessionRevoked):
		httpx.WriteError(w, r, http.StatusUnauthorized, "session_revoked",
			"This session has ended. Please sign in again.")

	case errors.Is(err, ErrCannotRevokeCurrent):
		httpx.WriteError(w, r, http.StatusConflict, "cannot_revoke_current",
			"Use sign out to end this session.")

	case errors.Is(err, ErrNotFound):
		httpx.WriteError(w, r, http.StatusNotFound, httpx.CodeNotFound, httpx.MsgNotFound)

	default:
		h.logger.ErrorContext(r.Context(), "unhandled auth error",
			"error", err, "path", r.URL.Path, "method", r.Method)
		httpx.WriteError(w, r, http.StatusInternalServerError, httpx.CodeInternal, httpx.MsgInternal)
	}
}
