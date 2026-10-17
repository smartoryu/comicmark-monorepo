import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:go_router/go_router.dart';

import '../../../app/router.dart';
import '../../../core/api_error.dart';
import '../../../core/widgets.dart';
import '../application/auth_controller.dart';
import 'widgets/auth_scaffold.dart';
import 'widgets/validators.dart';

/// The "check your inbox" screen, and the landing point for a confirmation link.
///
/// Three states in one screen, because they are the same moment to a reader:
///
///  * a link was just followed and carries `verify_token`, so the app completes it
///  * an address is known and no link was followed, so the app offers a resend
///  * a link was already used or has expired, so the app says so and offers a
///    fresh one
///
/// Resending does not need a session. That is deliberate on the API side: when
/// verification is enforced, an unverified reader has no access token, so the
/// resend has to work without one.
class VerifyEmailPage extends ConsumerStatefulWidget {
  const VerifyEmailPage({super.key, this.token, this.email});

  /// The token from an emailed link, when one was followed.
  final String? token;

  /// The address to resend to, when known.
  final String? email;

  @override
  ConsumerState<VerifyEmailPage> createState() => _VerifyEmailPageState();
}

class _VerifyEmailPageState extends ConsumerState<VerifyEmailPage> {
  late final TextEditingController _email;
  final _formKey = GlobalKey<FormState>();

  bool _busy = false;
  bool? _verified;
  ApiError? _error;
  String? _notice;

  @override
  void initState() {
    super.initState();
    _email = TextEditingController(text: widget.email ?? '');

    // A token in the URL means a link was just followed.
    if (widget.token != null && widget.token!.isNotEmpty) {
      WidgetsBinding.instance.addPostFrameCallback((_) => _completeVerification());
    }
  }

  @override
  void dispose() {
    _email.dispose();
    super.dispose();
  }

  Future<void> _completeVerification() async {
    setState(() {
      _busy = true;
      _error = null;
    });

    try {
      await ref.read(authRepositoryProvider).verifyEmail(widget.token!);

      // The server's view of the reader has changed, so the cached account is
      // refreshed before anything reads it.
      await ref.read(authControllerProvider.notifier).refreshUser();
      ref.read(authControllerProvider.notifier).markEmailVerified();

      if (mounted) setState(() => _verified = true);
    } on ApiError catch (error) {
      if (mounted) {
        setState(() {
          _verified = false;
          // An expired or already-used link is not an error worth a red banner:
          // it is the normal outcome of clicking an old email.
          _notice = error.code == ApiErrorCode.tokenInvalid
              ? 'That link has expired or has already been used. Request a new one below.'
              : null;
          _error = error.code == ApiErrorCode.tokenInvalid ? null : error;
        });
      }
    } finally {
      if (mounted) setState(() => _busy = false);
    }
  }

  Future<void> _resend() async {
    if (!(_formKey.currentState?.validate() ?? false)) return;

    setState(() {
      _busy = true;
      _error = null;
      _notice = null;
    });

    try {
      await ref
          .read(authRepositoryProvider)
          .requestEmailVerification(_email.text.trim());

      // The API always answers 202, whether or not the address exists, so this
      // wording must not promise that an email was sent to a specific address.
      if (mounted) {
        setState(() => _notice =
            'If ${_email.text.trim()} belongs to an unverified account, a new link is on its way.');
      }
    } on ApiError catch (error) {
      if (mounted) setState(() => _error = error);
    } finally {
      if (mounted) setState(() => _busy = false);
    }
  }

  @override
  Widget build(BuildContext context) {
    return AuthScaffold(
      title: _title,
      subtitle: _subtitle,
      showBack: _verified != true,
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.stretch,
        children: [
          if (_error != null) ...[
            ErrorBanner.fromError(_error!, onDismiss: () => setState(() => _error = null)),
            const SizedBox(height: 16),
          ],
          if (_notice != null) ...[
            NoticeBanner(message: _notice!),
            const SizedBox(height: 16),
          ],
          if (_verified == true) ..._verifiedBody(context) else ..._pendingBody(context),
        ],
      ),
    );
  }

  String get _title {
    if (_verified == true) return 'Email confirmed';
    if (_verified == false) return 'This link did not work';
    return 'Check your inbox';
  }

  String? get _subtitle {
    if (_verified == true) return null;
    if (_verified == false) return 'Request a new link and try again.';
    return 'Confirm your address to finish setting up your account.';
  }

  /// Shown once a link has been accepted.
  List<Widget> _verifiedBody(BuildContext context) {
    final signedIn = ref.watch(authControllerProvider).value != null;

    return [
      const Icon(Icons.mark_email_read_outlined, size: 56),
      const SizedBox(height: 16),
      Text(
        'Your email address is confirmed. You are all set.',
        style: Theme.of(context).textTheme.bodyMedium,
        textAlign: TextAlign.center,
      ),
      const SizedBox(height: 24),
      FilledButton(
        // Signed in already after registering, so the library is the right place;
        // otherwise there is a session waiting to be resumed.
        onPressed: () => context.go(signedIn ? Routes.library : Routes.login),
        child: Text(signedIn ? 'Go to your library' : 'Sign in'),
      ),
    ];
  }

  /// Shown before a link is followed, and after one fails.
  List<Widget> _pendingBody(BuildContext context) {
    return [
      const Icon(Icons.outgoing_mail, size: 56),
      const SizedBox(height: 16),
      Text(
        'We sent a confirmation link to your email address. Open it to confirm your account.',
        style: Theme.of(context).textTheme.bodyMedium,
        textAlign: TextAlign.center,
      ),
      const SizedBox(height: 24),
      Form(
        key: _formKey,
        child: TextFormField(
          controller: _email,
          enabled: !_busy,
          keyboardType: TextInputType.emailAddress,
          autocorrect: false,
          decoration: const InputDecoration(
            labelText: 'Email',
            prefixIcon: Icon(Icons.alternate_email),
          ),
          validator: (value) => validateEmail(value),
        ),
      ),
      const SizedBox(height: 16),
      AsyncButton(
        onPressed: _resend,
        busy: _busy,
        label: 'Resend the link',
      ),
      const SizedBox(height: 12),
      TextButton(
        onPressed: _busy ? null : () => context.go(Routes.library),
        child: const Text("I'll do this later"),
      ),
    ];
  }
}
