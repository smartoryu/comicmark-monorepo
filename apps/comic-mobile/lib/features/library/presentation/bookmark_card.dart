import 'package:cached_network_image/cached_network_image.dart';
import 'package:flutter/material.dart';
import 'package:shimmer/shimmer.dart';

import '../models/bookmark.dart';

/// One bookmark in the library.
///
/// The card carries the three things a reader needs at a glance — the cover, the
/// comic, and the chapter they are up to — plus the one action they perform most:
/// advancing the chapter.
class BookmarkCard extends StatelessWidget {
  const BookmarkCard({
    super.key,
    required this.bookmark,
    required this.onOpen,
    required this.onAdvance,
    required this.onShowActions,
    this.busy = false,
  });

  final Bookmark bookmark;

  /// Opens the chapter in the reader.
  final VoidCallback onOpen;

  /// Advances the chapter by one.
  final VoidCallback onAdvance;

  /// Opens the actions menu.
  final VoidCallback onShowActions;

  /// Whether an update for this card is in flight.
  final bool busy;

  @override
  Widget build(BuildContext context) {
    final theme = Theme.of(context);

    return Card(
      margin: EdgeInsets.zero,
      child: InkWell(
        onTap: onOpen,
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.stretch,
          children: [
            Expanded(child: _Cover(bookmark: bookmark)),
            Padding(
              padding: const EdgeInsets.fromLTRB(10, 8, 4, 4),
              child: Column(
                crossAxisAlignment: CrossAxisAlignment.start,
                mainAxisSize: MainAxisSize.min,
                children: [
                  Text(
                    bookmark.title,
                    maxLines: 1,
                    overflow: TextOverflow.ellipsis,
                    style: theme.textTheme.titleSmall,
                  ),
                  const SizedBox(height: 2),
                  Text(
                    bookmark.sourceHost ?? '',
                    maxLines: 1,
                    overflow: TextOverflow.ellipsis,
                    style: theme.textTheme.bodySmall
                        ?.copyWith(color: theme.colorScheme.onSurfaceVariant),
                  ),
                  const SizedBox(height: 6),
                  Row(
                    children: [
                      Flexible(
                        child: _ChapterChip(bookmark: bookmark),
                      ),
                      const Spacer(),
                      // The +1 action. Disabled while busy so a double tap cannot
                      // advance two chapters.
                      IconButton(
                        onPressed: busy ? null : onAdvance,
                        icon: busy
                            ? const SizedBox(
                                height: 16,
                                width: 16,
                                child: CircularProgressIndicator(strokeWidth: 2),
                              )
                            : const Icon(Icons.add_circle_outline),
                        tooltip: bookmark.canAdvance
                            ? 'Next chapter'
                            : 'Set the next chapter',
                        visualDensity: VisualDensity.compact,
                        iconSize: 20,
                      ),
                      IconButton(
                        onPressed: onShowActions,
                        icon: const Icon(Icons.more_vert),
                        tooltip: 'More actions',
                        visualDensity: VisualDensity.compact,
                        iconSize: 20,
                      ),
                    ],
                  ),
                ],
              ),
            ),
          ],
        ),
      ),
    );
  }
}

/// The cover, with a placeholder while it loads and a fallback when there is
/// none.
class _Cover extends StatelessWidget {
  const _Cover({required this.bookmark});

  final Bookmark bookmark;

  @override
  Widget build(BuildContext context) {
    final theme = Theme.of(context);
    final url = bookmark.coverUrl;

    if (url == null || url.isEmpty) return _placeholder(theme, withIcon: true);

    return CachedNetworkImage(
      imageUrl: url,
      fit: BoxFit.cover,
      // A shimmer rather than a spinner: covers arrive at different speeds, and a
      // grid of spinners is noisier than a grid of shimmering placeholders.
      placeholder: (context, _) => _shimmer(theme),
      errorWidget: (context, _, _) => _placeholder(theme, withIcon: true),
      fadeInDuration: const Duration(milliseconds: 150),
    );
  }

  Widget _shimmer(ThemeData theme) {
    return Shimmer.fromColors(
      baseColor: theme.colorScheme.surfaceContainerHighest,
      highlightColor: theme.colorScheme.surfaceContainerLow,
      child: Container(color: theme.colorScheme.surfaceContainerHighest),
    );
  }

  Widget _placeholder(ThemeData theme, {bool withIcon = false}) {
    return Container(
      color: theme.colorScheme.surfaceContainerHighest,
      alignment: Alignment.center,
      child: withIcon
          ? Icon(
              Icons.menu_book_outlined,
              size: 32,
              color: theme.colorScheme.outline,
            )
          : null,
    );
  }
}

/// The chapter, and the status when it is not simply "reading".
class _ChapterChip extends StatelessWidget {
  const _ChapterChip({required this.bookmark});

  final Bookmark bookmark;

  @override
  Widget build(BuildContext context) {
    final theme = Theme.of(context);

    return Row(
      mainAxisSize: MainAxisSize.min,
      children: [
        Flexible(
          child: Text(
            bookmark.chapterLabel,
            maxLines: 1,
            overflow: TextOverflow.ellipsis,
            style: theme.textTheme.labelMedium?.copyWith(
              color: theme.colorScheme.primary,
              fontWeight: FontWeight.w600,
            ),
          ),
        ),
        if (bookmark.status != BookmarkStatus.reading) ...[
          const SizedBox(width: 6),
          Container(
            padding: const EdgeInsets.symmetric(horizontal: 6, vertical: 2),
            decoration: BoxDecoration(
              color: theme.colorScheme.secondaryContainer,
              borderRadius: BorderRadius.circular(6),
            ),
            child: Text(
              bookmark.status.label,
              style: theme.textTheme.labelSmall
                  ?.copyWith(color: theme.colorScheme.onSecondaryContainer),
            ),
          ),
        ],
      ],
    );
  }
}
