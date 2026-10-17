import 'dart:async';

import 'package:firebase_auth/firebase_auth.dart';
import 'package:firebase_core/firebase_core.dart';
import 'package:flutter/foundation.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:google_sign_in/google_sign_in.dart';

import '../../../app/env.dart';

/// Raised when Google sign-in cannot even be attempted.
///
/// Distinct from an [ApiError] because nothing reached the API: the problem is
/// local configuration.
class FirebaseSignInUnavailable implements Exception {
  const FirebaseSignInUnavailable(this.message);

  final String message;

  @override
  String toString() => 'FirebaseSignInUnavailable($message)';
}

/// Performs Google sign-in and returns a Firebase ID token.
///
/// The whole Firebase surface of the app lives behind this interface, so the rest
/// of the code never imports the SDK and tests substitute a fake.
abstract interface class FirebaseSignIn {
  /// Returns the ID token, or null when the reader dismissed the prompt.
  Future<String?> signIn();

  /// Whether the SDK was initialised successfully.
  bool get isAvailable;
}

/// The provider the sign-in screen talks to.
///
/// When the build carries no Firebase configuration this resolves to a stub that
/// reports itself unavailable, so the app never tries to initialise the SDK.
final firebaseSignInProvider = Provider<FirebaseSignIn>((ref) {
  if (!firebaseConfigured) {
    return const UnavailableFirebaseSignIn(
      'Google sign-in is not set up in this build.',
    );
  }
  return FirebaseSignInService();
});

/// The real implementation.
///
/// Two paths, because Google sign-in is genuinely different on the web: there is
/// no native account picker and no `google_sign_in` plugin, so the web flow goes
/// through Firebase's popup. Both paths converge on the same Firebase user, and
/// the ID token from that user is what the API verifies.
class FirebaseSignInService implements FirebaseSignIn {
  FirebaseSignInService();

  FirebaseApp? _app;
  GoogleSignIn? _googleSignIn;
  bool _failed = false;

  @override
  bool get isAvailable => !_failed;

  @override
  Future<String?> signIn() async {
    final app = await _ensureInitialised();
    if (app == null) {
      throw const FirebaseSignInUnavailable(
        'Google sign-in could not be started on this device.',
      );
    }

    final auth = FirebaseAuth.instanceFor(app: app);

    try {
      final credential = kIsWeb
          ? await _webCredential(auth)
          : await _nativeCredential(auth);

      if (credential == null) return null; // Dismissed.

      final result = await auth.signInWithCredential(credential);
      // The ID token is what the API verifies; the app never asserts identity
      // itself.
      return await result.user?.getIdToken();
    } on FirebaseAuthException catch (error) {
      throw FirebaseSignInUnavailable(_describe(error));
    }
  }

  /// Web: Firebase owns the whole flow, including the popup.
  Future<AuthCredential?> _webCredential(FirebaseAuth auth) async {
    final provider = GoogleAuthProvider()
      ..setCustomParameters(const {'prompt': 'select_account'});

    final result = await auth.signInWithPopup(provider);
    return result.credential;
  }

  /// Native: the Google plugin obtains an ID token, which is exchanged for a
  /// Firebase credential.
  ///
  /// This is the documented path on Android and iOS. `signInWithProvider` is not
  /// supported there, which is why the plugin is involved at all.
  ///
  /// Note that google_sign_in 7.x returns *only* an ID token: the access token
  /// was removed from the authentication object, and it is not needed, because
  /// Firebase accepts a credential built from the ID token alone.
  Future<AuthCredential?> _nativeCredential(FirebaseAuth auth) async {
    final googleSignIn = _googleSignIn ??= GoogleSignIn.instance;

    if (!googleSignIn.supportsAuthenticate()) {
      throw const FirebaseSignInUnavailable(
        'Google sign-in is not available on this platform.',
      );
    }

    // 7.x requires an explicit initialisation. The server client id is what
    // makes Android return an ID token, and the client id covers builds where
    // no GoogleService-Info.plist supplies one.
    await googleSignIn.initialize(
      clientId: googleIosClientId.isEmpty ? null : googleIosClientId,
      serverClientId: googleServerClientId.isEmpty ? null : googleServerClientId,
    );

    final account = await googleSignIn.authenticate();
    final idToken = account.authentication.idToken;

    if (idToken == null || idToken.isEmpty) {
      throw const FirebaseSignInUnavailable(
        'Google did not return a sign-in token. Please try again.',
      );
    }

    return GoogleAuthProvider.credential(idToken: idToken);
  }

  /// Initialises Firebase once, or reports that it cannot be.
  Future<FirebaseApp?> _ensureInitialised() async {
    final existing = _app;
    if (existing != null) return existing;
    if (_failed) return null;

    try {
      // messagingSenderId is required by the SDK's options even though this app
      // never sends messages, which is why firebaseConfigured insists on it.
      final options = FirebaseOptions(
        apiKey: firebaseApiKey,
        appId: firebaseAppId,
        messagingSenderId: firebaseMessagingSenderId,
        projectId: firebaseProjectId,
        // Only the web popup flow uses this, so it stays optional.
        authDomain: firebaseAuthDomain.isEmpty ? null : firebaseAuthDomain,
      );

      _app = Firebase.apps.isEmpty
          ? await Firebase.initializeApp(options: options)
          : Firebase.app();
      return _app;
    } catch (_) {
      // Marked unavailable rather than thrown: a build without working Firebase
      // should still run, minus the Google button.
      _failed = true;
      return null;
    }
  }

  /// Turns a Firebase failure into something a reader can act on.
  String _describe(FirebaseAuthException error) {
    return switch (error.code) {
      'popup-closed-by-user' ||
      'cancelled' ||
      'web-context-cancelled' =>
        'Sign-in was cancelled.',
      'network-request-failed' => 'Cannot reach Google. Check your connection.',
      'account-exists-with-different-credential' =>
        'That email is already registered with a password. Sign in with your password instead.',
      'operation-not-allowed' =>
        'Google sign-in is not enabled for this project.',
      _ => 'Google sign-in did not complete. Please try again.',
    };
  }
}

/// Stands in when the build carries no Firebase configuration.
class UnavailableFirebaseSignIn implements FirebaseSignIn {
  const UnavailableFirebaseSignIn(this.message);

  final String message;

  @override
  bool get isAvailable => false;

  @override
  Future<String?> signIn() async => throw FirebaseSignInUnavailable(message);
}
