import 'dart:convert';
import 'dart:typed_data';

import 'package:comicmark/app/providers.dart';
import 'package:comicmark/core/api_client.dart';
import 'package:comicmark/core/api_error.dart';
import 'package:comicmark/core/secure_store.dart';
import 'package:comicmark/features/auth/application/auth_controller.dart';
import 'package:comicmark/features/library/models/bookmark.dart';
import 'package:dio/dio.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_secure_storage/flutter_secure_storage.dart';
import 'package:flutter_test/flutter_test.dart';

/// Shared test scaffolding.
///
/// The app talks to one HTTP client, so the fakes here stand in for the API at
/// the HTTP boundary: the repositories, controllers and widgets under test are
/// the real ones, and only the network is simulated.

/// A canned response for one API route.
class RouteReply {
  const RouteReply(this.status, this.body);

  final int status;
  final Map<String, Object?> body;

  /// A successful JSON reply.
  static RouteReply ok(Map<String, Object?> body) => RouteReply(200, body);

  /// An API error envelope.
  static RouteReply error(int status, String code, String message,
          {Map<String, Object?>? details}) =>
      RouteReply(status, {
        'error': {
          'code': code,
          'message': message,
          'details': ?details,
        },
      });
}

/// A Dio adapter that answers from a routing table.
///
/// Routes are matched by method and path suffix, so a test only declares what it
/// cares about.
class FakeApi {
  FakeApi();

  /// method+path-suffix to replies, consumed in order so a route can answer
  /// differently on a retry.
  final Map<String, List<RouteReply Function(Map<String, Object?> body)>>
      _routes = {};

  /// A delay applied to every reply.
  ///
  /// Under a widget test the clock is fake, so this is what lets a test observe
  /// the optimistic state before the server has answered and then advance time
  /// past it.
  Duration delay = Duration.zero;

  /// Every request seen, for assertions about what was sent.
  final List<RequestOptions> requests = [];
  final List<Map<String, Object?>> bodies = [];

  /// Registers a route that always answers the same way.
  void on(String method, String pathSuffix, RouteReply reply) {
    onComputed(method, pathSuffix, (_) => reply);
  }

  /// Replaces a route's reply outright.
  ///
  /// Use this to change what the API returns *after* an action, which is clearer
  /// than queueing a sequence: a single registered reply repeats, so queueing one
  /// more alongside it is ambiguous about which call gets which.
  void set(String method, String pathSuffix, RouteReply reply) {
    _routes[_key(method, pathSuffix)] = [(body) => reply];
  }

  /// Registers a route whose reply depends on the request body.
  void onComputed(
    String method,
    String pathSuffix,
    RouteReply Function(Map<String, Object?> body) reply,
  ) {
    _routes.putIfAbsent(_key(method, pathSuffix), () => []).add(reply);
  }

  /// The number of requests that reached a route.
  int countFor(String method, String pathSuffix) => requests
      .where((r) =>
          r.method.toUpperCase() == method.toUpperCase() &&
          r.path.endsWith(pathSuffix))
      .length;

  Dio buildDio() {
    final dio = Dio()..httpClientAdapter = _Adapter(this);
    return dio;
  }

  RouteReply respond(RequestOptions options, Map<String, Object?> body) {
    requests.add(options);
    bodies.add(body);

    final path = options.path;
    for (final entry in _routes.entries) {
      final parts = entry.key.split(' ');
      if (parts[0] != options.method.toUpperCase()) continue;
      if (!path.endsWith(parts[1])) continue;

      // Queued replies are consumed in order, and the last one then repeats, so
      // a route is always answerable and a test can describe a sequence (the
      // first load, then the reload) without exhausting the route.
      final replies = entry.value;
      if (replies.length == 1) return replies.first(body);
      return replies.removeAt(0)(body);
    }

    throw StateError('No fake route for ${options.method} $path');
  }

  static String _key(String method, String suffix) =>
      '${method.toUpperCase()} $suffix';
}

class _Adapter implements HttpClientAdapter {
  _Adapter(this.api);

  final FakeApi api;

  @override
  Future<ResponseBody> fetch(
    RequestOptions options,
    Stream<Uint8List>? requestStream,
    Future<void>? cancelFuture,
  ) async {
    final body = switch (options.data) {
      final Map<dynamic, dynamic> map => map.cast<String, Object?>(),
      final String text when text.isNotEmpty => jsonDecode(text) as Map<String, Object?>,
      _ => <String, Object?>{},
    };

    if (api.delay > Duration.zero) {
      await Future<void>.delayed(api.delay);
    }

    final reply = api.respond(options, body);
    return ResponseBody.fromString(
      jsonEncode(reply.body),
      reply.status,
      headers: {
        Headers.contentTypeHeader: [Headers.jsonContentType],
      },
    );
  }

  @override
  void close({bool force = false}) {}
}

/// Builds a [ProviderContainer] whose HTTP layer is [api].
///
/// A container rather than pumpWidget: provider-level tests are faster and say
/// more about the logic than a widget tree would.
ProviderContainer testContainer(FakeApi api, {TokenStore? tokens}) {
  final container = ProviderContainer(
    overrides: [
      tokenStoreProvider.overrideWithValue(tokens ?? TokenStore()),
      apiClientProvider.overrideWith((ref) {
        return ApiClient(
          tokenStore: ref.watch(tokenStoreProvider),
          dio: api.buildDio(),
          onSessionExpired: () =>
              ref.read(authControllerProvider.notifier).handleSessionExpired(),
        );
      }),
    ],
  );
  addTearDown(container.dispose);
  return container;
}

/// Signs a reader in so tests can start from a session.
Future<void> signIn(
  ProviderContainer container,
  FakeApi api, {
  String id = 'user-1',
  String email = 'reader@example.com',
  bool emailVerified = true,
}) async {
  api.on('POST', '/auth/register', RouteReply(201, {
    'access_token': 'access-1',
    'refresh_token': 'refresh-1',
    'token_type': 'Bearer',
    'expires_in': 900,
    'session_id': 'session-1',
    'user': {
      'id': id,
      'email': email,
      'email_verified': emailVerified,
      'created_at': '2026-01-01T00:00:00.000Z',
    },
  }));

  await container.read(authControllerProvider.notifier).register(
        email: email,
        password: 'correct horse battery',
      );
}

/// A bookmark JSON body, for stubbing list and detail routes.
Map<String, Object?> bookmarkJson({
  String id = 'bookmark-1',
  String title = 'One Piece',
  String currentUrl = 'https://reader.example.com/one-piece-chapter-1050',
  String chapter = 'Chapter 1050',
  double? chapterNumber = 1050,
  String? coverUrl = 'https://cdn.example.com/cover.jpg',
  String status = 'reading',
  String? lastReadAt,
}) {
  return {
    'id': id,
    'title': title,
    'current_url': currentUrl,
    'chapter': chapter,
    'chapter_number': chapterNumber,
    'cover_url': coverUrl,
    'source_host': 'reader.example.com',
    'status': status,
    'last_read_at': lastReadAt,
    'created_at': '2026-01-01T00:00:00.000Z',
    'updated_at': null,
  };
}

/// A library list reply.
RouteReply libraryReply(List<Map<String, Object?>> bookmarks) {
  return RouteReply.ok({
    'bookmarks': bookmarks,
    'total': bookmarks.length,
    'limit': 50,
    'offset': 0,
  });
}

/// Clears the platform storage between tests.
void resetSecureStorage() {
  TestWidgetsFlutterBinding.ensureInitialized();
  FlutterSecureStorage.setMockInitialValues({});
}

/// A bookmark built directly, for tests that do not need the HTTP layer.
Bookmark sampleBookmark({
  String id = 'bookmark-1',
  String title = 'One Piece',
  String chapter = 'Chapter 1050',
  double? chapterNumber = 1050,
  BookmarkStatus status = BookmarkStatus.reading,
}) {
  return Bookmark(
    id: id,
    title: title,
    currentUrl: 'https://reader.example.com/one-piece-chapter-1050',
    chapter: chapter,
    chapterNumber: chapterNumber,
    coverUrl: 'https://cdn.example.com/cover.jpg',
    sourceHost: 'reader.example.com',
    status: status,
    createdAt: DateTime(2026, 1, 1),
  );
}

/// An [ApiError] with the code a test needs.
ApiError apiError(String code, String message) =>
    ApiError(code: code, message: message);
