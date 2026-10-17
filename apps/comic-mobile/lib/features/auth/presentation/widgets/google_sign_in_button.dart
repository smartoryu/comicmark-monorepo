import 'package:flutter/material.dart';

/// The Google sign-in button.
///
/// Rendered only when the API reports Firebase as available, so it is never an
/// offer the backend cannot honour.
class GoogleSignInButton extends StatelessWidget {
  const GoogleSignInButton({
    super.key,
    required this.onPressed,
    this.busy = false,
  });

  final VoidCallback? onPressed;
  final bool busy;

  @override
  Widget build(BuildContext context) {
    final theme = Theme.of(context);

    return OutlinedButton(
      onPressed: busy ? null : onPressed,
      child: busy
          ? const SizedBox(
              height: 20,
              width: 20,
              child: CircularProgressIndicator(strokeWidth: 2),
            )
          : Row(
              mainAxisAlignment: MainAxisAlignment.center,
              children: [
                // The multicolour Google mark is a brand asset the package does
                // not ship, and drawing an approximation of it would be worse
                // than a plain glyph.
                const Icon(Icons.account_circle_outlined, size: 20),
                const SizedBox(width: 10),
                Text(
                  'Continue with Google',
                  style: theme.textTheme.labelLarge,
                ),
              ],
            ),
    );
  }
}
