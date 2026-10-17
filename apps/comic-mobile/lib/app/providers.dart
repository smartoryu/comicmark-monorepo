import 'package:flutter_riverpod/flutter_riverpod.dart';

import '../core/api_client.dart';
import '../core/secure_store.dart';
import '../features/auth/application/auth_controller.dart';

/// Where the whole app's dependencies are declared.
///
/// Everything is a provider so tests can substitute any layer, and nothing above
/// this file reaches for a global.

/// Secure token storage.
final tokenStoreProvider = Provider<TokenStore>((ref) => TokenStore());

/// The HTTP client, shared by every feature.
///
/// The callback is what turns "the refresh token was rejected" into "the reader
/// is signed out". It is wired as a closure rather than a provider that the auth
/// controller installs, because a provider may not modify another while building:
/// the closure runs on a 401, long after both providers exist.
final apiClientProvider = Provider<ApiClient>((ref) {
  return ApiClient(
    tokenStore: ref.watch(tokenStoreProvider),
    onSessionExpired: () =>
        ref.read(authControllerProvider.notifier).handleSessionExpired(),
  );
});
