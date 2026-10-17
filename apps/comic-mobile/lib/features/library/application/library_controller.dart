import 'package:flutter_riverpod/flutter_riverpod.dart';

import '../../../app/providers.dart';
import '../../../core/api_error.dart';
import '../../../core/retry.dart';
import '../../auth/application/auth_controller.dart';
import '../data/bookmark_repository.dart';
import '../models/bookmark.dart';

/// The bookmark repository.
final bookmarkRepositoryProvider = Provider<BookmarkRepository>(
  (ref) => BookmarkRepository(ref.watch(apiClientProvider)),
);

/// Which slice of the library is being shown.
class LibraryFilter {
  const LibraryFilter({this.status, this.query = ''});

  /// null means every status.
  final BookmarkStatus? status;
  final String query;

  LibraryFilter copyWith({
    BookmarkStatus? status,
    bool clearStatus = false,
    String? query,
  }) {
    return LibraryFilter(
      status: clearStatus ? null : (status ?? this.status),
      query: query ?? this.query,
    );
  }

  @override
  bool operator ==(Object other) =>
      other is LibraryFilter && other.status == status && other.query == query;

  @override
  int get hashCode => Object.hash(status, query);
}

/// Holds the active library filter.
///
/// A Notifier rather than a raw StateProvider, which Riverpod 3 moved to a
/// legacy import.
class LibraryFilterNotifier extends Notifier<LibraryFilter> {
  @override
  LibraryFilter build() => const LibraryFilter();

  /// Sets the status filter; null clears it.
  void setStatus(BookmarkStatus? status) {
    state = status == null
        ? LibraryFilter(query: state.query)
        : LibraryFilter(status: status, query: state.query);
  }

  /// Sets the search term.
  void setQuery(String query) => state = LibraryFilter(status: state.status, query: query);

  /// Clears everything.
  void clear() => state = const LibraryFilter();
}

/// The active filter.
final libraryFilterProvider =
    NotifierProvider<LibraryFilterNotifier, LibraryFilter>(
  LibraryFilterNotifier.new,
);

/// The library, as a list the UI can update in place.
///
/// A plain `AsyncNotifier` over `List<Bookmark>` rather than a paged stream: a
/// reader's library is tens of items, and holding it whole is what makes the +1
/// optimistic update simple, since the item can be replaced by identity.
final libraryControllerProvider =
    AsyncNotifierProvider<LibraryController, List<Bookmark>>(
  LibraryController.new,
  retry: apiRetry,
);

class LibraryController extends AsyncNotifier<List<Bookmark>> {
  @override
  Future<List<Bookmark>> build() async {
    // Every dependency is read before the first await. Watching after an await
    // is not supported: the element may already have been rebuilt, and in
    // practice it makes a failed build re-run forever, which shows a permanent
    // spinner and re-requests on every frame instead of surfacing the error.
    //
    // The session is watched because signing in loads the library and signing
    // out empties it, rather than leaving another reader's bookmarks on screen.
    final user = ref.watch(authControllerProvider).value;
    final filter = ref.watch(libraryFilterProvider);
    final repository = ref.watch(bookmarkRepositoryProvider);

    if (user == null) return const [];

    final page = await repository.list(
      status: filter.status,
      query: filter.query,
    );
    return page.bookmarks;
  }

  /// Re-reads the library.
  Future<void> reload() async {
    state = const AsyncLoading();
    state = await AsyncValue.guard(() => _fetch());
  }

  Future<List<Bookmark>> _fetch() async {
    final filter = ref.read(libraryFilterProvider);
    final repository = ref.read(bookmarkRepositoryProvider);

    final page = await repository.list(
      status: filter.status,
      query: filter.query,
    );
    return page.bookmarks;
  }

  /// Advances a chapter by one.
  ///
  /// The card is updated immediately and rolled back if the server refuses, so a
  /// tap feels instant without the app ever showing a chapter the server did not
  /// accept. A `chapter_not_numeric` refusal is rethrown untouched: the caller
  /// opens an editor instead of showing an error, because the reader can fix it.
  Future<void> readNext(String id) async {
    final current = state.value;
    if (current == null) return;

    final index = current.indexWhere((b) => b.id == id);
    if (index < 0) return;

    final original = current[index];
    if (!original.canAdvance) {
      throw const ApiError(
        code: ApiErrorCode.chapterNotNumeric,
        message: 'This chapter has no number to advance.',
      );
    }

    state = AsyncData(_replace(current, index, original.withAdvancedChapter()));

    try {
      final saved = await ref.read(bookmarkRepositoryProvider).readNext(id);
      _replaceById(saved);
    } catch (error) {
      // Roll back only if the row is still the optimistic one; a later refresh
      // may have already corrected it.
      final latest = state.value;
      if (latest != null) {
        final at = latest.indexWhere((b) => b.id == id);
        if (at >= 0 && latest[at].chapterNumber == original.chapterNumber! + 1) {
          state = AsyncData(_replace(latest, at, original));
        }
      }
      rethrow;
    }
  }

  /// Changes a bookmark's status.
  Future<void> setStatus(String id, BookmarkStatus status) async {
    final current = state.value;
    if (current == null) return;

    final index = current.indexWhere((b) => b.id == id);
    if (index < 0) return;

    final original = current[index];
    state = AsyncData(_replace(current, index, original.withStatus(status)));

    try {
      final saved = await ref
          .read(bookmarkRepositoryProvider)
          .update(id, status: status);
      _replaceById(saved);
    } catch (_) {
      final latest = state.value;
      if (latest != null) {
        final at = latest.indexWhere((b) => b.id == id);
        if (at >= 0) state = AsyncData(_replace(latest, at, original));
      }
      rethrow;
    }
  }

  /// Saves an edited chapter, used after a `chapter_not_numeric` refusal and by
  /// the edit sheet.
  Future<Bookmark> editChapter(
    String id, {
    required String chapter,
    required double? chapterNumber,
  }) async {
    final saved = await ref.read(bookmarkRepositoryProvider).update(
          id,
          chapter: chapter,
          chapterNumber: chapterNumber,
          clearChapterNumber: chapterNumber == null,
        );
    _replaceById(saved);
    return saved;
  }

  /// Records that the reader opened a chapter.
  ///
  /// Fire-and-forget: the library reorders on the next load, and a failed write
  /// here must not interrupt opening the reader.
  Future<void> markRead(String id) async {
    try {
      final saved = await ref.read(bookmarkRepositoryProvider).markRead(id);
      _replaceById(saved);
    } on ApiError {
      // Opening the chapter already succeeded as far as the reader is concerned.
    }
  }

  /// Saves a new bookmark and puts it at the top.
  Future<Bookmark> add({
    required String title,
    required String currentUrl,
    String? chapter,
    double? chapterNumber,
    String? coverUrl,
  }) async {
    final saved = await ref.read(bookmarkRepositoryProvider).create(
          title: title,
          currentUrl: currentUrl,
          chapter: chapter,
          chapterNumber: chapterNumber,
          coverUrl: coverUrl,
        );

    final current = state.value ?? const <Bookmark>[];
    state = AsyncData([saved, ...current]);
    return saved;
  }

  /// Removes a bookmark, restoring it if the server refuses.
  Future<void> remove(String id) async {
    final current = state.value;
    if (current == null) return;

    final index = current.indexWhere((b) => b.id == id);
    if (index < 0) return;

    final original = current[index];
    state = AsyncData(
      current.where((b) => b.id != id).toList(growable: false),
    );

    try {
      await ref.read(bookmarkRepositoryProvider).delete(id);
    } catch (_) {
      final latest = state.value ?? const <Bookmark>[];
      state = AsyncData([...latest]..insert(index.clamp(0, latest.length), original));
      rethrow;
    }
  }

  /// Replaces one row in the current list.
  void _replaceById(Bookmark saved) {
    final current = state.value;
    if (current == null) return;

    final index = current.indexWhere((b) => b.id == saved.id);
    if (index < 0) return;

    state = AsyncData(_replace(current, index, saved));
  }

  static List<Bookmark> _replace(
    List<Bookmark> list,
    int index,
    Bookmark replacement,
  ) {
    final copy = [...list];
    copy[index] = replacement;
    return copy;
  }
}

/// How many bookmarks the reader has in total, shown in the library header.
final libraryCountProvider = Provider<int?>((ref) {
  final library = ref.watch(libraryControllerProvider).value;
  return library?.length;
});
