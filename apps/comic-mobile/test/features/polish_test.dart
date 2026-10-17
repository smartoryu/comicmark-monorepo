import 'package:comicmark/app/router.dart';
import 'package:comicmark/core/api_error.dart';
import 'package:comicmark/features/sessions/presentation/sessions_page.dart';
import 'package:comicmark/main.dart';
import 'package:flutter/material.dart';
import 'package:shimmer/shimmer.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';

import '../support/fakes.dart';

/// The states a reader sees when something is missing or broken.
///
/// These are easy to leave as an afterthought and are exactly what a reader hits
/// first on a bad connection, so each one is asserted to say something they can
/// act on rather than showing an empty screen.
void main() {
  setUp(resetSecureStorage);

  /// Pumps the app signed in, with the library answering [reply].
  Future<FakeApi> pumpSignedIn(WidgetTester tester, RouteReply reply) async {
    final api = FakeApi();
    final container = testContainer(api);
    await tester.runAsync(() => signIn(container, api));
    api.on('GET', '/bookmarks', reply);

    await tester.pumpWidget(
      UncontrolledProviderScope(
        container: container,
        child: const ComicMarkApp(),
      ),
    );
    await tester.pump();
    await tester.pump(const Duration(milliseconds: 100));
    await tester.pump(const Duration(milliseconds: 100));

    return api;
  }

  testWidgets('the library shows shimmering placeholders while covers load',
      (tester) async {
    // A cover URL means the image widget is built, and its placeholder is the
    // shimmer. The image itself never loads in a test, so the placeholder stays.
    await pumpSignedIn(
      tester,
      libraryReply([bookmarkJson(title: 'One Piece', coverUrl: 'https://cdn.example.com/c.jpg')]),
    );

    expect(find.byType(Shimmer), findsWidgets,
        reason: 'a grid of spinners is noisier than a grid of shimmering covers');
    // The card's content is present even before the cover arrives.
    expect(find.text('One Piece'), findsOneWidget);
  });

  testWidgets('an unreachable server is explained rather than shown as empty',
      (tester) async {
    await pumpSignedIn(
      tester,
      RouteReply.error(500, ApiErrorCode.internal, 'Something went wrong on our side.'),
    );

    expect(find.text('Your library could not be loaded'), findsOneWidget);
    expect(find.text('Try again'), findsOneWidget,
        reason: 'a failed load needs a way to retry');
  });

  testWidgets('an empty library invites the reader to add something',
      (tester) async {
    await pumpSignedIn(tester, libraryReply([]));

    expect(find.text('No bookmarks yet'), findsOneWidget);
    // The wording tells them what to do, not just that there is nothing.
    expect(
      find.textContaining('Paste a chapter URL'),
      findsOneWidget,
    );
    expect(find.text('Add your first comic'), findsOneWidget);
  });

  testWidgets('a device list with nothing in it says so deliberately',
      (tester) async {
    final api = FakeApi();
    final container = testContainer(api);
    await tester.runAsync(() => signIn(container, api));
    api.on('GET', '/auth/sessions', RouteReply.ok({'sessions': <Object?>[]}));

    await tester.pumpWidget(
      UncontrolledProviderScope(
        container: container,
        child: const MaterialApp(home: SessionsPage()),
      ),
    );
    await tester.pump();
    await tester.pump(const Duration(milliseconds: 100));

    expect(find.text('No active devices'), findsOneWidget);
    expect(find.text('Check again'), findsOneWidget);
  });

  testWidgets('a failed device load offers a retry', (tester) async {
    final api = FakeApi();
    final container = testContainer(api);
    await tester.runAsync(() => signIn(container, api));
    api.on(
      'GET',
      '/auth/sessions',
      RouteReply.error(500, ApiErrorCode.internal, 'Something went wrong.'),
    );

    await tester.pumpWidget(
      UncontrolledProviderScope(
        container: container,
        child: const MaterialApp(home: SessionsPage()),
      ),
    );
    await tester.pump();
    await tester.pump(const Duration(milliseconds: 100));

    expect(find.text('Your devices could not be loaded'), findsOneWidget);
    expect(find.text('Try again'), findsOneWidget);
  });

  // A form that fails silently is the worst version of this, so every one of them
  // is checked to surface the API's own wording.
  testWidgets('a failed sign-in says what went wrong', (tester) async {
    final api = FakeApi();
    final container = testContainer(api);
    api.on(
      'POST',
      '/auth/login',
      RouteReply.error(401, ApiErrorCode.invalidCredentials,
          'The email or password you entered is incorrect.'),
    );

    await tester.pumpWidget(
      UncontrolledProviderScope(
        container: container,
        child: const ComicMarkApp(),
      ),
    );
    await tester.pump();
    await tester.pump(const Duration(milliseconds: 100));

    container.read(routerProvider).go(Routes.login);
    await tester.pumpAndSettle();

    await tester.enterText(find.byType(TextField).first, 'reader@example.com');
    await tester.enterText(find.byType(TextField).last, 'wrong password');
    await tester.tap(find.widgetWithText(FilledButton, 'Sign in'));
    await tester.pump();
    await tester.pump(const Duration(milliseconds: 100));

    expect(
      find.textContaining('The email or password you entered is incorrect.'),
      findsOneWidget,
    );
  });
}
