package auth

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// signInOnTwoDevices registers an account and signs in a second time, returning
// the service plus both sessions' tokens.
func signInOnTwoDevices(t *testing.T, svc *testService) (*AuthResult, *AuthResult) {
	t.Helper()

	first, err := svc.Register(context.Background(), registerInput("reader@example.com", "correct horse battery"))
	require.NoError(t, err)

	phone := testDevice()
	phone.Platform = "android"
	phone.UserAgent = "Dart/3.13.5 (dart:io) Android"

	second, err := svc.Login(context.Background(), LoginInput{
		Email: "reader@example.com", Password: "correct horse battery", Device: phone,
	})
	require.NoError(t, err)

	return first, second
}

func principalFor(t *testing.T, svc *testService, accessToken string) *Principal {
	t.Helper()

	principal, err := svc.Authenticate(context.Background(), accessToken)
	require.NoError(t, err)
	return principal
}

func TestListSessionsFlagsTheCallingDevice(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	first, second := signInOnTwoDevices(t, svc)

	principal := principalFor(t, svc, second.Tokens.AccessToken)
	views, err := svc.ListSessions(ctx, principal)
	require.NoError(t, err)
	require.Len(t, views, 2)

	currentCount := 0
	for _, v := range views {
		if v.Current {
			currentCount++
			assert.Equal(t, second.Tokens.SessionID, v.ID,
				"the flag must mark the device making the request")
		}
	}
	assert.Equal(t, 1, currentCount, "exactly one session is the current device")

	byID := make(map[string]SessionView, len(views))
	for _, v := range views {
		byID[v.ID] = v
	}

	// Registration created the session for the browser device...
	desktop, ok := byID[first.Tokens.SessionID]
	require.True(t, ok)
	assert.False(t, desktop.Current)
	assert.Equal(t, "Chrome on macOS", desktop.DeviceLabel)
	assert.Equal(t, "web", desktop.Platform)

	// ...and the later sign-in created one for the Android app, each labelled
	// from its own User-Agent rather than from a client-supplied string.
	phone, ok := byID[second.Tokens.SessionID]
	require.True(t, ok)
	assert.True(t, phone.Current)
	assert.Equal(t, "ComicMark app on Android", phone.DeviceLabel)
	assert.Equal(t, "android", phone.Platform)
}

func TestListSessionsExcludesRevokedDevices(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	_, second := signInOnTwoDevices(t, svc)
	principal := principalFor(t, svc, second.Tokens.AccessToken)

	views, err := svc.ListSessions(ctx, principal)
	require.NoError(t, err)
	require.Len(t, views, 2)

	for _, v := range views {
		if !v.Current {
			require.NoError(t, svc.RevokeSession(ctx, principal, v.ID))
		}
	}

	views, err = svc.ListSessions(ctx, principal)
	require.NoError(t, err)
	require.Len(t, views, 1)
	assert.True(t, views[0].Current)
}

func TestRenameSessionThroughTheUseCase(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	first, second := signInOnTwoDevices(t, svc)
	principal := principalFor(t, svc, second.Tokens.AccessToken)

	renamed, err := svc.RenameSession(ctx, principal, first.Tokens.SessionID, "  Work laptop  ")
	require.NoError(t, err)
	assert.Equal(t, "Work laptop", renamed.DeviceLabel, "the label should be trimmed")

	stored, err := svc.repo.GetSession(ctx, first.Tokens.SessionID)
	require.NoError(t, err)
	assert.Equal(t, "Work laptop", stored.DeviceLabel)
}

func TestRenameSessionRejectsBadLabels(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	first, second := signInOnTwoDevices(t, svc)
	principal := principalFor(t, svc, second.Tokens.AccessToken)

	for name, label := range map[string]string{
		"empty":      "",
		"whitespace": "   ",
		"too long":   strings.Repeat("x", MaxDeviceLabelLength+1),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := svc.RenameSession(ctx, principal, first.Tokens.SessionID, label)
			require.ErrorIs(t, err, ErrInvalidDeviceLabel)

			var fieldErr *FieldError
			require.ErrorAs(t, err, &fieldErr)
			assert.Equal(t, "device_label", fieldErr.Field)
		})
	}
}

func TestRevokeSessionSignsTheOtherDeviceOut(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	first, second := signInOnTwoDevices(t, svc)
	principal := principalFor(t, svc, second.Tokens.AccessToken)

	require.NoError(t, svc.RevokeSession(ctx, principal, first.Tokens.SessionID))

	// The revoked device cannot use its access or refresh token any more.
	_, err := svc.Authenticate(ctx, first.Tokens.AccessToken)
	require.ErrorIs(t, err, ErrSessionRevoked)

	_, err = svc.RotateRefresh(ctx, first.Tokens.RefreshToken)
	require.Error(t, err)

	// The caller is unaffected.
	_, err = svc.Authenticate(ctx, second.Tokens.AccessToken)
	require.NoError(t, err, "revoking another device must not sign the caller out")
}

// Silently killing the credentials the caller is mid-request with would produce
// confusing errors, so it is refused with a status the client can act on.
func TestRevokeSessionRefusesTheCallingDevice(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	_, second := signInOnTwoDevices(t, svc)
	principal := principalFor(t, svc, second.Tokens.AccessToken)

	err := svc.RevokeSession(ctx, principal, principal.SessionID)
	require.ErrorIs(t, err, ErrCannotRevokeCurrent)

	// The session still works.
	_, err = svc.Authenticate(ctx, second.Tokens.AccessToken)
	require.NoError(t, err)
}

func TestRevokeSessionCannotTouchAnotherUsersDevice(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	_, mine := signInOnTwoDevices(t, svc)
	myPrincipal := principalFor(t, svc, mine.Tokens.AccessToken)

	// A second account, with its own session.
	theirs, err := svc.Register(ctx, registerInput("other@example.com", "another fine password"))
	require.NoError(t, err)

	// Reported as not found rather than forbidden, so session ids cannot be
	// probed for existence.
	err = svc.RevokeSession(ctx, myPrincipal, theirs.Tokens.SessionID)
	require.ErrorIs(t, err, ErrNotFound)

	_, err = svc.Authenticate(ctx, theirs.Tokens.AccessToken)
	require.NoError(t, err, "the other account's session must be untouched")
}

func TestRenameSessionCannotTouchAnotherUsersDevice(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	_, mine := signInOnTwoDevices(t, svc)
	myPrincipal := principalFor(t, svc, mine.Tokens.AccessToken)

	theirs, err := svc.Register(ctx, registerInput("other@example.com", "another fine password"))
	require.NoError(t, err)

	_, err = svc.RenameSession(ctx, myPrincipal, theirs.Tokens.SessionID, "hijacked")
	require.ErrorIs(t, err, ErrNotFound)

	stored, err := svc.repo.GetSession(ctx, theirs.Tokens.SessionID)
	require.NoError(t, err)
	assert.NotEqual(t, "hijacked", stored.DeviceLabel)
}

func TestUseCaseRevokeSessionReportsAnUnknownSession(t *testing.T) {
	svc := newTestService(t)

	_, second := signInOnTwoDevices(t, svc)
	principal := principalFor(t, svc, second.Tokens.AccessToken)

	require.ErrorIs(t, svc.RevokeSession(context.Background(), principal, "no-such-session"), ErrNotFound)
	require.ErrorIs(t, svc.RevokeSession(context.Background(), principal, ""), ErrNotFound)
}

func TestRevokeOtherSessionsKeepsOnlyTheCaller(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	first, second := signInOnTwoDevices(t, svc)
	third, err := svc.Login(ctx, LoginInput{
		Email: "reader@example.com", Password: "correct horse battery", Device: testDevice(),
	})
	require.NoError(t, err)

	principal := principalFor(t, svc, second.Tokens.AccessToken)
	require.NoError(t, svc.RevokeOtherSessions(ctx, principal))

	for _, tokens := range []*TokenPair{first.Tokens, third.Tokens} {
		_, err := svc.Authenticate(ctx, tokens.AccessToken)
		require.ErrorIs(t, err, ErrSessionRevoked)
	}

	_, err = svc.Authenticate(ctx, second.Tokens.AccessToken)
	require.NoError(t, err, "sign out everywhere else must keep the caller signed in")

	views, err := svc.ListSessions(ctx, principal)
	require.NoError(t, err)
	require.Len(t, views, 1)
	assert.True(t, views[0].Current)
}

func TestPruneExpiredTokensReportsWhatItRemoved(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	first, _ := signInOnTwoDevices(t, svc)
	_ = first

	// Nothing is expired yet.
	deleted, err := svc.PruneExpiredTokens(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(0), deleted)

	// Move past the refresh lifetime of everything issued so far.
	svc.now = func() time.Time { return time.Now().Add(31 * 24 * time.Hour) }

	deleted, err = svc.PruneExpiredTokens(ctx)
	require.NoError(t, err)
	assert.Positive(t, deleted)
}

func TestSessionBelongsToUser(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	registered, err := svc.Register(ctx, registerInput("reader@example.com", "correct horse battery"))
	require.NoError(t, err)

	ok, err := svc.SessionBelongsToUser(ctx, registered.User.ID, registered.Tokens.SessionID)
	require.NoError(t, err)
	assert.True(t, ok)

	ok, err = svc.SessionBelongsToUser(ctx, "another-user", registered.Tokens.SessionID)
	require.NoError(t, err)
	assert.False(t, ok)

	ok, err = svc.SessionBelongsToUser(ctx, registered.User.ID, "no-such-session")
	require.NoError(t, err)
	assert.False(t, ok)
}
