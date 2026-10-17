import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:go_router/go_router.dart';

import '../../../app/router.dart';
import '../../../core/api_error.dart';
import '../../../core/widgets.dart';
import '../application/auth_controller.dart';
import 'widgets/auth_scaffold.dart';
import 'widgets/validators.dart';

/// Asks for a password reset link.
class ForgotPasswordPage extends ConsumerStatefulWidget {
  const ForgotPasswordPage({super.key});

  @override
  ConsumerState<ForgotPasswordPage> createState() => _ForgotPasswordPageState();
}

class _ForgotPasswordPageState extends ConsumerState<ForgotPasswordPage> {
  final _formKey = GlobalKey<FormState>();
  final _email = TextEditingController();

  bool _busy = false;
  bool _sent = false;
  ApiError? _error;

  @override
  void dispose() {
    _email.dispose();
    super.dispose();
  }

  Future<void> _submit() async {
    if (!(_formKey.currentState?.validate() ?? false)) return;

    setState(() {
      _busy = true;
      _error = null;
    });

    try {
      await ref
          .read(authRepositoryProvider)
          .forgotPassword(_email.text.trim());

      // The API answers 202 whether or not the address has an account, so the
      // screen must not claim an email was sent. Saying "if that address has an
      // account" is what keeps this from being an account-existence oracle.
      if (mounted) setState(() => _sent = true);
    } on ApiError catch (error) {
      if (mounted) setState(() => _error = error);
    } finally {
      if (mounted) setState(() => _busy = false);
    }
  }

  @override
  Widget build(BuildContext context) {
    return AuthScaffold(
      title: _sent ? 'Check your inbox' : 'Reset your password',
      subtitle: _sent
          ? null
          : 'We will email you a link to choose a new one.',
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.stretch,
        children: _sent ? _sentBody(context) : _formBody(context),
      ),
    );
  }

  List<Widget> _formBody(BuildContext context) {
    return [
      if (_error != null) ...[
        ErrorBanner.fromError(_error!, onDismiss: () => setState(() => _error = null)),
        const SizedBox(height: 16),
      ],
      Form(
        key: _formKey,
        child: TextFormField(
          controller: _email,
          enabled: !_busy,
          autofillHints: const [AutofillHints.email],
          keyboardType: TextInputType.emailAddress,
          autocorrect: false,
          decoration: const InputDecoration(
            labelText: 'Email',
            prefixIcon: Icon(Icons.alternate_email),
          ),
          validator: validateEmail,
          onFieldSubmitted: (_) => _submit(),
        ),
      ),
      const SizedBox(height: 20),
      AsyncButton(
        onPressed: _submit,
        busy: _busy,
        label: 'Send the link',
      ),
      const SizedBox(height: 24),
      TextButton(
        onPressed: _busy ? null : () => context.go(Routes.login),
        child: const Text('Back to sign in'),
      ),
    ];
  }

  List<Widget> _sentBody(BuildContext context) {
    return [
      const Icon(Icons.outgoing_mail, size: 56),
      const SizedBox(height: 16),
      Text(
        'If ${_email.text.trim()} has an account, a reset link is on its way. '
        "Choosing a new password signs you out on every device.",
        style: Theme.of(context).textTheme.bodyMedium,
        textAlign: TextAlign.center,
      ),
      const SizedBox(height: 24),
      FilledButton(
        onPressed: () => context.go(Routes.login),
        child: const Text('Back to sign in'),
      ),
      const SizedBox(height: 8),
      TextButton(
        onPressed: () => setState(() => _sent = false),
        child: const Text('Use a different address'),
      ),
    ];
  }
}
