import 'package:flutter_secure_storage/flutter_secure_storage.dart';

/// Persists the access and refresh tokens.
///
/// The tokens are the reader's whole session, so they live in the platform's
/// secure storage (Keychain on iOS, EncryptedSharedPreferences on Android, and
/// an encrypted store on the web) rather than in plain preferences.
///
/// On web, secure storage needs a secure context: HTTPS or localhost. Both
/// development (`localhost:5173`) and the embedded build (served from the API)
/// satisfy that. If the plugin is unavailable anyway, the store degrades to
/// memory so the app still works for the session rather than refusing to start.
class TokenStore {
  /// [storage] is injectable so tests can run without platform channels.
  TokenStore({FlutterSecureStorage? storage})
      : _storage = storage ?? const FlutterSecureStorage();

  final FlutterSecureStorage _storage;

  /// Set when the platform store turns out to be unusable, so the fallback is
  /// used for the rest of the session.
  bool _degraded = false;

  String? _memoryAccessToken;
  String? _memoryRefreshToken;

  static const _accessKey = 'comicmark.access_token';
  static const _refreshKey = 'comicmark.refresh_token';

  /// Whether tokens are being kept in memory only, which means the session ends
  /// when the app is closed.
  bool get isDegraded => _degraded;

  /// The stored refresh token, or null when nobody is signed in.
  Future<String?> readRefreshToken() async {
    if (_degraded) return _memoryRefreshToken;
    try {
      return await _storage.read(key: _refreshKey);
    } catch (_) {
      _degrade();
      return _memoryRefreshToken;
    }
  }

  /// The stored access token, or null when nobody is signed in.
  Future<String?> readAccessToken() async {
    if (_degraded) return _memoryAccessToken;
    try {
      return await _storage.read(key: _accessKey);
    } catch (_) {
      _degrade();
      return _memoryAccessToken;
    }
  }

  /// Replaces both tokens.
  ///
  /// This is what a sign-in, a registration and a token refresh all do, so it
  /// takes the pair together: storing them separately would allow a crash
  /// between the two writes to leave a mismatched session behind.
  Future<void> writeTokens({
    required String accessToken,
    required String refreshToken,
  }) async {
    _memoryAccessToken = accessToken;
    _memoryRefreshToken = refreshToken;

    if (_degraded) return;
    try {
      await _storage.write(key: _accessKey, value: accessToken);
      await _storage.write(key: _refreshKey, value: refreshToken);
    } catch (_) {
      _degrade();
    }
  }

  /// Forgets the session.
  Future<void> clear() async {
    _memoryAccessToken = null;
    _memoryRefreshToken = null;

    if (_degraded) return;
    try {
      await _storage.delete(key: _accessKey);
      await _storage.delete(key: _refreshKey);
    } catch (_) {
      _degrade();
    }
  }

  /// Switches to memory-only storage after a platform failure.
  void _degrade() {
    _degraded = true;
  }
}
