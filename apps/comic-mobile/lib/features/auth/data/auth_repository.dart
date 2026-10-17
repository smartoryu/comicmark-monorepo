import '../../../core/api_client.dart';
import '../models/auth_user.dart';

/// Every auth endpoint, in one place.
///
/// The repository deals in typed results and lets [ApiError] propagate: turning a
/// failure into UI text is the caller's job, because the same error means
/// different things on different screens.
class AuthRepository {
  const AuthRepository(this._client);

  final ApiClient _client;

  /// Registers an account and returns the new session.
  ///
  /// The API signs the new reader in immediately, so the app can show "check your
  /// inbox" while already authenticated.
  Future<AuthResult> register({
    required String email,
    required String password,
    String? displayName,
  }) {
    return _client.post<AuthResult>(
      '/auth/register',
      body: {
        'email': email,
        'password': password,
        if (displayName != null && displayName.trim().isNotEmpty)
          'display_name': displayName.trim(),
      },
      decode: (data) => AuthResult.fromJson(_asMap(data)),
    );
  }

  /// Signs in with an email and password.
  Future<AuthResult> login({required String email, required String password}) {
    return _client.post<AuthResult>(
      '/auth/login',
      body: {'email': email, 'password': password},
      decode: (data) => AuthResult.fromJson(_asMap(data)),
    );
  }

  /// Exchanges a Firebase ID token for a ComicMark session.
  ///
  /// Only reachable when `/auth/providers` reports Firebase as available.
  Future<AuthResult> signInWithFirebase(String idToken) {
    return _client.post<AuthResult>(
      '/auth/firebase',
      body: {'id_token': idToken},
      decode: (data) => AuthResult.fromJson(_asMap(data)),
    );
  }

  /// Ends the session behind a refresh token.
  Future<void> logout(String refreshToken) {
    return _client.post<void>(
      '/auth/logout',
      body: {'refresh_token': refreshToken},
      decode: (_) {},
    );
  }

  /// The signed-in account.
  Future<AuthUser> me() {
    return _client.get<AuthUser>(
      '/auth/me',
      decode: (data) => AuthUser.fromJson(_asMap(data)),
    );
  }

  /// Which sign-in methods the API offers.
  Future<AuthProviders> providers() {
    return _client.get<AuthProviders>(
      '/auth/providers',
      decode: (data) => AuthProviders.fromJson(_asMap(data)),
    );
  }

  // --- email verification ---------------------------------------------------

  /// Asks for a confirmation link.
  ///
  /// Deliberately takes only an address: when verification is enforced an
  /// unverified reader has no token to authenticate with, so this must work
  /// without one. The API always answers 202, whether or not the address exists.
  Future<void> requestEmailVerification(String email) {
    return _client.post<void>(
      '/auth/verification/email/initiate',
      body: {'email': email},
      decode: (_) {},
    );
  }

  /// Completes verification with the token from the emailed link.
  Future<void> verifyEmail(String token) {
    return _client.post<void>(
      '/auth/verification/email/validate',
      body: {'token': token},
      decode: (_) {},
    );
  }

  // --- password -------------------------------------------------------------

  /// Changes the password of the signed-in reader.
  Future<void> changePassword({
    required String currentPassword,
    required String newPassword,
  }) {
    return _client.put<void>(
      '/auth/password',
      body: {'current_password': currentPassword, 'new_password': newPassword},
      decode: (_) {},
    );
  }

  /// Asks for a reset link. Always succeeds, so it cannot reveal which addresses
  /// have accounts.
  Future<void> forgotPassword(String email) {
    return _client.post<void>(
      '/auth/password/forgot',
      body: {'email': email},
      decode: (_) {},
    );
  }

  /// Sets a new password from a reset link. The API signs every device out.
  Future<void> resetPassword({
    required String token,
    required String newPassword,
  }) {
    return _client.post<void>(
      '/auth/password/reset',
      body: {'token': token, 'new_password': newPassword},
      decode: (_) {},
    );
  }

  // --- device sessions ------------------------------------------------------

  /// The reader's active devices, most recently used first.
  Future<List<DeviceSession>> sessions() {
    return _client.get<List<DeviceSession>>(
      '/auth/sessions',
      decode: (data) {
        final list = _asMap(data)['sessions'];
        if (list is! List) return const [];
        return list
            .whereType<Map>()
            .map((item) => DeviceSession.fromJson(item.cast<String, Object?>()))
            .toList(growable: false);
      },
    );
  }

  /// Renames a device.
  Future<DeviceSession> renameSession(String id, String label) {
    return _client.patch<DeviceSession>(
      '/auth/sessions/$id',
      body: {'device_label': label},
      decode: (data) => DeviceSession.fromJson(_asMap(data)),
    );
  }

  /// Signs one device out.
  Future<void> revokeSession(String id) {
    return _client.delete<void>('/auth/sessions/$id', decode: (_) {});
  }

  /// Signs every device out except this one.
  Future<void> revokeOtherSessions() {
    return _client.delete<void>('/auth/sessions', decode: (_) {});
  }
}

/// Reads a JSON object, tolerating anything else.
///
/// The API always returns an object, so a non-object means something upstream
/// changed; treating it as empty lets the caller report a sensible error rather
/// than crashing on a cast.
Map<String, Object?> _asMap(Object? data) {
  if (data is Map) return data.cast<String, Object?>();
  return const {};
}
