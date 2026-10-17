#!/usr/bin/env bash
#
# End-to-end smoke test for the ComicMark API.
#
# This drives a real server over HTTP against a throwaway database, so it covers
# the wiring that unit tests deliberately fake: routing, JSON shapes, status
# codes, the mailer and the scraper.
#
# Two details make it possible to run with no infrastructure at all:
#
#   * MAILER_DRIVER=console prints the action links to the log, and this script
#     greps them out. That is how verification and reset tokens are harvested
#     without an SMTP server or an inbox.
#   * A fixture "comic site" is served from a local directory, and
#     SCRAPER_ALLOW_PRIVATE_IPS=true is what lets the scraper reach it. That
#     flag is refused in production by the configuration validator.
#
# Usage:  bash scripts/e2e.sh
# Exits non-zero on the first failure, so it is safe to wire into CI.

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
APP_DIR="$ROOT/apps/go-modular"

WORK="$(mktemp -d)"
SERVER_LOG="$WORK/server.log"
FIXTURE_DIR="$WORK/fixture"
DB_PATH="$WORK/e2e.db"

SERVER_PID=""
FIXTURE_PID=""

cleanup() {
    local status=$?
    [[ -n "$SERVER_PID" ]] && kill "$SERVER_PID" 2>/dev/null || true
    [[ -n "$FIXTURE_PID" ]] && kill "$FIXTURE_PID" 2>/dev/null || true
    wait 2>/dev/null || true

    if [[ $status -ne 0 ]]; then
        echo
        echo "--- server log (last 40 lines) ---"
        tail -40 "$SERVER_LOG" 2>/dev/null || true
    fi
    rm -rf "$WORK"
    exit $status
}
trap cleanup EXIT

# --- output helpers ---------------------------------------------------------

PASS=0
step() { printf '\n\033[1m%s\033[0m\n' "$1"; }
ok()   { PASS=$((PASS + 1)); printf '  \033[32m✓\033[0m %s\n' "$1"; }
fail() { printf '  \033[31m✗\033[0m %s\n' "$1" >&2; exit 1; }

# assert_eq <description> <expected> <actual>
assert_eq() {
    if [[ "$2" == "$3" ]]; then ok "$1"; else fail "$1 (expected '$2', got '$3')"; fi
}

# assert_contains <description> <haystack> <needle>
assert_contains() {
    if [[ "$2" == *"$3"* ]]; then ok "$1"; else fail "$1 (expected to find '$3' in '$2')"; fi
}

require_tools() {
    for tool in jq python3 curl go; do
        command -v "$tool" >/dev/null 2>&1 || fail "$tool is required but not installed"
    done
}

# --- setup ------------------------------------------------------------------

require_tools

# A port that is very unlikely to be in use, so the run does not collide with a
# development server.
PORT="${E2E_PORT:-18765}"
while lsof -nP -iTCP:"$PORT" -sTCP:LISTEN >/dev/null 2>&1; do
    PORT=$((PORT + 1))
done
BASE="http://127.0.0.1:$PORT"

FIXTURE_PORT="${E2E_FIXTURE_PORT:-18766}"
while lsof -nP -iTCP:"$FIXTURE_PORT" -sTCP:LISTEN >/dev/null 2>&1; do
    FIXTURE_PORT=$((FIXTURE_PORT + 1))
done
FIXTURE_BASE="http://127.0.0.1:$FIXTURE_PORT"

step "Starting a fixture comic site on $FIXTURE_BASE"
mkdir -p "$FIXTURE_DIR/one-piece-chapter-1050"
cat >"$FIXTURE_DIR/one-piece-chapter-1050/index.html" <<'HTML'
<!doctype html>
<html lang="en">
  <head>
    <title>One Piece Chapter 1050 - Fixture Reader</title>
    <meta property="og:title" content="One Piece" />
    <meta property="og:site_name" content="Fixture Reader" />
    <meta property="og:image" content="/covers/one-piece.jpg" />
    <meta name="description" content="Read One Piece Chapter 1050 online." />
  </head>
  <body><h1>One Piece</h1><h2>Chapter 1050</h2></body>
</html>
HTML
mkdir -p "$FIXTURE_DIR/covers"
# A one-pixel PNG, so the cover link resolves to something real.
printf '\x89PNG\r\n\x1a\n' >"$FIXTURE_DIR/covers/one-piece.jpg"

# A page with nothing useful, for the manual-fallback path.
mkdir -p "$FIXTURE_DIR/blocked"
cat >"$FIXTURE_DIR/blocked/index.html" <<'HTML'
<!doctype html>
<html><head><title>Prologue</title></head><body>Nothing here.</body></html>
HTML

python3 -m http.server "$FIXTURE_PORT" --bind 127.0.0.1 --directory "$FIXTURE_DIR" \
    >"$WORK/fixture.log" 2>&1 &
FIXTURE_PID=$!

for _ in $(seq 1 50); do
    curl -sf -o /dev/null "$FIXTURE_BASE/" && break
    sleep 0.1
done
curl -sf -o /dev/null "$FIXTURE_BASE/" || fail "the fixture site did not start"
ok "fixture site serving $FIXTURE_DIR"

step "Building the API"
( cd "$APP_DIR" && go build -o "$WORK/comicmark-api" ./apps/api )
ok "built $WORK/comicmark-api"

step "Starting the API on $BASE"
# The rate limits are raised for this run: a smoke test hammers the account
# endpoints from one address far faster than a person could, and tripping the
# limiter would only test the limiter. That limiter has its own unit tests.
JWT_SECRET="e2e-secret-that-is-long-enough-to-pass-validation" \
DATABASE_PATH="$DB_PATH" \
SERVER_ADDR="127.0.0.1:$PORT" \
APP_ENV=development \
APP_BASE_URL="$BASE" \
MAILER_DRIVER=console \
SCRAPER_ALLOW_PRIVATE_IPS=true \
SCRAPER_CONTACT_URL="https://example.com/bot" \
RATE_LIMIT_RPS=500 \
RATE_LIMIT_BURST=1000 \
AUTH_RATE_LIMIT_PER_MIN=500 \
SENSITIVE_RATE_LIMIT_PER_MIN=500 \
"$WORK/comicmark-api" >"$SERVER_LOG" 2>&1 &
SERVER_PID=$!

for _ in $(seq 1 100); do
    curl -sf -o /dev/null "$BASE/healthz" && break
    sleep 0.1
done
curl -sf -o /dev/null "$BASE/healthz" || fail "the API did not start"

HEALTH="$(curl -sf "$BASE/healthz")"
assert_eq "healthz reports ok" "ok" "$(jq -r .status <<<"$HEALTH")"
assert_eq "healthz reports the database ok" "ok" "$(jq -r .db <<<"$HEALTH")"

# The schema was applied on boot.
assert_eq "migrations were applied" "0001_init" \
    "$(python3 - "$DB_PATH" <<'PY'
import sqlite3, sys
with sqlite3.connect(sys.argv[1]) as db:
    print(db.execute("SELECT version FROM schema_migrations ORDER BY version").fetchone()[0])
PY
)"

# --- helpers ----------------------------------------------------------------

# api <method> <path> [body] [token]
#
# Prints the body and records the status in a file. A file rather than a
# variable, because this is always called inside a command substitution and a
# variable assigned there would not reach the caller.
api() {
    local method="$1" path="$2" body="${3:-}" token="${4:-}"
    local args=(-s -o "$WORK/response.json" -w '%{http_code}' -X "$method" "$BASE$path")

    [[ -n "$body" ]] && args+=(-H 'Content-Type: application/json' -d "$body")
    [[ -n "$token" ]] && args+=(-H "Authorization: Bearer $token")

    curl "${args[@]}" >"$WORK/status"
    cat "$WORK/response.json"
}

# status -> the HTTP status of the most recent api call
status() { cat "$WORK/status"; }

# token_from_log <parameter> -> the most recent matching token
token_from_log() {
    grep -o "$1=[A-Za-z0-9_-]*" "$SERVER_LOG" | tail -1 | cut -d= -f2
}

TOKEN=""
ACCESS=""
REFRESH=""

# --- registration -----------------------------------------------------------

step "Registering an account"
BODY="$(api POST /api/v1/auth/register \
    '{"email":"reader@example.com","password":"correct horse battery","display_name":"E2E Reader"}')"
assert_eq "register returns 201" "201" "$(status)"
assert_eq "the new account starts unverified" "false" "$(jq -r .user.email_verified <<<"$BODY")"
ACCESS="$(jq -r .access_token <<<"$BODY")"
REFRESH="$(jq -r .refresh_token <<<"$BODY")"
SESSION="$(jq -r .session_id <<<"$BODY")"
[[ -n "$ACCESS" && "$ACCESS" != "null" ]] || fail "register returned no access token"
ok "received a token pair and a session"

BODY="$(api POST /api/v1/auth/register \
    '{"email":"reader@example.com","password":"correct horse battery"}')"
assert_eq "a duplicate address is refused" "409" "$(status)"
assert_eq "with a nameable code" "email_taken" "$(jq -r .error.code <<<"$BODY")"

BODY="$(api POST /api/v1/auth/register '{"email":"weak@example.com","password":"short"}')"
assert_eq "a short password is refused" "400" "$(status)"
assert_eq "against the password field" "password" \
    "$(jq -r '.error.details | keys[0]' <<<"$BODY")"

# --- email verification -----------------------------------------------------

step "Verifying the email through the mailed link"
sleep 0.3
TOKEN="$(token_from_log verify_token)"
[[ -n "$TOKEN" ]] || fail "no verification link was printed by the console mailer"
ok "harvested a verification token from the log"

BODY="$(api POST /api/v1/auth/verification/email/validate "{\"token\":\"$TOKEN\"}")"
assert_eq "the token validates" "200" "$(status)"
assert_eq "and the account is verified" "true" "$(jq -r .email_verified <<<"$BODY")"

BODY="$(api POST /api/v1/auth/verification/email/validate "{\"token\":\"$TOKEN\"}")"
assert_eq "the same token cannot be reused" "401" "$(status)"
assert_eq "reported as an invalid token" "token_invalid" "$(jq -r .error.code <<<"$BODY")"

BODY="$(api POST /api/v1/auth/verification/email/initiate '{"email":"stranger@example.com"}')"
assert_eq "resending for an unknown address is accepted" "202" "$(status)"

# --- sign-in ----------------------------------------------------------------

step "Signing in"
BODY="$(api POST /api/v1/auth/login '{"email":"reader@example.com","password":"correct horse battery"}')"
assert_eq "sign-in succeeds" "200" "$(status)"
ACCESS="$(jq -r .access_token <<<"$BODY")"
REFRESH="$(jq -r .refresh_token <<<"$BODY")"
ok "received a fresh token pair"

BODY="$(api POST /api/v1/auth/login '{"email":"reader@example.com","password":"wrong password"}')"
assert_eq "a wrong password is refused" "401" "$(status)"
assert_eq "generically" "invalid_credentials" "$(jq -r .error.code <<<"$BODY")"

BODY="$(api GET /api/v1/auth/me)"
assert_eq "an unauthenticated read is refused" "401" "$(status)"

BODY="$(api GET /api/v1/auth/me '' "$ACCESS")"
assert_eq "the token identifies the reader" "reader@example.com" "$(jq -r .email <<<"$BODY")"

# --- preview and save -------------------------------------------------------

step "Previewing a chapter link"
BODY="$(api POST /api/v1/bookmarks/parse "{\"url\":\"$FIXTURE_BASE/one-piece-chapter-1050\"}" "$ACCESS")"
assert_eq "the preview succeeds" "200" "$(status)"
assert_eq "with the page title" "One Piece" "$(jq -r .title <<<"$BODY")"
assert_eq "the chapter from the URL" "1050" "$(jq -r .chapter_number <<<"$BODY")"
assert_eq "and a cover resolved against the page" \
    "$FIXTURE_BASE/covers/one-piece.jpg" "$(jq -r .cover_url <<<"$BODY")"
assert_eq "the preview is complete" "false" "$(jq -r .partial <<<"$BODY")"
assert_eq "nothing was saved by previewing" "0" \
    "$(jq -r .total <<<"$(api GET /api/v1/bookmarks '' "$ACCESS")")"

# Schemes are refused regardless of the private-address setting below.
BODY="$(api POST /api/v1/bookmarks/parse '{"url":"file:///etc/passwd"}' "$ACCESS")"
assert_eq "a file link is refused" "400" "$(status)"
assert_eq "as a link the reader can fix" "url_not_allowed" "$(jq -r .error.code <<<"$BODY")"

BODY="$(api POST /api/v1/bookmarks/parse '{"url":"not-a-url"}' "$ACCESS")"
assert_eq "a link with no scheme is refused" "400" "$(status)"

# Note on what this script cannot check: private addresses are refused by the
# scraper guard, but this run sets SCRAPER_ALLOW_PRIVATE_IPS=true so that the
# fixture site on loopback can be reached at all. That refusal, and the fact that
# production rejects the flag outright, are covered by the guard's own tests.

# A page that cannot be fetched is a partial preview rather than an error, which
# is the manual-fallback path the app depends on whenever a site is down.
BODY="$(api POST /api/v1/bookmarks/parse '{"url":"http://127.0.0.1:9/nothing"}' "$ACCESS")"
assert_eq "an unreachable page is not an error" "200" "$(status)"
assert_eq "it comes back as a partial preview" "true" "$(jq -r .partial <<<"$BODY")"
assert_eq "with a reason the reader can see" "true"     "$(jq -r '.warnings | length > 0' <<<"$BODY")"

step "Saving the bookmark"
BODY="$(api POST /api/v1/bookmarks "{
    \"title\":\"One Piece\",
    \"current_url\":\"$FIXTURE_BASE/one-piece-chapter-1050\",
    \"chapter\":\"Chapter 1050\",
    \"chapter_number\":1050,
    \"cover_url\":\"$FIXTURE_BASE/covers/one-piece.jpg\"
}" "$ACCESS")"
assert_eq "the bookmark is created" "201" "$(status)"
BOOKMARK_ID="$(jq -r .id <<<"$BODY")"
assert_eq "with the default status" "reading" "$(jq -r .status <<<"$BODY")"
assert_eq "and the host derived from the link" "127.0.0.1" "$(jq -r .source_host <<<"$BODY")"

BODY="$(api POST /api/v1/bookmarks "{
    \"title\":\"One Piece\",
    \"current_url\":\"$FIXTURE_BASE/one-piece-chapter-1050\"
}" "$ACCESS")"
assert_eq "saving the same link twice is refused" "409" "$(status)"
assert_eq "with a nameable code" "duplicate_bookmark" "$(jq -r .error.code <<<"$BODY")"

# --- the +1 action ----------------------------------------------------------

step "Advancing the chapter"
BODY="$(api PATCH "/api/v1/bookmarks/$BOOKMARK_ID/read-next" '' "$ACCESS")"
assert_eq "the chapter advances" "200" "$(status)"
assert_eq "to the next number" "1051" "$(jq -r .chapter_number <<<"$BODY")"
assert_eq "preserving the site's wording" "Chapter 1051" "$(jq -r .chapter <<<"$BODY")"

BODY="$(api GET "/api/v1/bookmarks/$BOOKMARK_ID" '' "$ACCESS")"
assert_eq "and the change persisted" "Chapter 1051" "$(jq -r .chapter <<<"$BODY")"

step "A chapter with no number asks instead of guessing"
BODY="$(api POST /api/v1/bookmarks "{
    \"title\":\"Fixture Comic\",
    \"current_url\":\"$FIXTURE_BASE/blocked\",
    \"chapter\":\"Prologue\"
}" "$ACCESS")"
assert_eq "the bookmark is created" "201" "$(status)"
PROLOGUE_ID="$(jq -r .id <<<"$BODY")"
assert_eq "with no chapter number" "null" "$(jq -r .chapter_number <<<"$BODY")"

BODY="$(api PATCH "/api/v1/bookmarks/$PROLOGUE_ID/read-next" '' "$ACCESS")"
assert_eq "advancing it is refused" "409" "$(status)"
assert_eq "with the code the app branches on" "chapter_not_numeric" "$(jq -r .error.code <<<"$BODY")"

BODY="$(api PATCH "/api/v1/bookmarks/$PROLOGUE_ID" '{"chapter":"Chapter 4","chapter_number":4}' "$ACCESS")"
assert_eq "editing the chapter is accepted" "200" "$(status)"

BODY="$(api PATCH "/api/v1/bookmarks/$PROLOGUE_ID/read-next" '' "$ACCESS")"
assert_eq "and then advancing works" "Chapter 5" "$(jq -r .chapter <<<"$BODY")"

# --- the library ------------------------------------------------------------

step "Listing the library"
BODY="$(api GET "/api/v1/bookmarks" '' "$ACCESS")"
assert_eq "both bookmarks are listed" "2" "$(jq -r .total <<<"$BODY")"
assert_eq "the most recently read comes first" "$PROLOGUE_ID" \
    "$(jq -r '.bookmarks[0].id' <<<"$BODY")"

BODY="$(api GET "/api/v1/bookmarks?q=one%20piece" '' "$ACCESS")"
assert_eq "searching by title narrows the list" "1" "$(jq -r .total <<<"$BODY")"

BODY="$(api GET "/api/v1/bookmarks?status=reading&limit=1" '' "$ACCESS")"
assert_eq "filtering by status narrows the list" "2" "$(jq -r .total <<<"$BODY")"
assert_eq "and the page size is applied" "1" "$(jq -r '.bookmarks | length' <<<"$BODY")"
assert_eq "and echoed back" "1" "$(jq -r .limit <<<"$BODY")"

BODY="$(api GET "/api/v1/bookmarks?status=nonsense" '' "$ACCESS")"
assert_eq "an unknown status is refused" "400" "$(status)"

# --- isolation --------------------------------------------------------------

step "One reader cannot reach another reader's bookmark"
BODY="$(api POST /api/v1/auth/register '{"email":"other@example.com","password":"another good password"}')"
OTHER_ACCESS="$(jq -r .access_token <<<"$BODY")"

BODY="$(api GET "/api/v1/bookmarks/$BOOKMARK_ID" '' "$OTHER_ACCESS")"
assert_eq "reading it is not found" "404" "$(status)"

BODY="$(api PATCH "/api/v1/bookmarks/$BOOKMARK_ID" '{"title":"Hijacked"}' "$OTHER_ACCESS")"
assert_eq "editing it is not found" "404" "$(status)"

BODY="$(api DELETE "/api/v1/bookmarks/$BOOKMARK_ID" '' "$OTHER_ACCESS")"
assert_eq "deleting it is not found" "404" "$(status)"

assert_eq "the bookmark is untouched" "One Piece" \
    "$(jq -r .title <<<"$(api GET "/api/v1/bookmarks/$BOOKMARK_ID" '' "$ACCESS")")"

# --- refresh rotation -------------------------------------------------------

step "Rotating the refresh token"
BODY="$(api POST /api/v1/auth/refresh "{\"refresh_token\":\"$REFRESH\"}")"
assert_eq "a refresh is accepted" "200" "$(status)"
ROTATED="$(jq -r .refresh_token <<<"$BODY")"
ROTATED_ACCESS="$(jq -r .access_token <<<"$BODY")"
[[ "$ROTATED" != "$REFRESH" ]] || fail "the refresh token was not rotated"
ok "the token was rotated"

BODY="$(api POST /api/v1/auth/refresh "{\"refresh_token\":\"$REFRESH\"}")"
assert_eq "replaying the old token is refused" "401" "$(status)"
assert_eq "as a reuse rather than a plain failure" "token_reused" "$(jq -r .error.code <<<"$BODY")"

BODY="$(api POST /api/v1/auth/refresh "{\"refresh_token\":\"$ROTATED\"}")"
assert_eq "and the whole family is cut off" "401" "$(status)"

BODY="$(api GET /api/v1/auth/me '' "$ROTATED_ACCESS")"
assert_eq "the access token from the replayed session is dead too" "401" "$(status)"

# --- device sessions --------------------------------------------------------

step "Managing devices"
# The replay check above revoked the session that ACCESS belonged to, which is
# the behaviour being asserted there, so start this step with a fresh one.
BODY="$(api POST /api/v1/auth/login '{"email":"reader@example.com","password":"correct horse battery"}')"
assert_eq "signing in after a revoked session works" "200" "$(status)"
ACCESS="$(jq -r .access_token <<<"$BODY")"

# The count is measured relative to what is already signed in, because the
# earlier steps left sessions behind.
BODY="$(api GET /api/v1/auth/sessions '' "$ACCESS")"
BEFORE="$(jq -r '.sessions | length' <<<"$BODY")"
assert_eq "the calling device is the only one marked current" "1" \
    "$(jq -r '[.sessions[] | select(.current)] | length' <<<"$BODY")"

BODY="$(api POST /api/v1/auth/login '{"email":"reader@example.com","password":"correct horse battery"}')"
PHONE_ACCESS="$(jq -r .access_token <<<"$BODY")"
PHONE_SESSION="$(jq -r .session_id <<<"$BODY")"
PHONE_REFRESH="$(jq -r .refresh_token <<<"$BODY")"

BODY="$(api POST /api/v1/auth/login '{"email":"reader@example.com","password":"correct horse battery"}')"
DESKTOP_ACCESS="$(jq -r .access_token <<<"$BODY")"
DESKTOP_SESSION="$(jq -r .session_id <<<"$BODY")"

BODY="$(api GET /api/v1/auth/sessions '' "$PHONE_ACCESS")"
assert_eq "each sign-in adds a session" "$((BEFORE + 2))" "$(jq -r '.sessions | length' <<<"$BODY")"
assert_eq "and the calling device is marked" "true" \
    "$(jq -r --arg id "$PHONE_SESSION" '.sessions[] | select(.id == $id) | .current' <<<"$BODY")"

BODY="$(api DELETE "/api/v1/auth/sessions/$PHONE_SESSION" '' "$PHONE_ACCESS")"
assert_eq "a device cannot revoke itself" "409" "$(status)"
assert_eq "with a code the app acts on" "cannot_revoke_current" "$(jq -r .error.code <<<"$BODY")"

BODY="$(api PATCH "/api/v1/auth/sessions/$DESKTOP_SESSION" '{"device_label":"Work laptop"}' "$PHONE_ACCESS")"
assert_eq "a device can be renamed" "200" "$(status)"
assert_eq "and the new name comes back" "Work laptop" "$(jq -r .device_label <<<"$BODY")"

BODY="$(api DELETE "/api/v1/auth/sessions/$DESKTOP_SESSION" '' "$PHONE_ACCESS")"
assert_eq "the other device can be signed out" "204" "$(status)"

BODY="$(api GET /api/v1/auth/sessions '' "$PHONE_ACCESS")"
assert_eq "and its session is gone from the list" "$((BEFORE + 1))" \
    "$(jq -r '.sessions | length' <<<"$BODY")"

BODY="$(api GET /api/v1/auth/me '' "$DESKTOP_ACCESS")"
assert_eq "and is signed out at once" "401" "$(status)"

BODY="$(api POST /api/v1/auth/refresh "{\"refresh_token\":\"$PHONE_REFRESH\"}")"
assert_eq "while the calling device keeps working" "200" "$(status)"

# --- password reset ---------------------------------------------------------

step "Resetting the password"
BODY="$(api POST /api/v1/auth/login '{"email":"reader@example.com","password":"correct horse battery"}')"
RESET_SESSION_ACCESS="$(jq -r .access_token <<<"$BODY")"

BODY="$(api POST /api/v1/auth/password/forgot '{"email":"reader@example.com"}')"
assert_eq "a reset is accepted" "202" "$(status)"

BODY="$(api POST /api/v1/auth/password/forgot '{"email":"nobody@example.com"}')"
assert_eq "an unknown address answers the same way" "202" "$(status)"

sleep 0.3
TOKEN="$(token_from_log reset_token)"
[[ -n "$TOKEN" ]] || fail "no reset link was printed by the console mailer"
ok "harvested a reset token from the log"

BODY="$(api POST /api/v1/auth/password/reset "{\"token\":\"$TOKEN\",\"new_password\":\"short\"}")"
assert_eq "a weak new password is refused" "400" "$(status)"

BODY="$(api POST /api/v1/auth/password/reset "{\"token\":\"$TOKEN\",\"new_password\":\"a brand new password\"}")"
assert_eq "a valid reset succeeds" "204" "$(status)"

BODY="$(api GET /api/v1/auth/me '' "$RESET_SESSION_ACCESS")"
assert_eq "every session is signed out by the reset" "401" "$(status)"

BODY="$(api POST /api/v1/auth/login '{"email":"reader@example.com","password":"a brand new password"}')"
assert_eq "the new password works" "200" "$(status)"
ACCESS="$(jq -r .access_token <<<"$BODY")"

BODY="$(api POST /api/v1/auth/login '{"email":"reader@example.com","password":"correct horse battery"}')"
assert_eq "and the old one does not" "401" "$(status)"

# --- changing the password --------------------------------------------------

step "Changing the password while signed in"
BODY="$(api PUT /api/v1/auth/password '{"current_password":"wrong","new_password":"another good password"}' "$ACCESS")"
assert_eq "a wrong current password is refused" "400" "$(status)"
assert_eq "against the current-password field" "current_password" \
    "$(jq -r '.error.details | keys[0]' <<<"$BODY")"

BODY="$(api PUT /api/v1/auth/password '{"current_password":"a brand new password","new_password":"another good password"}' "$ACCESS")"
assert_eq "a correct one is accepted" "204" "$(status)"

BODY="$(api GET /api/v1/auth/me '' "$ACCESS")"
assert_eq "and the caller stays signed in" "200" "$(status)"

# --- deleting ---------------------------------------------------------------

step "Removing a bookmark"
BODY="$(api DELETE "/api/v1/bookmarks/$BOOKMARK_ID" '' "$ACCESS")"
assert_eq "the bookmark is removed" "204" "$(status)"

BODY="$(api GET "/api/v1/bookmarks/$BOOKMARK_ID" '' "$ACCESS")"
assert_eq "and is gone" "404" "$(status)"

# --- done -------------------------------------------------------------------

# The server is stopped by the trap, which also prints the log on failure.
step "All end-to-end checks passed ($PASS assertions)"
