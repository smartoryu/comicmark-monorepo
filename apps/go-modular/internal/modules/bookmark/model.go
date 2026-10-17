// Package bookmark stores and serves a reader's comic bookmarks.
//
// A bookmark is a chapter the reader is up to: the URL they were reading, the
// chapter as the site wrote it, and the chapter as a number so it can be advanced
// with one tap. The package owns its table, its HTTP routes and its use cases; the
// only things it needs from the rest of the application arrive through Deps.
package bookmark

import (
	"errors"
	"slices"
	"time"
)

// Status is where a reader is with a comic.
type Status string

// The four states a bookmark can be in.
const (
	// StatusReading is a comic the reader is actively following. It is the
	// default and what the library shows first.
	StatusReading Status = "reading"
	// StatusPaused is a comic the reader has set aside but not abandoned.
	StatusPaused Status = "paused"
	// StatusCompleted is a comic the reader has finished.
	StatusCompleted Status = "completed"
	// StatusDropped is a comic the reader has given up on.
	StatusDropped Status = "dropped"
)

// Statuses lists every valid status, in display order.
var Statuses = []Status{StatusReading, StatusPaused, StatusCompleted, StatusDropped}

// ValidStatus reports whether a value is one of the four statuses.
func ValidStatus(s string) bool {
	return slices.Contains(Statuses, Status(s))
}

// DefaultStatus is applied when a create request omits one.
const DefaultStatus = StatusReading

// Domain errors. The HTTP layer maps these to status codes; nothing else should
// need to inspect them.
var (
	// ErrNotFound means the bookmark does not exist, or belongs to somebody
	// else. The two cases are deliberately indistinguishable so another user's
	// bookmark id cannot be probed for existence.
	ErrNotFound = errors.New("bookmark: not found")

	// ErrDuplicate means the reader has already saved this URL.
	ErrDuplicate = errors.New("bookmark: this link is already saved")

	// ErrChapterNotNumeric means the chapter has no number to advance. The
	// client must ask the user rather than guess, because a bookmark silently
	// advanced to the wrong chapter is worse than one that asks.
	ErrChapterNotNumeric = errors.New("bookmark: chapter has no number")

	// Input problems, reported to the caller as field errors.
	ErrTitleRequired   = errors.New("bookmark: a title is required")
	ErrURLRequired     = errors.New("bookmark: a link is required")
	ErrInvalidStatus   = errors.New("bookmark: unknown status")
	ErrNothingToUpdate = errors.New("bookmark: nothing to update")
)

// FieldError is a single invalid input, reported as one field in the validation
// response.
type FieldError struct {
	Field   string
	Message string
	cause   error
}

func (e *FieldError) Error() string { return e.Field + ": " + e.Message }

// Unwrap lets errors.Is match the underlying sentinel.
func (e *FieldError) Unwrap() error { return e.cause }

func newFieldError(field, message string, cause error) *FieldError {
	return &FieldError{Field: field, Message: message, cause: cause}
}

// Limits on stored text, so a hostile or broken page cannot stuff the database.
const (
	// MaxTitleLength bounds the comic title.
	MaxTitleLength = 300
	// MaxChapterLength bounds the chapter text.
	MaxChapterLength = 100
	// maxURLLength bounds a stored link. Real chapter URLs are far shorter; this
	// only exists to stop something absurd reaching the database.
	maxURLLength = 4096
	// MaxQueryLength bounds a library search term.
	MaxQueryLength = 200

	// DefaultPageSize is how many bookmarks a list returns when the client does
	// not ask for a size.
	DefaultPageSize = 50
	// MaxPageSize bounds what a client may ask for.
	MaxPageSize = 200
)

// Bookmark is a chapter the reader is up to.
type Bookmark struct {
	ID     string
	UserID string

	Title string
	// CurrentURL is the chapter the reader was last at. Advancing the chapter
	// does not change it: the reader taps through to open this link.
	CurrentURL string

	// Chapter is the chapter as the site wrote it, e.g. "Chapter 1050".
	Chapter string
	// ChapterNumber is the parsed value, nil when the text carries no number.
	ChapterNumber *float64

	CoverURL   string
	SourceHost string
	Status     Status

	// LastReadAt is when the reader last opened the link. It drives the
	// "continue reading" order of the library.
	LastReadAt *time.Time

	CreatedAt time.Time
	UpdatedAt *time.Time
}

// ChapterKnown reports whether the chapter can be advanced numerically.
func (b *Bookmark) ChapterKnown() bool { return b.ChapterNumber != nil }
