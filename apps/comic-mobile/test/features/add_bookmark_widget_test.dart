import 'package:comicmark/core/api_error.dart';
import 'package:comicmark/features/bookmark_add/presentation/add_bookmark_sheet.dart';
import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';

import '../support/fakes.dart';

/// Widget tests for the add-a-comic sheet.
///
/// The behaviour that matters here is the manual fallback: when the API reports
/// a partial preview, every field has to be editable and the reader has to be
/// told why, because that is the path taken whenever a site blocks the scraper.
void main() {
  setUp(resetSecureStorage);

  /// Pumps the sheet on its own, with the API faked.
  ///
  /// The viewport is made taller than the default 800x600: the sheet is a
  /// scrolling list, and a widget below the fold is never built, so an assertion
  /// about the save button would fail for reasons that have nothing to do with
  /// the behaviour under test.
  Future<FakeApi> pumpSheet(WidgetTester tester) async {
    tester.view.physicalSize = const Size(900, 1800);
    tester.view.devicePixelRatio = 1.0;
    addTearDown(tester.view.reset);

    final api = FakeApi();
    final container = testContainer(api);

    // The sheet borrows the library controller to save, so there must be a
    // session, and the real async sign-in needs the real clock.
    await tester.runAsync(() => signIn(container, api));

    await tester.pumpWidget(
      UncontrolledProviderScope(
        container: container,
        child: const MaterialApp(
          home: Scaffold(body: AddBookmarkSheet()),
        ),
      ),
    );
    await tester.pump();

    return api;
  }

  /// A complete preview reply.
  RouteReply fullPreview() => RouteReply.ok({
        'title': 'One Piece',
        'cover_url': 'https://cdn.example.com/cover.jpg',
        'site_name': 'Manga Reader',
        'current_url': 'https://reader.example.com/one-piece-chapter-1050',
        'chapter': 'Chapter 1050',
        'chapter_number': 1050,
        'source_host': 'reader.example.com',
        'partial': false,
        'warnings': <String>[],
      });

  /// A preview the API could not complete.
  RouteReply partialPreview() => RouteReply.ok({
        'title': 'reader.example.com',
        'cover_url': null,
        'current_url': 'https://reader.example.com/one-piece-chapter-1050',
        'chapter': '1050',
        'chapter_number': 1050,
        'source_host': 'reader.example.com',
        'partial': true,
        'warnings': ['the site replied with 403'],
      });

  Future<void> enterUrl(WidgetTester tester, String url) async {
    await tester.enterText(find.byType(TextField).first, url);
    await tester.pump();
  }

  testWidgets('previews a link and shows the fields it found', (tester) async {
    final api = await pumpSheet(tester);
    api.on('POST', '/bookmarks/parse', fullPreview());

    await enterUrl(tester, 'https://reader.example.com/one-piece-chapter-1050');
    await tester.tap(find.text('Preview'));
    await tester.pump();
    await tester.pump(const Duration(seconds: 1));

    // The title and chapter arrive filled in.
    expect(find.widgetWithText(TextField, 'One Piece'), findsOneWidget);
    expect(find.widgetWithText(TextField, 'Chapter 1050'), findsOneWidget);
    expect(find.widgetWithText(TextField, 'Manga Reader'), findsNothing,
        reason: 'the site name is a label, not an editable field');

    // A complete preview needs no explanation.
    expect(find.textContaining('could not be read'), findsNothing);
  });

  // This is the path taken whenever a site blocks the bot, so it is the one that
  // most needs to work.
  testWidgets('a partial preview explains itself and stays editable',
      (tester) async {
    final api = await pumpSheet(tester);
    api.on('POST', '/bookmarks/parse', partialPreview());

    await enterUrl(tester, 'https://reader.example.com/one-piece-chapter-1050');
    await tester.tap(find.text('Preview'));
    await tester.pump();
    await tester.pump(const Duration(seconds: 1));

    // The reader is told what went wrong rather than left guessing.
    expect(find.textContaining('the site replied with 403'), findsOneWidget);

    // And every field they might need to fix is present.
    expect(find.widgetWithText(TextField, 'reader.example.com'), findsWidgets);
    expect(find.text('Title'), findsOneWidget);
    expect(find.text('Chapter'), findsOneWidget);
    expect(find.text('Number'), findsOneWidget);
    expect(find.text('Cover image link'), findsOneWidget);

    // The fallback still ends in a save, not a dead end.
    expect(find.text('Save to library'), findsOneWidget);
  });

  testWidgets('the reader can type a title when the page had none',
      (tester) async {
    final api = await pumpSheet(tester);
    api.on('POST', '/bookmarks/parse', partialPreview());
    api.on('POST', '/bookmarks', RouteReply(201, bookmarkJson(title: 'One Piece')));

    await enterUrl(tester, 'https://reader.example.com/one-piece-chapter-1050');
    await tester.tap(find.text('Preview'));
    await tester.pump();
    await tester.pump(const Duration(seconds: 1));

    // Replace the host-derived fallback title with the real one.
    final titleField = find.widgetWithText(TextField, 'reader.example.com').first;
    await tester.enterText(titleField, 'One Piece');
    await tester.pump();

    await tester.tap(find.text('Save to library'));
    await tester.pump();
    await tester.pump(const Duration(seconds: 1));

    expect(api.bodies.last['title'], 'One Piece',
        reason: 'the typed title is what gets saved');
    expect(api.bodies.last['chapter_number'], 1050);
  });

  testWidgets('a refused link is reported as something the reader can fix',
      (tester) async {
    final api = await pumpSheet(tester);
    api.on(
      'POST',
      '/bookmarks/parse',
      RouteReply.error(400, ApiErrorCode.urlNotAllowed,
          'That link cannot be opened. Check it and try again.'),
    );

    await enterUrl(tester, 'http://169.254.169.254/latest/meta-data/');
    await tester.tap(find.text('Preview'));
    await tester.pump();
    await tester.pump(const Duration(seconds: 1));

    expect(
      find.textContaining('That link cannot be opened'),
      findsOneWidget,
      reason: 'the refusal is the reader\'s mistake and should be named',
    );
  });

  testWidgets('a duplicate is named exactly', (tester) async {
    final api = await pumpSheet(tester);
    api.on('POST', '/bookmarks/parse', fullPreview());
    api.on(
      'POST',
      '/bookmarks',
      RouteReply.error(409, ApiErrorCode.duplicateBookmark,
          'You have already saved this link.'),
    );

    await enterUrl(tester, 'https://reader.example.com/one-piece-chapter-1050');
    await tester.tap(find.text('Preview'));
    await tester.pump();
    await tester.pump(const Duration(seconds: 1));

    await tester.tap(find.text('Save to library'));
    await tester.pump();
    await tester.pump(const Duration(seconds: 1));

    expect(find.textContaining('already saved this link'), findsOneWidget);
  });

  testWidgets('previewing without a link says so rather than sending nothing',
      (tester) async {
    final api = await pumpSheet(tester);

    await tester.tap(find.text('Preview'));
    await tester.pump();

    expect(find.text('Paste a chapter link first.'), findsOneWidget);
    expect(api.countFor('POST', '/bookmarks/parse'), 0);
  });
}
