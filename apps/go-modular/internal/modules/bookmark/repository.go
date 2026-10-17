package bookmark

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"comicmark/go-modular/internal/pkg/sqltime"
)

// Repository is the data access layer for bookmarks.
type Repository struct {
	db *sql.DB
}

// NewRepository returns a repository backed by a connection pool.
func NewRepository(db *sql.DB) *Repository { return &Repository{db: db} }

// columns is shared by every read so the scan order cannot drift.
const columns = `id, user_id, title, current_url, chapter, chapter_number, cover_url,
                 source_host, status, last_read_at, created_at, updated_at`

// Create inserts a bookmark, assigning an id and timestamps when unset.
func (r *Repository) Create(ctx context.Context, b *Bookmark) error {
	if b.ID == "" {
		b.ID = uuid.Must(uuid.NewV7()).String()
	}
	if b.CreatedAt.IsZero() {
		b.CreatedAt = time.Now()
	}
	if b.Status == "" {
		b.Status = DefaultStatus
	}

	_, err := r.db.ExecContext(ctx, `
		INSERT INTO bookmarks (
			id, user_id, title, current_url, chapter, chapter_number,
			cover_url, source_host, status, last_read_at, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		b.ID, b.UserID, b.Title, b.CurrentURL,
		nullString(b.Chapter), b.ChapterNumber,
		nullString(b.CoverURL), nullString(b.SourceHost),
		string(b.Status), sqltime.Null(b.LastReadAt),
		sqltime.Format(b.CreatedAt), sqltime.Null(b.UpdatedAt),
	)
	if err != nil {
		if isUniqueViolation(err) {
			// The unique index is (user_id, current_url), so a clash means this
			// reader already saved this chapter.
			return ErrDuplicate
		}
		return fmt.Errorf("bookmark: create: %w", err)
	}
	return nil
}

// Get returns one bookmark owned by userID.
//
// Ownership is part of the query rather than a check afterwards, so another
// user's id is simply not found and cannot be probed.
func (r *Repository) Get(ctx context.Context, userID, id string) (*Bookmark, error) {
	row := r.db.QueryRowContext(ctx,
		`SELECT `+columns+` FROM bookmarks WHERE id = ? AND user_id = ?`, id, userID)
	return scanBookmark(row)
}

// Filter narrows a library listing.
type Filter struct {
	// Status limits results to one state when set.
	Status Status
	// Query matches the title or the source host, case-insensitively.
	Query string
	// Limit caps the page size. Zero means DefaultPageSize.
	Limit int
	// Offset skips that many rows.
	Offset int
}

// List returns a reader's bookmarks, most recently read first.
//
// The ordering is "continue reading": anything opened before the others comes
// first, newest first, and anything never opened sits at the end in the order it
// was added. The `last_read_at IS NULL` term is spelled out because SQLite only
// grew NULLS LAST in 3.30 and this keeps the intent obvious.
func (r *Repository) List(ctx context.Context, userID string, filter Filter) ([]*Bookmark, error) {
	where, args := whereClause(userID, filter)

	query := strings.Builder{}
	query.WriteString(`SELECT ` + columns + ` FROM bookmarks`)
	query.WriteString(where)
	query.WriteString(` ORDER BY last_read_at IS NULL, last_read_at DESC, created_at DESC, id DESC`)
	query.WriteString(` LIMIT ? OFFSET ?`)
	args = append(args, effectiveLimit(filter), max(filter.Offset, 0))

	rows, err := r.db.QueryContext(ctx, query.String(), args...)
	if err != nil {
		return nil, fmt.Errorf("bookmark: list: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []*Bookmark
	for rows.Next() {
		b, err := scanBookmark(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("bookmark: list: %w", err)
	}
	return out, nil
}

// Count returns how many bookmarks match a filter.
//
// It takes the same filter as List on purpose: a total that ignored the filter
// would disagree with the page it describes, and a client paging through a
// filtered library would either stop early or loop forever.
func (r *Repository) Count(ctx context.Context, userID string, filter Filter) (int, error) {
	where, args := whereClause(userID, filter)

	var total int
	err := r.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM bookmarks`+where, args...).Scan(&total)
	if err != nil {
		return 0, fmt.Errorf("bookmark: count: %w", err)
	}
	return total, nil
}

// whereClause builds the predicate shared by List and Count, so the two can
// never drift apart.
func whereClause(userID string, filter Filter) (string, []any) {
	clause := strings.Builder{}
	clause.WriteString(` WHERE user_id = ?`)
	args := []any{userID}

	if filter.Status != "" {
		clause.WriteString(` AND status = ?`)
		args = append(args, string(filter.Status))
	}
	if term := strings.TrimSpace(filter.Query); term != "" {
		// LIKE with a leading wildcard cannot use an index, which is fine at the
		// scale of one reader's library. The chapter is included because a reader
		// searching their library may well remember the chapter rather than the
		// title.
		clause.WriteString(` AND (title LIKE ? ESCAPE '\' OR source_host LIKE ? ESCAPE '\' OR chapter LIKE ? ESCAPE '\')`)
		pattern := "%" + escapeLike(term) + "%"
		args = append(args, pattern, pattern, pattern)
	}

	return clause.String(), args
}

// effectiveLimit resolves the page size a request will actually use.
func effectiveLimit(filter Filter) int {
	limit := filter.Limit
	if limit <= 0 {
		return DefaultPageSize
	}
	if limit > MaxPageSize {
		return MaxPageSize
	}
	return limit
}

// Update writes the mutable fields of a bookmark.
//
// The caller has already loaded the row, so a zero rows-affected result means it
// was deleted in the meantime.
func (r *Repository) Update(ctx context.Context, b *Bookmark) error {
	updatedAt := time.Now()
	b.UpdatedAt = &updatedAt

	res, err := r.db.ExecContext(ctx, `
		UPDATE bookmarks SET
			title = ?, current_url = ?, chapter = ?, chapter_number = ?,
			cover_url = ?, source_host = ?, status = ?, last_read_at = ?, updated_at = ?
		WHERE id = ? AND user_id = ?`,
		b.Title, b.CurrentURL,
		nullString(b.Chapter), b.ChapterNumber,
		nullString(b.CoverURL), nullString(b.SourceHost),
		string(b.Status), sqltime.Null(b.LastReadAt),
		sqltime.Format(updatedAt), b.ID, b.UserID,
	)
	if err != nil {
		if isUniqueViolation(err) {
			return ErrDuplicate
		}
		return fmt.Errorf("bookmark: update: %w", err)
	}

	if n, err := res.RowsAffected(); err == nil && n == 0 {
		// Either it was deleted or it belongs to somebody else; both are "not
		// found" as far as this caller is concerned.
		return ErrNotFound
	}
	return nil
}

// Delete removes a bookmark owned by userID.
func (r *Repository) Delete(ctx context.Context, userID, id string) error {
	res, err := r.db.ExecContext(ctx,
		`DELETE FROM bookmarks WHERE id = ? AND user_id = ?`, id, userID)
	if err != nil {
		return fmt.Errorf("bookmark: delete: %w", err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return ErrNotFound
	}
	return nil
}

// --- scanning ---------------------------------------------------------------

// rowScanner is implemented by both *sql.Row and *sql.Rows.
type rowScanner interface {
	Scan(dest ...any) error
}

func scanBookmark(row rowScanner) (*Bookmark, error) {
	var (
		b          Bookmark
		chapter    sql.NullString
		number     sql.NullFloat64
		cover      sql.NullString
		host       sql.NullString
		lastRead   sql.NullString
		created    string
		updated    sql.NullString
		statusText string
	)

	err := row.Scan(&b.ID, &b.UserID, &b.Title, &b.CurrentURL,
		&chapter, &number, &cover, &host, &statusText,
		&lastRead, &created, &updated)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("bookmark: scan: %w", err)
	}

	b.Chapter = chapter.String
	if number.Valid {
		value := number.Float64
		b.ChapterNumber = &value
	}
	b.CoverURL = cover.String
	b.SourceHost = host.String
	b.Status = Status(statusText)

	if b.LastReadAt, err = sqltime.ParseNull(lastRead); err != nil {
		return nil, err
	}
	if b.CreatedAt, err = sqltime.Parse(created); err != nil {
		return nil, err
	}
	if b.UpdatedAt, err = sqltime.ParseNull(updated); err != nil {
		return nil, err
	}

	return &b, nil
}

// --- helpers ----------------------------------------------------------------

func nullString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// escapeLike neutralises the LIKE wildcards in a search term, so searching for
// "50%" looks for that text rather than matching everything.
func escapeLike(term string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(term)
}

// isUniqueViolation reports whether an error is a SQLite uniqueness failure.
//
// The driver exposes no typed error, so this matches the message.
func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "UNIQUE constraint failed") ||
		strings.Contains(msg, "constraint failed: UNIQUE")
}
