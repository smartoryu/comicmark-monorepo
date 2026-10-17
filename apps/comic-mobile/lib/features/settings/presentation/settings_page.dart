import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:go_router/go_router.dart';

import '../../../app/env.dart';
import '../../../app/router.dart';
import '../../../core/widgets.dart';
import '../../auth/application/auth_controller.dart';
import '../../password/presentation/change_password_sheet.dart';

/// Account settings.
class SettingsPage extends ConsumerWidget {
  const SettingsPage({super.key});

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final user = ref.watch(authControllerProvider).value;
    if (user == null) {
      // The router guards this route, so arriving here signed out means the
      // session ended while the screen was open.
      return Scaffold(
        appBar: AppBar(title: const Text('Settings')),
        body: EmptyState(
          icon: Icons.lock_outline,
          title: 'You are signed out',
          message: 'Sign in to manage your account.',
          action: FilledButton(
            onPressed: () => context.go(Routes.login),
            child: const Text('Sign in'),
          ),
        ),
      );
    }

    return Scaffold(
      appBar: AppBar(title: const Text('Settings')),
      body: ListView(
        children: [
          const SizedBox(height: 8),
          _AccountHeader(
            name: user.shortName,
            email: user.email,
            initial: user.initial,
            avatarUrl: user.avatarUrl,
          ),
          const SizedBox(height: 8),

          // Only shown while it is actionable, so a verified reader is not
          // nagged about something already done.
          if (!user.emailVerified)
            Padding(
              padding: const EdgeInsets.fromLTRB(16, 8, 16, 8),
              child: NoticeBanner(
                icon: Icons.mark_email_unread_outlined,
                message:
                    'Confirm your email address to secure your account.',
              ),
            ),
          if (!user.emailVerified)
            ListTile(
              leading: const Icon(Icons.outgoing_mail),
              title: const Text('Confirm your email'),
              subtitle: Text(user.email),
              trailing: const Icon(Icons.chevron_right),
              onTap: () => context.go(
                '${Routes.verifyEmail}?email=${Uri.encodeComponent(user.email)}',
              ),
            ),

          const Divider(height: 24),
          ListTile(
            leading: const Icon(Icons.password_outlined),
            title: const Text('Change password'),
            subtitle: const Text('Keep other devices signed in'),
            trailing: const Icon(Icons.chevron_right),
            onTap: () async {
              final changed = await ChangePasswordSheet.show(context);
              if (changed && context.mounted) {
                ScaffoldMessenger.of(context).showSnackBar(
                  const SnackBar(content: Text('Password changed.')),
                );
              }
            },
          ),
          ListTile(
            leading: const Icon(Icons.devices_outlined),
            title: const Text('Devices & sessions'),
            subtitle: const Text('See where you are signed in, and sign devices out'),
            trailing: const Icon(Icons.chevron_right),
            onTap: () => context.go(Routes.sessions),
          ),
          ListTile(
            leading: const Icon(Icons.help_outline),
            title: const Text('Forgot your password?'),
            subtitle: const Text('Reset it and sign every device out'),
            trailing: const Icon(Icons.chevron_right),
            onTap: () => context.go(Routes.forgotPassword),
          ),

          const Divider(height: 24),
          ListTile(
            leading: Icon(
              Icons.logout,
              color: Theme.of(context).colorScheme.error,
            ),
            title: Text(
              'Sign out',
              style: TextStyle(color: Theme.of(context).colorScheme.error),
            ),
            onTap: () => _confirmSignOut(context, ref),
          ),

          const SizedBox(height: 24),
          Padding(
            padding: const EdgeInsets.symmetric(horizontal: 16),
            child: Text(
              _buildInfo(),
              style: Theme.of(context).textTheme.bodySmall?.copyWith(
                    color: Theme.of(context).colorScheme.onSurfaceVariant,
                  ),
            ),
          ),
          const SizedBox(height: 32),
        ],
      ),
    );
  }

  /// A one-line summary of how this build talks to the API.
  ///
  /// Shown in the app rather than only in the README, because "which server is
  /// this pointed at" is the first question when something looks wrong.
  String _buildInfo() {
    final target = usesSameOrigin ? 'this server' : apiBaseUrl;
    final features = clientSupportsFirebaseSignIn ? ' with Google sign-in' : '';
    return 'ComicMark$features\nConnected to $target';
  }

  Future<void> _confirmSignOut(BuildContext context, WidgetRef ref) async {
    final confirmed = await showDialog<bool>(
      context: context,
      builder: (context) => AlertDialog(
        title: const Text('Sign out?'),
        content: const Text(
          'This device will need to sign in again. Your bookmarks stay in your account.',
        ),
        actions: [
          TextButton(
            onPressed: () => Navigator.of(context).pop(false),
            child: const Text('Cancel'),
          ),
          FilledButton(
            onPressed: () => Navigator.of(context).pop(true),
            child: const Text('Sign out'),
          ),
        ],
      ),
    );

    if (confirmed != true || !context.mounted) return;

    await ref.read(authControllerProvider.notifier).signOut();
    if (context.mounted) context.go(Routes.library);
  }
}

/// The account summary at the top of the screen.
class _AccountHeader extends StatelessWidget {
  const _AccountHeader({
    required this.name,
    required this.email,
    required this.initial,
    this.avatarUrl,
  });

  final String name;
  final String email;
  final String initial;
  final String? avatarUrl;

  @override
  Widget build(BuildContext context) {
    final theme = Theme.of(context);

    return Padding(
      padding: const EdgeInsets.fromLTRB(16, 8, 16, 16),
      child: Row(
        children: [
          CircleAvatar(
            radius: 28,
            backgroundColor: theme.colorScheme.primaryContainer,
            child: Text(
              initial,
              style: theme.textTheme.titleLarge?.copyWith(
                color: theme.colorScheme.onPrimaryContainer,
              ),
            ),
          ),
          const SizedBox(width: 16),
          Expanded(
            child: Column(
              crossAxisAlignment: CrossAxisAlignment.start,
              children: [
                Text(name, style: theme.textTheme.titleMedium),
                const SizedBox(height: 2),
                Text(
                  email,
                  style: theme.textTheme.bodySmall
                      ?.copyWith(color: theme.colorScheme.onSurfaceVariant),
                ),
              ],
            ),
          ),
        ],
      ),
    );
  }
}
