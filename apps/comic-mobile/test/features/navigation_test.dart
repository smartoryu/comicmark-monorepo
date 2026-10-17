import 'package:comicmark/app/router.dart';
import 'package:comicmark/main.dart';
import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';

import '../support/fakes.dart';

/// Navigation behaviour.
///
/// The web build has a real address bar and a real back button, and the emailed
/// links are real URLs, so this is where those three meet: a link must open the
/// right screen, and a screen the reader navigated *into* must be returnable
/// from.
void main() {
  setUp(resetSecureStorage);

  /// Pumps the app signed out, on the library.
  Future<(FakeApi, ProviderContainer)> pumpApp(WidgetTester tester) async {
    final api = FakeApi();
    final container = testContainer(api);

    await tester.pumpWidget(
      UncontrolledProviderScope(
        container: container,
        child: const ComicMarkApp(),
      ),
    );
    await tester.pump();
    await tester.pump(const Duration(seconds: 1));

    return (api, container);
  }

  group('deep links', () {
    // This is the URL inside a password reset email.
    testWidgets('a reset link opens the reset screen with its token',
        (tester) async {
      final (_, container) = await pumpApp(tester);

      container.read(routerProvider).go('/reset-password?reset_token=abc123');
      await tester.pumpAndSettle();

      expect(find.text('Choose a new password'), findsOneWidget);
    });

    testWidgets('a reset link without a token says so rather than failing',
        (tester) async {
      final (_, container) = await pumpApp(tester);

      container.read(routerProvider).go('/reset-password');
      await tester.pumpAndSettle();

      expect(find.text('This link is incomplete'), findsOneWidget);
      expect(find.text('Request a new link'), findsOneWidget);
    });

    // This is the URL inside a verification email.
    testWidgets('a verification link confirms the address', (tester) async {
      final (api, container) = await pumpApp(tester);

      api.on('POST', '/auth/verification/email/validate', RouteReply.ok({}));
      // refreshUser finds no session, so it reports signed out rather than
      // calling the API.
      container.read(routerProvider).go('/verify-email?verify_token=abc123');
      await tester.pump();
      await tester.pump(const Duration(seconds: 1));

      expect(find.text('Email confirmed'), findsOneWidget);
      expect(api.countFor('POST', '/auth/verification/email/validate'), 1);
    });

    testWidgets('a verification link that is already used is explained',
        (tester) async {
      final (api, container) = await pumpApp(tester);

      api.on(
        'POST',
        '/auth/verification/email/validate',
        RouteReply.error(401, 'token_invalid', 'That link is invalid or has expired.'),
      );

      container.read(routerProvider).go('/verify-email?verify_token=stale');
      await tester.pump();
      await tester.pump(const Duration(seconds: 1));

      expect(find.text('This link did not work'), findsOneWidget);
      expect(find.textContaining('expired or has already been used'), findsOneWidget);
      // And a fresh link can be asked for, because resending needs no session.
      expect(find.text('Resend the link'), findsOneWidget);
    });

    testWidgets('an unknown path lands somewhere useful', (tester) async {
      final (_, container) = await pumpApp(tester);

      container.read(routerProvider).go('/nonsense/path');
      await tester.pumpAndSettle();

      expect(find.textContaining('There is nothing at'), findsOneWidget);
      expect(find.text('Go to your library'), findsOneWidget);
    });
  });

  group('back navigation', () {
    /// Signs in, so the screens that need a session are reachable.
    Future<ProviderContainer> pumpSignedIn(WidgetTester tester) async {
      final api = FakeApi();
      final container = testContainer(api);
      await tester.runAsync(() => signIn(container, api));
      api.on('GET', '/bookmarks', libraryReply([bookmarkJson(coverUrl: null)]));
      // One device, so the sessions screen renders its list rather than its
      // empty state: the assertion below is about the header it shows.
      api.on('GET', '/auth/sessions', RouteReply.ok({
        'sessions': [
          {
            'id': 'session-1',
            'device_label': 'Chrome on macOS',
            'platform': 'web',
            'current': true,
            'last_seen_at': DateTime.now().toUtc().toIso8601String(),
            'created_at': '2026-01-01T00:00:00.000Z',
          },
        ],
      }));

      await tester.pumpWidget(
        UncontrolledProviderScope(
          container: container,
          child: const ComicMarkApp(),
        ),
      );
      await tester.pump();
      await tester.pump(const Duration(seconds: 1));

      return container;
    }

    // The reader navigated *into* settings, so going back must return them to the
    // library rather than closing the app, which is what the web back button and
    // the Android back gesture both do.
    testWidgets('settings returns to the library', (tester) async {
      final container = await pumpSignedIn(tester);

      container.read(routerProvider).go(Routes.library);
      await tester.pumpAndSettle();

      await tester.tap(find.byTooltip('Settings'));
      await tester.pumpAndSettle();
      expect(find.text('Change password'), findsOneWidget);

      container.read(routerProvider).pop();
      await tester.pumpAndSettle();

      expect(find.text('One Piece'), findsOneWidget,
          reason: 'back should return to the library');
    });

    testWidgets('the devices screen returns to settings', (tester) async {
      final container = await pumpSignedIn(tester);

      container.read(routerProvider).go(Routes.settings);
      await tester.pumpAndSettle();

      await tester.tap(find.text('Devices & sessions'));
      await tester.pumpAndSettle();
      expect(find.textContaining('Signed in on'), findsOneWidget);

      container.read(routerProvider).pop();
      await tester.pumpAndSettle();

      expect(find.text('Change password'), findsOneWidget,
          reason: 'back should return to settings');
    });

    // A completed sign-in must not be returnable: pressing back into the form
    // would offer to sign in again while already signed in.
    testWidgets('signing in replaces the sign-in screen in history',
        (tester) async {
      final (api, container) = await pumpApp(tester);

      api.on('POST', '/auth/login', RouteReply.ok({
        'access_token': 'access-1',
        'refresh_token': 'refresh-1',
        'token_type': 'Bearer',
        'expires_in': 900,
        'session_id': 'session-1',
        'user': {
          'id': 'user-1',
          'email': 'reader@example.com',
          'email_verified': true,
          'created_at': '2026-01-01T00:00:00.000Z',
        },
      }));
      api.on('GET', '/bookmarks', libraryReply([]));

      container.read(routerProvider).go(Routes.login);
      await tester.pumpAndSettle();

      await tester.enterText(find.byType(TextField).first, 'reader@example.com');
      await tester.enterText(find.byType(TextField).last, 'correct horse battery');
      await tester.tap(find.widgetWithText(FilledButton, 'Sign in'));
      // A frame for the sign-in to resolve, then a frame for the library request
      // the new session triggers.
      await tester.pump();
      await tester.pump(const Duration(milliseconds: 100));
      await tester.pump(const Duration(milliseconds: 100));

      // The library is showing, and the sign-in screen is not in history.
      expect(find.text('No bookmarks yet'), findsOneWidget);
      expect(container.read(routerProvider).canPop(), isFalse,
          reason: 'a completed sign-in should leave nothing to go back to');
    });
  });
}
