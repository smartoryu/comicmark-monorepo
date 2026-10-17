import 'package:flutter/material.dart';

/// The shell every account screen shares.
///
/// One layout means the sign-in, registration, verification and reset screens all
/// look like the same app, and it keeps the forms themselves short enough to read.
class AuthScaffold extends StatelessWidget {
  const AuthScaffold({
    super.key,
    required this.title,
    required this.child,
    this.subtitle,
    this.showBack = true,
  });

  final String title;
  final String? subtitle;
  final Widget child;

  /// Whether to offer a way back. Hidden on the sign-in screen when it is the
  /// destination, so a reader is not offered a back button into a screen they
  /// cannot reach.
  final bool showBack;

  @override
  Widget build(BuildContext context) {
    final theme = Theme.of(context);

    return Scaffold(
      appBar: showBack && Navigator.of(context).canPop()
          ? AppBar(leading: BackButton(onPressed: () => Navigator.of(context).maybePop()))
          : null,
      body: SafeArea(
        child: Center(
          child: SingleChildScrollView(
            padding: const EdgeInsets.symmetric(horizontal: 24, vertical: 32),
            child: ConstrainedBox(
              // A form stretched across a desktop browser window is hard to read.
              constraints: const BoxConstraints(maxWidth: 420),
              child: Column(
                crossAxisAlignment: CrossAxisAlignment.stretch,
                children: [
                  Row(
                    children: [
                      Icon(Icons.auto_stories_outlined, color: theme.colorScheme.primary),
                      const SizedBox(width: 8),
                      Text(
                        'ComicMark',
                        style: theme.textTheme.titleMedium?.copyWith(
                          color: theme.colorScheme.primary,
                          fontWeight: FontWeight.w600,
                        ),
                      ),
                    ],
                  ),
                  const SizedBox(height: 32),
                  Text(title, style: theme.textTheme.headlineSmall),
                  if (subtitle != null) ...[
                    const SizedBox(height: 8),
                    Text(
                      subtitle!,
                      style: theme.textTheme.bodyMedium
                          ?.copyWith(color: theme.colorScheme.onSurfaceVariant),
                    ),
                  ],
                  const SizedBox(height: 24),
                  child,
                ],
              ),
            ),
          ),
        ),
      ),
    );
  }
}

/// A labelled separator, for "or continue with".
class OrDivider extends StatelessWidget {
  const OrDivider({super.key, this.label = 'or'});

  final String label;

  @override
  Widget build(BuildContext context) {
    final colour = Theme.of(context).colorScheme.outlineVariant;

    return Row(
      children: [
        Expanded(child: Divider(color: colour)),
        Padding(
          padding: const EdgeInsets.symmetric(horizontal: 12),
          child: Text(
            label,
            style: Theme.of(context)
                .textTheme
                .bodySmall
                ?.copyWith(color: Theme.of(context).colorScheme.onSurfaceVariant),
          ),
        ),
        Expanded(child: Divider(color: colour)),
      ],
    );
  }
}
