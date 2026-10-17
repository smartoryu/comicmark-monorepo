package database

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"
	"time"
)

// migrationsDir holds the embedded schema migrations. Files are plain SQL named
// NNNN_description.sql and are applied in filename order.
//
//go:embed migrations/*.sql
var migrationsDir embed.FS

const (
	migrationsPath = "migrations"
	schemaTable    = "schema_migrations"
)

// Migrator applies embedded migrations to a database, recording each applied
// version so runs are idempotent.
type Migrator struct {
	db  *sql.DB
	fs  fs.FS
	dir string
	// now is injectable so tests can assert recorded timestamps.
	now func() time.Time
}

// NewMigrator returns a Migrator backed by the embedded migrations.
func NewMigrator(db *sql.DB) *Migrator {
	return &Migrator{db: db, fs: migrationsDir, dir: migrationsPath, now: time.Now}
}

// Migration is a single versioned schema change.
type Migration struct {
	Version string // filename without the .sql suffix, e.g. "0001_init"
	Name    string // filename including the suffix
	SQL     string
}

// Up applies every migration that has not been applied yet and returns the
// versions it applied, in order. Each migration runs inside its own
// transaction together with the bookkeeping insert, so a failure leaves the
// schema unchanged.
func (m *Migrator) Up(ctx context.Context) ([]string, error) {
	if err := m.ensureSchemaTable(ctx); err != nil {
		return nil, err
	}

	applied, err := m.appliedVersions(ctx)
	if err != nil {
		return nil, err
	}

	migrations, err := m.load()
	if err != nil {
		return nil, err
	}

	var ran []string
	for _, mig := range migrations {
		if _, done := applied[mig.Version]; done {
			continue
		}
		if err := m.apply(ctx, mig); err != nil {
			return ran, err
		}
		ran = append(ran, mig.Version)
	}
	return ran, nil
}

// Applied returns the versions already recorded, in application order.
func (m *Migrator) Applied(ctx context.Context) ([]string, error) {
	if err := m.ensureSchemaTable(ctx); err != nil {
		return nil, err
	}
	rows, err := m.db.QueryContext(ctx,
		`SELECT version FROM `+schemaTable+` ORDER BY version`)
	if err != nil {
		return nil, fmt.Errorf("database: list applied migrations: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var versions []string
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, fmt.Errorf("database: scan applied migration: %w", err)
		}
		versions = append(versions, v)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("database: iterate applied migrations: %w", err)
	}
	return versions, nil
}

func (m *Migrator) ensureSchemaTable(ctx context.Context) error {
	const stmt = `
		CREATE TABLE IF NOT EXISTS ` + schemaTable + ` (
			version    TEXT PRIMARY KEY,
			applied_at TEXT NOT NULL
		)`
	if _, err := m.db.ExecContext(ctx, stmt); err != nil {
		return fmt.Errorf("database: create %s table: %w", schemaTable, err)
	}
	return nil
}

func (m *Migrator) appliedVersions(ctx context.Context) (map[string]struct{}, error) {
	versions, err := m.Applied(ctx)
	if err != nil {
		return nil, err
	}
	set := make(map[string]struct{}, len(versions))
	for _, v := range versions {
		set[v] = struct{}{}
	}
	return set, nil
}

// load reads and sorts the migration files.
func (m *Migrator) load() ([]Migration, error) {
	entries, err := fs.ReadDir(m.fs, m.dir)
	if err != nil {
		return nil, fmt.Errorf("database: read migrations: %w", err)
	}

	migrations := make([]Migration, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		body, err := fs.ReadFile(m.fs, path.Join(m.dir, e.Name()))
		if err != nil {
			return nil, fmt.Errorf("database: read migration %s: %w", e.Name(), err)
		}
		migrations = append(migrations, Migration{
			Version: strings.TrimSuffix(e.Name(), ".sql"),
			Name:    e.Name(),
			SQL:     string(body),
		})
	}

	if len(migrations) == 0 {
		return nil, fmt.Errorf("database: no migrations found in %s", m.dir)
	}

	// Filename order is the version order, which is why the NNNN_ prefix exists.
	sort.Slice(migrations, func(i, j int) bool {
		return migrations[i].Name < migrations[j].Name
	})
	return migrations, nil
}

func (m *Migrator) apply(ctx context.Context, mig Migration) error {
	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("database: begin migration %s: %w", mig.Name, err)
	}
	defer func() {
		// Rollback after a successful commit is a no-op.
		_ = tx.Rollback()
	}()

	if _, err := tx.ExecContext(ctx, mig.SQL); err != nil {
		return fmt.Errorf("database: apply migration %s: %w", mig.Name, err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO `+schemaTable+` (version, applied_at) VALUES (?, ?)`,
		mig.Version, m.now().UTC().Format(time.RFC3339),
	); err != nil {
		return fmt.Errorf("database: record migration %s: %w", mig.Name, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("database: commit migration %s: %w", mig.Name, err)
	}
	return nil
}
