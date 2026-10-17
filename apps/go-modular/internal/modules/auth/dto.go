package auth

import "time"

// Request and response shapes for the auth endpoints.
//
// The struct tags do double duty: `json` drives decoding and encoding, and
// `validate` is read by the validation package, which reports problems using the
// JSON name. Limits are duplicated here and in the use case on purpose: the DTO
// rejects obviously malformed input before any work happens, and the use case
// enforces the policy that applies regardless of transport.

// registerRequest is the sign-up payload.
type registerRequest struct {
	Email       string `json:"email" validate:"required,email,max=254"`
	Password    string `json:"password" validate:"required,min=8,max=1024"`
	DisplayName string `json:"display_name" validate:"omitempty,max=80"`
}

// loginRequest is the sign-in payload.
type loginRequest struct {
	Email    string `json:"email" validate:"required,email,max=254"`
	Password string `json:"password" validate:"required,max=1024"`
}

// refreshRequest exchanges a refresh token.
type refreshRequest struct {
	RefreshToken string `json:"refresh_token" validate:"required"`
}

// logoutRequest names the session to end.
type logoutRequest struct {
	RefreshToken string `json:"refresh_token" validate:"required"`
}

// emailRequest is used by the endpoints that take an address and reveal nothing,
// so the same shape serves verification resend and password reset requests.
type emailRequest struct {
	Email string `json:"email" validate:"required,email,max=254"`
}

// tokenRequest carries a one-time token.
type tokenRequest struct {
	Token string `json:"token" validate:"required"`
}

// resetPasswordRequest completes a password reset.
type resetPasswordRequest struct {
	Token       string `json:"token" validate:"required"`
	NewPassword string `json:"new_password" validate:"required,min=8,max=1024"`
}

// changePasswordRequest changes the password of a signed-in user.
type changePasswordRequest struct {
	CurrentPassword string `json:"current_password" validate:"required,max=1024"`
	NewPassword     string `json:"new_password" validate:"required,min=8,max=1024"`
}

// renameSessionRequest renames a device.
type renameSessionRequest struct {
	DeviceLabel string `json:"device_label" validate:"required,max=64"`
}

// firebaseRequest exchanges a Firebase ID token for ComicMark tokens.
type firebaseRequest struct {
	IDToken string `json:"id_token" validate:"required"`
}

// --- responses --------------------------------------------------------------

// userResponse is the public representation of an account. It deliberately has
// no password field to leak.
type userResponse struct {
	ID            string    `json:"id"`
	Email         string    `json:"email"`
	DisplayName   string    `json:"display_name,omitempty"`
	AvatarURL     string    `json:"avatar_url,omitempty"`
	EmailVerified bool      `json:"email_verified"`
	CreatedAt     time.Time `json:"created_at"`
}

// authResponse is returned by register, login, refresh and the provider exchange.
// Tokens are flat rather than nested, matching the documented API shape.
type authResponse struct {
	AccessToken      string       `json:"access_token"`
	RefreshToken     string       `json:"refresh_token"`
	TokenType        string       `json:"token_type"`
	ExpiresIn        int          `json:"expires_in"`
	AccessExpiresAt  time.Time    `json:"access_expires_at"`
	RefreshExpiresAt time.Time    `json:"refresh_expires_at"`
	SessionID        string       `json:"session_id"`
	User             userResponse `json:"user"`
}

// sessionResponse describes one signed-in device.
type sessionResponse struct {
	ID          string    `json:"id"`
	DeviceLabel string    `json:"device_label"`
	Platform    string    `json:"platform"`
	IP          string    `json:"ip,omitempty"`
	Current     bool      `json:"current"`
	LastSeenAt  time.Time `json:"last_seen_at"`
	CreatedAt   time.Time `json:"created_at"`
}

// providersResponse tells the client which sign-in methods exist, so the app can
// hide the Google button without a rebuild when Firebase is not configured.
type providersResponse struct {
	Password bool `json:"password"`
	Firebase bool `json:"firebase"`
}

// --- mapping ----------------------------------------------------------------

func newUserResponse(user *User) userResponse {
	return userResponse{
		ID:            user.ID,
		Email:         user.Email,
		DisplayName:   user.DisplayName,
		AvatarURL:     user.AvatarURL,
		EmailVerified: user.EmailVerified(),
		CreatedAt:     user.CreatedAt,
	}
}

func newAuthResponse(result *AuthResult) authResponse {
	return authResponse{
		AccessToken:      result.Tokens.AccessToken,
		RefreshToken:     result.Tokens.RefreshToken,
		TokenType:        result.Tokens.TokenType,
		ExpiresIn:        result.Tokens.ExpiresIn,
		AccessExpiresAt:  result.Tokens.AccessExpiresAt,
		RefreshExpiresAt: result.Tokens.RefreshExpiresAt,
		SessionID:        result.Tokens.SessionID,
		User:             newUserResponse(result.User),
	}
}

func newSessionResponse(view SessionView) sessionResponse {
	return sessionResponse{
		ID:          view.ID,
		DeviceLabel: view.DeviceLabel,
		Platform:    view.Platform,
		IP:          view.IP,
		Current:     view.Current,
		LastSeenAt:  view.LastSeenAt,
		CreatedAt:   view.CreatedAt,
	}
}
