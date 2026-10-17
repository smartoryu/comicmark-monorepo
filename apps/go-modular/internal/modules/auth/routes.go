package auth

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"net/http"

	"comicmark/go-modular/internal/config"
	"comicmark/go-modular/internal/pkg/jwt"
	"comicmark/go-modular/internal/pkg/mailer"
	"comicmark/go-modular/internal/pkg/password"
)

// FirebaseVerifier verifies a Firebase ID token and returns the identity behind
// it. It is satisfied by internal/pkg/firebaseauth when Firebase is configured.
//
// Declaring the interface here rather than depending on the concrete type keeps
// the Firebase SDK out of this module's import graph, and lets the whole Google
// sign-in path be tested with a stub.
type FirebaseVerifier interface {
	// VerifyIDToken validates the token and returns the provider subject (the
	// Firebase uid), the email, whether that email is verified, and a display
	// name when one is available.
	VerifyIDToken(ctx context.Context, idToken string) (FirebaseIdentity, error)
}

// FirebaseIdentity is what a verified Firebase token tells us.
type FirebaseIdentity struct {
	UID           string
	Email         string
	EmailVerified bool
	DisplayName   string
	PhotoURL      string
}

// Deps is what the auth module needs from the application.
type Deps struct {
	Config    *config.Config
	Logger    *slog.Logger
	DB        *sql.DB
	JWT       *jwt.Manager
	Passwords *password.Hasher
	Mailer    mailer.AsyncMailer

	// AuthLimiter guards sign-in and registration.
	AuthLimiter func(http.Handler) http.Handler
	// SensitiveLimiter guards the endpoints that send email, where each request
	// costs a message and a one-time token.
	SensitiveLimiter func(http.Handler) http.Handler

	// Firebase is optional. When nil the provider route is not mounted and the
	// providers endpoint reports Firebase as unavailable.
	Firebase FirebaseVerifier
}

// Module is the wired auth feature.
type Module struct {
	service *Service
	handler *handler
	deps    Deps
}

// New builds the auth module.
func New(deps Deps) (*Module, error) {
	switch {
	case deps.Config == nil:
		return nil, errors.New("auth: config is required")
	case deps.DB == nil:
		return nil, errors.New("auth: database is required")
	case deps.JWT == nil:
		return nil, errors.New("auth: jwt manager is required")
	case deps.Passwords == nil:
		return nil, errors.New("auth: password hasher is required")
	case deps.Mailer == nil:
		return nil, errors.New("auth: mailer is required")
	case deps.AuthLimiter == nil:
		return nil, errors.New("auth: auth rate limiter is required")
	case deps.SensitiveLimiter == nil:
		return nil, errors.New("auth: sensitive rate limiter is required")
	}

	logger := deps.Logger
	if logger == nil {
		logger = slog.Default()
	}

	repo := NewRepository(deps.DB)

	// The notifier owns token issuing because it is what knows the configured
	// lifetimes; the service consumes those tokens.
	notifier, err := NewNotifier(NotifierOptions{
		Repository:            repo,
		Mailer:                deps.Mailer,
		Logger:                logger,
		BaseURL:               deps.Config.App.BaseURL,
		VerificationTokenTTL:  deps.Config.App.VerificationTokenTTL,
		PasswordResetTokenTTL: deps.Config.App.PasswordResetTokenTTL,
	})
	if err != nil {
		return nil, err
	}

	service, err := NewService(Options{
		Repository:               repo,
		JWT:                      deps.JWT,
		Passwords:                deps.Passwords,
		Notifier:                 notifier,
		Logger:                   logger,
		BaseURL:                  deps.Config.App.BaseURL,
		RequireEmailVerification: deps.Config.App.RequireEmailVerification,
		SessionIdleTTL:           deps.Config.App.SessionIdleTTL,
		SessionTouchInterval:     deps.Config.App.SessionTouchInterval,
		VerificationTokenTTL:     deps.Config.App.VerificationTokenTTL,
		PasswordResetTokenTTL:    deps.Config.App.PasswordResetTokenTTL,
	})
	if err != nil {
		return nil, err
	}

	return &Module{
		service: service,
		handler: newHandler(service, logger, deps.Firebase),
		deps:    deps,
	}, nil
}

// Service exposes the use cases, so other modules can share the same instance
// rather than opening their own connection to the same tables.
func (m *Module) Service() *Service { return m.service }

// RequireAuth is the middleware protecting routes that need a signed-in user.
// It is what other modules apply to their own routes.
func (m *Module) RequireAuth(next http.Handler) http.Handler {
	return m.service.RequireAuth(next)
}

// RegisterRoutes mounts the auth endpoints on the API's v1 mux.
//
// Rate limits are applied per route rather than to the whole group: signing in
// deserves a stricter budget than reading your own profile, and the endpoints
// that send email deserve a stricter one still.
func (m *Module) RegisterRoutes(api *http.ServeMux) {
	h := m.handler

	account := func(pattern string, handler http.HandlerFunc) {
		api.Handle(pattern, m.deps.AuthLimiter(handler))
	}
	sensitive := func(pattern string, handler http.HandlerFunc) {
		api.Handle(pattern, m.deps.SensitiveLimiter(handler))
	}
	open := func(pattern string, handler http.HandlerFunc) {
		api.HandleFunc(pattern, handler)
	}
	protected := func(pattern string, handler http.HandlerFunc) {
		api.Handle(pattern, m.RequireAuth(handler))
	}

	// Account lifecycle.
	account("POST /api/v1/auth/register", h.Register)
	account("POST /api/v1/auth/login", h.Login)
	account("POST /api/v1/auth/refresh", h.Refresh)
	open("GET /api/v1/auth/providers", h.Providers)

	// Signing out requires a valid session. The refresh token in the body is what
	// names the session to end, and holding it is what proves authority over it.
	protected("POST /api/v1/auth/logout", h.Logout)
	protected("GET /api/v1/auth/me", h.Me)

	// Email verification.
	sensitive("POST /api/v1/auth/verification/email/initiate", h.InitiateEmailVerification)
	open("POST /api/v1/auth/verification/email/validate", h.ValidateEmailVerification)
	// The link inside the email; not rate limited, because a mail client may
	// fetch it more than once.
	open("GET /api/v1/auth/verification/email/verify", h.VerifyEmailByLink)

	// Password.
	protected("PUT /api/v1/auth/password", h.ChangePassword)
	sensitive("POST /api/v1/auth/password/forgot", h.ForgotPassword)
	account("POST /api/v1/auth/password/reset", h.ResetPassword)

	// Device sessions.
	protected("GET /api/v1/auth/sessions", h.ListSessions)
	protected("DELETE /api/v1/auth/sessions", h.RevokeOtherSessions)
	protected("PATCH /api/v1/auth/sessions/{id}", h.RenameSession)
	protected("DELETE /api/v1/auth/sessions/{id}", h.RevokeSession)

	// Optional provider sign-in. Absent when Firebase is not configured, which
	// is what makes the app hide the Google button.
	if m.deps.Firebase != nil {
		account("POST /api/v1/auth/firebase", h.SignInWithFirebase)
	}
}
