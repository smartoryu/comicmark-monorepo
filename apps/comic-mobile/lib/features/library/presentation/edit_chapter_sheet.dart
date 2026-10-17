import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';

import '../../../core/api_error.dart';
import '../../../core/widgets.dart';
import '../application/library_controller.dart';
import '../models/bookmark.dart';

/// Asks the reader for the chapter.
///
/// Shown in two situations, which are the same problem from the reader's side:
///
///  * the +1 button was tapped on a chapter that has no number, so there is
///    nothing to add one to
///  * the reader chose to correct a chapter
///
/// Leaving the number blank is allowed and meaningful: it records "this chapter
/// has no number", which keeps the +1 button in its edit state instead of
/// guessing.
class EditChapterSheet extends ConsumerStatefulWidget {
  const EditChapterSheet({super.key, required this.bookmark});

  final Bookmark bookmark;

  /// Shows the sheet and returns true when a change was saved.
  static Future<bool> show(BuildContext context, Bookmark bookmark) async {
    final saved = await showModalBottomSheet<bool>(
      context: context,
      isScrollControlled: true,
      builder: (_) => EditChapterSheet(bookmark: bookmark),
    );
    return saved ?? false;
  }

  @override
  ConsumerState<EditChapterSheet> createState() => _EditChapterSheetState();
}

class _EditChapterSheetState extends ConsumerState<EditChapterSheet> {
  late final TextEditingController _chapter;
  late final TextEditingController _number;

  bool _busy = false;
  ApiError? _error;

  @override
  void initState() {
    super.initState();
    _chapter = TextEditingController(text: widget.bookmark.chapter);

    final number = widget.bookmark.chapterNumber;
    _number = TextEditingController(
      text: number == null
          ? ''
          : (number == number.truncateToDouble()
              ? number.toInt().toString()
              : number.toString()),
    );
  }

  @override
  void dispose() {
    _chapter.dispose();
    _number.dispose();
    super.dispose();
  }

  Future<void> _save() async {
    setState(() {
      _busy = true;
      _error = null;
    });

    final rawNumber = _number.text.trim();
    final number = rawNumber.isEmpty ? null : double.tryParse(rawNumber);

    // A number that will not parse is worth saying so rather than silently
    // storing no number, which would look like the app lost it.
    if (rawNumber.isNotEmpty && number == null) {
      setState(() {
        _busy = false;
        _error = const ApiError(
          code: ApiErrorCode.validationFailed,
          message: 'The chapter number must be a number, or left blank.',
        );
      });
      return;
    }

    try {
      await ref.read(libraryControllerProvider.notifier).editChapter(
            widget.bookmark.id,
            chapter: _chapter.text.trim(),
            chapterNumber: number,
          );
      if (mounted) Navigator.of(context).pop(true);
    } on ApiError catch (error) {
      if (mounted) setState(() => _error = error);
    } finally {
      if (mounted) setState(() => _busy = false);
    }
  }

  @override
  Widget build(BuildContext context) {
    final theme = Theme.of(context);
    final media = MediaQuery.of(context);

    return Padding(
      // Lifts the sheet above the keyboard, which otherwise covers the fields.
      padding: EdgeInsets.only(bottom: media.viewInsets.bottom),
      child: SafeArea(
        child: Padding(
          padding: const EdgeInsets.fromLTRB(20, 20, 20, 20),
          child: Column(
            mainAxisSize: MainAxisSize.min,
            crossAxisAlignment: CrossAxisAlignment.stretch,
            children: [
              Text('Chapter', style: theme.textTheme.titleMedium),
              const SizedBox(height: 4),
              Text(
                widget.bookmark.title,
                style: theme.textTheme.bodySmall
                    ?.copyWith(color: theme.colorScheme.onSurfaceVariant),
              ),
              const SizedBox(height: 16),
              if (_error != null) ...[
                ErrorBanner.fromError(_error!, onDismiss: () => setState(() => _error = null)),
                const SizedBox(height: 16),
              ],
              TextField(
                controller: _chapter,
                enabled: !_busy,
                autofocus: true,
                textInputAction: TextInputAction.next,
                decoration: const InputDecoration(
                  labelText: 'Chapter',
                  hintText: 'Chapter 1051',
                ),
              ),
              const SizedBox(height: 12),
              TextField(
                controller: _number,
                enabled: !_busy,
                keyboardType: const TextInputType.numberWithOptions(decimal: true),
                textInputAction: TextInputAction.done,
                decoration: const InputDecoration(
                  labelText: 'Chapter number',
                  hintText: '1051',
                  helperText: 'Leave blank if this chapter has no number.',
                ),
                onSubmitted: (_) => _save(),
              ),
              const SizedBox(height: 20),
              AsyncButton(
                onPressed: _save,
                busy: _busy,
                label: 'Save chapter',
              ),
            ],
          ),
        ),
      ),
    );
  }
}
