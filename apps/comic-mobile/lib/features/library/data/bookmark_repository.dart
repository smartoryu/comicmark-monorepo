import '../../../core/api_client.dart';
import '../models/bookmark.dart';

/// Every bookmark endpoint.
class BookmarkRepository {
  const BookmarkRepository(this._client);

  final ApiClient _client;

  /// Turns a URL into a preview without saving anything.
  ///
  /// A partial preview is a normal outcome, not an error: the API reports a site
  /// that blocks the bot as `partial: true` so the reader can still save the
  /// bookmark by typing what the page did not provide.
  Future<BookmarkPreview> parse(String url) {
    return _client.post<BookmarkPreview>(
      '/bookmarks/parse',
      body: {'url': url},
      decode: (data) => BookmarkPreview.fromJson(_asMap(data)),
    );
  }

  /// Saves a bookmark.
  Future<Bookmark> create({
    required String title,
    required String currentUrl,
    String? chapter,
    double? chapterNumber,
    String? coverUrl,
    BookmarkStatus? status,
  }) {
    final body = <String, Object?>{
      'title': title,
      'current_url': currentUrl,
      // Sent even when null so the server records "no number" rather than
      // leaving the column unset.
      'chapter_number': chapterNumber,
    };
    if (chapter != null && chapter.isNotEmpty) body['chapter'] = chapter;
    if (coverUrl != null && coverUrl.isNotEmpty) body['cover_url'] = coverUrl;
    if (status != null) body['status'] = status.wire;

    return _client.post<Bookmark>(
      '/bookmarks',
      body: body,
      decode: (data) => Bookmark.fromJson(_asMap(data)),
    );
  }

  /// Reads a page of the library.
  Future<BookmarkPage> list({
    BookmarkStatus? status,
    String? query,
    int? limit,
    int? offset,
  }) {
    final parameters = <String, dynamic>{};
    if (status != null) parameters['status'] = status.wire;
    if (query != null && query.trim().isNotEmpty) parameters['q'] = query.trim();
    if (limit != null) parameters['limit'] = limit;
    if (offset != null) parameters['offset'] = offset;

    return _client.get<BookmarkPage>(
      '/bookmarks',
      query: parameters,
      decode: (data) => BookmarkPage.fromJson(_asMap(data)),
    );
  }

  /// Reads one bookmark.
  Future<Bookmark> get(String id) {
    return _client.get<Bookmark>(
      '/bookmarks/$id',
      decode: (data) => Bookmark.fromJson(_asMap(data)),
    );
  }

  /// Edits a bookmark.
  ///
  /// [clearChapterNumber] is separate from [chapterNumber] because the two mean
  /// different things: omitting the number leaves it alone, while clearing it
  /// says "this chapter is not numeric", which is what sends the +1 button into
  /// its editor.
  Future<Bookmark> update(
    String id, {
    String? title,
    String? chapter,
    double? chapterNumber,
    bool clearChapterNumber = false,
    String? coverUrl,
    BookmarkStatus? status,
  }) {
    final body = <String, Object?>{};
    if (title != null) body['title'] = title;
    if (chapter != null) body['chapter'] = chapter;
    // An explicit null is how the client says "this chapter has no number",
    // which is what sends the +1 action into its editor.
    if (clearChapterNumber) {
      body['chapter_number'] = null;
    } else if (chapterNumber != null) {
      body['chapter_number'] = chapterNumber;
    }
    if (coverUrl != null) body['cover_url'] = coverUrl;
    if (status != null) body['status'] = status.wire;

    return _client.patch<Bookmark>(
      '/bookmarks/$id',
      body: body,
      decode: (data) => Bookmark.fromJson(_asMap(data)),
    );
  }

  /// Advances the chapter by one.
  ///
  /// Throws [ApiError] with `chapter_not_numeric` when the chapter has no
  /// number, which the caller turns into an editor rather than a guess.
  Future<Bookmark> readNext(String id) {
    return _client.patch<Bookmark>(
      '/bookmarks/$id/read-next',
      decode: (data) => Bookmark.fromJson(_asMap(data)),
    );
  }

  /// Records that the reader opened the link, which drives the library order.
  Future<Bookmark> markRead(String id) {
    return _client.post<Bookmark>(
      '/bookmarks/$id/read',
      decode: (data) => Bookmark.fromJson(_asMap(data)),
    );
  }

  /// Removes a bookmark.
  Future<void> delete(String id) {
    return _client.delete<void>('/bookmarks/$id', decode: (_) {});
  }
}

Map<String, Object?> _asMap(Object? data) {
  if (data is Map) return data.cast<String, Object?>();
  return const {};
}
