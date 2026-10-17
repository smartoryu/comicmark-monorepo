// Package database opens and configures the ComicMark SQLite database.
//
// SQLite is configured with write-ahead logging for concurrent reads, a busy
// timeout so brief write contention retries instead of failing, and foreign key
// enforcement. Writes are serialised through a single connection because SQLite
// allows only one writer at a time; using more connections would surface as
// SQLITE_BUSY under load rather than as queued work.
package database

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	// Registers the CGO-free "sqlite" driver.
	_ "modernc.org/sqlite"
)

// DriverName is the database/sql driver registered by modernc.org/sqlite.
const DriverName = "sqlite"

// pragmas applied to every connection. journal_mode is persisted in the
// database file, while busy_timeout and foreign_keys are per-connection, so
// they belong in the DSN rather than in a one-off setup statement.
const pragmaQuery = "?_pragma=journal_mode(WAL)" +
	"&_pragma=busy_timeout(5000)" +
	"&_pragma=foreign_keys(1)"

// pingTimeout bounds the connectivity check performed by Open.
const pingTimeout = 5 * time.Second

// DSN builds the connection string for a SQLite file path.
func DSN(path string) string {
	return "file:" + escapePath(path) + pragmaQuery
}

// escapePath percent-escapes the characters that would otherwise terminate or
// confuse the file URI, leaving path separators intact.
func escapePath(path string) string {
	r := strings.NewReplacer("%", "%25", "?", "%3F", "#", "%23")
	return r.Replace(path)
}

// Open opens the SQLite database at path, creating the file and its parent
// directory when needed, then verifies the connection is usable.
//
// The returned *sql.DB is safe for concurrent use by multiple goroutines.
func Open(ctx context.Context, path string) (*sql.DB, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("database: path must not be empty")
	}

	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return nil, fmt.Errorf("database: create directory %q: %w", dir, err)
		}
	}

	db, err := sql.Open(DriverName, DSN(path))
	if err != nil {
		return nil, fmt.Errorf("database: open %q: %w", path, err)
	}

	// SQLite serialises writers; a single connection turns contention into a
	// queue instead of SQLITE_BUSY errors.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	// SQLite connections are cheap and hold no server-side state, so there is
	// no reason to recycle them.
	db.SetConnMaxLifetime(0)
	db.SetConnMaxIdleTime(0)

	pingCtx, cancel := context.WithTimeout(ctx, pingTimeout)
	defer cancel()

	if err := db.PingContext(pingCtx); err != nil {
		// Closing here avoids leaking a pool the caller has no reference to.
		_ = db.Close()
		return nil, fmt.Errorf("database: ping %q: %w", path, err)
	}

	return db, nil
}
