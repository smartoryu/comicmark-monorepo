# ComicMark

Keep your place in every comic you read.

Copy a chapter URL, paste it into ComicMark, and the backend reads the page's
OpenGraph metadata to fill in the cover and the chapter. Your library then shows
where you are in each comic, with a one-tap **+1** to move to the next chapter.

The API and the web app are served from a single Go binary, so one address runs
the whole product. The same Flutter app also builds for Android and iOS.

```
apps/
  go-modular/     ComicMark API — Go, net/http, SQLite
  comic-mobile/   ComicMark app — Flutter (Android, iOS, web)
scripts/
  e2e.sh            end-to-end smoke test against a throwaway database
  firebase-smoke.sh Firebase sign-in against the Authentication emulator
```

---

## Requirements

| Tool | Version | Why |
| --- | --- | --- |
| go | 1.27.0 | `modernc.org/sqlite`, `x/net` and the validator all require go ≥ 1.26 |
| node | 24.19 | the monorepo tooling |
| pnpm | 11.23.0 | workspace manager |
| moon | ≥ 2.5.3 | task runner |
| flutter | 3.47+ | the app, including the web build |
| docker | any | optional: Mailpit for reading test email |
| python3, jq | any | used by the smoke-test scripts |

The first four are pinned in `.moon/toolchains.yml` and installed by `moon setup`.

---

## Quickstart

```bash
# 1. Install the pinned toolchains and dependencies.
moon setup
pnpm install

# 2. Let the API create its database and start.
moon run go-modular:dev          # http://localhost:8080/api/v1, health at /healthz

# 3. Run the app against it, in another terminal.
moon run comic-mobile:web        # http://localhost:5173
```

On its first run, `dev` creates `apps/go-modular/.env` from `.env.example` with a
freshly generated `JWT_SECRET`, because the API deliberately refuses to start
without one: a default secret would be a known value in every deployment that
forgot to change it. An existing `.env` is never touched.

`moon run comic-mobile:web` passes `API_BASE_URL=http://localhost:8080` and the API
allows that origin through `CORS_ORIGINS`, which is why the two ports can talk.

### One origin instead of two

To run the app and the API from the same address — no second port, no CORS:

```bash
moon run comic-mobile:web-embed  # builds the web app into the API's embedded static dir
moon run go-modular:serve        # http://localhost:8080 serves both
```

The embedded build passes no `API_BASE_URL`, so the app makes same-origin
requests. That is also how a deployment should look.

### Other ways to run the app

```bash
moon run comic-mobile:android    # Android emulator, API on 10.0.2.2:8080
moon run comic-mobile:ios        # iOS simulator, API on localhost:8080
```

---

## Reading the emails

Verification and password-reset links are emailed. Two ways to see them, chosen by
`MAILER_DRIVER`:

**`console`** (the default in development) prints each message to the API log,
including the link:

```
msg="mailer: email sent (console driver)" to=reader@example.com \
  link="http://localhost:5173/?verify_token=AbC123..."
```

Copy that link into a browser. `scripts/e2e.sh` does this automatically by grepping
the log.

**`smtp`** delivers for real. `pnpm run compose:up` starts the Mailpit stack that
the generated `compose.yaml` keeps enabled:

```bash
pnpm run compose:up
# MAILER_DRIVER=smtp in apps/go-modular/.env
# then read the mail at http://localhost:8025
```

Nothing else in ComicMark needs Docker: the database is a SQLite file.

---

## Configuration

Every setting has a default, so the API starts with only a JWT secret configured.
The full list with comments is in
[`apps/go-modular/.env.example`](apps/go-modular/.env.example); copy it to
`apps/go-modular/.env`. The ones worth knowing:

| Variable | Default | Notes |
| --- | --- | --- |
| `JWT_SECRET` | *(none)* | **Required.** `openssl rand -hex 32`. Startup fails without it outside development |
| `APP_BASE_URL` | `http://localhost:5173` | Where verification and reset links point. For the embedded build, use the API's own origin |
| `DATABASE_PATH` | `./data/comicmark.db` | SQLite file; the parent directory is created |
| `SERVER_ADDR` | `:8080` | |
| `CORS_ORIGINS` | `http://localhost:5173` | Only needed when the app is on a different origin |
| `MAILER_DRIVER` | `console` | `console` or `smtp` |
| `AUTH_REQUIRE_EMAIL_VERIFICATION` | `false` | `true` in production. Refused in production alongside the console mailer, because it would lock every new account out |
| `SCRAPER_ALLOW_PRIVATE_IPS` | `false` | **See the warning below.** Development and the end-to-end script only |
| `FIREBASE_PROJECT_ID` | *(none)* | Setting it enables Google sign-in, which is otherwise absent entirely |

### ⚠️ `SCRAPER_ALLOW_PRIVATE_IPS`

`POST /bookmarks/parse` fetches a URL the reader supplies, which makes it the one
place in ComicMark that can be pointed at any network destination. By default the
scraper refuses anything that is not a globally routable address: loopback,
private ranges, link-local (including the `169.254.169.254` cloud metadata
endpoint), multicast, and carrier-grade NAT. It re-checks the address actually
connected to — which is what closes the DNS-rebinding gap — and re-validates every
redirect hop.

Setting this flag to `true` disables all of that so the end-to-end script can
scrape a fixture on loopback. **Configuration refuses the flag outright when
`APP_ENV=production`.** Do not set it anywhere real.

---

## Secrets

Nothing secret is committed, and nothing credential-bearing is in this
repository's history.

- **Local configuration lives in `apps/go-modular/.env`**, which is gitignored and
  generated with a fresh `JWT_SECRET` on first run. `.env.example` documents every
  setting and contains no values.
- **No credentials are baked into the source.** Firebase configuration is passed at
  build time with `--dart-define`; a Firebase web API key is a public identifier
  rather than a secret, and no service-account file is stored here.
- **`SMTP_PASSWORD` and the SMTP settings** come from the environment. Mailpit,
  the local mail catcher, needs no credentials at all.
- The organisational template's Postgres, MinIO, Redis and ClickHouse stacks were
  **removed** rather than configured: ComicMark uses SQLite and has no object
  storage, cache or analytics to run, and those stacks shipped hard-coded
  development passwords. They can be restored from
  <https://oss.zero-one-group.com/monorepo/templates> if one is ever needed, at
  which point their credentials belong in the environment.

A deployment is expected to supply `JWT_SECRET`, `DATABASE_PATH` and any mail or
Firebase settings through its own environment; the API refuses to start without a
secret rather than falling back to a default.

---

## Google sign-in (optional)

ComicMark works without it. The feature is designed to be *absent* rather than
broken when unconfigured:

- The API mounts `/api/v1/auth/firebase` **only** when `FIREBASE_PROJECT_ID` is set.
- `GET /api/v1/auth/providers` reports which methods exist.
- The app asks that endpoint and renders the Google button only when the answer is
  yes, so there is never a button that cannot work.

To enable it against a real project:

```bash
# 1. Backend: point at the project.
#    FIREBASE_PROJECT_ID=your-project
#    FIREBASE_CREDENTIALS_FILE=./service-account.json   (or use Application Default Credentials)

# 2. App: pass the web configuration at build time. None of it is secret.
moon run comic-mobile:web -- \
  --dart-define=FIREBASE_PROJECT_ID=your-project \
  --dart-define=FIREBASE_API_KEY=... \
  --dart-define=FIREBASE_MESSAGING_SENDER_ID=... \
  --dart-define=FIREBASE_APP_ID=... \
  --dart-define=FIREBASE_AUTH_DOMAIN=your-project.firebaseapp.com
```

On the web the sign-in goes through a Firebase popup. On Android and iOS it goes
through the Google plugin, which needs `GOOGLE_SERVER_CLIENT_ID` (Android) and
optionally `GOOGLE_IOS_CLIENT_ID`.

### Without a Firebase project

The Authentication emulator is enough to exercise the whole path:

```bash
bash scripts/firebase-smoke.sh
```

It starts the emulator, creates an identity, exchanges its ID token for a ComicMark
session, and checks that signing in again reuses the account.

---

## Development

| Command | What it does |
| --- | --- |
| `moon run go-modular:dev` | API with reload on Go and SQL edits, migrations applied |
| `moon run go-modular:test` | Go tests |
| `moon run go-modular:test-race` | Go tests under the race detector |
| `moon run go-modular:e2e` | end-to-end smoke test (`scripts/e2e.sh`) |
| `moon run go-modular:lint` / `:format` | `go vet` / `gofmt` |
| `moon run comic-mobile:web` | app in Chrome against the API |
| `moon run comic-mobile:test` / `:analyze` | Flutter tests / analysis |
| `moon run comic-mobile:web-embed` | build the web app into the API binary |
| `moon run :test` | everything |

Migrations are plain SQL in
`apps/go-modular/internal/pkg/database/migrations/`, applied on boot by
`--auto-migrate` (on by default in development, off otherwise), tracked in a
`schema_migrations` table. Add a file named `NNNN_description.sql`.

The API's endpoints are documented in the handler sources, where each handler
carries the request and response shapes it deals in. The organisational
`go-modular` template ships generated Swagger and a Scalar UI, but this build
hand-rolls its HTTP layer and does not include that generator; adding it would
mean adopting `swag` and its annotations across the handlers.

---

## How it fits together

**Authentication.** Email and password plus optional Google. Passwords are
argon2id. Access tokens are 15-minute HS256 JWTs; refresh tokens are 30-day JWTs
stored only as SHA-256 hashes and rotated on every use. Presenting a refresh token
that has already been rotated is treated as theft: the whole session is revoked at
once, because there is no way to tell the legitimate client from a thief.

**Email verification and password reset.** Single-use tokens, hashed at rest, with
a short TTL. A password reset revokes every session. The resend endpoint is
deliberately unauthenticated, because an unverified reader has no token to
authenticate with.

**Device sessions.** Every sign-in creates a session, and the app lists them so a
reader can sign out a device they have lost. Revoking a session takes effect on the
device's next request rather than whenever its access token happens to expire.

**The +1 action.** `chapter` keeps the site's own wording and `chapter_number` holds
the parsed value. Advancing rewrites only the digits, so "Chapter 1050" becomes
"Chapter 1051". When a chapter has no number the API answers `409
chapter_not_numeric` and the app opens an editor rather than guessing, because a
bookmark silently advanced to the wrong chapter is worse than one that asks.

**The scraper.** Reads `og:title`, `og:image` and friends, falls back to the
Twitter card and then `<title>`, and resolves relative covers against the final URL
after redirects. A site that blocks the bot is reported as a *partial* preview
rather than an error, so the reader can always fill in the gaps by hand.

---

## Web notes

- The Flutter web build is embedded in the Go binary, so one process serves the
  app, its assets and the API. Client-side routes and the emailed
  `/verify-email` and `/reset-password` links all return the app shell, so a hard
  refresh or a deep link works.
- API paths that do not exist return JSON `404`, never the shell, so a mistyped
  endpoint cannot look like a successful page load.
- Forward navigation pushes a history entry, so the browser back button and the
  Android back gesture return to where the reader came from.

---

## Testing

```bash
moon run :test              # Go and Flutter, everything
moon run go-modular:e2e     # 94 assertions through a real server
bash scripts/firebase-smoke.sh   # Google sign-in via the emulator
```

The end-to-end script starts its own server on a free port with a throwaway
database, serves a fixture comic site from a local directory, and reads the
verification and reset links out of the log rather than needing an inbox.
