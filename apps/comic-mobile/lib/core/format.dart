/// Small formatting helpers.
///
/// Relative times are written by hand rather than pulling in `intl`: the app
/// needs exactly two formats, and both are a dozen lines that read clearly.
library;

/// Renders an instant as "just now", "5 minutes ago", "3 days ago", or a date
/// once it is far enough in the past for a date to be more useful.
String formatRelativeTime(DateTime? instant, {DateTime? now}) {
  if (instant == null) return 'never';

  final reference = now ?? DateTime.now();
  final difference = reference.difference(instant.toLocal());

  // A clock skew between device and server can put an instant slightly in the
  // future; "just now" is better than "in -3 seconds".
  if (difference.isNegative || difference.inSeconds < 45) return 'just now';

  if (difference.inMinutes < 60) {
    final minutes = difference.inMinutes;
    return minutes == 1 ? '1 minute ago' : '$minutes minutes ago';
  }

  if (difference.inHours < 24) {
    final hours = difference.inHours;
    return hours == 1 ? '1 hour ago' : '$hours hours ago';
  }

  if (difference.inDays < 7) {
    final days = difference.inDays;
    return days == 1 ? 'yesterday' : '$days days ago';
  }

  return formatDate(instant);
}

/// Renders a date without a time, in the local calendar.
String formatDate(DateTime instant) {
  final local = instant.toLocal();
  const months = [
    'Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun',
    'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec',
  ];
  return '${months[local.month - 1]} ${local.day}, ${local.year}';
}

/// Renders a chapter number the way a site would.
///
/// Whole numbers lose their decimal part, because "1051" is what a reader
/// expects and "1051.0" looks like a mistake. Half-chapters keep it.
///
/// This mirrors the server's formatting deliberately: the text the reader sees
/// after a +1 must match what the server stored.
String formatChapterNumber(double? value) {
  if (value == null) return '';
  if (value == value.truncateToDouble() && value.abs() < 1e15) {
    return value.toInt().toString();
  }
  return value.toString();
}

/// A short label for a bookmark card: the chapter text when the site gave one,
/// otherwise the number, otherwise nothing.
String chapterLabel({required String chapter, required double? number}) {
  final text = chapter.trim();
  if (text.isNotEmpty) return text;

  final formatted = formatChapterNumber(number);
  return formatted.isEmpty ? '' : 'Chapter $formatted';
}
