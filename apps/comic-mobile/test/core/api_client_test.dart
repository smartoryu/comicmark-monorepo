import 'dart:async';
import 'dart:convert';
import 'dart:typed_data';

import 'package:comicmark/core/api_client.dart';
import 'package:comicmark/core/api_error.dart';
import 'package:comicmark/core/secure_store.dart';
import 'package:dio/dio.dart';
import 'package:flutter_secure_storage/flutter_secure_storage.dart';
import 'package:flutter_test/flutter_test.dart';

/// A Dio adapter that answers from a callback, so the client can be driven
/// through real request/response plumbing without a network.
class _FakeAdapter implements HttpClientAdapter {
  _FakeAdapter(this.handler);

  final Future<ResponseBody> Function(RequestOptions options) handler;

  /// Every request the adapter saw, for assertions about retries.
  final List<RequestOptions> seen = [];

  @override
  Future<ResponseBody> fetch(
    RequestOptions options,
    Stream<Uint8List>? requestStream,
    Future<void>? cancelFuture,
  ) {
    seen.add(options);
    return handler(options);
  }

  @override
  void close({bool force = false}) {}
}

ResponseBody _json(Map<String, Object?> body, int status) => ResponseBody.fromString(
      jsonEncode(body),
      status,
      headers: {
        Headers.contentTypeHeader: [Headers.jsonContentType],
      },
    );

/// The error envelope the API always answers with.
ResponseBody _error(String code, String message, int status) => _json({
      'error': {'code': code, 'message': message},
    }, status);

Map<String, Object?> _session(String access, String refresh) => {
      'access_token': access,
      'refresh_token': refresh,
      'token_type': 'Bearer',
      'expires_in': 900,
      'session_id': 'session-1',
      'user': {
        'id': 'user-1',
        'email': 'reader@example.com',
        'email_verified': true,
        'created_at': '2026-01-01T00:00:00.000Z',
      },
    };

void main() {
  // Keeps secure storage off the platform channels.
  TestWidgetsFlutterBinding.ensureInitialized();

  setUp(() {
    FlutterSecureStorage.setMockInitialValues({});
  });

  group('ApiClient', () {
    test('attaches the bearer token to requests', () async {
      final tokens = TokenStore();
      await tokens.writeTokens(accessToken: 'access-1', refreshToken: 'refresh-1');

      final adapter = _FakeAdapter((_) async => _json({'ok': true}, 200));
      final dio = Dio()..httpClientAdapter = adapter;
      final client = ApiClient(tokenStore: tokens, dio: dio);
      await client.restoreSession();

      await client.get<Object?>('/bookmarks', decode: (data) => data);

      expect(adapter.seen, hasLength(1));
      expect(adapter.seen.single.headers['Authorization'], 'Bearer access-1');
    });

    test('tells the API which platform this is', () async {
      final adapter = _FakeAdapter((_) async => _json({'ok': true}, 200));
      final dio = Dio()..httpClientAdapter = adapter;
      final client = ApiClient(tokenStore: TokenStore(), dio: dio);

      await client.get<Object?>('/bookmarks', decode: (data) => data);

      // A Dart User-Agent cannot express the platform, so the header does.
      expect(adapter.seen.single.headers['X-Client-Platform'], isNotNull);
    });

    test('prefixes every path with the API version', () async {
      final adapter = _FakeAdapter((_) async => _json({'ok': true}, 200));
      final dio = Dio()..httpClientAdapter = adapter;
      final client = ApiClient(tokenStore: TokenStore(), dio: dio);

      await client.get<Object?>('/bookmarks', decode: (data) => data);

      expect(adapter.seen.single.path, '/api/v1/bookmarks');
    });

    test('refreshes once and replays the request that was refused', () async {
      final tokens = TokenStore();
      await tokens.writeTokens(accessToken: 'stale', refreshToken: 'refresh-1');

      var refreshCalls = 0;
      final adapter = _FakeAdapter((options) async {
        if (options.path.endsWith('/auth/refresh')) {
          refreshCalls++;
          return _json(_session('fresh', 'refresh-2'), 200);
        }
        // The first attempt is refused; the replay carries the new token.
        if (options.headers['Authorization'] == 'Bearer fresh') {
          return _json({'bookmarks': []}, 200);
        }
        return _error(ApiErrorCode.unauthorized, 'Sign in to continue.', 401);
      });

      final dio = Dio()..httpClientAdapter = adapter;
      final client = ApiClient(tokenStore: tokens, dio: dio);
      await client.restoreSession();

      final result = await client.get<Object?>('/bookmarks', decode: (data) => data);

      expect(refreshCalls, 1);
      expect(result, {'bookmarks': []});
      // The new pair replaced the old one.
      expect(await tokens.readAccessToken(), 'fresh');
      expect(await tokens.readRefreshToken(), 'refresh-2');
    });

    // This is the behaviour that protects the rotating refresh token: a library
    // screen fires several requests at once, and without single-flight the
    // repeats would look like a replay to the server, which would then sign the
    // reader out.
    test('refreshes only once when many requests are refused at once', () async {
      final tokens = TokenStore();
      await tokens.writeTokens(accessToken: 'stale', refreshToken: 'refresh-1');

      var refreshCalls = 0;
      final adapter = _FakeAdapter((options) async {
        if (options.path.endsWith('/auth/refresh')) {
          refreshCalls++;
          // A delay widens the window in which the other requests arrive.
          await Future<void>.delayed(const Duration(milliseconds: 20));
          return _json(_session('fresh', 'refresh-2'), 200);
        }
        if (options.headers['Authorization'] == 'Bearer fresh') {
          return _json({'ok': true}, 200);
        }
        return _error(ApiErrorCode.unauthorized, 'Sign in to continue.', 401);
      });

      final dio = Dio()..httpClientAdapter = adapter;
      final client = ApiClient(tokenStore: tokens, dio: dio);
      await client.restoreSession();

      await Future.wait([
        client.get<Object?>('/bookmarks', decode: (data) => data),
        client.get<Object?>('/sessions', decode: (data) => data),
        client.get<Object?>('/auth/me', decode: (data) => data),
      ]);

      expect(refreshCalls, 1, reason: 'a rotating token must be spent exactly once');
    });

    test('clears the session when the refresh itself is refused', () async {
      final tokens = TokenStore();
      await tokens.writeTokens(accessToken: 'stale', refreshToken: 'refresh-1');

      var sessionExpired = false;
      final adapter = _FakeAdapter((options) async {
        if (options.path.endsWith('/auth/refresh')) {
          return _error(ApiErrorCode.sessionRevoked, 'This session has ended.', 401);
        }
        return _error(ApiErrorCode.unauthorized, 'Sign in to continue.', 401);
      });

      final dio = Dio()..httpClientAdapter = adapter;
      final client = ApiClient(
        tokenStore: tokens,
        dio: dio,
        onSessionExpired: () async => sessionExpired = true,
      );
      await client.restoreSession();

      await expectLater(
        client.get<Object?>('/bookmarks', decode: (data) => data),
        throwsA(isA<ApiError>()),
      );

      expect(sessionExpired, isTrue, reason: 'the app must be told to sign in again');
      expect(await tokens.readRefreshToken(), isNull, reason: 'the session must be forgotten');
    });

    // A 401 from sign-in means the password was wrong, not that a token expired.
    // Refreshing there would be pointless and would spend a token for nothing.
    test('never refreshes for the sign-in endpoints', () async {
      final tokens = TokenStore();
      await tokens.writeTokens(accessToken: 'stale', refreshToken: 'refresh-1');

      var refreshCalls = 0;
      final adapter = _FakeAdapter((options) async {
        if (options.path.endsWith('/auth/refresh')) {
          refreshCalls++;
          return _json(_session('fresh', 'refresh-2'), 200);
        }
        return _error(ApiErrorCode.invalidCredentials, 'The email or password you entered is incorrect.', 401);
      });

      final dio = Dio()..httpClientAdapter = adapter;
      final client = ApiClient(tokenStore: tokens, dio: dio);
      await client.restoreSession();

      await expectLater(
        client.post<Object?>(
          '/auth/login',
          body: {'email': 'reader@example.com', 'password': 'wrong'},
          decode: (data) => data,
        ),
        throwsA(
          isA<ApiError>().having((e) => e.code, 'code', ApiErrorCode.invalidCredentials),
        ),
      );

      expect(refreshCalls, 0);
    });

    test('does not retry when there is no session to refresh', () async {
      var refreshCalls = 0;
      final adapter = _FakeAdapter((options) async {
        if (options.path.endsWith('/auth/refresh')) refreshCalls++;
        return _error(ApiErrorCode.unauthorized, 'Sign in to continue.', 401);
      });

      final dio = Dio()..httpClientAdapter = adapter;
      final client = ApiClient(tokenStore: TokenStore(), dio: dio);

      await expectLater(
        client.get<Object?>('/bookmarks', decode: (data) => data),
        throwsA(isA<ApiError>()),
      );

      expect(refreshCalls, 0, reason: 'there is nothing to refresh with');
    });

    test('surfaces the error envelope unchanged', () async {
      final adapter = _FakeAdapter(
        (_) async => _json({
          'error': {
            'code': ApiErrorCode.chapterNotNumeric,
            'message': 'This chapter has no number to advance.',
            'details': {'chapter': 'Prologue'},
            'request_id': 'trace-1',
          },
        }, 409),
      );
      final dio = Dio()..httpClientAdapter = adapter;
      final client = ApiClient(tokenStore: TokenStore(), dio: dio);

      await expectLater(
        client.patch<Object?>('/bookmarks/1/read-next', decode: (data) => data),
        throwsA(
          isA<ApiError>()
              .having((e) => e.code, 'code', ApiErrorCode.chapterNotNumeric)
              .having((e) => e.statusCode, 'status', 409)
              .having((e) => e.requestId, 'requestId', 'trace-1')
              .having((e) => e.details?['chapter'], 'detail', 'Prologue'),
        ),
      );
    });

    test('reports an unreachable server as a network failure', () async {
      final adapter = _FakeAdapter(
        (options) async => throw DioException.connectionError(
          requestOptions: options,
          reason: 'refused',
        ),
      );
      final dio = Dio()..httpClientAdapter = adapter;
      final client = ApiClient(tokenStore: TokenStore(), dio: dio);

      await expectLater(
        client.get<Object?>('/bookmarks', decode: (data) => data),
        throwsA(
          isA<ApiError>()
              .having((e) => e.code, 'code', ApiErrorCode.network)
              .having((e) => e.isNetworkFailure, 'isNetworkFailure', isTrue),
        ),
      );
    });
  });

  group('TokenStore', () {
    test('round-trips both tokens', () async {
      final store = TokenStore();

      await store.writeTokens(accessToken: 'access-1', refreshToken: 'refresh-1');

      expect(await store.readAccessToken(), 'access-1');
      expect(await store.readRefreshToken(), 'refresh-1');
      expect(store.isDegraded, isFalse);
    });

    test('clears both tokens', () async {
      final store = TokenStore();
      await store.writeTokens(accessToken: 'access-1', refreshToken: 'refresh-1');

      await store.clear();

      expect(await store.readAccessToken(), isNull);
      expect(await store.readRefreshToken(), isNull);
    });

    test('reports no session before anything is stored', () async {
      final store = TokenStore();

      expect(await store.readAccessToken(), isNull);
      expect(await store.readRefreshToken(), isNull);
    });
  });

  group('ApiError', () {
    test('says which codes mean the caller must sign in again', () {
      for (final code in [
        ApiErrorCode.unauthorized,
        ApiErrorCode.sessionRevoked,
        ApiErrorCode.tokenInvalid,
        ApiErrorCode.tokenReused,
      ]) {
        expect(
          ApiError(code: code, message: 'x').isUnauthenticated,
          isTrue,
          reason: '$code should require signing in',
        );
      }

      expect(
        ApiError(code: ApiErrorCode.notFound, message: 'x').isUnauthenticated,
        isFalse,
      );
    });

    test('joins field problems into one readable line', () {
      const error = ApiError(
        code: ApiErrorCode.validationFailed,
        message: 'Please check the highlighted fields.',
        details: {'email': 'Enter a valid email address.', 'password': 'Use at least 8 characters.'},
      );

      expect(error.detailSummary, contains('Enter a valid email address.'));
      expect(error.detailSummary, contains('Use at least 8 characters.'));
      expect(error.isValidationFailure, isTrue);
    });

    test('falls back to the message when there are no field problems', () {
      const error = ApiError(code: ApiErrorCode.notFound, message: 'Not found.');

      expect(error.detailSummary, 'Not found.');
      expect(error.isValidationFailure, isFalse);
    });
  });
}
