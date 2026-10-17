/// The account model, and the shapes the auth endpoints return.
library;

/// A signed-in account.
class AuthUser {
  const AuthUser({
    required this.id,
    required this.email,
    required this.emailVerified,
    this.displayName,
    this.avatarUrl,
    this.createdAt,
  });

  final String id;
  final String email;
  final bool emailVerified;
  final String? displayName;
  final String? avatarUrl;
  final DateTime? createdAt;

  /// What to call the reader. Falls back to the part of the address before the
  /// @, which is friendlier than showing the whole thing.
  String get shortName {
    final name = displayName?.trim();
    if (name != null && name.isNotEmpty) return name;

    final local = email.split('@').first;
    return local.isEmpty ? email : local;
  }

  /// The first letter, for an avatar placeholder.
  String get initial {
    final name = shortName;
    return name.isEmpty ? '?' : name.substring(0, 1).toUpperCase();
  }

  factory AuthUser.fromJson(Map<String, Object?> json) {
    return AuthUser(
      id: json['id'] as String? ?? '',
      email: json['email'] as String? ?? '',
      emailVerified: json['email_verified'] as bool? ?? false,
      displayName: json['display_name'] as String?,
      avatarUrl: json['avatar_url'] as String?,
      createdAt: _parseDate(json['created_at']),
    );
  }
}

/// A token pair plus the session it belongs to.
///
/// Returned by register, sign-in, refresh and the provider exchange, so one type
/// covers every way a session begins.
class AuthSession {
  const AuthSession({
    required this.accessToken,
    required this.refreshToken,
    required this.sessionId,
  });

  final String accessToken;
  final String refreshToken;

  /// Which device session these tokens belong to. The sessions screen uses it to
  /// mark "this device".
  final String sessionId;

  factory AuthSession.fromJson(Map<String, Object?> json) {
    return AuthSession(
      accessToken: json['access_token'] as String? ?? '',
      refreshToken: json['refresh_token'] as String? ?? '',
      sessionId: json['session_id'] as String? ?? '',
    );
  }
}

/// The result of a sign-in or registration: who, and with which tokens.
class AuthResult {
  const AuthResult({required this.user, required this.session});

  final AuthUser user;
  final AuthSession session;

  factory AuthResult.fromJson(Map<String, Object?> json) {
    return AuthResult(
      user: AuthUser.fromJson(
        (json['user'] as Map?)?.cast<String, Object?>() ?? const {},
      ),
      session: AuthSession.fromJson(json),
    );
  }
}

/// Which sign-in methods the API offers.
///
/// Fetched rather than assumed, so the app never shows a Google button that
/// cannot work: the route does not exist unless a Firebase project is configured.
class AuthProviders {
  const AuthProviders({required this.password, required this.firebase});

  final bool password;
  final bool firebase;

  /// Used before the real answer arrives, so the sign-in form does not flicker.
  static const fallback = AuthProviders(password: true, firebase: false);

  factory AuthProviders.fromJson(Map<String, Object?> json) {
    return AuthProviders(
      password: json['password'] as bool? ?? true,
      firebase: json['firebase'] as bool? ?? false,
    );
  }
}

/// A signed-in device, for the sessions screen.
class DeviceSession {
  const DeviceSession({
    required this.id,
    required this.deviceLabel,
    required this.platform,
    required this.current,
    required this.lastSeenAt,
    this.ip,
    this.createdAt,
  });

  final String id;
  final String deviceLabel;
  final String platform;

  /// Whether this is the device making the request. The UI marks it and offers
  /// no revoke action for it, matching the API's 409.
  final bool current;

  final DateTime lastSeenAt;
  final String? ip;
  final DateTime? createdAt;

  factory DeviceSession.fromJson(Map<String, Object?> json) {
    return DeviceSession(
      id: json['id'] as String? ?? '',
      deviceLabel: json['device_label'] as String? ?? 'Unknown device',
      platform: json['platform'] as String? ?? 'unknown',
      current: json['current'] as bool? ?? false,
      lastSeenAt: DateTime.tryParse(json['last_seen_at'] as String? ?? '') ??
          DateTime.fromMillisecondsSinceEpoch(0),
      ip: json['ip'] as String?,
      createdAt: _parseDate(json['created_at']),
    );
  }
}

DateTime? _parseDate(Object? raw) {
  if (raw is! String) return null;
  return DateTime.tryParse(raw);
}
