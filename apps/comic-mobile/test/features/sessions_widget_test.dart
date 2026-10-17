import 'package:comicmark/features/sessions/presentation/sessions_page.dart';
import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';

import '../support/fakes.dart';

/// Widget tests for the devices screen.
///
/// This is the screen that answers "I lost my phone", so the behaviours that
/// matter are: every device is recognisable, the current one is marked, and a
/// device can be signed out without the reader being able to sign themselves out
/// by accident.
void main() {
  setUp(resetSecureStorage);

  Map<String, Object?> sessionJson({
    required String id,
    required String label,
    required String platform,
    required bool current,
    String? lastSeenAt,
  }) {
    return {
      'id': id,
      'device_label': label,
      'platform': platform,
      'ip': '203.0.113.7',
      'current': current,
      'last_seen_at': lastSeenAt ?? DateTime.now().toUtc().toIso8601String(),
      'created_at': '2026-01-01T00:00:00.000Z',
    };
  }

  RouteReply sessionsReply(List<Map<String, Object?>> sessions) =>
      RouteReply.ok({'sessions': sessions});

  /// Pumps the devices screen with the API faked.
  Future<FakeApi> pumpSessions(
    WidgetTester tester,
    List<Map<String, Object?>> sessions,
  ) async {
    tester.view.physicalSize = const Size(900, 1600);
    tester.view.devicePixelRatio = 1.0;
    addTearDown(tester.view.reset);

    final api = FakeApi();
    final container = testContainer(api);
    await tester.runAsync(() => signIn(container, api));
    api.on('GET', '/auth/sessions', sessionsReply(sessions));

    await tester.pumpWidget(
      UncontrolledProviderScope(
        container: container,
        child: const MaterialApp(home: SessionsPage()),
      ),
    );
    await tester.pump();
    await tester.pump(const Duration(seconds: 1));

    return api;
  }

  List<Map<String, Object?>> twoDevices() => [
        sessionJson(
          id: 'session-1',
          label: 'Chrome on macOS',
          platform: 'web',
          current: false,
        ),
        sessionJson(
          id: 'session-2',
          label: 'ComicMark app on Android',
          platform: 'android',
          current: true,
        ),
      ];

  testWidgets('lists every device with its platform and recency', (tester) async {
    await pumpSessions(tester, twoDevices());

    expect(find.text('Chrome on macOS'), findsOneWidget);
    expect(find.text('ComicMark app on Android'), findsOneWidget);
    // The subtitle carries the platform and how long ago the device was used.
    expect(find.textContaining('Web browser ·'), findsOneWidget);
    expect(find.textContaining('Android ·'), findsOneWidget);
    expect(find.textContaining('Signed in on 2 devices'), findsOneWidget);
  });

  testWidgets('marks the device making the request', (tester) async {
    await pumpSessions(tester, twoDevices());

    // Exactly one badge, on the current device.
    expect(find.text('This device'), findsOneWidget);
  });

  testWidgets('offers no sign-out action for the current device', (tester) async {
    await pumpSessions(tester, twoDevices());

    // The current device's menu has no revoke entry, because the API refuses it.
    final menus = find.byType(PopupMenuButton<String>);
    expect(menus, findsNWidgets(2));

    await tester.tap(menus.last);
    await tester.pumpAndSettle();

    expect(find.text('Rename'), findsOneWidget);
    expect(find.text('Sign out'), findsNothing,
        reason: 'offering an action the API rejects is worse than omitting it');
  });

  testWidgets('offers sign-out for another device', (tester) async {
    await pumpSessions(tester, twoDevices());

    await tester.tap(find.byType(PopupMenuButton<String>).first);
    await tester.pumpAndSettle();

    expect(find.text('Rename'), findsOneWidget);
    expect(find.text('Sign out'), findsOneWidget);
  });

  testWidgets('revoking a device removes its row', (tester) async {
    final api = await pumpSessions(tester, twoDevices());
    api.on('DELETE', '/auth/sessions/session-1', const RouteReply(204, {}));

    expect(find.text('Chrome on macOS'), findsOneWidget);

    await tester.tap(find.byType(PopupMenuButton<String>).first);
    await tester.pumpAndSettle();
    await tester.tap(find.text('Sign out'));
    await tester.pumpAndSettle();

    // The confirmation names the device, so a mis-tap is recoverable.
    expect(find.textContaining('Chrome on macOS'), findsWidgets);

    await tester.tap(find.widgetWithText(FilledButton, 'Sign out'));
    // Settle so the dialog closes before the list is inspected.
    await tester.pumpAndSettle();

    expect(find.text('Chrome on macOS'), findsNothing);
    expect(find.text('ComicMark app on Android'), findsOneWidget);
    expect(find.text('This device'), findsOneWidget,
        reason: 'the reader is still signed in on this device');
  });

  testWidgets('revoking every other device leaves only this one',
      (tester) async {
    final api = await pumpSessions(tester, twoDevices());
    api.on('DELETE', '/auth/sessions', const RouteReply(204, {}));

    // After revoking the others, the list holds only the current device.
    api.set('GET', '/auth/sessions', sessionsReply([twoDevices().last]));

    await tester.tap(find.textContaining('Sign out the other device'));
    await tester.pumpAndSettle();
    await tester.tap(find.widgetWithText(FilledButton, 'Sign out others'));
    await tester.pumpAndSettle();

    expect(find.text('ComicMark app on Android'), findsOneWidget);
    expect(find.text('Chrome on macOS'), findsNothing);
    expect(find.textContaining('Sign out the other'), findsNothing);
  });

  testWidgets('a single device says so without offering sign-out-others',
      (tester) async {
    await pumpSessions(tester, [
      sessionJson(
        id: 'session-2',
        label: 'ComicMark app on Android',
        platform: 'android',
        current: true,
      ),
    ]);

    expect(find.textContaining('Signed in on 1 device'), findsOneWidget);
    expect(find.textContaining('Sign out the other'), findsNothing);
    expect(find.text('This device'), findsOneWidget);
  });
}
