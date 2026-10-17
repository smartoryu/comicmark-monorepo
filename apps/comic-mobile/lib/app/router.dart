import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:go_router/go_router.dart';

import '../features/auth/application/auth_controller.dart';
import '../features/auth/presentation/forgot_password_page.dart';
import '../features/auth/presentation/login_page.dart';
import '../features/auth/presentation/register_page.dart';
import '../features/auth/presentation/reset_password_page.dart';
import '../features/auth/presentation/verify_email_page.dart';
import '../features/library/presentation/library_page.dart';
import '../features/sessions/presentation/sessions_page.dart';
import '../features/settings/presentation/settings_page.dart';

/// Route paths, named so no screen has to spell a string twice.
abstract final class Routes {
  static const library = '/';
  static const login = '/login';
  static const register = '/register';
  static const verifyEmail = '/verify-email';
  static const forgotPassword = '/forgot-password';
  static const resetPassword = '/reset-password';
  static const sessions = '/settings/sessions';
  static const settings = '/settings';
}

/// The app's router.
///
/// The library is the landing screen and is reachable without an account, so a
/// reader who has not signed in yet sees the app rather than a wall. Screens that
/// genuinely need a session — settings and the device list — declare
/// [requiresSession] and are redirected; everything else renders its own
/// signed-out state, which is what makes the library show a "sign in to see your
/// bookmarks" prompt instead of an empty grid that looks broken.
final routerProvider = Provider<GoRouter>((ref) {
  return GoRouter(
    initialLocation: Routes.library,
    // Errors are shown inside the app rather than as Flutter's red screen, so a
    // mistyped link still leaves the reader somewhere useful.
    errorBuilder: (context, state) => _RouteNotFound(location: state.uri.toString()),
    redirect: (context, state) {
      // Read once per navigation rather than watching: watching here would rebuild
      // the whole router whenever the session changed, which would reset the
      // navigation stack under the reader.
      final auth = ref.read(authControllerProvider);

      // While the stored session is still being read, no redirect is safe: sending
      // a signed-in reader to the sign-in screen and back looks like a flicker.
      if (auth.isLoading) return null;

      final signedIn = auth.value != null;
      final requiresSession = _sessionRoutes.contains(state.matchedLocation);

      if (requiresSession && !signedIn) {
        // Remember where they were headed so sign-in can continue to it.
        return '${Routes.login}?next=${Uri.encodeComponent(state.matchedLocation)}';
      }

      // A signed-in reader has no business on the sign-in or registration screen.
      final isAuthEntry = state.matchedLocation == Routes.login ||
          state.matchedLocation == Routes.register;
      if (isAuthEntry && signedIn) return Routes.library;

      return null;
    },
    routes: [
      GoRoute(
        path: Routes.library,
        name: 'library',
        builder: (context, state) => const LibraryPage(),
      ),
      GoRoute(
        path: Routes.login,
        name: 'login',
        builder: (context, state) => LoginPage(
          next: state.uri.queryParameters['next'],
          notice: state.uri.queryParameters['notice'],
        ),
      ),
      GoRoute(
        path: Routes.register,
        name: 'register',
        builder: (context, state) => const RegisterPage(),
      ),
      // Reached from an emailed link, or from the sign-in screen when the API
      // refuses an unverified account.
      GoRoute(
        path: Routes.verifyEmail,
        name: 'verifyEmail',
        builder: (context, state) => VerifyEmailPage(
          token: state.uri.queryParameters['verify_token'],
          email: state.uri.queryParameters['email'],
        ),
      ),
      GoRoute(
        path: Routes.forgotPassword,
        name: 'forgotPassword',
        builder: (context, state) => const ForgotPasswordPage(),
      ),
      // Reached from an emailed link, which carries the token as a parameter.
      GoRoute(
        path: Routes.resetPassword,
        name: 'resetPassword',
        builder: (context, state) => ResetPasswordPage(
          token: state.uri.queryParameters['reset_token'],
        ),
      ),
      GoRoute(
        path: Routes.settings,
        name: 'settings',
        builder: (context, state) => const SettingsPage(),
        routes: [
          GoRoute(
            path: 'sessions',
            name: 'sessions',
            builder: (context, state) => const SessionsPage(),
          ),
        ],
      ),
    ],
  );
});

/// Routes that need a session before they are worth showing.
const _sessionRoutes = {Routes.settings, Routes.sessions};

/// Shown for an unknown path.
class _RouteNotFound extends StatelessWidget {
  const _RouteNotFound({required this.location});

  final String location;

  @override
  Widget build(BuildContext context) {
    return Scaffold(
      appBar: AppBar(title: const Text('Not found')),
      body: Center(
        child: Padding(
          padding: const EdgeInsets.all(24),
          child: Column(
            mainAxisSize: MainAxisSize.min,
            children: [
              const Icon(Icons.explore_off_outlined, size: 48),
              const SizedBox(height: 16),
              Text(
                'There is nothing at $location.',
                textAlign: TextAlign.center,
                style: Theme.of(context).textTheme.titleMedium,
              ),
              const SizedBox(height: 24),
              FilledButton(
                onPressed: () => context.go(Routes.library),
                child: const Text('Go to your library'),
              ),
            ],
          ),
        ),
      ),
    );
  }
}
