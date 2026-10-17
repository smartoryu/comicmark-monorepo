import 'package:comicmark/core/api_error.dart';
import 'package:comicmark/features/auth/application/auth_controller.dart';
import 'package:comicmark/features/library/application/library_controller.dart';
import 'package:comicmark/features/library/models/bookmark.dart';
import 'package:flutter_test/flutter_test.dart';

import '../support/fakes.dart';

/// The library controller, driven through the real repositories against a faked
/// HTTP layer.
void main() {
  setUp(resetSecureStorage);

  group('loading', () {
    test('an account with bookmarks gets them', () async {
      final api = FakeApi();
      final container = testContainer(api);
      await signIn(container, api);
      api.on('GET', '/bookmarks', libraryReply([
        bookmarkJson(id: 'a', title: 'One Piece'),
        bookmarkJson(id: 'b', title: 'Berserk'),
      ]));

      final bookmarks = await container.read(libraryControllerProvider.future);

      expect(bookmarks.map((b) => b.title), ['One Piece', 'Berserk']);
    });

    test('a signed-out reader has an empty library rather than an error', () async {
      final api = FakeApi();
      final container = testContainer(api);

      // Let the stored-session check finish first. The library rebuilds when it
      // resolves, and the screen waits for the same thing before rendering.
      await container.read(authControllerProvider.future);

      // No session, so the controller must not even ask the API.
      final bookmarks = await container.read(libraryControllerProvider.future);

      expect(bookmarks, isEmpty);
      expect(api.countFor('GET', '/bookmarks'), 0);
    });
  });

  group('the +1 action', () {
    test('advances the chapter and adopts what the server returned', () async {
      final api = FakeApi();
      final container = testContainer(api);
      await signIn(container, api);

      api.on('GET', '/bookmarks', libraryReply([bookmarkJson()]));
      api.on(
        'PATCH',
        '/read-next',
        RouteReply.ok(bookmarkJson(chapter: 'Chapter 1051', chapterNumber: 1051)),
      );

      await container.read(libraryControllerProvider.future);
      await container.read(libraryControllerProvider.notifier).readNext('bookmark-1');

      final bookmarks = container.read(libraryControllerProvider).value!;
      expect(bookmarks.single.chapter, 'Chapter 1051');
      expect(bookmarks.single.chapterNumber, 1051);
    });

    // The whole point of the optimistic update: the card must move before the
    // server answers, or a tap feels broken on a slow connection.
    test('updates the card before the server answers', () async {
      final api = FakeApi();
      final container = testContainer(api);
      await signIn(container, api);

      api.on('GET', '/bookmarks', libraryReply([bookmarkJson()]));
      await container.read(libraryControllerProvider.future);

      // Never replies, so anything visible came from the optimistic update.
      api.onComputed('PATCH', '/read-next', (_) {
        throw StateError('unreachable');
      });

      final pending = container
          .read(libraryControllerProvider.notifier)
          .readNext('bookmark-1')
          .catchError((_) {});

      final optimistic = container.read(libraryControllerProvider).value!;
      expect(optimistic.single.chapter, 'Chapter 1051',
          reason: 'the card should advance immediately');
      expect(optimistic.single.chapterNumber, 1051);

      await pending;
    });

    test('rolls back when the server refuses', () async {
      final api = FakeApi();
      final container = testContainer(api);
      await signIn(container, api);

      api.on('GET', '/bookmarks', libraryReply([bookmarkJson()]));
      api.on(
        'PATCH',
        '/read-next',
        RouteReply.error(500, ApiErrorCode.internal, 'Something went wrong.'),
      );

      await container.read(libraryControllerProvider.future);

      await expectLater(
        container.read(libraryControllerProvider.notifier).readNext('bookmark-1'),
        throwsA(isA<ApiError>()),
      );

      final bookmarks = container.read(libraryControllerProvider).value!;
      expect(bookmarks.single.chapter, 'Chapter 1050',
          reason: 'a refused update must not leave a chapter the server rejected');
      expect(bookmarks.single.chapterNumber, 1050);
    });

    // The 409 is not a failure to report: it is the signal to ask the reader.
    test('refuses locally when the chapter has no number', () async {
      final api = FakeApi();
      final container = testContainer(api);
      await signIn(container, api);

      api.on(
        'GET',
        '/bookmarks',
        libraryReply([bookmarkJson(chapter: 'Prologue', chapterNumber: null)]),
      );
      await container.read(libraryControllerProvider.future);

      await expectLater(
        container.read(libraryControllerProvider.notifier).readNext('bookmark-1'),
        throwsA(
          isA<ApiError>()
              .having((e) => e.code, 'code', ApiErrorCode.chapterNotNumeric),
        ),
      );

      // Nothing was sent, because there is nothing to add one to.
      expect(api.countFor('PATCH', '/read-next'), 0);
    });

    test('turns the server 409 into the same signal', () async {
      final api = FakeApi();
      final container = testContainer(api);
      await signIn(container, api);

      // The chapter has a number locally, but the server disagrees.
      api.on('GET', '/bookmarks', libraryReply([bookmarkJson()]));
      api.on(
        'PATCH',
        '/read-next',
        RouteReply.error(409, ApiErrorCode.chapterNotNumeric,
            'This chapter has no number to advance.'),
      );
      await container.read(libraryControllerProvider.future);

      await expectLater(
        container.read(libraryControllerProvider.notifier).readNext('bookmark-1'),
        throwsA(
          isA<ApiError>()
              .having((e) => e.code, 'code', ApiErrorCode.chapterNotNumeric),
        ),
      );
    });
  });

  group('editing the chapter', () {
    test('saves the text and the number', () async {
      final api = FakeApi();
      final container = testContainer(api);
      await signIn(container, api);

      api.on('GET', '/bookmarks', libraryReply([
        bookmarkJson(chapter: 'Prologue', chapterNumber: null),
      ]));
      api.on(
        'PATCH',
        '/bookmarks/bookmark-1',
        RouteReply.ok(bookmarkJson(chapter: 'Chapter 1', chapterNumber: 1)),
      );

      await container.read(libraryControllerProvider.future);
      await container.read(libraryControllerProvider.notifier).editChapter(
            'bookmark-1',
            chapter: 'Chapter 1',
            chapterNumber: 1,
          );

      final saved = container.read(libraryControllerProvider).value!.single;
      expect(saved.chapter, 'Chapter 1');
      expect(saved.canAdvance, isTrue);
    });

    // Clearing the number is how a reader says the chapter is not numeric, and
    // the request has to carry an explicit null to say so.
    test('clearing the number sends an explicit null', () async {
      final api = FakeApi();
      final container = testContainer(api);
      await signIn(container, api);

      api.on('GET', '/bookmarks', libraryReply([bookmarkJson()]));
      api.on(
        'PATCH',
        '/bookmarks/bookmark-1',
        RouteReply.ok(bookmarkJson(chapter: 'Special', chapterNumber: null)),
      );

      await container.read(libraryControllerProvider.future);
      await container.read(libraryControllerProvider.notifier).editChapter(
            'bookmark-1',
            chapter: 'Special',
            chapterNumber: null,
          );

      expect(api.bodies.last['chapter_number'], isNull);
      expect(api.bodies.last.containsKey('chapter_number'), isTrue,
          reason: 'an absent key would mean "leave it alone"');

      final saved = container.read(libraryControllerProvider).value!.single;
      expect(saved.canAdvance, isFalse);
    });
  });

  group('status', () {
    test('changes optimistically and keeps the server value', () async {
      final api = FakeApi();
      final container = testContainer(api);
      await signIn(container, api);

      api.on('GET', '/bookmarks', libraryReply([bookmarkJson()]));
      api.on(
        'PATCH',
        '/bookmarks/bookmark-1',
        RouteReply.ok(bookmarkJson(status: 'completed')),
      );

      await container.read(libraryControllerProvider.future);
      await container
          .read(libraryControllerProvider.notifier)
          .setStatus('bookmark-1', BookmarkStatus.completed);

      expect(
        container.read(libraryControllerProvider).value!.single.status,
        BookmarkStatus.completed,
      );
    });

    test('rolls back when the server refuses', () async {
      final api = FakeApi();
      final container = testContainer(api);
      await signIn(container, api);

      api.on('GET', '/bookmarks', libraryReply([bookmarkJson()]));
      api.on(
        'PATCH',
        '/bookmarks/bookmark-1',
        RouteReply.error(500, ApiErrorCode.internal, 'Something went wrong.'),
      );

      await container.read(libraryControllerProvider.future);

      await expectLater(
        container
            .read(libraryControllerProvider.notifier)
            .setStatus('bookmark-1', BookmarkStatus.completed),
        throwsA(isA<ApiError>()),
      );

      expect(
        container.read(libraryControllerProvider).value!.single.status,
        BookmarkStatus.reading,
      );
    });
  });

  group('removing', () {
    test('takes the card away and keeps it away', () async {
      final api = FakeApi();
      final container = testContainer(api);
      await signIn(container, api);

      api.on('GET', '/bookmarks', libraryReply([bookmarkJson()]));
      api.on('DELETE', '/bookmarks/bookmark-1', const RouteReply(204, {}));

      await container.read(libraryControllerProvider.future);
      await container.read(libraryControllerProvider.notifier).remove('bookmark-1');

      expect(container.read(libraryControllerProvider).value, isEmpty);
    });

    test('restores the card when the server refuses', () async {
      final api = FakeApi();
      final container = testContainer(api);
      await signIn(container, api);

      api.on('GET', '/bookmarks', libraryReply([bookmarkJson()]));
      api.on(
        'DELETE',
        '/bookmarks/bookmark-1',
        RouteReply.error(404, ApiErrorCode.notFound, 'Not found.'),
      );

      await container.read(libraryControllerProvider.future);

      await expectLater(
        container.read(libraryControllerProvider.notifier).remove('bookmark-1'),
        throwsA(isA<ApiError>()),
      );

      expect(container.read(libraryControllerProvider).value, hasLength(1),
          reason: 'a refused delete must not lose the bookmark');
    });
  });

  group('adding', () {
    test('puts the new bookmark at the top', () async {
      final api = FakeApi();
      final container = testContainer(api);
      await signIn(container, api);

      api.on('GET', '/bookmarks', libraryReply([bookmarkJson(title: 'Berserk')]));
      api.on(
        'POST',
        '/bookmarks',
        RouteReply(201, bookmarkJson(id: 'new', title: 'One Piece')),
      );

      await container.read(libraryControllerProvider.future);
      await container.read(libraryControllerProvider.notifier).add(
            title: 'One Piece',
            currentUrl: 'https://reader.example.com/one-piece-chapter-1050',
            chapter: 'Chapter 1050',
            chapterNumber: 1050,
          );

      final bookmarks = container.read(libraryControllerProvider).value!;
      expect(bookmarks.first.title, 'One Piece');
      expect(bookmarks, hasLength(2));
    });

    test('reports a duplicate so the sheet can say so', () async {
      final api = FakeApi();
      final container = testContainer(api);
      await signIn(container, api);

      api.on('GET', '/bookmarks', libraryReply([bookmarkJson()]));
      api.on(
        'POST',
        '/bookmarks',
        RouteReply.error(409, ApiErrorCode.duplicateBookmark,
            'You have already saved this link.'),
      );

      await container.read(libraryControllerProvider.future);

      await expectLater(
        container.read(libraryControllerProvider.notifier).add(
              title: 'One Piece',
              currentUrl: 'https://reader.example.com/one-piece-chapter-1050',
            ),
        throwsA(
          isA<ApiError>()
              .having((e) => e.code, 'code', ApiErrorCode.duplicateBookmark),
        ),
      );
    });
  });

  group('marking read', () {
    // Opening the reader must not be interrupted by bookkeeping failing.
    test('swallows a failure', () async {
      final api = FakeApi();
      final container = testContainer(api);
      await signIn(container, api);

      api.on('GET', '/bookmarks', libraryReply([bookmarkJson()]));
      api.on(
        'POST',
        '/read',
        RouteReply.error(500, ApiErrorCode.internal, 'Something went wrong.'),
      );

      await container.read(libraryControllerProvider.future);

      await expectLater(
        container.read(libraryControllerProvider.notifier).markRead('bookmark-1'),
        completes,
      );
    });
  });
}
