import 'package:flutter_riverpod/flutter_riverpod.dart';

import '../../../core/api_error.dart';
import '../../library/application/library_controller.dart';
import '../../library/models/bookmark.dart';

/// The state of the "add a comic" flow.
class PreviewState {
  const PreviewState({
    this.preview,
    this.loading = false,
    this.error,
    this.url = '',
  });

  /// What the URL turned into, once fetched.
  final BookmarkPreview? preview;

  /// Whether a preview is being fetched.
  final bool loading;

  /// Why the last attempt failed, when it did.
  final ApiError? error;

  /// The URL being previewed, kept so a retry does not need it again.
  final String url;

  /// Whether there is something to show.
  bool get hasPreview => preview != null;

  /// Whether the reader still has to supply something.
  ///
  /// A partial preview is the API telling us the page did not publish everything,
  /// which is exactly when the form must be fully editable.
  bool get needsManualEntry => preview?.partial ?? false;

  /// The fields the reader may edit, seeded from the preview.
  factory PreviewState.fromPreview(BookmarkPreview preview, String url) {
    return PreviewState(preview: preview, url: url);
  }
}

/// Fetches a preview for a URL, and holds the values the reader edits before
/// saving.
///
/// Kept as a Notifier rather than local widget state so the sheet can rebuild the
/// editable fields when a preview arrives without losing what was typed.
class PreviewController extends Notifier<PreviewState> {
  @override
  PreviewState build() => const PreviewState();

  /// Fetches a preview for a URL.
  ///
  /// A URL the API refuses is reported as an error, because the reader can fix
  /// it. A partial preview is not an error: it arrives as data with
  /// [PreviewState.needsManualEntry] set, and the form opens for editing.
  Future<void> preview_(String url) async {
    state = PreviewState(loading: true, url: url);

    try {
      final preview = await ref.read(bookmarkRepositoryProvider).parse(url);
      state = PreviewState.fromPreview(preview, url);
    } on ApiError catch (error) {
      state = PreviewState(error: error, url: url);
    }
  }

  /// Clears the flow.
  void reset() => state = const PreviewState();

  /// Saves the bookmark, with whatever the reader ended up with.
  ///
  /// Returns the saved bookmark so the caller can react to a duplicate.
  Future<Bookmark> save({
    required String title,
    required String url,
    required String chapter,
    required double? chapterNumber,
    String? coverUrl,
  }) async {
    return ref.read(libraryControllerProvider.notifier).add(
          title: title,
          currentUrl: url,
          chapter: chapter,
          chapterNumber: chapterNumber,
          coverUrl: coverUrl,
        );
  }
}

/// The add-a-comic flow.
final previewControllerProvider =
    NotifierProvider<PreviewController, PreviewState>(PreviewController.new);
