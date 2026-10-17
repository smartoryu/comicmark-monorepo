import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:go_router/go_router.dart';

import '../../../app/router.dart';
import '../../../core/api_error.dart';
import '../../../core/widgets.dart';
import '../application/auth_controller.dart';
import 'widgets/auth_scaffold.dart';
import 'widgets/validators.dart';

/// Chooses a new password from an emailed reset link.
class ResetPasswordPage extends ConsumerStatefulWidget {
  const ResetPasswordPage({super.key, this.token});

  /// The token from the emailed link. Without it there is nothing to reset.
  final String? token;

  @override
  ConsumerState<ResetPasswordPage> createState() => _ResetPasswordPageState();
}

class _ResetPasswordPageState extends ConsumerState<ResetPasswordPage> {
  final _formKey = GlobalKey<FormState>();
  final _password = TextEditingController();
  final _confirm = TextEditingController();

  bool _busy = false;
  bool _showPassword = false;
  bool _done = false;
  ApiError? _error;

  bool get _hasToken => widget.token != null && widget.token!.isNotEmpty;

  @override
  void dispose() {
    _password.dispose();
    _confirm.dispose();
    super.dispose();
  }

  Future<void> _submit() async {
    if (!(_formKey.currentState?.validate() ?? false)) return;

    setState(() {
      _busy = true;
      _error = null;
    });

    try {
      await ref.read(authRepositoryProvider).resetPassword(
            token: widget.token!,
            newPassword: _password.text,
          );

      // The API signs every device out on reset, so any session the app was
      // holding is already dead. Clearing it locally keeps the two in step
      // rather than leaving a stale token to be discovered by the next request.
      await ref.read(authControllerProvider.notifier).signOut();

      if (mounted) setState(() => _done = true);
    } on ApiError catch (error) {
      if (mounted) setState(() => _error = error);
    } finally {
      if (mounted) setState(() => _busy = false);
    }
  }

  @override
  Widget build(BuildContext context) {
    if (!_hasToken) return _missingToken(context);
    if (_done) return _success(context);

    return AuthScaffold(
      title: 'Choose a new password',
      subtitle: 'You will be signed out everywhere else.',
      child: Form(
        key: _formKey,
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.stretch,
          children: [
            if (_error != null) ...[
              ErrorBanner.fromError(_error!, onDismiss: () => setState(() => _error = null)),
              const SizedBox(height: 16),
            ],
            TextFormField(
              controller: _password,
              enabled: !_busy,
              autofillHints: const [AutofillHints.newPassword],
              obscureText: !_showPassword,
              textInputAction: TextInputAction.next,
              decoration: InputDecoration(
                labelText: 'New password',
                prefixIcon: const Icon(Icons.lock_outline),
                helperText: 'At least $minPasswordLength characters.',
                suffixIcon: IconButton(
                  tooltip: _showPassword ? 'Hide password' : 'Show password',
                  icon: Icon(_showPassword ? Icons.visibility_off : Icons.visibility),
                  onPressed: () => setState(() => _showPassword = !_showPassword),
                ),
              ),
              validator: validatePassword,
            ),
            const SizedBox(height: 12),
            TextFormField(
              controller: _confirm,
              enabled: !_busy,
              obscureText: !_showPassword,
              textInputAction: TextInputAction.done,
              decoration: const InputDecoration(
                labelText: 'Confirm new password',
                prefixIcon: Icon(Icons.lock_reset),
              ),
              // Catching a typo here is the whole reason for a confirmation
              // field: the reader cannot see what they typed.
              validator: (value) {
                if ((value ?? '').isEmpty) return 'Confirm your new password.';
                if (value != _password.text) return 'Those passwords do not match.';
                return null;
              },
              onFieldSubmitted: (_) => _submit(),
            ),
            const SizedBox(height: 20),
            AsyncButton(
              onPressed: _submit,
              busy: _busy,
              label: 'Set the new password',
            ),
          ],
        ),
      ),
    );
  }

  /// A reset link that arrives without a token cannot be acted on.
  Widget _missingToken(BuildContext context) {
    return AuthScaffold(
      title: 'This link is incomplete',
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.stretch,
        children: [
          const Icon(Icons.link_off, size: 56),
          const SizedBox(height: 16),
          Text(
            'The reset link is missing its token. Request a new one and open it '
            'from your email.',
            style: Theme.of(context).textTheme.bodyMedium,
            textAlign: TextAlign.center,
          ),
          const SizedBox(height: 24),
          FilledButton(
            onPressed: () => context.go(Routes.forgotPassword),
            child: const Text('Request a new link'),
          ),
        ],
      ),
    );
  }

  Widget _success(BuildContext context) {
    return AuthScaffold(
      title: 'Password changed',
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.stretch,
        children: [
          const Icon(Icons.check_circle_outline, size: 56),
          const SizedBox(height: 16),
          Text(
            'Sign in with your new password. Every other device has been signed out.',
            style: Theme.of(context).textTheme.bodyMedium,
            textAlign: TextAlign.center,
          ),
          const SizedBox(height: 24),
          FilledButton(
            onPressed: () => context.go(Routes.login),
            child: const Text('Sign in'),
          ),
        ],
      ),
    );
  }
}
