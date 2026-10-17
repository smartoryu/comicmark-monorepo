import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';

import '../../../core/api_error.dart';
import '../../../core/widgets.dart';
import '../../auth/application/auth_controller.dart';
import '../../auth/presentation/widgets/validators.dart';

/// Changes the password of the signed-in reader.
///
/// The current password is required, which is what stops a borrowed unlocked
/// phone from being used to lock the owner out. Other devices stay signed in:
/// the reader proved they know the current password, and signing everything out
/// is what the reset flow is for.
class ChangePasswordSheet extends ConsumerStatefulWidget {
  const ChangePasswordSheet({super.key});

  /// Shows the sheet and reports whether the password was changed.
  static Future<bool> show(BuildContext context) async {
    final changed = await showModalBottomSheet<bool>(
      context: context,
      isScrollControlled: true,
      builder: (_) => const ChangePasswordSheet(),
    );
    return changed ?? false;
  }

  @override
  ConsumerState<ChangePasswordSheet> createState() => _ChangePasswordSheetState();
}

class _ChangePasswordSheetState extends ConsumerState<ChangePasswordSheet> {
  final _formKey = GlobalKey<FormState>();
  final _current = TextEditingController();
  final _next = TextEditingController();
  final _confirm = TextEditingController();

  bool _busy = false;
  bool _showPasswords = false;
  ApiError? _error;

  @override
  void dispose() {
    _current.dispose();
    _next.dispose();
    _confirm.dispose();
    super.dispose();
  }

  Future<void> _save() async {
    if (!(_formKey.currentState?.validate() ?? false)) return;

    setState(() {
      _busy = true;
      _error = null;
    });

    try {
      await ref.read(authRepositoryProvider).changePassword(
            currentPassword: _current.text,
            newPassword: _next.text,
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

    return Padding(
      padding: EdgeInsets.only(bottom: MediaQuery.of(context).viewInsets.bottom),
      child: SafeArea(
        child: SingleChildScrollView(
          padding: const EdgeInsets.fromLTRB(20, 20, 20, 24),
          child: Form(
            key: _formKey,
            child: Column(
              mainAxisSize: MainAxisSize.min,
              crossAxisAlignment: CrossAxisAlignment.stretch,
              children: [
                Row(
                  children: [
                    Text('Change password', style: theme.textTheme.titleLarge),
                    const Spacer(),
                    IconButton(
                      onPressed: _busy ? null : () => Navigator.of(context).pop(false),
                      icon: const Icon(Icons.close),
                      tooltip: 'Close',
                    ),
                  ],
                ),
                const SizedBox(height: 16),
                if (_error != null) ...[
                  ErrorBanner.fromError(_error!, onDismiss: () => setState(() => _error = null)),
                  const SizedBox(height: 16),
                ],
                TextFormField(
                  controller: _current,
                  enabled: !_busy,
                  obscureText: !_showPasswords,
                  textInputAction: TextInputAction.next,
                  decoration: const InputDecoration(
                    labelText: 'Current password',
                    prefixIcon: Icon(Icons.lock_outline),
                  ),
                  validator: (value) => (value ?? '').isEmpty
                      ? 'Enter your current password.'
                      : null,
                ),
                const SizedBox(height: 12),
                TextFormField(
                  controller: _next,
                  enabled: !_busy,
                  obscureText: !_showPasswords,
                  textInputAction: TextInputAction.next,
                  decoration: InputDecoration(
                    labelText: 'New password',
                    prefixIcon: const Icon(Icons.lock_reset),
                    helperText: 'At least $minPasswordLength characters.',
                    suffixIcon: IconButton(
                      tooltip: _showPasswords ? 'Hide passwords' : 'Show passwords',
                      icon: Icon(
                        _showPasswords ? Icons.visibility_off : Icons.visibility,
                      ),
                      onPressed: () =>
                          setState(() => _showPasswords = !_showPasswords),
                    ),
                  ),
                  validator: validatePassword,
                ),
                const SizedBox(height: 12),
                TextFormField(
                  controller: _confirm,
                  enabled: !_busy,
                  obscureText: !_showPasswords,
                  textInputAction: TextInputAction.done,
                  decoration: const InputDecoration(
                    labelText: 'Confirm new password',
                    prefixIcon: Icon(Icons.lock_reset),
                  ),
                  validator: (value) {
                    if ((value ?? '').isEmpty) return 'Confirm your new password.';
                    if (value != _next.text) return 'Those passwords do not match.';
                    return null;
                  },
                  onFieldSubmitted: (_) => _save(),
                ),
                const SizedBox(height: 20),
                AsyncButton(
                  onPressed: _save,
                  busy: _busy,
                  label: 'Change password',
                ),
                const SizedBox(height: 8),
                Text(
                  'Other devices stay signed in. To sign them all out, use '
                  '"Forgot password" instead.',
                  style: theme.textTheme.bodySmall
                      ?.copyWith(color: theme.colorScheme.onSurfaceVariant),
                ),
              ],
            ),
          ),
        ),
      ),
    );
  }
}
