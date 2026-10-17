package database

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOpenCreatesFileAndParentDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "comicmark.db")

	db, err := Open(context.Background(), path)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })

	_, err = db.ExecContext(context.Background(), "CREATE TABLE t (id TEXT)")
	require.NoError(t, err, "the database should be writable")
}

func TestOpenAppliesPragmas(t *testing.T) {
	db, err := Open(context.Background(), filepath.Join(t.TempDir(), "comicmark.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })

	ctx := context.Background()

	t.Run("journal_mode is WAL", func(t *testing.T) {
		var mode string
		require.NoError(t, db.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&mode))
		assert.Equal(t, "wal", mode)
	})

	t.Run("foreign keys are enforced", func(t *testing.T) {
		var enabled int
		require.NoError(t, db.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&enabled))
		assert.Equal(t, 1, enabled)

		// Prove enforcement rather than trusting the flag.
		_, err := db.ExecContext(ctx, "CREATE TABLE parent (id TEXT PRIMARY KEY)")
		require.NoError(t, err)
		_, err = db.ExecContext(ctx, "CREATE TABLE child (id TEXT PRIMARY KEY, parent_id TEXT NOT NULL REFERENCES parent(id))")
		require.NoError(t, err)

		_, err = db.ExecContext(ctx, "INSERT INTO child (id, parent_id) VALUES ('c1', 'missing')")
		require.Error(t, err, "inserting a dangling foreign key should fail")
	})

	t.Run("busy timeout is set", func(t *testing.T) {
		var timeout int
		require.NoError(t, db.QueryRowContext(ctx, "PRAGMA busy_timeout").Scan(&timeout))
		assert.Equal(t, 5000, timeout)
	})
}

func TestOpenSerialisesWritesThroughOneConnection(t *testing.T) {
	db, err := Open(context.Background(), filepath.Join(t.TempDir(), "comicmark.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })

	assert.Equal(t, 1, db.Stats().MaxOpenConnections)
}

func TestOpenRejectsEmptyPath(t *testing.T) {
	_, err := Open(context.Background(), "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must not be empty")
}

func TestDSNEscapesReservedCharacters(t *testing.T) {
	dsn := DSN("/tmp/a b/c#d?.db")

	assert.Contains(t, dsn, "file:/tmp/a b/c%23d%3F.db?")
	assert.Contains(t, dsn, "_pragma=journal_mode(WAL)")
	assert.Contains(t, dsn, "_pragma=busy_timeout(5000)")
	assert.Contains(t, dsn, "_pragma=foreign_keys(1)")
}
