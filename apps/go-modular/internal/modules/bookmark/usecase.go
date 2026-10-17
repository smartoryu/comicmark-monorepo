package bookmark

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"time"

	"comicmark/go-modular/internal/pkg/scraper"
)

// Fetcher is the scraper this module depends on.
//
// It is an interface rather than the concrete type so the use cases can be tested
// without a network, and so the module does not depend on how pages are fetched.
type Fetcher interface {
	Fetch(ctx context.Context, rawURL string) (*scraper.Result, error)
}

// Options configures the service.
type Options struct {
	Repository *Repository
	Fetcher    Fetcher
	Logger     *slog.Logger
	// Now is injectable for tests.
	Now func() time.Time
}

// Service implements the bookmark use cases.
type Service struct {
	repo    *Repository
	fetcher Fetcher
	logger  *slog.Logger
	now     func() time.Time
}

// NewService validates its options and returns the service.
func NewService(opts Options) (*Service, error) {
	switch {
	case opts.Repository == nil:
		return nil, errors.New("bookmark: repository is required")
	case opts.Fetcher == nil:
		return nil, errors.New("bookmark: fetcher is required")
	}

	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}

	return &Service{repo: opts.Repository, fetcher: opts.Fetcher, logger: logger, now: now}, nil
}

// --- request and result types -----------------------------------------------

// Preview is what a URL turns into before anything is saved.
type Preview struct {
	Title      string
	CoverURL   string
	SiteName   string
	CurrentURL string
	Chapter    string
	// ChapterNumber is nil when the page had no usable number, which is what
	// makes the preview partial.
	ChapterNumber *float64
	SourceHost    string

	// Partial tells the client to let the reader edit every field.
	Partial bool
	// Warnings explains what was missing, in the reader's terms.
	Warnings []string
}

// CreateInput is a request to save a bookmark.
type CreateInput struct {
	Title         string
	CurrentURL    string
	Chapter       string
	ChapterNumber *float64
	CoverURL      string
	Status        string
}

// UpdateInput is a partial edit. A nil field means "leave it alone", which is
// what makes PATCH behave like PATCH.
type UpdateInput struct {
	Title         *string
	Chapter       *string
	ChapterNumber *float64
	// ClearChapterNumber distinguishes "set the number to nothing" from "leave
	// the number alone", which a nil pointer cannot express on its own.
	ClearChapterNumber bool
	CoverURL           *string
	Status             *string
}

// --- preview ----------------------------------------------------------------

// Parse fetches a URL and reports what it found, without saving anything.
//
// A URL the guard refuses is an error, because it is the reader's mistake and
// they can fix it. Everything else, including a site that blocks the bot or is
// simply unreachable, comes back as a partial preview: the reader can still save
// the bookmark by typing what the page did not provide.
func (s *Service) Parse(ctx context.Context, rawURL string) (*Preview, error) {
	result, err := s.fetcher.Fetch(ctx, rawURL)
	if err != nil {
		return nil, err
	}

	preview := &Preview{
		Title:         result.Title,
		CoverURL:      result.CoverURL,
		SiteName:      result.SiteName,
		CurrentURL:    result.FinalURL,
		Chapter:       result.Chapter.Text,
		ChapterNumber: result.Chapter.Number,
		SourceHost:    hostOf(result.FinalURL),
		Partial:       result.Partial,
		Warnings:      result.Warnings,
	}

	// The parser leaves the chapter text empty when the number came from the URL
	// slug. Giving it a readable form means the client can show something
	// meaningful, and the reader can still edit it.
	if preview.Chapter == "" && preview.ChapterNumber != nil {
		preview.Chapter = scraper.FormatChapterNumber(*preview.ChapterNumber)
	}

	// The reader pasted a link, so a preview with no title at all would be
	// unusable. The host is a better default than an empty box.
	if strings.TrimSpace(preview.Title) == "" {
		preview.Title = preview.SourceHost
	}
	if preview.Warnings == nil {
		preview.Warnings = []string{}
	}

	return preview, nil
}

// --- create -----------------------------------------------------------------

// Create saves a bookmark.
func (s *Service) Create(ctx context.Context, userID string, in CreateInput) (*Bookmark, error) {
	title := strings.TrimSpace(in.Title)
	if err := validateTitle(title); err != nil {
		return nil, err
	}

	currentURL, err := normalizeURL(in.CurrentURL)
	if err != nil {
		return nil, err
	}

	chapter := strings.TrimSpace(in.Chapter)
	if len(chapter) > MaxChapterLength {
		chapter = chapter[:MaxChapterLength]
	}

	status := Status(strings.TrimSpace(in.Status))
	if status == "" {
		status = DefaultStatus
	}
	if !ValidStatus(string(status)) {
		return nil, newFieldError("status", "Choose one of: reading, paused, completed, dropped.", ErrInvalidStatus)
	}

	bookmark := &Bookmark{
		UserID:        userID,
		Title:         title,
		CurrentURL:    currentURL,
		Chapter:       chapter,
		ChapterNumber: in.ChapterNumber,
		CoverURL:      strings.TrimSpace(in.CoverURL),
		SourceHost:    hostOf(currentURL),
		Status:        status,
		CreatedAt:     s.now(),
	}

	if err := s.repo.Create(ctx, bookmark); err != nil {
		return nil, err
	}

	s.logger.InfoContext(ctx, "bookmark created",
		"user_id", userID, "bookmark_id", bookmark.ID, "chapter", bookmark.Chapter)

	return bookmark, nil
}

// --- read -------------------------------------------------------------------

// List returns a reader's library.
func (s *Service) List(ctx context.Context, userID string, filter Filter) ([]*Bookmark, error) {
	if filter.Query != "" && len(filter.Query) > MaxQueryLength {
		return nil, newFieldError("q", fmt.Sprintf("Search terms are limited to %d characters.", MaxQueryLength), nil)
	}
	if filter.Status != "" && !ValidStatus(string(filter.Status)) {
		return nil, newFieldError("status", "Choose one of: reading, paused, completed, dropped.", ErrInvalidStatus)
	}

	return s.repo.List(ctx, userID, filter)
}

// Get returns one bookmark belonging to the reader.
func (s *Service) Get(ctx context.Context, userID, id string) (*Bookmark, error) {
	return s.repo.Get(ctx, userID, id)
}

// --- update -----------------------------------------------------------------

// Update applies a partial edit.
func (s *Service) Update(ctx context.Context, userID, id string, in UpdateInput) (*Bookmark, error) {
	bookmark, err := s.repo.Get(ctx, userID, id)
	if err != nil {
		return nil, err
	}

	changed := false

	if in.Title != nil {
		title := strings.TrimSpace(*in.Title)
		if err := validateTitle(title); err != nil {
			return nil, err
		}
		if title != bookmark.Title {
			bookmark.Title = title
			changed = true
		}
	}

	if in.Chapter != nil {
		chapter := strings.TrimSpace(*in.Chapter)
		if len(chapter) > MaxChapterLength {
			chapter = chapter[:MaxChapterLength]
		}
		if chapter != bookmark.Chapter {
			bookmark.Chapter = chapter
			changed = true
		}
	}

	switch {
	case in.ClearChapterNumber:
		// The reader is telling us the chapter is not numeric after all.
		bookmark.ChapterNumber = nil
		changed = true
	case in.ChapterNumber != nil:
		if bookmark.ChapterNumber == nil || *bookmark.ChapterNumber != *in.ChapterNumber {
			bookmark.ChapterNumber = in.ChapterNumber
			changed = true
		}
	}

	if in.CoverURL != nil {
		cover := strings.TrimSpace(*in.CoverURL)
		if cover != bookmark.CoverURL {
			bookmark.CoverURL = cover
			changed = true
		}
	}

	if in.Status != nil {
		status := Status(strings.TrimSpace(*in.Status))
		if !ValidStatus(string(status)) {
			return nil, newFieldError("status", "Choose one of: reading, paused, completed, dropped.", ErrInvalidStatus)
		}
		if status != bookmark.Status {
			bookmark.Status = status
			changed = true
		}
	}

	if !changed {
		// Reported rather than treated as success, so a client sending the wrong
		// field name finds out instead of believing it worked.
		return nil, ErrNothingToUpdate
	}

	if err := s.repo.Update(ctx, bookmark); err != nil {
		return nil, err
	}

	s.logger.InfoContext(ctx, "bookmark updated", "user_id", userID, "bookmark_id", id)
	return bookmark, nil
}

// --- the +1 action ----------------------------------------------------------

// ReadNext advances the chapter by one and marks the bookmark as just read.
//
// When the chapter carries no number there is nothing to add one to, so it
// reports ErrChapterNotNumeric and the client opens an editor instead. Guessing
// would silently move the reader to the wrong chapter.
func (s *Service) ReadNext(ctx context.Context, userID, id string) (*Bookmark, error) {
	bookmark, err := s.repo.Get(ctx, userID, id)
	if err != nil {
		return nil, err
	}

	if !bookmark.ChapterKnown() {
		return nil, newFieldError("chapter",
			"This chapter has no number to advance. Edit it to set the next one.",
			ErrChapterNotNumeric)
	}

	// Increment rewrites only the digits, so the site's own wording survives.
	text, next, ok := scraper.Increment(bookmark.Chapter, bookmark.ChapterNumber)
	if !ok {
		// Unreachable while ChapterKnown holds, but the guard keeps the
		// invariant local rather than trusting it from a distance.
		return nil, newFieldError("chapter",
			"This chapter has no number to advance. Edit it to set the next one.",
			ErrChapterNotNumeric)
	}

	bookmark.Chapter = text
	bookmark.ChapterNumber = next

	readAt := s.now()
	bookmark.LastReadAt = &readAt

	if err := s.repo.Update(ctx, bookmark); err != nil {
		return nil, err
	}

	s.logger.InfoContext(ctx, "chapter advanced",
		"user_id", userID, "bookmark_id", id, "chapter", bookmark.Chapter)

	return bookmark, nil
}

// MarkRead records that the reader opened the link, without touching the chapter.
//
// This is what makes the library order itself by "continue reading".
func (s *Service) MarkRead(ctx context.Context, userID, id string) (*Bookmark, error) {
	bookmark, err := s.repo.Get(ctx, userID, id)
	if err != nil {
		return nil, err
	}

	readAt := s.now()
	bookmark.LastReadAt = &readAt

	if err := s.repo.Update(ctx, bookmark); err != nil {
		return nil, err
	}

	s.logger.DebugContext(ctx, "bookmark marked read", "user_id", userID, "bookmark_id", id)
	return bookmark, nil
}

// Delete removes a bookmark.
func (s *Service) Delete(ctx context.Context, userID, id string) error {
	if err := s.repo.Delete(ctx, userID, id); err != nil {
		return err
	}

	s.logger.InfoContext(ctx, "bookmark deleted", "user_id", userID, "bookmark_id", id)
	return nil
}

// --- validation -------------------------------------------------------------

func validateTitle(title string) error {
	switch {
	case title == "":
		return newFieldError("title", "Give this bookmark a title.", ErrTitleRequired)
	case len(title) > MaxTitleLength:
		return newFieldError("title",
			fmt.Sprintf("Titles are limited to %d characters.", MaxTitleLength), nil)
	}
	return nil
}

// normalizeURL validates a link before it is stored.
//
// It deliberately does not apply the scraper's SSRF rules: the reader may save
// any link they like, because ComicMark never fetches it again. What it does
// enforce is that the value is an absolute http(s) URL, so the "open reader"
// button cannot be turned into a link to a local file or a javascript: URI.
func normalizeURL(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", newFieldError("current_url", "A link is required.", ErrURLRequired)
	}
	if len(trimmed) > maxURLLength {
		return "", newFieldError("current_url", "That link is too long to store.", nil)
	}

	u, err := url.Parse(trimmed)
	if err != nil {
		return "", newFieldError("current_url", "Enter a valid link.", nil)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		// A javascript: or file: link would be handed straight to the reader's
		// device by the open button.
		return "", newFieldError("current_url", "Only http and https links can be saved.", nil)
	}
	if u.Host == "" {
		return "", newFieldError("current_url", "Enter a valid link.", nil)
	}

	return u.String(), nil
}

// hostOf extracts the host from a URL, for the badge the library shows.
func hostOf(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	return u.Hostname()
}
