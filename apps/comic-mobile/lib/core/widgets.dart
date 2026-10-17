import 'package:flutter/material.dart';

import 'api_error.dart';

/// Shared presentation pieces.
///
/// The screens repeat the same few patterns — a button that shows progress, an
/// error line, an empty state — so they live here rather than being re-invented
/// per screen with slightly different words each time.

/// A filled button that shows a spinner while an action is in flight.
///
/// While busy it is disabled, which is what stops a double tap from submitting a
/// form twice.
class AsyncButton extends StatelessWidget {
  const AsyncButton({
    super.key,
    required this.onPressed,
    required this.label,
    this.busy = false,
    this.icon,
  });

  final VoidCallback? onPressed;
  final String label;
  final bool busy;
  final IconData? icon;

  @override
  Widget build(BuildContext context) {
    final child = busy
        ? const SizedBox(
            height: 20,
            width: 20,
            child: CircularProgressIndicator(strokeWidth: 2),
          )
        : Text(label);

    return FilledButton(
      onPressed: busy ? null : onPressed,
      child: icon != null && !busy
          ? Row(
              mainAxisAlignment: MainAxisAlignment.center,
              children: [Icon(icon, size: 20), const SizedBox(width: 8), child],
            )
          : child,
    );
  }
}

/// A dismissible message above a form, used for the failure of a submit.
class ErrorBanner extends StatelessWidget {
  const ErrorBanner({super.key, required this.message, this.onDismiss});

  /// Builds a banner from an [ApiError], preferring its field detail when the
  /// API gave one: "Use at least 8 characters." is more useful than "Please
  /// check the highlighted fields.".
  factory ErrorBanner.fromError(ApiError error, {VoidCallback? onDismiss}) {
    return ErrorBanner(message: error.detailSummary, onDismiss: onDismiss);
  }

  final String message;
  final VoidCallback? onDismiss;

  @override
  Widget build(BuildContext context) {
    final scheme = Theme.of(context).colorScheme;

    return Container(
      width: double.infinity,
      padding: const EdgeInsets.all(12),
      decoration: BoxDecoration(
        color: scheme.errorContainer,
        borderRadius: BorderRadius.circular(12),
      ),
      child: Row(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Icon(Icons.error_outline, size: 20, color: scheme.onErrorContainer),
          const SizedBox(width: 10),
          Expanded(
            child: Text(
              message,
              style: TextStyle(color: scheme.onErrorContainer),
            ),
          ),
          if (onDismiss != null)
            IconButton(
              onPressed: onDismiss,
              icon: const Icon(Icons.close, size: 18),
              color: scheme.onErrorContainer,
              tooltip: 'Dismiss',
              visualDensity: VisualDensity.compact,
            ),
        ],
      ),
    );
  }
}

/// A quiet confirmation, used for "we sent you a link" style outcomes.
class NoticeBanner extends StatelessWidget {
  const NoticeBanner({super.key, required this.message, this.icon = Icons.info_outline});

  final String message;
  final IconData icon;

  @override
  Widget build(BuildContext context) {
    final scheme = Theme.of(context).colorScheme;

    return Container(
      width: double.infinity,
      padding: const EdgeInsets.all(12),
      decoration: BoxDecoration(
        color: scheme.secondaryContainer,
        borderRadius: BorderRadius.circular(12),
      ),
      child: Row(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Icon(icon, size: 20, color: scheme.onSecondaryContainer),
          const SizedBox(width: 10),
          Expanded(
            child: Text(
              message,
              style: TextStyle(color: scheme.onSecondaryContainer),
            ),
          ),
        ],
      ),
    );
  }
}

/// A centred message with an optional action, for a screen with nothing to show.
class EmptyState extends StatelessWidget {
  const EmptyState({
    super.key,
    required this.icon,
    required this.title,
    this.message,
    this.action,
  });

  final IconData icon;
  final String title;
  final String? message;
  final Widget? action;

  @override
  Widget build(BuildContext context) {
    final theme = Theme.of(context);

    return Center(
      child: Padding(
        padding: const EdgeInsets.all(32),
        child: Column(
          mainAxisSize: MainAxisSize.min,
          children: [
            Icon(icon, size: 56, color: theme.colorScheme.outline),
            const SizedBox(height: 16),
            Text(title, style: theme.textTheme.titleMedium, textAlign: TextAlign.center),
            if (message != null) ...[
              const SizedBox(height: 8),
              Text(
                message!,
                style: theme.textTheme.bodyMedium
                    ?.copyWith(color: theme.colorScheme.onSurfaceVariant),
                textAlign: TextAlign.center,
              ),
            ],
            if (action != null) ...[
              const SizedBox(height: 24),
              action!,
            ],
          ],
        ),
      ),
    );
  }
}

/// A full-screen spinner with a label, for a screen that is loading.
class LoadingView extends StatelessWidget {
  const LoadingView({super.key, this.label});

  final String? label;

  @override
  Widget build(BuildContext context) {
    return Center(
      child: Column(
        mainAxisSize: MainAxisSize.min,
        children: [
          const CircularProgressIndicator(),
          if (label != null) ...[
            const SizedBox(height: 16),
            Text(label!, style: Theme.of(context).textTheme.bodyMedium),
          ],
        ],
      ),
    );
  }
}

/// A small platform glyph for the sessions list.
IconData platformIcon(String platform) {
  return switch (platform) {
    'android' => Icons.android,
    'ios' => Icons.phone_iphone,
    'web' => Icons.language,
    _ => Icons.devices_other,
  };
}

/// A label for a device platform.
String platformLabel(String platform) {
  return switch (platform) {
    'android' => 'Android',
    'ios' => 'iOS',
    'web' => 'Web browser',
    _ => 'Unknown device',
  };
}
