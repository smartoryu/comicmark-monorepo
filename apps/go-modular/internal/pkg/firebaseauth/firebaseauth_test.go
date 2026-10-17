package firebaseauth

import (
	"context"
	"log/slog"
	"os"
	"testing"

	fbAuth "firebase.google.com/go/v4/auth"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func token(uid string, claims map[string]any) *fbAuth.Token {
	return &fbAuth.Token{UID: uid, Claims: claims}
}

// --- construction -----------------------------------------------------------

func TestNewRejectsAMissingProjectID(t *testing.T) {
	_, err := New(Options{Logger: slog.New(slog.DiscardHandler)})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "project ID")

	_, err = New(Options{ProjectID: "   ", Logger: slog.New(slog.DiscardHandler)})
	require.Error(t, err)
}

// The Admin SDK reads the emulator host from the environment rather than from
// configuration, so an operator configuring only FIREBASE_AUTH_EMULATOR_HOST
// must still reach the emulator.
func TestNewHonoursTheEmulatorHostFromConfig(t *testing.T) {
	// t.Setenv restores the previous value afterwards.
	t.Setenv(emulatorHostEnv, "")

	verifier, err := New(Options{
		ProjectID:        "comicmark-demo",
		AuthEmulatorHost: "localhost:9099",
		Logger:           slog.New(slog.DiscardHandler),
	})
	require.NoError(t, err)
	require.NotNil(t, verifier)

	assert.Equal(t, "localhost:9099", os.Getenv(emulatorHostEnv),
		"the SDK discovers the emulator through the environment")

	// With an emulator there are no credentials to find, which is what makes
	// local Google sign-in possible without a cloud project.
	assert.NotNil(t, verifier.client)
}

// --- claim extraction -------------------------------------------------------

func TestIdentityFromTokenReadsTheStandardClaims(t *testing.T) {
	identity, err := identityFromToken(token("firebase-uid-1", map[string]any{
		"email":          "reader@example.com",
		"email_verified": true,
		"name":           "Risf",
		"picture":        "https://cdn.example/avatar.png",
	}))
	require.NoError(t, err)

	assert.Equal(t, "firebase-uid-1", identity.UID)
	assert.Equal(t, "reader@example.com", identity.Email)
	assert.True(t, identity.EmailVerified)
	assert.Equal(t, "Risf", identity.DisplayName)
	assert.Equal(t, "https://cdn.example/avatar.png", identity.PhotoURL)
}

func TestIdentityFromTokenRejectsUnusableTokens(t *testing.T) {
	t.Run("nil token", func(t *testing.T) {
		_, err := identityFromToken(nil)
		require.Error(t, err)
	})

	t.Run("no subject", func(t *testing.T) {
		_, err := identityFromToken(token("", map[string]any{"email": "a@example.com"}))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "subject")
	})

	// Without an address there is nothing to match an account against.
	t.Run("no email", func(t *testing.T) {
		_, err := identityFromToken(token("uid", nil))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "email")
	})
}

// email_verified decides whether an identity may be linked to an existing
// account, so an unrecognised value must never be read as true.
func TestEmailVerifiedIsOnlyTrueWhenTheProviderSaysSo(t *testing.T) {
	for name, claim := range map[string]any{
		"absent":       nil,
		"true bool":    true,
		"false bool":   false,
		"true string":  "true",
		"True string":  "True",
		"false string": "false",
		"number":       1,
		"empty string": "",
		"junk":         "yes",
	} {
		t.Run(name, func(t *testing.T) {
			identity, err := identityFromToken(token("uid", map[string]any{
				"email":          "reader@example.com",
				"email_verified": claim,
			}))
			require.NoError(t, err)

			switch claim {
			case true, "true", "True":
				assert.True(t, identity.EmailVerified)
			default:
				assert.False(t, identity.EmailVerified,
					"only an explicit affirmative may mark an address verified")
			}
		})
	}
}

func TestIdentityFromTokenToleratesMissingOptionalClaims(t *testing.T) {
	identity, err := identityFromToken(token("uid", map[string]any{
		"email": "reader@example.com",
		// no name, no picture, no email_verified
	}))
	require.NoError(t, err)

	assert.Equal(t, "reader@example.com", identity.Email)
	assert.Empty(t, identity.DisplayName)
	assert.Empty(t, identity.PhotoURL)
	assert.False(t, identity.EmailVerified)
}

func TestIdentityFromTokenToleratesMistypedClaims(t *testing.T) {
	// A provider that sends a number where a string belongs must not panic.
	identity, err := identityFromToken(token("uid", map[string]any{
		"email":   "reader@example.com",
		"name":    42,
		"picture": map[string]any{"url": "https://example.com"},
	}))
	require.NoError(t, err)

	assert.Empty(t, identity.DisplayName)
	assert.Empty(t, identity.PhotoURL)
}

func TestVerifyIDTokenRejectsAnEmptyToken(t *testing.T) {
	// A verifier with no client is enough: the empty-token check happens first.
	v := &Verifier{logger: slog.New(slog.DiscardHandler)}

	_, err := v.VerifyIDToken(context.Background(), "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "id token is required")

	_, err = v.VerifyIDToken(context.Background(), "   ")
	require.Error(t, err)
}

func TestClaimStringTrimsSurroundingSpace(t *testing.T) {
	assert.Equal(t, "Risf", claimString(token("uid", map[string]any{"name": "  Risf  "}), "name"))
	assert.Empty(t, claimString(token("uid", map[string]any{"name": "   "}), "name"))
	assert.Empty(t, claimString(token("uid", nil), "name"))
}
