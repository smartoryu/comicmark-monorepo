#!/usr/bin/env bash
#
# Firebase sign-in smoke test.
#
# Google sign-in needs a Firebase project, which is why the feature is optional
# and why its route is not mounted at all when nothing is configured. This script
# exercises the same code path locally against the Firebase Authentication
# emulator, so the wiring can be proven without a cloud project or credentials.
#
# What it checks:
#   * /auth/providers reports Firebase as available when it is configured
#   * /auth/firebase accepts an emulator-issued ID token and issues a ComicMark
#     session for it
#   * signing in again with the same identity reuses the account rather than
#     creating a second one
#   * a bogus token is refused
#
# Requires: Java (for the emulator), npx, jq, python3, go.
#
# Usage:  bash scripts/firebase-smoke.sh

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
APP_DIR="$ROOT/apps/go-modular"

WORK="$(mktemp -d)"
PROJECT_ID="comicmark-smoke"
EMULATOR_PORT="${FIREBASE_EMULATOR_PORT:-9099}"
API_PORT="${FIREBASE_SMOKE_API_PORT:-18780}"
API_BASE="http://127.0.0.1:$API_PORT"
EMULATOR="http://127.0.0.1:$EMULATOR_PORT"

EMULATOR_PID=""
API_PID=""

cleanup() {
    local status=$?
    [[ -n "$API_PID" ]] && kill "$API_PID" 2>/dev/null || true
    [[ -n "$EMULATOR_PID" ]] && kill "$EMULATOR_PID" 2>/dev/null || true
    # The emulator spawns a child; make sure nothing is left holding the port.
    pkill -f "firebase.*emulators:start" 2>/dev/null || true
    wait 2>/dev/null || true

    if [[ $status -ne 0 ]]; then
        echo
        echo "--- API log ---"; tail -20 "$WORK/api.log" 2>/dev/null || true
        echo "--- emulator log ---"; tail -20 "$WORK/emulator.log" 2>/dev/null || true
    fi
    rm -rf "$WORK"
    exit $status
}
trap cleanup EXIT

step() { printf '\n\033[1m%s\033[0m\n' "$1"; }
ok()   { printf '  \033[32m✓\033[0m %s\n' "$1"; }
fail() { printf '  \033[31m✗\033[0m %s\n' "$1" >&2; exit 1; }

assert_eq() {
    if [[ "$2" == "$3" ]]; then ok "$1"; else fail "$1 (expected '$2', got '$3')"; fi
}

for tool in jq python3 go npx; do
    command -v "$tool" >/dev/null 2>&1 || fail "$tool is required but not installed"
done

# Refuse to talk to somebody else's emulator. A stale one on the same port would
# belong to a different project, and the only symptom would be a confusing
# audience mismatch when the token is verified.
if lsof -nP -iTCP:"$EMULATOR_PORT" -sTCP:LISTEN >/dev/null 2>&1; then
    fail "port $EMULATOR_PORT is already in use; stop the other emulator or set FIREBASE_EMULATOR_PORT"
fi
if lsof -nP -iTCP:"$API_PORT" -sTCP:LISTEN >/dev/null 2>&1; then
    fail "port $API_PORT is already in use; set FIREBASE_SMOKE_API_PORT"
fi

step "Starting the Firebase Authentication emulator"
cat >"$WORK/firebase.json" <<JSON
{
  "emulators": {
    "auth": { "port": $EMULATOR_PORT },
    "ui": { "enabled": false }
  }
}
JSON

( cd "$WORK" && npx --yes firebase-tools emulators:start \
    --only auth --project "$PROJECT_ID" >"$WORK/emulator.log" 2>&1 ) &
EMULATOR_PID=$!

for _ in $(seq 1 120); do
    curl -sf -o /dev/null "$EMULATOR/" && break
    sleep 0.5
done
curl -sf -o /dev/null "$EMULATOR/" || fail "the auth emulator did not start (check $WORK/emulator.log)"
ok "emulator listening on $EMULATOR"

step "Building and starting the API against the emulator"
( cd "$APP_DIR" && go build -o "$WORK/comicmark-api" ./apps/api )

JWT_SECRET="firebase-smoke-secret-that-is-long-enough" \
DATABASE_PATH="$WORK/smoke.db" \
SERVER_ADDR="127.0.0.1:$API_PORT" \
APP_ENV=development \
APP_BASE_URL="$API_BASE" \
MAILER_DRIVER=console \
FIREBASE_PROJECT_ID="$PROJECT_ID" \
FIREBASE_AUTH_EMULATOR_HOST="127.0.0.1:$EMULATOR_PORT" \
"$WORK/comicmark-api" >"$WORK/api.log" 2>&1 &
API_PID=$!

for _ in $(seq 1 100); do
    curl -sf -o /dev/null "$API_BASE/healthz" && break
    sleep 0.1
done
curl -sf -o /dev/null "$API_BASE/healthz" || fail "the API did not start"

grep -q "firebase sign-in is enabled" "$WORK/api.log" \
    || fail "the API did not report Firebase as enabled"
ok "the API started with Firebase enabled"

# --- the provider is advertised ---------------------------------------------

step "Checking that the app is told Google sign-in is available"
PROVIDERS="$(curl -sf "$API_BASE/api/v1/auth/providers")"
assert_eq "/auth/providers reports firebase" "true" "$(jq -r .firebase <<<"$PROVIDERS")"
assert_eq "/auth/providers still reports password" "true" "$(jq -r .password <<<"$PROVIDERS")"

# --- an identity from the emulator ------------------------------------------

step "Creating an identity in the emulator"
# The emulator accepts any API key; the signUp endpoint is the emulator's own
# Identity Toolkit, and it returns a real (if unsigned) ID token.
SIGNUP="$(curl -s -X POST \
    "$EMULATOR/identitytoolkit.googleapis.com/v1/accounts:signUp?key=fake-key" \
    -H 'Content-Type: application/json' \
    -d '{"email":"google-reader@example.com","password":"correct horse battery","returnSecureToken":true}')"

ID_TOKEN="$(jq -r .idToken <<<"$SIGNUP")"
FIREBASE_UID="$(jq -r .localId <<<"$SIGNUP")"
[[ -n "$ID_TOKEN" && "$ID_TOKEN" != "null" ]] || fail "the emulator returned no ID token: $SIGNUP"
ok "emulator issued an ID token for uid $FIREBASE_UID"

# --- exchanging it ----------------------------------------------------------

step "Exchanging the ID token for a ComicMark session"
BODY="$(curl -s -o "$WORK/response.json" -w '%{http_code}' -X POST \
    "$API_BASE/api/v1/auth/firebase" \
    -H 'Content-Type: application/json' \
    -d "$(jq -n --arg t "$ID_TOKEN" '{id_token: $t}')")"
RESPONSE="$(cat "$WORK/response.json")"

assert_eq "the exchange succeeds" "200" "$BODY"
ACCESS="$(jq -r .access_token <<<"$RESPONSE")"
[[ -n "$ACCESS" && "$ACCESS" != "null" ]] || fail "no access token was issued: $RESPONSE"
ok "a ComicMark token pair was issued"

assert_eq "the account uses the provider's email" "google-reader@example.com" \
    "$(jq -r .user.email <<<"$RESPONSE")"

# The account's verification state must be whatever the provider claimed rather
# than an assumption. The emulator reports password accounts as unverified, unlike
# real Google, so the claim is read out of the token and compared instead of being
# hardcoded. Both directions are covered by the API tests, which drive the
# endpoint with a stubbed verifier.
CLAIMED="$(python3 - "$ID_TOKEN" <<PYCLAIM
import base64, json, sys
payload = sys.argv[1].split('.')[1]
payload += '=' * (-len(payload) % 4)
claims = json.loads(base64.urlsafe_b64decode(payload))
print(str(claims.get('email_verified', False)).lower())
PYCLAIM
)"
assert_eq "and reflects the provider's verification claim" "$CLAIMED" \
    "$(jq -r .user.email_verified <<<"$RESPONSE")"

ME="$(curl -sf -H "Authorization: Bearer $ACCESS" "$API_BASE/api/v1/auth/me")"
assert_eq "the session works" "google-reader@example.com" "$(jq -r .email <<<"$ME")"

# --- the identity is stable -------------------------------------------------

step "Signing in again with the same identity"
# A second sign-in for the same provider subject must land on the same account,
# not create another one, or a reader would accumulate empty duplicates.
SIGNIN="$(curl -s -X POST \
    "$EMULATOR/identitytoolkit.googleapis.com/v1/accounts:signInWithPassword?key=fake-key" \
    -H 'Content-Type: application/json' \
    -d '{"email":"google-reader@example.com","password":"correct horse battery","returnSecureToken":true}')"
SECOND_TOKEN="$(jq -r .idToken <<<"$SIGNIN")"
assert_eq "the emulator issued the same uid" "$FIREBASE_UID" "$(jq -r .localId <<<"$SIGNIN")"

SECOND="$(curl -s -X POST "$API_BASE/api/v1/auth/firebase" \
    -H 'Content-Type: application/json' \
    -d "$(jq -n --arg t "$SECOND_TOKEN" '{id_token: $t}')")"

assert_eq "the same account is reused" "$(jq -r .user.id <<<"$RESPONSE")" \
    "$(jq -r .user.id <<<"$SECOND")"
assert_eq "and a fresh session is issued" "false" \
    "$(jq -r --arg a "$ACCESS" '.access_token == $a' <<<"$SECOND")"

# --- a forged token ---------------------------------------------------------

step "A bogus ID token is refused"
CODE="$(curl -s -o "$WORK/response.json" -w '%{http_code}' -X POST \
    "$API_BASE/api/v1/auth/firebase" \
    -H 'Content-Type: application/json' \
    -d '{"id_token":"not-a-real-token"}')"
assert_eq "the exchange is refused" "401" "$CODE"
assert_eq "with a code the app can act on" "invalid_provider_token" \
    "$(jq -r .error.code <"$WORK/response.json")"

step "Firebase smoke test passed"
