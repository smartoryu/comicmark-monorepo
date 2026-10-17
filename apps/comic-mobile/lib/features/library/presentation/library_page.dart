import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:go_router/go_router.dart';
import 'package:url_launcher/url_launcher.dart';

import '../../../app/router.dart';
import '../../../core/api_error.dart';
import '../../../core/widgets.dart';
import '../../auth/application/auth_controller.dart';
import '../../bookmark_add/presentation/add_bookmark_sheet.dart';
import '../application/library_controller.dart';
import '../models/bookmark.dart';
import 'bookmark_card.dart';
import 'edit_chapter_sheet.dart';

/// The library: the screen the app opens on.
///
/// It is reachable without an account, so it has four states rather than two: a
/// reader who is not signed in sees what the app is for and a way in, rather than
/// an empty grid that looks broken.
class LibraryPage extends ConsumerStatefulWidget {
  const LibraryPage({super.key});

  @override
  ConsumerState<LibraryPage> createState() => _LibraryPageState();
}

class _LibraryPageState extends ConsumerState<LibraryPage> {
  /// The bookmark with an update in flight, so its card can show progress.
  String? _busyId;

  /// Whether the reader has dismissed the confirm-your-email reminder for this
  /// visit.
  bool _verificationNoticeDismissed = false;

  @override
  Widget build(BuildContext context) {
    final status = ref.watch(sessionStatusProvider);

    return Scaffold(
      appBar: AppBar(
        title: const Text('ComicMark'),
        actions: [
          if (status == SessionStatus.signedIn)
            IconButton(
              tooltip: 'Settings',
              icon: const Icon(Icons.settings_outlined),
              onPressed: () => context.push(Routes.settings),
            ),
        ],
      ),
      floatingActionButton: status == SessionStatus.signedIn
          ? FloatingActionButton.extended(
              onPressed: _openAddSheet,
              icon: const Icon(Icons.add),
              label: const Text('Add comic'),
            )
          : null,
      body: switch (status) {
        SessionStatus.checking => const LoadingView(label: 'Loading your library'),
        SessionStatus.signedOut => _signedOut(context),
        SessionStatus.signedIn => _library(context),
      },
    );
  }

  /// Shown when nobody is signed in.
  ///
  /// The app opens here by design, so this is a first impression rather than an
  /// error: it explains what ComicMark does and offers both ways in.
  Widget _signedOut(BuildContext context) {
    return EmptyState(
      icon: Icons.auto_stories_outlined,
      title: 'Your comic library, in one place',
      message: 'Sign in to save chapter links and pick up where you left off on '
          'any device.',
      action: Column(
        mainAxisSize: MainAxisSize.min,
        children: [
          FilledButton(
            onPressed: () => context.push(Routes.login),
            child: const Text('Sign in'),
          ),
          const SizedBox(height: 8),
          TextButton(
            onPressed: () => context.push(Routes.register),
            child: const Text('Create an account'),
          ),
        ],
      ),
    );
  }

  Widget _library(BuildContext context) {
    final library = ref.watch(libraryControllerProvider);

    return library.when(
      loading: () => const LoadingView(),
      error: (error, _) => _errorState(context, error),
      data: (bookmarks) => bookmarks.isEmpty ? _empty(context) : _grid(context, bookmarks),
    );
  }

  Widget _errorState(BuildContext context, Object error) {
    final message = error is ApiError ? error.detailSummary : 'Something went wrong.';

    return EmptyState(
      icon: Icons.cloud_off_outlined,
      title: 'Your library could not be loaded',
      message: message,
      action: FilledButton(
        onPressed: () => ref.read(libraryControllerProvider.notifier).reload(),
        child: const Text('Try again'),
      ),
    );
  }

  Widget _empty(BuildContext context) {
    return EmptyState(
      icon: Icons.bookmark_add_outlined,
      title: 'No bookmarks yet',
      message: 'Paste a chapter URL and ComicMark will fill in the cover and '
          'chapter for you.',
      action: FilledButton.icon(
        onPressed: _openAddSheet,
        icon: const Icon(Icons.add),
        label: const Text('Add your first comic'),
      ),
    );
  }

  Widget _grid(BuildContext context, List<Bookmark> bookmarks) {
    final needsVerification =
        ref.watch(authControllerProvider).value?.emailVerified == false;

    return Column(
      children: [
        if (needsVerification && !_verificationNoticeDismissed)
          Padding(
            padding: const EdgeInsets.fromLTRB(12, 12, 12, 0),
            child: _verificationNotice(context),
          ),
        Expanded(child: _bookmarkGrid(context, bookmarks)),
      ],
    );
  }

  /// The reminder that the address is unconfirmed.
  ///
  /// Dismissible rather than blocking: with verification not enforced the reader
  /// can use the app fully, so this is a nudge rather than a gate.
  Widget _verificationNotice(BuildContext context) {
    final email = ref.watch(authControllerProvider).value?.email ?? '';

    return Material(
      color: Theme.of(context).colorScheme.secondaryContainer,
      borderRadius: BorderRadius.circular(12),
      child: Padding(
        padding: const EdgeInsets.fromLTRB(12, 8, 4, 8),
        child: Row(
          children: [
            Icon(
              Icons.mark_email_unread_outlined,
              size: 20,
              color: Theme.of(context).colorScheme.onSecondaryContainer,
            ),
            const SizedBox(width: 10),
            Expanded(
              child: Text(
                'Confirm your email address to secure your account.',
                style: Theme.of(context).textTheme.bodySmall?.copyWith(
                      color: Theme.of(context).colorScheme.onSecondaryContainer,
                    ),
              ),
            ),
            TextButton(
              onPressed: () => context.go(
                '${Routes.verifyEmail}?email=${Uri.encodeComponent(email)}',
              ),
              child: const Text('Confirm'),
            ),
            IconButton(
              tooltip: 'Dismiss',
              iconSize: 18,
              visualDensity: VisualDensity.compact,
              color: Theme.of(context).colorScheme.onSecondaryContainer,
              icon: const Icon(Icons.close),
              onPressed: () => setState(() => _verificationNoticeDismissed = true),
            ),
          ],
        ),
      ),
    );
  }

  Widget _bookmarkGrid(BuildContext context, List<Bookmark> bookmarks) {
    return RefreshIndicator(
      onRefresh: () => ref.read(libraryControllerProvider.notifier).reload(),
      child: LayoutBuilder(
        builder: (context, constraints) {
          // A phone shows two columns; a browser window has room for more, and a
          // grid that stops growing at two looks broken on a desktop.
          final columns = switch (constraints.maxWidth) {
            < 520 => 2,
            < 840 => 3,
            < 1200 => 4,
            _ => 5,
          };
          final ratio = columns <= 2 ? 0.62 : 0.68;

          return GridView.builder(
            padding: const EdgeInsets.fromLTRB(12, 12, 12, 96),
            physics: const AlwaysScrollableScrollPhysics(),
            gridDelegate: SliverGridDelegateWithFixedCrossAxisCount(
              crossAxisCount: columns,
              childAspectRatio: ratio,
              crossAxisSpacing: 12,
              mainAxisSpacing: 12,
            ),
            itemCount: bookmarks.length,
            itemBuilder: (context, index) {
              final bookmark = bookmarks[index];
              return BookmarkCard(
                bookmark: bookmark,
                busy: _busyId == bookmark.id,
                onOpen: () => _openReader(bookmark),
                onAdvance: () => _advance(bookmark),
                onShowActions: () => _showActions(bookmark),
              );
            },
          );
        },
      ),
    );
  }

  // --- actions ---------------------------------------------------------------

  /// Opens the chapter in the reader, and records the visit.
  Future<void> _openReader(Bookmark bookmark) async {
    final uri = Uri.tryParse(bookmark.currentUrl);
    if (uri == null) return;

    try {
      final opened = await launchUrl(uri, mode: LaunchMode.externalApplication);
      if (!opened) {
        _tell('That link could not be opened.');
        return;
      }
    } catch (_) {
      _tell('That link could not be opened.');
      return;
    }

    // Fire-and-forget: the reader is already looking at the comic, so a failed
    // bookkeeping write must not interrupt them.
    await ref.read(libraryControllerProvider.notifier).markRead(bookmark.id);
  }

  /// Advances the chapter, or asks for it when there is no number.
  Future<void> _advance(Bookmark bookmark) async {
    setState(() => _busyId = bookmark.id);

    try {
      await ref.read(libraryControllerProvider.notifier).readNext(bookmark.id);
    } on ApiError catch (error) {
      if (!mounted) return;

      // The API's 409 is the signal to ask rather than guess, so the sheet opens
      // prefilled instead of an error appearing.
      if (error.code == ApiErrorCode.chapterNotNumeric) {
        final saved = await EditChapterSheet.show(context, bookmark);
        if (saved) _tell('Chapter updated.');
      } else {
        _tell(error.detailSummary);
      }
    } finally {
      if (mounted) setState(() => _busyId = null);
    }
  }

  Future<void> _showActions(Bookmark bookmark) async {
    final action = await showModalBottomSheet<_BookmarkAction>(
      context: context,
      builder: (context) => SafeArea(
        child: Column(
          mainAxisSize: MainAxisSize.min,
          children: [
            ListTile(
              leading: const Icon(Icons.edit_outlined),
              title: const Text('Edit chapter'),
              onTap: () => Navigator.of(context).pop(const _EditChapterAction()),
            ),
            for (final status in BookmarkStatus.values)
              if (status != bookmark.status)
                ListTile(
                  leading: Icon(_statusIcon(status)),
                  title: Text('Mark as ${status.label.toLowerCase()}'),
                  onTap: () => Navigator.of(context).pop(
                    _BookmarkStatusAction(status),
                  ),
                ),
            const Divider(height: 1),
            ListTile(
              leading: Icon(
                Icons.delete_outline,
                color: Theme.of(context).colorScheme.error,
              ),
              title: Text(
                'Remove',
                style: TextStyle(color: Theme.of(context).colorScheme.error),
              ),
              onTap: () => Navigator.of(context).pop(const _DeleteAction()),
            ),
          ],
        ),
      ),
    );

    if (!mounted || action == null) return;

    switch (action) {
      case _EditChapterAction():
        await EditChapterSheet.show(context, bookmark);
      case _DeleteAction():
        await _confirmDelete(bookmark);
      case _BookmarkStatusAction(:final status):
        try {
          await ref
              .read(libraryControllerProvider.notifier)
              .setStatus(bookmark.id, status);
        } on ApiError catch (error) {
          _tell(error.detailSummary);
        }
    }
  }

  Future<void> _confirmDelete(Bookmark bookmark) async {
    final confirmed = await showDialog<bool>(
      context: context,
      builder: (context) => AlertDialog(
        title: const Text('Remove this bookmark?'),
        content: Text('"${bookmark.title}" will be removed from your library.'),
        actions: [
          TextButton(
            onPressed: () => Navigator.of(context).pop(false),
            child: const Text('Keep it'),
          ),
          FilledButton(
            onPressed: () => Navigator.of(context).pop(true),
            child: const Text('Remove'),
          ),
        ],
      ),
    );

    if (confirmed != true || !mounted) return;

    try {
      await ref.read(libraryControllerProvider.notifier).remove(bookmark.id);
    } on ApiError catch (error) {
      _tell(error.detailSummary);
    }
  }

  Future<void> _openAddSheet() async {
    final added = await AddBookmarkSheet.show(context);
    if (added && mounted) _tell('Added to your library.');
  }

  void _tell(String message) {
    if (!mounted) return;
    ScaffoldMessenger.of(context)
      ..hideCurrentSnackBar()
      ..showSnackBar(SnackBar(content: Text(message)));
  }

  IconData _statusIcon(BookmarkStatus status) => switch (status) {
        BookmarkStatus.reading => Icons.auto_stories_outlined,
        BookmarkStatus.paused => Icons.pause_circle_outline,
        BookmarkStatus.completed => Icons.check_circle_outline,
        BookmarkStatus.dropped => Icons.remove_circle_outline,
      };
}

/// What the actions sheet can return.
sealed class _BookmarkAction {
  const _BookmarkAction();
}

final class _EditChapterAction extends _BookmarkAction {
  const _EditChapterAction();
}

final class _DeleteAction extends _BookmarkAction {
  const _DeleteAction();
}

final class _BookmarkStatusAction extends _BookmarkAction {
  const _BookmarkStatusAction(this.status);

  final BookmarkStatus status;
}
