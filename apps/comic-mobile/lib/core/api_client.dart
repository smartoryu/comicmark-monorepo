import 'dart:async';

import 'package:dio/dio.dart';
import 'package:flutter/foundation.dart';

import '../app/env.dart';
import 'api_error.dart';
import 'secure_store.dart';

/// The HTTP layer.
///
/// Two things make this more than a thin wrapper around Dio:
///
///  * it attaches the bearer token, so no caller has to remember to
///  * it refreshes an expired access token **once** and replays the request, so
///    a fifteen-minute lifetime is invisible to the rest of the app
///
/// The refresh is single-flight: when several requests fail with 401 at the same
/// moment — which is exactly what a library screen does — only the first performs
/// the refresh and the rest wait for its result. Without that, a rotating refresh
/// token would be spent several times over and the server would (correctly) treat
/// the later attempts as a replay and sign the reader out.
class ApiClient {
  ApiClient({
    required TokenStore tokenStore,
    Dio? dio,
    this.onSessionExpired,
  })  : _tokens = tokenStore,
        _dio = dio ?? Dio() {
    _dio.options = _dio.options.copyWith(
      baseUrl: apiBaseUrl,
      connectTimeout: const Duration(seconds: 10),
      // The API's own scrape timeout is ten seconds, so this leaves room for the
      // response to be written without waiting forever on a wedged connection.
      receiveTimeout: const Duration(seconds: 30),
      sendTimeout: const Duration(seconds: 10),
      // Non-2xx is a normal outcome here: the API answers failures with a body
      // the app reads, so Dio must not treat them as exceptions to swallow.
      validateStatus: (status) => status != null && status < 400,
      headers: {
        'Accept': 'application/json',
        // Lets the API label a session with the right platform, which a Dart
        // User-Agent cannot express.
        'X-Client-Platform': _clientPlatform,
      },
    );

    _dio.interceptors.add(
      InterceptorsWrapper(
        onRequest: _attachToken,
        onError: _handleError,
      ),
    );
  }

  final Dio _dio;
  final TokenStore _tokens;

  /// Called when the stored session turns out to be unusable, so the app can
  /// send the reader back to sign in.
  final Future<void> Function()? onSessionExpired;

  /// Guards [_refreshInFlight] and [_accessToken].
  final _mutex = _SingleFlight();

  String? _accessToken;

  /// The Dio instance, for the rare caller that needs a raw request.
  Dio get dio => _dio;

  /// The token currently held, if any.
  String? get accessToken => _accessToken;

  /// Loads any stored session. Called once at start-up.
  ///
  /// A stored refresh token with no access token still counts as signed in:
  /// [hasSession] reports that, and the first request obtains a fresh pair.
  Future<void> restoreSession() async {
    _accessToken = await _tokens.readAccessToken();
  }

  /// Whether a session is stored, even if the access token has expired.
  Future<bool> hasSession() async => await _tokens.readRefreshToken() != null;

  /// Records a new session from a sign-in response.
  Future<void> adoptSession({
    required String accessToken,
    required String refreshToken,
  }) async {
    _accessToken = accessToken;
    await _tokens.writeTokens(
      accessToken: accessToken,
      refreshToken: refreshToken,
    );
  }

  /// Forgets the session.
  Future<void> clearSession() async {
    _accessToken = null;
    await _tokens.clear();
  }

  /// Sends a request and returns the decoded body.
  ///
  /// Every network failure becomes an [ApiError], so callers never handle a
  /// `DioException`.
  Future<T> request<T>(
    String method,
    String path, {
    Object? body,
    Map<String, dynamic>? query,
    required T Function(Object? data) decode,
  }) async {
    try {
      final response = await _dio.request<Object?>(
        _relativePath(path),
        data: body,
        queryParameters: query,
        options: Options(method: method),
      );
      return decode(response.data);
    } catch (error) {
      throw apiErrorFrom(error);
    }
  }

  /// A GET, for readability at the call sites.
  Future<T> get<T>(
    String path, {
    Map<String, dynamic>? query,
    required T Function(Object? data) decode,
  }) =>
      request('GET', path, query: query, decode: decode);

  Future<T> post<T>(
    String path, {
    Object? body,
    required T Function(Object? data) decode,
  }) =>
      request('POST', path, body: body, decode: decode);

  Future<T> patch<T>(
    String path, {
    Object? body,
    required T Function(Object? data) decode,
  }) =>
      request('PATCH', path, body: body, decode: decode);

  Future<T> put<T>(
    String path, {
    Object? body,
    required T Function(Object? data) decode,
  }) =>
      request('PUT', path, body: body, decode: decode);

  Future<T> delete<T>(
    String path, {
    required T Function(Object? data) decode,
  }) =>
      request('DELETE', path, decode: decode);

  // --- interceptors ---------------------------------------------------------

  /// The path without the API prefix, because Dio is given the prefix as its
  /// base URL.
  String _relativePath(String path) {
    final normalised = path.startsWith('/') ? path : '/$path';
    return '$apiPrefix$normalised';
  }

  void _attachToken(RequestOptions options, RequestInterceptorHandler handler) {
    // Never attach a token to the refresh call itself: it goes to a public
    // endpoint and carrying a stale token there only invites confusion.
    if (options.path.endsWith('/auth/refresh')) {
      handler.next(options);
      return;
    }

    final token = _accessToken;
    if (token != null && token.isNotEmpty) {
      options.headers['Authorization'] = 'Bearer $token';
    }
    handler.next(options);
  }

  Future<void> _handleError(
    DioException error,
    ErrorInterceptorHandler handler,
  ) async {
    final status = error.response?.statusCode;
    final code = _errorCode(error.response?.data);

    // Only an authentication failure is worth retrying, and only when there is
    // a refresh token to spend. Anything else propagates unchanged.
    final retryable = status == 401 &&
        code != ApiErrorCode.cannotRevokeCurrent &&
        !_isAuthEndpoint(error.requestOptions.path) &&
        await hasSession();

    if (!retryable) {
      handler.next(error);
      return;
    }

    final refreshed = await _refreshOnce();
    if (!refreshed) {
      // The refresh token is gone or was rejected, so this is a real sign-out.
      await _expireSession();
      handler.next(error);
      return;
    }

    try {
      handler.resolve(await _replay(error.requestOptions));
    } catch (_) {
      // A failed replay reports its own error rather than the original 401,
      // which would be misleading.
      handler.next(error);
    }
  }

  /// Whether a path belongs to the sign-in flow.
  ///
  /// A 401 from sign-in means "wrong password", not "expired token", so it must
  /// never trigger a refresh.
  bool _isAuthEndpoint(String path) =>
      path.contains('/auth/login') ||
      path.contains('/auth/register') ||
      path.contains('/auth/refresh') ||
      path.contains('/auth/password/') ||
      path.contains('/auth/verification/') ||
      path.contains('/auth/firebase');

  /// Replays a request with the freshly obtained token.
  Future<Response<Object?>> _replay(RequestOptions options) {
    final headers = Map<String, dynamic>.from(options.headers);
    headers['Authorization'] = 'Bearer $_accessToken';

    return _dio.request<Object?>(
      options.path,
      data: options.data,
      queryParameters: options.queryParameters,
      cancelToken: options.cancelToken,
      options: Options(
        method: options.method,
        headers: headers,
        contentType: options.contentType,
        responseType: options.responseType,
      ),
    );
  }

  /// Refreshes the token pair, at most once at a time.
  Future<bool> _refreshOnce() => _mutex.run(_performRefresh);

  Future<bool> _performRefresh() async {
    final refreshToken = await _tokens.readRefreshToken();
    if (refreshToken == null) return false;

    try {
      final response = await _dio.post<Object?>(
        '$apiPrefix/auth/refresh',
        data: {'refresh_token': refreshToken},
        // The interceptor must not see this call, or a 401 here would try to
        // refresh again.
        options: Options(extra: const {'skipAuthRefresh': true}),
      );

      final data = response.data;
      if (data is! Map) return false;

      final access = data['access_token'];
      final refresh = data['refresh_token'];
      if (access is! String || refresh is! String) return false;

      await adoptSession(accessToken: access, refreshToken: refresh);
      return true;
    } catch (_) {
      return false;
    }
  }

  Future<void> _expireSession() async {
    await clearSession();
    final callback = onSessionExpired;
    if (callback != null) await callback();
  }

  /// Reads the machine-readable code out of an error response body.
  static String? _errorCode(Object? data) {
    if (data is Map && data['error'] is Map) {
      final code = (data['error'] as Map)['code'];
      if (code is String) return code;
    }
    return null;
  }

  /// What the API is told about this client.
  ///
  /// It prefers this over sniffing the User-Agent, because a Dart HTTP client
  /// does not disclose its operating system and a wrong device label makes the
  /// sessions screen useless.
  static String get _clientPlatform {
    if (kIsWeb) return 'web';
    return defaultTargetPlatform == TargetPlatform.android ? 'android' : 'ios';
  }
}

/// Runs an async action at most once at a time, sharing the result.
///
/// This is what makes the token refresh single-flight. Without it a screen that
/// fires several requests at once would spend the rotating refresh token several
/// times, and the server would rightly treat the repeats as a replay.
class _SingleFlight {
  Future<bool>? _inFlight;

  Future<bool> run(Future<bool> Function() action) {
    final existing = _inFlight;
    if (existing != null) return existing;

    final future = action().whenComplete(() => _inFlight = null);
    _inFlight = future;
    return future;
  }
}
