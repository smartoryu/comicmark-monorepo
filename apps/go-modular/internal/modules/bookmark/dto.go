package bookmark

import (
	"encoding/json"
	"time"
)

// Request and response shapes for the bookmark endpoints.
//
// The `json` tags drive decoding and encoding, and `validate` is read by the
// validation package, which reports problems using the JSON name.

// parseRequest asks for a preview of a URL that has not been saved yet.
type parseRequest struct {
	URL string `json:"url" validate:"required,max=4096"`
}

// createRequest saves a bookmark.
//
// chapter_number is a pointer so an omitted value is distinguishable from zero:
// chapter 0 does not exist, but "we do not know the number" does.
type createRequest struct {
	Title         string   `json:"title" validate:"required,max=300"`
	CurrentURL    string   `json:"current_url" validate:"required,max=4096"`
	Chapter       string   `json:"chapter" validate:"omitempty,max=100"`
	ChapterNumber *float64 `json:"chapter_number"`
	CoverURL      string   `json:"cover_url" validate:"omitempty,max=4096"`
	Status        string   `json:"status" validate:"omitempty,oneof=reading paused completed dropped"`
}

// updateRequest is a partial edit: omitted fields are left alone.
type updateRequest struct {
	Title string `json:"title" validate:"omitempty,max=300"`
	// ChapterNumber is captured raw so that "absent" and "explicitly null" can be
	// told apart. A *float64 cannot express both, and the library needs both:
	// null means "this chapter has no number after all", which is what puts the
	// +1 button into its edit-the-chapter state.
	ChapterNumber json.RawMessage `json:"chapter_number"`
	Chapter       *string         `json:"chapter" validate:"omitempty,max=100"`
	CoverURL      *string         `json:"cover_url" validate:"omitempty,max=4096"`
	Status        *string         `json:"status" validate:"omitempty,oneof=reading paused completed dropped"`
}

// --- responses --------------------------------------------------------------

// bookmarkResponse is the public representation of a saved bookmark.
type bookmarkResponse struct {
	ID            string     `json:"id"`
	Title         string     `json:"title"`
	CurrentURL    string     `json:"current_url"`
	Chapter       string     `json:"chapter,omitempty"`
	ChapterNumber *float64   `json:"chapter_number"`
	CoverURL      string     `json:"cover_url,omitempty"`
	SourceHost    string     `json:"source_host,omitempty"`
	Status        string     `json:"status"`
	LastReadAt    *time.Time `json:"last_read_at"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     *time.Time `json:"updated_at"`
}

// previewResponse is what a URL looks like before it is saved.
type previewResponse struct {
	Title         string   `json:"title"`
	CoverURL      string   `json:"cover_url,omitempty"`
	SiteName      string   `json:"site_name,omitempty"`
	CurrentURL    string   `json:"current_url"`
	Chapter       string   `json:"chapter,omitempty"`
	ChapterNumber *float64 `json:"chapter_number"`
	SourceHost    string   `json:"source_host,omitempty"`
	// Partial tells the client to let the reader edit every field.
	Partial bool `json:"partial"`
	// Warnings is always present, so a client need not distinguish null from [].
	Warnings []string `json:"warnings"`
}

// listResponse wraps the library with enough information to page through it.
type listResponse struct {
	Bookmarks []bookmarkResponse `json:"bookmarks"`
	Total     int                `json:"total"`
	Limit     int                `json:"limit"`
	Offset    int                `json:"offset"`
}

// --- mapping ----------------------------------------------------------------

func newBookmarkResponse(b *Bookmark) bookmarkResponse {
	return bookmarkResponse{
		ID:            b.ID,
		Title:         b.Title,
		CurrentURL:    b.CurrentURL,
		Chapter:       b.Chapter,
		ChapterNumber: b.ChapterNumber,
		CoverURL:      b.CoverURL,
		SourceHost:    b.SourceHost,
		Status:        string(b.Status),
		LastReadAt:    b.LastReadAt,
		CreatedAt:     b.CreatedAt,
		UpdatedAt:     b.UpdatedAt,
	}
}

func newPreviewResponse(p *Preview) previewResponse {
	warnings := p.Warnings
	if warnings == nil {
		warnings = []string{}
	}

	return previewResponse{
		Title:         p.Title,
		CoverURL:      p.CoverURL,
		SiteName:      p.SiteName,
		CurrentURL:    p.CurrentURL,
		Chapter:       p.Chapter,
		ChapterNumber: p.ChapterNumber,
		SourceHost:    p.SourceHost,
		Partial:       p.Partial,
		Warnings:      warnings,
	}
}
