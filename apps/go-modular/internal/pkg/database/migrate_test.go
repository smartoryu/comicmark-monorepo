package database

import (
	"context"
	"database/sql"
	"io/fs"
	"path/filepath"
	"testing"
	"testing/fstest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := Open(context.Background(), filepath.Join(t.TempDir(), "comicmark.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	return db
}

func tableExists(t *testing.T, db *sql.DB, name string) bool {
	t.Helper()
	var found string
	err := db.QueryRowContext(context.Background(),
		`SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?`, name).Scan(&found)
	if err == sql.ErrNoRows {
		return false
	}
	require.NoError(t, err)
	return true
}

func TestUpAppliesEveryMigrationAndIsIdempotent(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	m := NewMigrator(db)

	applied, err := m.Up(ctx)
	require.NoError(t, err)
	assert.Equal(t, []string{"0001_init"}, applied)

	for _, table := range []string{
		"users", "auth_identities", "sessions", "refresh_tokens", "one_time_tokens", "bookmarks",
	} {
		assert.True(t, tableExists(t, db, table), "expected table %q to exist", table)
	}

	// Running again must not reapply anything or fail.
	appliedAgain, err := m.Up(ctx)
	require.NoError(t, err)
	assert.Empty(t, appliedAgain, "a second run should apply nothing")

	versions, err := m.Applied(ctx)
	require.NoError(t, err)
	assert.Equal(t, []string{"0001_init"}, versions)
}

func TestUpRecordsAppliedTimestamp(t *testing.T) {
	db := openTestDB(t)
	fixed := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)

	m := NewMigrator(db)
	m.now = func() time.Time { return fixed }

	_, err := m.Up(context.Background())
	require.NoError(t, err)

	var got string
	require.NoError(t, db.QueryRowContext(context.Background(),
		`SELECT applied_at FROM schema_migrations WHERE version = ?`, "0001_init").Scan(&got))
	assert.Equal(t, fixed.Format(time.RFC3339), got)
}

func TestUpAppliesOnlyNewMigrationsInFilenameOrder(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	// Start with a single-migration set.
	first := NewMigrator(db)
	first.fs = fstest.MapFS{
		"migrations/0001_alpha.sql": &fstest.MapFile{
			Data: []byte(`CREATE TABLE alpha (id TEXT PRIMARY KEY)`),
		},
	}
	applied, err := first.Up(ctx)
	require.NoError(t, err)
	require.Equal(t, []string{"0001_alpha"}, applied)

	// Now the set grows; only the new one should run.
	first.fs = fstest.MapFS{
		"migrations/0001_alpha.sql": &fstest.MapFile{
			Data: []byte(`CREATE TABLE alpha (id TEXT PRIMARY KEY)`),
		},
		"migrations/0002_beta.sql": &fstest.MapFile{
			Data: []byte(`CREATE TABLE beta (id TEXT PRIMARY KEY)`),
		},
	}
	applied, err = first.Up(ctx)
	require.NoError(t, err)
	assert.Equal(t, []string{"0002_beta"}, applied)

	assert.True(t, tableExists(t, db, "alpha"))
	assert.True(t, tableExists(t, db, "beta"))
}

func TestUpRollsBackAFailingMigration(t *testing.T) {
	db := openTestDB(t)

	m := NewMigrator(db)
	m.fs = fstest.MapFS{
		"migrations/0001_broken.sql": &fstest.MapFile{
			Data: []byte("CREATE TABLE good (id TEXT PRIMARY KEY);\nTHIS IS NOT SQL;"),
		},
	}

	_, err := m.Up(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "0001_broken")

	// The migration is transactional, so the statement before the failure must
	// not survive and must not be recorded as applied.
	assert.False(t, tableExists(t, db, "good"), "a failed migration must roll back entirely")

	applied, err := NewMigrator(db).Applied(context.Background())
	require.NoError(t, err)
	assert.Empty(t, applied)
}

func TestUpRejectsAnEmptyMigrationSet(t *testing.T) {
	db := openTestDB(t)

	// The directory exists but holds no .sql files, which can only mean a
	// packaging mistake.
	m := NewMigrator(db)
	m.fs = fstest.MapFS{
		"migrations": &fstest.MapFile{Mode: fs.ModeDir},
	}

	_, err := m.Up(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no migrations found")
}

func TestUpRejectsAMissingMigrationDirectory(t *testing.T) {
	db := openTestDB(t)

	m := NewMigrator(db)
	m.fs = fstest.MapFS{}

	_, err := m.Up(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "read migrations")
}
