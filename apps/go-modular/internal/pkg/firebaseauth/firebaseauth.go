// Package firebaseauth verifies Firebase Authentication ID tokens.
//
// Firebase is the identity broker for Google sign-in: the app authenticates with
// Firebase, then exchanges the resulting ID token for ComicMark's own tokens.
// Verifying the ID token is what proves the caller really did authenticate, so
// the client is never trusted to assert who it is.
//
// The package is optional. When Firebase is not configured the API does not mount
// the provider route at all, so the feature disappears rather than failing at
// runtime.
package firebaseauth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"

	firebase "firebase.google.com/go/v4"
	fbAuth "firebase.google.com/go/v4/auth"
	"google.golang.org/api/option"
)

// emulatorHostEnv is the variable the Firebase Admin SDK reads to talk to the
// Authentication emulator. It is honoured so Google sign-in can be exercised
// locally without a cloud project.
const emulatorHostEnv = "FIREBASE_AUTH_EMULATOR_HOST"

// Options configures the verifier.
type Options struct {
	// ProjectID is the Firebase project the tokens must belong to.
	ProjectID string
	// CredentialsFile is a service-account JSON path. Empty means Application
	// Default Credentials.
	CredentialsFile string
	// AuthEmulatorHost, when set, points the SDK at a local emulator.
	AuthEmulatorHost string
	Logger           *slog.Logger
}

// Identity is the verified content of an ID token.
type Identity struct {
	// UID is Firebase's stable user id.
	UID string
	// Email is the address the provider reports.
	Email string
	// EmailVerified is the provider's own verification state. It matters:
	// linking to an existing account without it would let anyone who can add an
	// unverified address take over that account.
	EmailVerified bool
	DisplayName   string
	PhotoURL      string
}

// Verifier checks ID tokens against a Firebase project.
type Verifier struct {
	client *fbAuth.Client
	logger *slog.Logger
}

// New builds a verifier. It fails loudly when credentials or the project id are
// unusable, because a silently disabled sign-in method is worse than a startup
// error.
func New(opts Options) (*Verifier, error) {
	if strings.TrimSpace(opts.ProjectID) == "" {
		return nil, errors.New("firebaseauth: project ID is required")
	}

	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}

	// The Admin SDK reads the emulator host from the environment rather than
	// from configuration, so an operator setting only FIREBASE_AUTH_EMULATOR_HOST
	// via our config still reaches the emulator.
	if host := strings.TrimSpace(opts.AuthEmulatorHost); host != "" {
		if err := os.Setenv(emulatorHostEnv, host); err != nil {
			return nil, fmt.Errorf("firebaseauth: set %s: %w", emulatorHostEnv, err)
		}
	}

	var clientOpts []option.ClientOption
	switch {
	case strings.TrimSpace(opts.CredentialsFile) != "":
		clientOpts = append(clientOpts, option.WithCredentialsFile(opts.CredentialsFile))
	case strings.TrimSpace(opts.AuthEmulatorHost) != "":
		// The emulator issues unsigned tokens and needs no credentials; without
		// this the SDK would still try to find some and fail at startup.
		clientOpts = append(clientOpts, option.WithoutAuthentication())
	}

	app, err := firebase.NewApp(context.Background(), &firebase.Config{ProjectID: opts.ProjectID}, clientOpts...)
	if err != nil {
		return nil, fmt.Errorf("firebaseauth: initialise app: %w", err)
	}

	client, err := app.Auth(context.Background())
	if err != nil {
		return nil, fmt.Errorf("firebaseauth: initialise auth client: %w", err)
	}

	logger.Debug("firebase auth client ready", "project_id", opts.ProjectID)
	return &Verifier{client: client, logger: logger}, nil
}

// VerifyIDToken validates an ID token's signature, issuer, audience and expiry,
// and returns the identity behind it.
//
// Revocation is not checked on every call: that would add a network round trip to
// each sign-in, and a revoked token stops working when it expires. ComicMark's
// own access token lifetime is what actually bounds a session.
func (v *Verifier) VerifyIDToken(ctx context.Context, idToken string) (*Identity, error) {
	if strings.TrimSpace(idToken) == "" {
		return nil, errors.New("firebaseauth: id token is required")
	}

	token, err := v.client.VerifyIDToken(ctx, idToken)
	if err != nil {
		return nil, fmt.Errorf("firebaseauth: verify id token: %w", err)
	}

	return identityFromToken(token)
}

// identityFromToken extracts the identity from an already-verified token.
//
// It is separate from VerifyIDToken so the claim handling can be tested without a
// Firebase project: the signature check is the SDK's job, and this is ours.
func identityFromToken(token *fbAuth.Token) (*Identity, error) {
	if token == nil {
		return nil, errors.New("firebaseauth: id token is empty")
	}
	if strings.TrimSpace(token.UID) == "" {
		return nil, errors.New("firebaseauth: id token has no subject")
	}

	identity := &Identity{
		UID:           token.UID,
		Email:         claimString(token, "email"),
		EmailVerified: claimBool(token, "email_verified"),
		DisplayName:   claimString(token, "name"),
		PhotoURL:      claimString(token, "picture"),
	}

	// Without an address there is nothing to match an account against, and the
	// provider has not proven anything about ownership.
	if identity.Email == "" {
		return nil, errors.New("firebaseauth: id token has no email address")
	}

	return identity, nil
}

// claimString reads a string claim, tolerating a missing or mistyped value.
func claimString(token *fbAuth.Token, key string) string {
	if token.Claims == nil {
		return ""
	}
	value, ok := token.Claims[key].(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(value)
}

// claimBool reads a boolean claim. Firebase sends email_verified as a real
// boolean, but a string "true" is accepted rather than being read as false:
// quietly treating a verified address as unverified would block a legitimate
// sign-in, and treating an unverified one as verified is the dangerous
// direction, so an unrecognised value must never become true.
func claimBool(token *fbAuth.Token, key string) bool {
	if token.Claims == nil {
		return false
	}
	switch value := token.Claims[key].(type) {
	case bool:
		return value
	case string:
		return strings.EqualFold(value, "true")
	default:
		return false
	}
}
