/// Build-time configuration.
///
/// Everything here arrives through `--dart-define`, so a build carries its own
/// settings and no secrets are committed. The values are compile-time constants,
/// which is why they can be read from a `const` context anywhere in the app.
library;

/// The API's base URL.
///
/// The default is deliberately the empty string. A build that sets nothing
/// issues same-origin relative requests, which is exactly right for the embedded
/// web build where the Go binary serves the app and the API from one address.
///
/// Development builds pass an absolute URL:
///
///   flutter run -d chrome --web-port=5173 \
///     --dart-define=API_BASE_URL=http://localhost:8080
///
/// and the API must then allow that origin through `CORS_ORIGINS`.
const String apiBaseUrl = String.fromEnvironment('API_BASE_URL');

/// The API version prefix every request shares.
const String apiPrefix = '/api/v1';

/// Firebase configuration, injected the same way.
///
/// These are empty by default, and the app does not initialise Firebase unless
/// [firebaseConfigured] is true. That keeps the Firebase SDK inert when no
/// project exists, which matches how the backend only mounts `/auth/firebase`
/// when it is configured.
///
/// Note that a Firebase web API key is a public identifier, not a secret; it is
/// still passed at build time so that no environment-specific values live in the
/// repository.
const String firebaseProjectId = String.fromEnvironment('FIREBASE_PROJECT_ID');
const String firebaseApiKey = String.fromEnvironment('FIREBASE_API_KEY');
const String firebaseAuthDomain = String.fromEnvironment('FIREBASE_AUTH_DOMAIN');
const String firebaseAppId = String.fromEnvironment('FIREBASE_APP_ID');
const String firebaseMessagingSenderId =
    String.fromEnvironment('FIREBASE_MESSAGING_SENDER_ID');

// Google sign-in needs a client id per platform. On Android the ID token has to
// be requested on behalf of a server (web) client, and on iOS the plugin reads
// its client id from the GoogleService-Info.plist unless one is given here.
const String googleServerClientId =
    String.fromEnvironment('GOOGLE_SERVER_CLIENT_ID');
const String googleIosClientId = String.fromEnvironment('GOOGLE_IOS_CLIENT_ID');

/// Whether the build carries enough Firebase configuration to initialise.
///
/// `messagingSenderId` is required by the SDK's options even though the app never
/// sends messages, so a build without it cannot initialise at all and is treated
/// as unconfigured.
bool get firebaseConfigured =>
    firebaseProjectId.isNotEmpty &&
    firebaseApiKey.isNotEmpty &&
    firebaseAppId.isNotEmpty &&
    firebaseMessagingSenderId.isNotEmpty;

/// Whether requests go to the same origin that served the app.
bool get usesSameOrigin => apiBaseUrl.isEmpty;

/// The absolute URL for an API path, or a relative one for same-origin builds.
Uri apiUri(String path, [Map<String, dynamic>? query]) {
  final normalised = path.startsWith('/') ? path : '/$path';
  final full = '$apiPrefix$normalised';

  if (usesSameOrigin) {
    return Uri.parse(full).replace(queryParameters: query);
  }
  return Uri.parse('$apiBaseUrl$full').replace(queryParameters: query);
}
