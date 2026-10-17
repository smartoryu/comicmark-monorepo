import 'package:flutter_riverpod/flutter_riverpod.dart';

import '../../../app/env.dart';
import '../../../app/providers.dart';
import '../../../core/api_error.dart';
import '../data/auth_repository.dart';
import '../models/auth_user.dart';

/// The auth repository.
final authRepositoryProvider = Provider<AuthRepository>(
  (ref) => AuthRepository(ref.watch(apiClientProvider)),
);

/// Which sign-in methods the API offers, fetched once.
final authProvidersProvider = FutureProvider<AuthProviders>((ref) async {
  try {
    return await ref.watch(authRepositoryProvider).providers();
  } on ApiError {
    // The sign-in screen must still render if this call fails, and email and
    // password is the safe assumption: it is the method that always exists.
    return AuthProviders.fallback;
  }
});

/// The signed-in account, or null when nobody is signed in.
///
/// `AsyncValue` carries the three states the router and the screens care about:
/// loading while the stored session is read, data (possibly null) once known, and
/// error only if start-up itself failed.
final authControllerProvider =
    AsyncNotifierProvider<AuthController, AuthUser?>(AuthController.new);

/// Holds the session and performs every action that changes it.
class AuthController extends AsyncNotifier<AuthUser?> {
  bool _sessionExpiredHandled = false;

  @override
  Future<AuthUser?> build() async {
    final client = ref.watch(apiClientProvider);

    await client.restoreSession();
    if (!await client.hasSession()) return null;

    try {
      return await ref.read(authRepositoryProvider).me();
    } on ApiError catch (error) {
      // A stored token that the server rejects is stale: clear it and start
      // signed out. Anything else (the server being down) leaves the stored
      // session alone so the reader is not signed out by a network blip.
      if (error.isUnauthenticated) {
        await client.clearSession();
        return null;
      }
      rethrow;
    }
  }

  /// Registers an account and adopts its session.
  Future<void> register({
    required String email,
    required String password,
    String? displayName,
  }) async {
    final result = await ref.read(authRepositoryProvider).register(
          email: email,
          password: password,
          displayName: displayName,
        );
    await _adopt(result);
  }

  /// Signs in and adopts the session.
  Future<void> login({required String email, required String password}) async {
    final result = await ref
        .read(authRepositoryProvider)
        .login(email: email, password: password);
    await _adopt(result);
  }

  /// Signs in with a Firebase ID token, when that provider is enabled.
  Future<void> signInWithFirebase(String idToken) async {
    final result = await ref.read(authRepositoryProvider).signInWithFirebase(idToken);
    await _adopt(result);
  }

  /// Signs out, ending this device's session.
  ///
  /// The local session is cleared even if the API call fails: a reader who taps
  /// sign out must not be left signed in because the network was down.
  Future<void> signOut() async {
    final client = ref.read(apiClientProvider);
    final refreshToken = await ref.read(tokenStoreProvider).readRefreshToken();

    if (refreshToken != null) {
      try {
        await ref.read(authRepositoryProvider).logout(refreshToken);
      } on ApiError {
        // Nothing to do: the local session is cleared below regardless.
      }
    }

    await client.clearSession();
    state = const AsyncData(null);
  }

  /// Re-reads the account.
  ///
  /// Used after verifying an email, where the server's view of the reader has
  /// changed and the app needs to reflect it.
  Future<void> refreshUser() async {
    if (!await ref.read(apiClientProvider).hasSession()) {
      state = const AsyncData(null);
      return;
    }

    try {
      final user = await ref.read(authRepositoryProvider).me();
      state = AsyncData(user);
    } on ApiError catch (error, stackTrace) {
      if (error.isUnauthenticated) {
        await ref.read(apiClientProvider).clearSession();
        state = const AsyncData(null);
        return;
      }
      state = AsyncError(error, stackTrace);
    }
  }

  /// Marks the address as verified locally, so the banner disappears without a
  /// round trip.
  void markEmailVerified() {
    final current = state.value;
    if (current == null || current.emailVerified) return;

    state = AsyncData(
      AuthUser(
        id: current.id,
        email: current.email,
        emailVerified: true,
        displayName: current.displayName,
        avatarUrl: current.avatarUrl,
        createdAt: current.createdAt,
      ),
    );
  }

  /// Stores a new session and publishes the account.
  Future<void> _adopt(AuthResult result) async {
    await ref.read(apiClientProvider).adoptSession(
          accessToken: result.session.accessToken,
          refreshToken: result.session.refreshToken,
        );
    _sessionExpiredHandled = false;
    state = AsyncData(result.user);
  }

  /// Called by the HTTP layer when the refresh token is no longer usable.
  ///
  /// This is how a rejected refresh token becomes "the reader is signed out"
  /// rather than a stream of failed requests.
  Future<void> handleSessionExpired() async {
    // The HTTP layer can report this from several failing requests at once, and
    // the reader only needs to be told once per signed-out period.
    if (_sessionExpiredHandled) return;
    _sessionExpiredHandled = true;

    if (state.value != null) {
      state = const AsyncData(null);
    }
  }
}

/// Convenience: whether a session is currently held.
///
/// Three states, because "still checking" is not "signed out": the library shows
/// a spinner rather than a sign-in prompt while the stored session is read.
enum SessionStatus { checking, signedIn, signedOut }

/// The current session status, for screens that branch on it.
final sessionStatusProvider = Provider<SessionStatus>((ref) {
  final auth = ref.watch(authControllerProvider);

  return auth.when(
    loading: () => SessionStatus.checking,
    error: (_, _) => SessionStatus.signedOut,
    data: (user) => user == null ? SessionStatus.signedOut : SessionStatus.signedIn,
  );
});

/// Whether the build carries Firebase configuration at all.
///
/// The API is still the authority on whether the provider is *mounted*; this only
/// says whether the client could initialise the SDK if asked.
bool get clientSupportsFirebaseSignIn => firebaseConfigured;
