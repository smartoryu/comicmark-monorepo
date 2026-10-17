/// Form validation shared by the account screens.
///
/// These mirror the server's rules deliberately. The server is the authority —
/// it re-checks everything — but catching a problem before a round trip is what
/// makes a form feel responsive, and the messages match so a reader never sees
/// two different explanations for the same rule.
library;

/// The shortest password the API accepts.
const int minPasswordLength = 8;

/// The longest password the API accepts, which the hasher also enforces.
const int maxPasswordLength = 1024;

/// The longest email the API accepts, per RFC 5321.
const int maxEmailLength = 254;

/// Validates an email field.
///
/// Deliberately permissive: a stricter pattern rejects addresses that are
/// actually valid, and the server's own validator is what decides.
String? validateEmail(String? value) {
  final email = value?.trim() ?? '';

  if (email.isEmpty) return 'Enter your email address.';
  if (email.length > maxEmailLength) {
    return 'Email addresses cannot be longer than $maxEmailLength characters.';
  }

  // One @, something before it, and a dot with something after it in the domain.
  final at = email.indexOf('@');
  final validShape = at > 0 &&
      at == email.lastIndexOf('@') &&
      email.contains('.') &&
      !email.endsWith('.') &&
      !email.contains(' ');

  if (!validShape) return 'Enter a valid email address.';
  return null;
}

/// Validates a password field.
///
/// [requireLength] is false on the sign-in form, where an existing password may
/// predate the current rule; rejecting it there would lock a reader out with a
/// message about a rule their password never had to satisfy.
String? validatePassword(String? value, {bool requireLength = true}) {
  final password = value ?? '';

  if (password.isEmpty) return 'Enter your password.';
  if (!requireLength) {
    return password.length > maxPasswordLength
        ? 'Passwords cannot be longer than $maxPasswordLength characters.'
        : null;
  }

  if (password.length < minPasswordLength) {
    return 'Use at least $minPasswordLength characters.';
  }
  if (password.length > maxPasswordLength) {
    return 'Passwords cannot be longer than $maxPasswordLength characters.';
  }
  return null;
}

/// Validates that a password is not simply the email address.
///
/// The server refuses this, and saying so up front is friendlier than a 400.
String? validatePasswordNotEmail(String? password, String email) {
  final value = password ?? '';
  if (value.isEmpty) return 'Enter your password.';
  if (value.toLowerCase() == email.trim().toLowerCase()) {
    return 'Choose a password that is not your email address.';
  }
  return null;
}

/// Validates a required free-text field.
String? validateRequired(String? value, {String label = 'This field'}) {
  if ((value ?? '').trim().isEmpty) return '$label is required.';
  return null;
}
