package main

import (
	"context"
	"log/slog"

	"comicmark/go-modular/internal/config"
	"comicmark/go-modular/internal/modules/auth"
	"comicmark/go-modular/internal/pkg/firebaseauth"
)

// newFirebaseVerifier builds the Firebase ID-token verifier when Firebase is
// configured, and returns a nil verifier otherwise.
//
// A nil result is not an error: it is what makes the whole Google sign-in path
// disappear, leaving email and password working. Absence of a Firebase project
// must never stop the server from starting.
func newFirebaseVerifier(cfg *config.Config, logger *slog.Logger) (auth.FirebaseVerifier, error) {
	if !cfg.FirebaseEnabled() {
		logger.Info("firebase sign-in is disabled",
			"hint", "set FIREBASE_PROJECT_ID (and credentials) to enable Google sign-in")
		return nil, nil
	}

	verifier, err := firebaseauth.New(firebaseauth.Options{
		ProjectID:        cfg.Firebase.ProjectID,
		CredentialsFile:  cfg.Firebase.CredentialsFile,
		AuthEmulatorHost: cfg.Firebase.AuthEmulatorHost,
		Logger:           logger,
	})
	if err != nil {
		return nil, err
	}

	logger.Info("firebase sign-in is enabled",
		"project_id", cfg.Firebase.ProjectID,
		"emulator", cfg.Firebase.AuthEmulatorHost != "")

	return firebaseAdapter{inner: verifier}, nil
}

// firebaseAdapter maps the SDK-backed verifier onto the auth module's narrow
// interface.
//
// The two identity types are deliberately separate: the module declares what it
// needs, and nothing else in the codebase has to import the Firebase SDK. The
// conversion belongs here, in the composition root.
type firebaseAdapter struct {
	inner *firebaseauth.Verifier
}

func (a firebaseAdapter) VerifyIDToken(ctx context.Context, idToken string) (auth.FirebaseIdentity, error) {
	identity, err := a.inner.VerifyIDToken(ctx, idToken)
	if err != nil {
		return auth.FirebaseIdentity{}, err
	}

	return auth.FirebaseIdentity{
		UID:           identity.UID,
		Email:         identity.Email,
		EmailVerified: identity.EmailVerified,
		DisplayName:   identity.DisplayName,
		PhotoURL:      identity.PhotoURL,
	}, nil
}
