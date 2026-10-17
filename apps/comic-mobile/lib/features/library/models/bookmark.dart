/// The bookmark model and the preview a URL turns into.
library;

/// Where a reader is with a comic.
enum BookmarkStatus {
  reading('reading', 'Reading'),
  paused('paused', 'Paused'),
  completed('completed', 'Finished'),
  dropped('dropped', 'Dropped');

  const BookmarkStatus(this.wire, this.label);

  /// The value the API uses.
  final String wire;

  /// The value a reader sees.
  final String label;

  static BookmarkStatus fromWire(String? value) {
    return BookmarkStatus.values.firstWhere(
      (status) => status.wire == value,
      orElse: () => BookmarkStatus.reading,
    );
  }
}

/// A saved chapter.
class Bookmark {
  const Bookmark({
    required this.id,
    required this.title,
    required this.currentUrl,
    required this.status,
    this.chapter = '',
    this.chapterNumber,
    this.coverUrl,
    this.sourceHost,
    this.lastReadAt,
    this.createdAt,
    this.updatedAt,
  });

  final String id;
  final String title;

  /// The chapter the reader was last at. Tapping the card opens this.
  final String currentUrl;

  final BookmarkStatus status;

  /// The chapter as the site wrote it, e.g. "Chapter 1050".
  final String chapter;

  /// The parsed number, null when the chapter text carries none. A null here is
  /// what makes the +1 button open an editor instead of advancing.
  final double? chapterNumber;

  final String? coverUrl;
  final String? sourceHost;
  final DateTime? lastReadAt;
  final DateTime? createdAt;
  final DateTime? updatedAt;

  /// Whether the chapter can be advanced with one tap.
  bool get canAdvance => chapterNumber != null;

  /// What to show on the card: the site's wording when there is one, otherwise
  /// the number.
  String get chapterLabel {
    final text = chapter.trim();
    if (text.isNotEmpty) return text;

    final number = chapterNumber;
    if (number == null) return 'No chapter set';
    return number == number.truncateToDouble()
        ? 'Chapter ${number.toInt()}'
        : 'Chapter $number';
  }

  factory Bookmark.fromJson(Map<String, Object?> json) {
    return Bookmark(
      id: json['id'] as String? ?? '',
      title: json['title'] as String? ?? '',
      currentUrl: json['current_url'] as String? ?? '',
      status: BookmarkStatus.fromWire(json['status'] as String?),
      chapter: json['chapter'] as String? ?? '',
      chapterNumber: _toDouble(json['chapter_number']),
      coverUrl: json['cover_url'] as String?,
      sourceHost: json['source_host'] as String?,
      lastReadAt: _toDate(json['last_read_at']),
      createdAt: _toDate(json['created_at']),
      updatedAt: _toDate(json['updated_at']),
    );
  }

  /// A copy with the fields a +1 changes, used for the optimistic update.
  ///
  /// The optimistic value is a guess that the server may correct, so it is
  /// applied to the model and then replaced by whatever the API returns.
  Bookmark withAdvancedChapter() {
    final number = chapterNumber;
    if (number == null) return this;

    final next = number + 1;
    final formatted = next == next.truncateToDouble()
        ? next.toInt().toString()
        : next.toString();

    // Only the digits are replaced, so the site's own wording survives, matching
    // what the server does.
    final text = chapter.trim();
    final updated = text.isEmpty
        ? formatted
        : _replaceLastNumber(text, formatted) ?? formatted;

    return Bookmark(
      id: id,
      title: title,
      currentUrl: currentUrl,
      status: status,
      chapter: updated,
      chapterNumber: next,
      coverUrl: coverUrl,
      sourceHost: sourceHost,
      lastReadAt: DateTime.now(),
      createdAt: createdAt,
      updatedAt: updatedAt,
    );
  }

  /// A copy with a new status, for the optimistic update of a status change.
  Bookmark withStatus(BookmarkStatus status) {
    return Bookmark(
      id: id,
      title: title,
      currentUrl: currentUrl,
      status: status,
      chapter: chapter,
      chapterNumber: chapterNumber,
      coverUrl: coverUrl,
      sourceHost: sourceHost,
      lastReadAt: lastReadAt,
      createdAt: createdAt,
      updatedAt: updatedAt,
    );
  }
}

/// What a URL looks like before it is saved.
class BookmarkPreview {
  const BookmarkPreview({
    required this.title,
    required this.currentUrl,
    this.coverUrl,
    this.siteName,
    this.chapter = '',
    this.chapterNumber,
    this.sourceHost,
    this.partial = false,
    this.warnings = const [],
  });

  final String title;
  final String currentUrl;
  final String? coverUrl;
  final String? siteName;
  final String chapter;
  final double? chapterNumber;
  final String? sourceHost;

  /// Whether the preview is incomplete, in which case every field must be
  /// editable: the reader is about to supply what the page did not publish.
  final bool partial;
  final List<String> warnings;

  factory BookmarkPreview.fromJson(Map<String, Object?> json) {
    final warnings = json['warnings'];
    return BookmarkPreview(
      title: json['title'] as String? ?? '',
      currentUrl: json['current_url'] as String? ?? '',
      coverUrl: json['cover_url'] as String?,
      siteName: json['site_name'] as String?,
      chapter: json['chapter'] as String? ?? '',
      chapterNumber: _toDouble(json['chapter_number']),
      sourceHost: json['source_host'] as String?,
      partial: json['partial'] as bool? ?? false,
      warnings: warnings is List
          ? warnings.whereType<String>().toList(growable: false)
          : const [],
    );
  }
}

/// A page of the library.
class BookmarkPage {
  const BookmarkPage({
    required this.bookmarks,
    required this.total,
    required this.limit,
    required this.offset,
  });

  final List<Bookmark> bookmarks;
  final int total;
  final int limit;
  final int offset;

  /// Whether more bookmarks exist beyond this page.
  bool get hasMore => offset + bookmarks.length < total;

  factory BookmarkPage.fromJson(Map<String, Object?> json) {
    final items = json['bookmarks'];
    return BookmarkPage(
      bookmarks: items is List
          ? items
              .whereType<Map>()
              .map((item) => Bookmark.fromJson(item.cast<String, Object?>()))
              .toList(growable: false)
          : const [],
      total: json['total'] as int? ?? 0,
      limit: json['limit'] as int? ?? 0,
      offset: json['offset'] as int? ?? 0,
    );
  }
}

/// Reads a number that may arrive as an int or a double, which JSON does not
/// distinguish.
double? _toDouble(Object? raw) {
  if (raw is num) return raw.toDouble();
  if (raw is String) return double.tryParse(raw);
  return null;
}

DateTime? _toDate(Object? raw) {
  if (raw is! String) return null;
  return DateTime.tryParse(raw);
}

/// Replaces the last number in a string, used to build the optimistic chapter.
String? _replaceLastNumber(String text, String replacement) {
  final matches = RegExp(r'\d+(?:\.\d+)?').allMatches(text).toList();
  if (matches.isEmpty) return null;

  final last = matches.last;
  return text.replaceRange(last.start, last.end, replacement);
}
