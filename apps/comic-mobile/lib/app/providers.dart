import 'package:flutter_riverpod/flutter_riverpod.dart';

import '../core/api_client.dart';
import '../core/secure_store.dart';

/// Where the whole app's dependencies are declared.
///
/// Everything is a provider so tests can substitute any layer, and nothing above
/// this file reaches for a global.

/// Secure token storage.
final tokenStoreProvider = Provider<TokenStore>((ref) => TokenStore());

/// The HTTP client, shared by every feature.
///
/// The callback wired here is what turns "the refresh token was rejected" into
/// "the reader is signed out": the auth controller installs a handler and the
/// router then treats them as signed out.
final apiClientProvider = Provider<ApiClient>((ref) {
  return ApiClient(
    tokenStore: ref.watch(tokenStoreProvider),
    onSessionExpired: () async {
      // Read rather than watch: this closure runs outside a build, and holding a
      // reference to the handler keeps the client independent of the auth
      // feature.
      final handler = ref.read(sessionExpiredHandlerProvider);
      if (handler != null) await handler();
    },
  );
});

/// Holds the callback the HTTP client calls when a stored session turns out to be
/// unusable.
///
/// The client is built before the auth controller exists, so the arrow has to
/// point this way rather than the client holding a reference to the controller.
class SessionExpiredHandler extends Notifier<Future<void> Function()?> {
  @override
  Future<void> Function()? build() => null;

  /// Installs the handler, or clears it with null.
  void install(Future<void> Function()? handler) => state = handler;
}

/// The installed session-expiry handler.
final sessionExpiredHandlerProvider =
    NotifierProvider<SessionExpiredHandler, Future<void> Function()?>(
  SessionExpiredHandler.new,
);
