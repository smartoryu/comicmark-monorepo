import 'package:cached_network_image/cached_network_image.dart';
import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:shimmer/shimmer.dart';

import '../../../core/api_error.dart';
import '../../../core/widgets.dart';
import '../../library/models/bookmark.dart';
import '../application/preview_controller.dart';

/// Adds a comic by pasting a chapter URL.
///
/// The flow is: paste, preview, adjust, save. Every field stays editable, because
/// the API reports a site that blocks the bot as a partial preview rather than an
/// error: the reader can always type what the page did not publish.
class AddBookmarkSheet extends ConsumerStatefulWidget {
  const AddBookmarkSheet({super.key});

  /// Shows the sheet and reports whether a bookmark was saved.
  static Future<bool> show(BuildContext context) async {
    final added = await showModalBottomSheet<bool>(
      context: context,
      isScrollControlled: true,
      useSafeArea: true,
      builder: (_) => const AddBookmarkSheet(),
    );
    return added ?? false;
  }

  @override
  ConsumerState<AddBookmarkSheet> createState() => _AddBookmarkSheetState();
}

class _AddBookmarkSheetState extends ConsumerState<AddBookmarkSheet> {
  final _url = TextEditingController();
  final _title = TextEditingController();
  final _chapter = TextEditingController();
  final _number = TextEditingController();
  final _coverUrl = TextEditingController();

  bool _saving = false;
  ApiError? _saveError;
  String? _fieldError;

  @override
  void dispose() {
    _url.dispose();
    _title.dispose();
    _chapter.dispose();
    _number.dispose();
    _coverUrl.dispose();
    super.dispose();
  }

  /// Reads the clipboard and previews whatever is there.
  Future<void> _pasteFromClipboard() async {
    final data = await Clipboard.getData(Clipboard.kTextPlain);
    final text = data?.text?.trim();

    if (text == null || text.isEmpty) {
      setState(() => _fieldError = 'There is nothing to paste.');
      return;
    }

    _url.text = text;
    await _preview(text);
  }

  Future<void> _preview(String url) async {
    setState(() => _fieldError = null);
    if (url.trim().isEmpty) {
      setState(() => _fieldError = 'Paste a chapter link first.');
      return;
    }

    await ref.read(previewControllerProvider.notifier).preview_(url.trim());

    final preview = ref.read(previewControllerProvider).preview;
    if (preview != null) _seedFields(preview);
  }

  /// Fills the editable fields from a preview, without overwriting edits.
  void _seedFields(BookmarkPreview preview) {
    _title.text = preview.title;
    _chapter.text = preview.chapter;
    _coverUrl.text = preview.coverUrl ?? '';

    final number = preview.chapterNumber;
    _number.text = number == null
        ? ''
        : (number == number.truncateToDouble()
            ? number.toInt().toString()
            : number.toString());
  }

  Future<void> _save() async {
    final url = _url.text.trim();
    if (url.isEmpty) {
      setState(() => _fieldError = 'Paste a chapter link first.');
      return;
    }

    final title = _title.text.trim();
    if (title.isEmpty) {
      setState(() => _fieldError = 'Give this bookmark a title.');
      return;
    }

    final rawNumber = _number.text.trim();
    final number = rawNumber.isEmpty ? null : double.tryParse(rawNumber);
    if (rawNumber.isNotEmpty && number == null) {
      setState(() => _fieldError = 'The chapter number must be a number, or left blank.');
      return;
    }

    setState(() {
      _saving = true;
      _saveError = null;
      _fieldError = null;
    });

    try {
      await ref.read(previewControllerProvider.notifier).save(
            title: title,
            url: url,
            chapter: _chapter.text.trim(),
            chapterNumber: number,
            coverUrl: _coverUrl.text.trim().isEmpty ? null : _coverUrl.text.trim(),
          );
      if (mounted) Navigator.of(context).pop(true);
    } on ApiError catch (error) {
      if (!mounted) return;

      // A duplicate is worth naming exactly, because the reader's next step is
      // different from any other failure: the bookmark already exists.
      setState(() => _saveError = error);
    } finally {
      if (mounted) setState(() => _saving = false);
    }
  }

  @override
  Widget build(BuildContext context) {
    final state = ref.watch(previewControllerProvider);
    final theme = Theme.of(context);

    return DraggableScrollableSheet(
      expand: false,
      initialChildSize: 0.85,
      minChildSize: 0.5,
      maxChildSize: 0.95,
      builder: (context, scrollController) {
        return Padding(
          padding: EdgeInsets.only(bottom: MediaQuery.of(context).viewInsets.bottom),
          child: ListView(
            controller: scrollController,
            padding: const EdgeInsets.fromLTRB(20, 20, 20, 24),
            children: [
              Row(
                children: [
                  Text('Add a comic', style: theme.textTheme.titleLarge),
                  const Spacer(),
                  IconButton(
                    onPressed: _saving ? null : () => Navigator.of(context).pop(false),
                    icon: const Icon(Icons.close),
                    tooltip: 'Close',
                  ),
                ],
              ),
              const SizedBox(height: 4),
              Text(
                'Paste a chapter link and ComicMark fills in the rest.',
                style: theme.textTheme.bodySmall
                    ?.copyWith(color: theme.colorScheme.onSurfaceVariant),
              ),
              const SizedBox(height: 20),

              if (_fieldError != null) ...[
                ErrorBanner(message: _fieldError!, onDismiss: () => setState(() => _fieldError = null)),
                const SizedBox(height: 12),
              ],
              if (state.error != null) ...[
                ErrorBanner.fromError(state.error!),
                const SizedBox(height: 12),
              ],
              if (_saveError != null) ...[
                ErrorBanner.fromError(_saveError!, onDismiss: () => setState(() => _saveError = null)),
                const SizedBox(height: 12),
              ],

              TextField(
                controller: _url,
                enabled: !_saving,
                keyboardType: TextInputType.url,
                autocorrect: false,
                decoration: InputDecoration(
                  labelText: 'Chapter link',
                  hintText: 'https://example.com/one-piece-chapter-1050',
                  prefixIcon: const Icon(Icons.link),
                  suffixIcon: IconButton(
                    tooltip: 'Paste from clipboard',
                    icon: const Icon(Icons.content_paste),
                    onPressed: _saving ? null : _pasteFromClipboard,
                  ),
                ),
                onSubmitted: (value) => _preview(value),
              ),
              const SizedBox(height: 12),
              Row(
                children: [
                  Expanded(
                    child: OutlinedButton.icon(
                      onPressed: _saving ? null : () => _preview(_url.text),
                      icon: const Icon(Icons.search, size: 18),
                      label: const Text('Preview'),
                    ),
                  ),
                ],
              ),
              const SizedBox(height: 20),

              if (state.loading) const _PreviewSkeleton(),
              if (state.hasPreview) _previewFields(state.preview!),

              const SizedBox(height: 24),
              AsyncButton(
                onPressed: _save,
                busy: _saving,
                label: 'Save to library',
              ),
            ],
          ),
        );
      },
    );
  }

  /// The editable result of a preview.
  Widget _previewFields(BookmarkPreview preview) {
    final theme = Theme.of(context);

    return Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: [
        Row(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            _CoverThumb(url: preview.coverUrl),
            const SizedBox(width: 12),
            Expanded(
              child: Column(
                crossAxisAlignment: CrossAxisAlignment.start,
                children: [
                  if (preview.siteName != null)
                    Text(
                      preview.siteName!,
                      style: theme.textTheme.labelMedium
                          ?.copyWith(color: theme.colorScheme.onSurfaceVariant),
                    ),
                  TextField(
                    controller: _title,
                    enabled: !_saving,
                    textCapitalization: TextCapitalization.words,
                    decoration: const InputDecoration(labelText: 'Title'),
                  ),
                ],
              ),
            ),
          ],
        ),
        const SizedBox(height: 12),

        // A partial preview is the API saying the page did not publish
        // everything, so the reader is told plainly rather than left wondering
        // why the fields are empty.
        if (preview.partial) ...[
          NoticeBanner(
            icon: Icons.edit_note,
            message: preview.warnings.isEmpty
                ? 'Some details could not be read from that page. Fill them in below.'
                : 'Some details could not be read: ${preview.warnings.join(", ")}.',
          ),
          const SizedBox(height: 12),
        ],

        Row(
          children: [
            Expanded(
              child: TextField(
                controller: _chapter,
                enabled: !_saving,
                decoration: const InputDecoration(labelText: 'Chapter'),
              ),
            ),
            const SizedBox(width: 12),
            SizedBox(
              width: 130,
              child: TextField(
                controller: _number,
                enabled: !_saving,
                keyboardType: const TextInputType.numberWithOptions(decimal: true),
                decoration: const InputDecoration(
                  labelText: 'Number',
                  hintText: '1050',
                ),
              ),
            ),
          ],
        ),
        const SizedBox(height: 6),
        Text(
          'The number powers the +1 button. Leave it blank if the chapter has none.',
          style: theme.textTheme.bodySmall
              ?.copyWith(color: theme.colorScheme.onSurfaceVariant),
        ),
        const SizedBox(height: 12),
        TextField(
          controller: _coverUrl,
          enabled: !_saving,
          keyboardType: TextInputType.url,
          autocorrect: false,
          decoration: const InputDecoration(labelText: 'Cover image link'),
        ),
      ],
    );
  }
}

/// The cover thumbnail beside the title.
class _CoverThumb extends StatelessWidget {
  const _CoverThumb({this.url});

  final String? url;

  @override
  Widget build(BuildContext context) {
    final theme = Theme.of(context);
    final placeholder = Container(
      color: theme.colorScheme.surfaceContainerHighest,
      alignment: Alignment.center,
      child: Icon(Icons.menu_book_outlined, color: theme.colorScheme.outline),
    );

    return ClipRRect(
      borderRadius: BorderRadius.circular(8),
      child: SizedBox(
        width: 64,
        height: 88,
        child: url == null || url!.isEmpty
            ? placeholder
            : CachedNetworkImage(
                imageUrl: url!,
                fit: BoxFit.cover,
                placeholder: (context, _) => placeholder,
                errorWidget: (context, _, _) => placeholder,
              ),
      ),
    );
  }
}

/// Placeholder content while a preview is fetched.
///
/// A skeleton rather than a spinner: it shows the shape of what is coming, which
/// makes the wait feel shorter than an empty box.
class _PreviewSkeleton extends StatelessWidget {
  const _PreviewSkeleton();

  @override
  Widget build(BuildContext context) {
    final theme = Theme.of(context);

    Widget bar(double widthFactor, double height) {
      return FractionallySizedBox(
        alignment: Alignment.centerLeft,
        widthFactor: widthFactor,
        child: Container(
          height: height,
          decoration: BoxDecoration(
            color: theme.colorScheme.surfaceContainerHighest,
            borderRadius: BorderRadius.circular(6),
          ),
        ),
      );
    }

    return Shimmer.fromColors(
      baseColor: theme.colorScheme.surfaceContainerHighest,
      highlightColor: theme.colorScheme.surfaceContainerLow,
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Row(
            crossAxisAlignment: CrossAxisAlignment.start,
            children: [
              Container(
                width: 64,
                height: 88,
                decoration: BoxDecoration(
                  color: theme.colorScheme.surfaceContainerHighest,
                  borderRadius: BorderRadius.circular(8),
                ),
              ),
              const SizedBox(width: 12),
              Expanded(
                child: Column(
                  children: [
                    bar(0.8, 16),
                    const SizedBox(height: 10),
                    bar(0.5, 16),
                  ],
                ),
              ),
            ],
          ),
          const SizedBox(height: 20),
          bar(1, 16),
          const SizedBox(height: 10),
          bar(0.6, 16),
        ],
      ),
    );
  }
}
