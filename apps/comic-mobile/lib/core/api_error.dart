import 'package:dio/dio.dart';

/// A failure that is safe to show a reader.
///
/// The API answers every failure with a single envelope:
///
///   {"error": {"code": "...", "message": "...", "details": {...},
///              "request_id": "..."}}
///
/// so the whole app needs one error type and one parser. [code] is what the app
/// branches on; [message] is already written for a person, and [requestId] is
/// what a reader can quote when something goes wrong.
class ApiError implements Exception {
  const ApiError({
    required this.code,
    required this.message,
    this.statusCode,
    this.details,
    this.requestId,
  });

  /// Machine-readable reason, e.g. `chapter_not_numeric`.
  final String code;

  /// Human-readable text, safe to display as-is.
  final String message;

  /// HTTP status, when the failure came from a response.
  final int? statusCode;

  /// Field-level problems, keyed by the JSON field name.
  final Map<String, String>? details;

  /// Correlation id for a bug report.
  final String? requestId;

  /// Whether the caller needs to sign in again.
  bool get isUnauthenticated => code == ApiErrorCode.unauthorized ||
      code == ApiErrorCode.sessionRevoked ||
      code == ApiErrorCode.tokenInvalid ||
      code == ApiErrorCode.tokenReused;

  /// Whether the request never reached the server.
  bool get isNetworkFailure => code == ApiErrorCode.network;

  /// Whether the reader can fix this by editing a field.
  bool get isValidationFailure =>
      code == ApiErrorCode.validationFailed || details != null;

  /// A single message describing every field problem, for a snackbar.
  String get detailSummary {
    final entries = details;
    if (entries == null || entries.isEmpty) return message;
    return entries.values.join(' ');
  }

  @override
  String toString() => 'ApiError($code, $message)';
}

/// The error codes the API can return.
///
/// Kept as constants so a typo becomes a compile error rather than a silent
/// mismatch with the server.
abstract final class ApiErrorCode {
  static const badRequest = 'bad_request';
  static const validationFailed = 'validation_failed';
  static const unauthorized = 'unauthorized';
  static const forbidden = 'forbidden';
  static const notFound = 'not_found';
  static const conflict = 'conflict';
  static const rateLimited = 'rate_limited';
  static const internal = 'internal';

  // Account-specific codes.
  static const emailTaken = 'email_taken';
  static const invalidCredentials = 'invalid_credentials';
  static const emailNotVerified = 'email_not_verified';
  static const tokenInvalid = 'token_invalid';
  static const tokenReused = 'token_reused';
  static const sessionRevoked = 'session_revoked';
  static const cannotRevokeCurrent = 'cannot_revoke_current';
  static const invalidProviderToken = 'invalid_provider_token';

  // Bookmark-specific codes.
  static const chapterNotNumeric = 'chapter_not_numeric';
  static const duplicateBookmark = 'duplicate_bookmark';
  static const urlNotAllowed = 'url_not_allowed';
  static const urlNotFound = 'url_not_found';

  /// The code used for a request that never reached the server.
  static const network = 'network_unavailable';
}

/// Translates anything thrown by the HTTP layer into an [ApiError].
///
/// Nothing above this layer should ever see a `DioException`: the app's own
/// vocabulary is the API's error envelope plus one code for "the network is
/// down", which is the one failure the API cannot describe.
ApiError apiErrorFrom(Object error) {
  if (error is ApiError) return error;

  if (error is! DioException) {
    return ApiError(
      code: ApiErrorCode.internal,
      message: 'Something went wrong. Please try again.',
    );
  }

  final response = error.response;
  final data = response?.data;

  if (data is Map && data['error'] is Map) {
    final envelope = data['error'] as Map;
    return ApiError(
      code: envelope['code'] as String? ?? ApiErrorCode.internal,
      message: envelope['message'] as String? ??
          'Something went wrong. Please try again.',
      statusCode: response?.statusCode,
      details: _parseDetails(envelope['details']),
      requestId: envelope['request_id'] as String?,
    );
  }

  return switch (error.type) {
    DioExceptionType.connectionTimeout ||
    DioExceptionType.sendTimeout ||
    DioExceptionType.receiveTimeout =>
      const ApiError(
        code: ApiErrorCode.network,
        message: 'The server took too long to answer. Please try again.',
      ),
    DioExceptionType.connectionError =>
      const ApiError(
        code: ApiErrorCode.network,
        message: 'Cannot reach the server. Check your connection and try again.',
      ),
    DioExceptionType.cancel => const ApiError(
        code: ApiErrorCode.network,
        message: 'That request was cancelled.',
      ),
    _ => ApiError(
        code: ApiErrorCode.internal,
        message: response == null
            ? 'Cannot reach the server. Check your connection and try again.'
            : 'Something went wrong. Please try again.',
        statusCode: response?.statusCode,
      ),
  };
}

/// Reads the field-level details map, ignoring anything that is not a string.
Map<String, String>? _parseDetails(Object? raw) {
  if (raw is! Map) return null;

  final out = <String, String>{};
  for (final entry in raw.entries) {
    final key = entry.key;
    final value = entry.value;
    if (key is String) {
      out[key] = value is String ? value : value.toString();
    }
  }
  return out.isEmpty ? null : out;
}
