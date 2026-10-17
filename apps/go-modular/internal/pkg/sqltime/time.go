// Package sqltime encodes timestamps for storage in SQLite.
//
// SQLite has no date type, so times are stored as TEXT and compared
// lexicographically. That only works if the encoding is fixed width:
// time.RFC3339Nano drops trailing zeros, which makes "…:00Z" sort *after*
// "…:00.5Z" even though it is earlier. Milliseconds with a fixed number of
// digits keep string order and chronological order the same.
//
// Both feature modules store times through this package so the format cannot
// drift apart.
package sqltime

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Layout is RFC 3339 with milliseconds, always the same width.
const Layout = "2006-01-02T15:04:05.000Z07:00"

// ErrInvalid is returned when a stored value cannot be read back.
var ErrInvalid = errors.New("sqltime: invalid timestamp")

// Format renders a time for storage, in UTC.
func Format(t time.Time) string { return t.UTC().Format(Layout) }

// Parse reads a stored timestamp.
func Parse(s string) (time.Time, error) {
	t, err := time.Parse(Layout, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("%w: %q", ErrInvalid, s)
	}
	return t.UTC(), nil
}

// Null renders an optional timestamp for storage: nil becomes SQL NULL.
func Null(t *time.Time) any {
	if t == nil {
		return nil
	}
	return Format(*t)
}

// ParseNull reads a nullable timestamp column.
func ParseNull(ns sql.NullString) (*time.Time, error) {
	if !ns.Valid || ns.String == "" {
		return nil, nil
	}
	t, err := Parse(ns.String)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// Optional returns a pointer to a copy, for assigning a column value.
func Optional(t time.Time) *time.Time { return &t }
