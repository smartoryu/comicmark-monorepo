-- ComicMark initial schema.
--
-- Conventions used throughout:
--   * ids are UUIDv7 stored as TEXT, so they sort by creation time
--   * timestamps are RFC3339 UTC strings (SQLite has no date type), which keeps
--     comparisons lexicographic and avoids driver-specific time handling
--   * foreign keys are always declared; PRAGMA foreign_keys=1 is set in the DSN

CREATE TABLE users (
    id                TEXT PRIMARY KEY,
    email             TEXT NOT NULL,
    display_name      TEXT,
    avatar_url        TEXT,
    -- argon2id PHC string. NULL for accounts that only exist through a social
    -- provider.
    password_hash     TEXT,
    -- NULL means unverified.
    email_verified_at TEXT,
    created_at        TEXT NOT NULL,
    updated_at        TEXT
);

CREATE UNIQUE INDEX idx_users_email_lower ON users (LOWER(email));

-- Provider links, so one account can carry several sign-in methods.
-- provider is 'password' or 'firebase'.
CREATE TABLE auth_identities (
    id               TEXT PRIMARY KEY,
    user_id          TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    provider         TEXT NOT NULL,
    -- Firebase uid for provider='firebase'; the user id for provider='password'.
    provider_subject TEXT NOT NULL,
    email            TEXT,
    created_at       TEXT NOT NULL
);

CREATE UNIQUE INDEX idx_auth_identities_provider ON auth_identities (provider, provider_subject);
CREATE INDEX idx_auth_identities_user ON auth_identities (user_id);

-- One row per signed-in device. Device management works by revoking the
-- session, which cascades to its refresh token families.
CREATE TABLE sessions (
    id           TEXT PRIMARY KEY,
    user_id      TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    device_label TEXT NOT NULL,
    -- web | android | ios, from the X-Client-Platform header when present.
    platform     TEXT NOT NULL,
    user_agent   TEXT,
    ip           TEXT,
    last_seen_at TEXT NOT NULL,
    revoked_at   TEXT,
    created_at   TEXT NOT NULL
);

CREATE INDEX idx_sessions_user ON sessions (user_id, last_seen_at DESC);

-- Rotating refresh tokens. Only the SHA-256 hash of the token is stored.
-- family_id groups a rotation chain so that replaying a rotated token can
-- revoke the whole chain.
CREATE TABLE refresh_tokens (
    id          TEXT PRIMARY KEY,
    user_id     TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    session_id  TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    token_hash  TEXT NOT NULL,
    family_id   TEXT NOT NULL,
    expires_at  TEXT NOT NULL,
    revoked_at  TEXT,
    replaced_by TEXT,
    created_at  TEXT NOT NULL
);

CREATE UNIQUE INDEX idx_refresh_tokens_hash ON refresh_tokens (token_hash);
CREATE INDEX idx_refresh_tokens_family ON refresh_tokens (family_id);
CREATE INDEX idx_refresh_tokens_session ON refresh_tokens (session_id);

-- Single-use tokens for email verification and password reset. The raw token
-- only ever exists in the email; the database stores its SHA-256 hash.
CREATE TABLE one_time_tokens (
    id          TEXT PRIMARY KEY,
    user_id     TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    -- email_verification | password_reset
    purpose     TEXT NOT NULL,
    token_hash  TEXT NOT NULL,
    expires_at  TEXT NOT NULL,
    consumed_at TEXT,
    created_at  TEXT NOT NULL
);

CREATE UNIQUE INDEX idx_one_time_tokens_hash ON one_time_tokens (token_hash);
CREATE INDEX idx_one_time_tokens_user_purpose ON one_time_tokens (user_id, purpose);

CREATE TABLE bookmarks (
    id             TEXT PRIMARY KEY,
    user_id        TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    title          TEXT NOT NULL,
    current_url    TEXT NOT NULL,
    -- Free text as the site wrote it, e.g. "Chapter 1050".
    chapter        TEXT,
    -- Parsed value used by the +1 action; NULL when the chapter text has no
    -- number, in which case the client must ask the user to edit it.
    chapter_number REAL,
    cover_url      TEXT,
    source_host    TEXT,
    -- reading | paused | completed | dropped
    status         TEXT NOT NULL DEFAULT 'reading',
    last_read_at   TEXT,
    created_at     TEXT NOT NULL,
    updated_at     TEXT
);

CREATE UNIQUE INDEX idx_bookmarks_user_url ON bookmarks (user_id, current_url);
CREATE INDEX idx_bookmarks_user_status ON bookmarks (user_id, status);
CREATE INDEX idx_bookmarks_user_lastread ON bookmarks (user_id, last_read_at DESC);
