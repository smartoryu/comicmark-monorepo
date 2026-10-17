import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';

import '../../../core/api_error.dart';
import '../../../core/format.dart';
import '../../../core/widgets.dart';
import '../../auth/models/auth_user.dart';
import '../application/sessions_controller.dart';

/// The reader's signed-in devices.
///
/// This is the screen that answers "I lost my phone": every active session is
/// listed with enough detail to recognise it, and each can be signed out.
class SessionsPage extends ConsumerWidget {
  const SessionsPage({super.key});

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final sessions = ref.watch(sessionsControllerProvider);

    return Scaffold(
      appBar: AppBar(
        title: const Text('Devices & sessions'),
        actions: [
          IconButton(
            tooltip: 'Refresh',
            icon: const Icon(Icons.refresh),
            onPressed: () => ref.read(sessionsControllerProvider.notifier).reload(),
          ),
        ],
      ),
      body: sessions.when(
        loading: () => const LoadingView(label: 'Loading your devices'),
        error: (error, _) => _error(context, ref, error),
        data: (list) => _list(context, ref, list),
      ),
    );
  }

  Widget _error(BuildContext context, WidgetRef ref, Object error) {
    return EmptyState(
      icon: Icons.cloud_off_outlined,
      title: 'Your devices could not be loaded',
      message: error is ApiError ? error.detailSummary : 'Something went wrong.',
      action: FilledButton(
        onPressed: () => ref.read(sessionsControllerProvider.notifier).reload(),
        child: const Text('Try again'),
      ),
    );
  }

  Widget _list(BuildContext context, WidgetRef ref, List<DeviceSession> sessions) {
    // Not reachable through normal use, because the device making the request is
    // always in its own list. Showing something deliberate beats showing a header
    // that says "signed in on 0 devices".
    if (sessions.isEmpty) {
      return EmptyState(
        icon: Icons.devices_other_outlined,
        title: 'No active devices',
        message: 'Nothing is signed in to this account right now.',
        action: FilledButton(
          onPressed: () => ref.read(sessionsControllerProvider.notifier).reload(),
          child: const Text('Check again'),
        ),
      );
    }

    final others = sessions.where((session) => !session.current).length;

    return ListView(
      padding: const EdgeInsets.symmetric(vertical: 8),
      children: [
        Padding(
          padding: const EdgeInsets.fromLTRB(16, 8, 16, 12),
          child: Text(
            'Signed in on ${sessions.length} '
            '${sessions.length == 1 ? "device" : "devices"}.',
            style: Theme.of(context).textTheme.bodyMedium?.copyWith(
                  color: Theme.of(context).colorScheme.onSurfaceVariant,
                ),
          ),
        ),
        for (final session in sessions)
          _SessionTile(
            session: session,
            onRevoke: session.current
                ? null
                : () => _revoke(context, ref, session),
            onRename: () => _rename(context, ref, session),
          ),
        if (others > 0) ...[
          const Divider(height: 32),
          Padding(
            padding: const EdgeInsets.symmetric(horizontal: 16),
            child: OutlinedButton.icon(
              onPressed: () => _revokeOthers(context, ref),
              icon: const Icon(Icons.logout, size: 18),
              label: Text(
                others == 1
                    ? 'Sign out the other device'
                    : 'Sign out the other $others devices',
              ),
            ),
          ),
          const SizedBox(height: 8),
          Padding(
            padding: const EdgeInsets.symmetric(horizontal: 16),
            child: Text(
              'Other devices keep working until you sign them out here or their '
              'session goes idle.',
              style: Theme.of(context).textTheme.bodySmall?.copyWith(
                    color: Theme.of(context).colorScheme.onSurfaceVariant,
                  ),
            ),
          ),
        ],
        const SizedBox(height: 24),
      ],
    );
  }

  Future<void> _revoke(
    BuildContext context,
    WidgetRef ref,
    DeviceSession session,
  ) async {
    final confirmed = await showDialog<bool>(
      context: context,
      builder: (context) => AlertDialog(
        title: const Text('Sign this device out?'),
        content: Text(
          '"${session.deviceLabel}" will need to sign in again.',
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

    try {
      await ref.read(sessionsControllerProvider.notifier).revoke(session.id);
    } on ApiError catch (error) {
      if (!context.mounted) return;
      ScaffoldMessenger.of(context)
          .showSnackBar(SnackBar(content: Text(error.detailSummary)));
    }
  }

  Future<void> _revokeOthers(BuildContext context, WidgetRef ref) async {
    final confirmed = await showDialog<bool>(
      context: context,
      builder: (context) => AlertDialog(
        title: const Text('Sign out everywhere else?'),
        content: const Text(
          'Every other device will need to sign in again. This device stays signed in.',
        ),
        actions: [
          TextButton(
            onPressed: () => Navigator.of(context).pop(false),
            child: const Text('Cancel'),
          ),
          FilledButton(
            onPressed: () => Navigator.of(context).pop(true),
            child: const Text('Sign out others'),
          ),
        ],
      ),
    );

    if (confirmed != true || !context.mounted) return;

    try {
      await ref.read(sessionsControllerProvider.notifier).revokeOthers();
    } on ApiError catch (error) {
      if (!context.mounted) return;
      ScaffoldMessenger.of(context)
          .showSnackBar(SnackBar(content: Text(error.detailSummary)));
    }
  }

  Future<void> _rename(
    BuildContext context,
    WidgetRef ref,
    DeviceSession session,
  ) async {
    final controller = TextEditingController(text: session.deviceLabel);
    final formKey = GlobalKey<FormState>();

    final label = await showDialog<String>(
      context: context,
      builder: (context) => AlertDialog(
        title: const Text('Name this device'),
        content: Form(
          key: formKey,
          child: TextFormField(
            controller: controller,
            autofocus: true,
            decoration: const InputDecoration(labelText: 'Device name'),
            validator: (value) => (value ?? '').trim().isEmpty
                ? 'Give this device a name.'
                : null,
          ),
        ),
        actions: [
          TextButton(
            onPressed: () => Navigator.of(context).pop(),
            child: const Text('Cancel'),
          ),
          FilledButton(
            onPressed: () {
              if (formKey.currentState?.validate() ?? false) {
                Navigator.of(context).pop(controller.text.trim());
              }
            },
            child: const Text('Save'),
          ),
        ],
      ),
    );

    controller.dispose();
    if (label == null || !context.mounted) return;

    try {
      await ref.read(sessionsControllerProvider.notifier).rename(session.id, label);
    } on ApiError catch (error) {
      if (!context.mounted) return;
      ScaffoldMessenger.of(context)
          .showSnackBar(SnackBar(content: Text(error.detailSummary)));
    }
  }
}

/// One device row.
class _SessionTile extends StatelessWidget {
  const _SessionTile({
    required this.session,
    required this.onRevoke,
    required this.onRename,
  });

  final DeviceSession session;

  /// Null for the current device: the API refuses to revoke it, and the reader
  /// has sign out for that.
  final VoidCallback? onRevoke;

  final VoidCallback onRename;

  @override
  Widget build(BuildContext context) {
    final theme = Theme.of(context);

    return ListTile(
      leading: CircleAvatar(
        backgroundColor: session.current
            ? theme.colorScheme.primaryContainer
            : theme.colorScheme.surfaceContainerHighest,
        child: Icon(
          platformIcon(session.platform),
          size: 20,
          color: session.current
              ? theme.colorScheme.onPrimaryContainer
              : theme.colorScheme.onSurfaceVariant,
        ),
      ),
      title: Row(
        children: [
          Flexible(
            child: Text(
              session.deviceLabel,
              overflow: TextOverflow.ellipsis,
            ),
          ),
          if (session.current) ...[
            const SizedBox(width: 8),
            Container(
              padding: const EdgeInsets.symmetric(horizontal: 6, vertical: 2),
              decoration: BoxDecoration(
                color: theme.colorScheme.primaryContainer,
                borderRadius: BorderRadius.circular(6),
              ),
              child: Text(
                'This device',
                style: theme.textTheme.labelSmall
                    ?.copyWith(color: theme.colorScheme.onPrimaryContainer),
              ),
            ),
          ],
        ],
      ),
      subtitle: Text(
        '${platformLabel(session.platform)} · ${formatRelativeTime(session.lastSeenAt)}',
      ),
      trailing: PopupMenuButton<String>(
        tooltip: 'Device actions',
        onSelected: (value) {
          if (value == 'rename') onRename();
          if (value == 'revoke') onRevoke?.call();
        },
        itemBuilder: (context) => [
          const PopupMenuItem(value: 'rename', child: Text('Rename')),
          // No revoke entry for this device: the API refuses it, and offering it
          // only to fail would be worse than not offering it.
          if (onRevoke != null)
            const PopupMenuItem(value: 'revoke', child: Text('Sign out')),
        ],
      ),
    );
  }
}
