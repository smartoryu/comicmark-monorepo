import 'api_error.dart';

/// The retry policy for the app's data loads.
///
/// Riverpod retries a failed build up to ten times with an exponential backoff
/// (200ms doubling to 6.4s) for any exception that is not an `Error`. Left at
/// that default, a failed load keeps the screen spinning for around twelve
/// seconds and the error state — with its "Try again" control — is effectively
/// unreachable, because the provider is nearly always mid-retry.
///
/// This policy is deliberately tighter than the default, and deliberately
/// explicit rather than inherited:
///
///   * a **network** failure gets one retry after a short pause, which absorbs a
///     momentary blip without hiding a real outage
///   * a **refusal from the API** is not retried at all: the server has already
///     answered, and asking the same question again will get the same answer
///   * a programming **error** is never retried
///
/// After that the failure surfaces, and the reader decides whether to try again.
Duration? apiRetry(int retryCount, Object error) {
  if (error is Error) return null;
  if (error is ApiError && !error.isNetworkFailure) return null;

  return retryCount < 1 ? const Duration(milliseconds: 400) : null;
}
