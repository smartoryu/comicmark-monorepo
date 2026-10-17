import 'package:comicmark/core/api_error.dart';
import 'package:comicmark/main.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';

import '../support/fakes.dart';

/// Widget tests for the library.
///
/// The whole app is pumped, so the router, the theme regime and the real screens
/// are exercised rather than a harness assembled for the test.
void main() {
  setUp(resetSecureStorage);

  /// Pumps the app with the API faked, and waits for the library to render.
  ///
  /// Explicit pumps rather than pumpAndSettle: the cover placeholders shimmer
  /// forever, so settling would never happen.
  Future<FakeApi> pumpLibrary(
    WidgetTester tester, {
    required List<Map<String, Object?>> bookmarks,
    bool extraRoutes = false,
  }) async {
    final api = FakeApi();
    final container = testContainer(api);
    // Widget tests run on a fake clock, so the real asynchronous sign-in has to
    // happen outside it.
    await tester.runAsync(() => signIn(container, api));

    api.on('GET', '/bookmarks', libraryReply(bookmarks));

    await tester.pumpWidget(
      UncontrolledProviderScope(
        container: container,
        child: const ComicMarkApp(),
      ),
    );
    // A few frames: one for the session check, one for the library request.
    await tester.pump();
    await tester.pump(const Duration(milliseconds: 100));

    return api;
  }

  testWidgets('renders a bookmark with its title and chapter', (tester) async {
    await pumpLibrary(tester, bookmarks: [
      bookmarkJson(title: 'One Piece', chapter: 'Chapter 1050', coverUrl: null),
    ]);

    expect(find.text('One Piece'), findsOneWidget);
    expect(find.text('Chapter 1050'), findsOneWidget);
    expect(find.text('reader.example.com'), findsOneWidget);
  });

  testWidgets('renders every bookmark in the library', (tester) async {
    await pumpLibrary(tester, bookmarks: [
      bookmarkJson(id: 'a', title: 'One Piece', coverUrl: null),
      bookmarkJson(id: 'b', title: 'Berserk', coverUrl: null),
      bookmarkJson(id: 'c', title: 'Vagabond', coverUrl: null),
    ]);

    expect(find.text('One Piece'), findsOneWidget);
    expect(find.text('Berserk'), findsOneWidget);
    expect(find.text('Vagabond'), findsOneWidget);
  });

  // The +1 button must move the chip without waiting for the server, or a tap
  // feels broken on a slow connection.
  testWidgets('the +1 button advances the chapter optimistically', (tester) async {
    final api = await pumpLibrary(tester, bookmarks: [bookmarkJson(coverUrl: null)]);

    // A slow server, so the optimistic value is observable before the reply.
    api.delay = const Duration(seconds: 5);
    api.on(
      'PATCH',
      '/read-next',
      RouteReply.ok(bookmarkJson(chapter: 'Chapter 1051', chapterNumber: 1051)),
    );

    expect(find.text('Chapter 1050'), findsOneWidget);

    await tester.tap(find.byTooltip('Next chapter'));
    await tester.pump();

    expect(find.text('Chapter 1051'), findsOneWidget,
        reason: 'the chip should move before the server replies');
    expect(find.text('Chapter 1050'), findsNothing);

    // The server's answer then replaces the guess, which here agrees with it.
    await tester.pump(const Duration(seconds: 6));
    expect(find.text('Chapter 1051'), findsOneWidget);
  });

  testWidgets('the +1 button rolls the chapter back when the server refuses',
      (tester) async {
    final api = await pumpLibrary(tester, bookmarks: [bookmarkJson(coverUrl: null)]);

    api.delay = const Duration(seconds: 5);
    api.on(
      'PATCH',
      '/read-next',
      RouteReply.error(500, ApiErrorCode.internal, 'Something went wrong.'),
    );

    await tester.tap(find.byTooltip('Next chapter'));
    await tester.pump();

    // The guess is shown first, which is what makes the tap feel instant.
    expect(find.text('Chapter 1051'), findsOneWidget);

    // Then the refusal arrives and the card returns to the server's truth.
    await tester.pump(const Duration(seconds: 6));

    expect(find.text('Chapter 1050'), findsOneWidget);
    expect(find.text('Chapter 1051'), findsNothing,
        reason: 'a refused update must not leave a chapter the server rejected');
  });

  // A chapter with no number cannot be advanced, so the button asks instead.
  testWidgets('the +1 button opens the editor when the chapter has no number',
      (tester) async {
    await pumpLibrary(tester, bookmarks: [
      bookmarkJson(chapter: 'Prologue', chapterNumber: null, coverUrl: null),
    ]);

    expect(find.byTooltip('Set the next chapter'), findsOneWidget,
        reason: 'the tooltip should say the chapter needs setting');

    await tester.tap(find.byTooltip('Set the next chapter'));
    await tester.pump();
    await tester.pump(const Duration(milliseconds: 100));

    // The sheet opens prefilled with what the site said.
    expect(find.text('Chapter'), findsWidgets);
    expect(find.text('Save chapter'), findsOneWidget);
  });

  testWidgets('a signed-out reader is offered a way in rather than an empty grid',
      (tester) async {
    final api = FakeApi();
    final container = testContainer(api);

    await tester.pumpWidget(
      UncontrolledProviderScope(
        container: container,
        child: const ComicMarkApp(),
      ),
    );
    await tester.pump();
    await tester.pump(const Duration(milliseconds: 100));

    expect(find.text('Your comic library, in one place'), findsOneWidget);
    expect(find.text('Sign in'), findsWidgets);
    // And nothing was requested from the API.
    expect(api.countFor('GET', '/bookmarks'), 0);
  });

  testWidgets('an account with no bookmarks is invited to add one', (tester) async {
    await pumpLibrary(tester, bookmarks: []);

    expect(find.text('No bookmarks yet'), findsOneWidget);
    expect(find.text('Add your first comic'), findsOneWidget);
  });

  testWidgets('an unverified reader is reminded, and can dismiss the reminder',
      (tester) async {
    final api = FakeApi();
    final container = testContainer(api);
    await tester.runAsync(() => signIn(container, api, emailVerified: false));
    api.on('GET', '/bookmarks', libraryReply([bookmarkJson(coverUrl: null)]));

    await tester.pumpWidget(
      UncontrolledProviderScope(
        container: container,
        child: const ComicMarkApp(),
      ),
    );
    await tester.pump();
    await tester.pump(const Duration(milliseconds: 100));

    expect(
      find.text('Confirm your email address to secure your account.'),
      findsOneWidget,
    );

    await tester.tap(find.byTooltip('Dismiss'));
    await tester.pump();

    expect(
      find.text('Confirm your email address to secure your account.'),
      findsNothing,
    );
    // The library itself is unaffected: verification is not enforced in this
    // configuration.
    expect(find.text('One Piece'), findsOneWidget);
  });
}
