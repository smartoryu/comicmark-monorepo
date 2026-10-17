import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:go_router/go_router.dart';

import '../../../app/router.dart';
import '../../../core/api_error.dart';
import '../../../core/widgets.dart';
import '../application/auth_controller.dart';
import '../application/firebase_sign_in.dart';
import 'widgets/auth_scaffold.dart';
import 'widgets/google_sign_in_button.dart';
import 'widgets/validators.dart';

/// Sign-in.
class LoginPage extends ConsumerStatefulWidget {
  const LoginPage({super.key, this.next, this.notice});

  /// Where to continue after a successful sign-in, when the reader was sent here
  /// from a screen that needed a session.
  final String? next;

  /// A short message to show on arrival, e.g. "Your session ended".
  final String? notice;

  @override
  ConsumerState<LoginPage> createState() => _LoginPageState();
}

class _LoginPageState extends ConsumerState<LoginPage> {
  final _formKey = GlobalKey<FormState>();
  final _email = TextEditingController();
  final _password = TextEditingController();

  bool _busy = false;
  ApiError? _error;

  @override
  void dispose() {
    _email.dispose();
    _password.dispose();
    super.dispose();
  }

  Future<void> _submit() async {
    if (!(_formKey.currentState?.validate() ?? false)) return;

    setState(() {
      _busy = true;
      _error = null;
    });

    try {
      await ref.read(authControllerProvider.notifier).login(
            email: _email.text.trim(),
            password: _password.text,
          );
      if (mounted) _continue();
    } on ApiError catch (error) {
      if (!mounted) return;

      // An unverified account is not a dead end: send the reader to the screen
      // that can resend the link, carrying the address so they need not retype it.
      if (error.code == ApiErrorCode.emailNotVerified) {
        context.go(
          '${Routes.verifyEmail}?email=${Uri.encodeComponent(_email.text.trim())}',
        );
        return;
      }
      setState(() => _error = error);
    } finally {
      if (mounted) setState(() => _busy = false);
    }
  }

  Future<void> _signInWithGoogle() async {
    setState(() {
      _busy = true;
      _error = null;
    });

    try {
      final idToken = await ref.read(firebaseSignInProvider).signIn();
      if (idToken == null) return; // The reader dismissed the prompt.
      await ref.read(authControllerProvider.notifier).signInWithFirebase(idToken);
      if (mounted) _continue();
    } on ApiError catch (error) {
      if (mounted) setState(() => _error = error);
    } on FirebaseSignInUnavailable catch (error) {
      if (mounted) {
        setState(() => _error = ApiError(
              code: ApiErrorCode.internal,
              message: error.message,
            ));
      }
    } finally {
      if (mounted) setState(() => _busy = false);
    }
  }

  /// Leaves the sign-in screen, honouring the requested destination.
  void _continue() {
    final next = widget.next;
    context.go(next != null && next.isNotEmpty ? next : Routes.library);
  }

  @override
  Widget build(BuildContext context) {
    final providers = ref.watch(authProvidersProvider);

    return AuthScaffold(
      title: 'Sign in',
      subtitle: 'Pick up where you left off.',
      child: Form(
        key: _formKey,
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.stretch,
          children: [
            if (widget.notice != null) ...[
              NoticeBanner(message: widget.notice!),
              const SizedBox(height: 16),
            ],
            if (_error != null) ...[
              ErrorBanner.fromError(_error!, onDismiss: () => setState(() => _error = null)),
              const SizedBox(height: 16),
            ],
            TextFormField(
              controller: _email,
              enabled: !_busy,
              autofillHints: const [AutofillHints.email],
              keyboardType: TextInputType.emailAddress,
              textInputAction: TextInputAction.next,
              autocorrect: false,
              decoration: const InputDecoration(
                labelText: 'Email',
                prefixIcon: Icon(Icons.alternate_email),
              ),
              validator: (value) => validateEmail(value),
            ),
            const SizedBox(height: 12),
            TextFormField(
              controller: _password,
              enabled: !_busy,
              autofillHints: const [AutofillHints.password],
              obscureText: true,
              textInputAction: TextInputAction.done,
              decoration: const InputDecoration(
                labelText: 'Password',
                prefixIcon: Icon(Icons.lock_outline),
              ),
              validator: (value) => validatePassword(value, requireLength: false),
              onFieldSubmitted: (_) => _submit(),
            ),
            Align(
              alignment: Alignment.centerRight,
              child: TextButton(
                onPressed: _busy ? null : () => context.go(Routes.forgotPassword),
                child: const Text('Forgot your password?'),
              ),
            ),
            const SizedBox(height: 8),
            AsyncButton(
              onPressed: _submit,
              busy: _busy,
              label: 'Sign in',
            ),
            // The Google button is rendered only when the API says Firebase is
            // mounted. With no Firebase project the route does not exist, so
            // offering the button would only produce a failure.
            if (providers.value?.firebase ?? false) ...[
              const SizedBox(height: 20),
              const OrDivider(),
              const SizedBox(height: 20),
              GoogleSignInButton(
                onPressed: _busy ? null : _signInWithGoogle,
                busy: _busy,
              ),
            ],
            const SizedBox(height: 24),
            Row(
              mainAxisAlignment: MainAxisAlignment.center,
              children: [
                const Text('New here?'),
                TextButton(
                  onPressed: _busy ? null : () => context.go(Routes.register),
                  child: const Text('Create an account'),
                ),
              ],
            ),
          ],
        ),
      ),
    );
  }
}
