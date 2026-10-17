package bookmark

import (
	"context"
	"database/sql"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"comicmark/go-modular/internal/pkg/database"
	"comicmark/go-modular/internal/pkg/scraper"
	"comicmark/go-modular/internal/pkg/sqltime"
)

// These tests run the use cases against a real SQLite file, so the SQL, the
// unique index, the ordering and the ownership scoping are all exercised rather
// than mocked.

func newTestRepository(t *testing.T) (*Repository, *sql.DB) {
	t.Helper()

	db, err := database.Open(context.Background(), filepath.Join(t.TempDir(), "bookmarks.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })

	_, err = database.NewMigrator(db).Up(context.Background())
	require.NoError(t, err)

	return NewRepository(db), db
}

// seedUser inserts a user row directly.
//
// bookmarks.user_id is a foreign key, so a test reader has to exist. Inserting
// the row here rather than calling the auth module keeps this package's tests
// independent of it, which is the point of the constraint.
func seedUser(t *testing.T, db *sql.DB, id, email string) {
	t.Helper()

	_, err := db.ExecContext(context.Background(),
		`INSERT INTO users (id, email, created_at) VALUES (?, ?, ?)`,
		id, email, sqltime.Format(time.Now()))
	require.NoError(t, err)
}

// stubFetcher answers without a network, and records what it was asked for.
type stubFetcher struct {
	result    *scraper.Result
	err       error
	lastURL   string
	callCount int
}

func (f *stubFetcher) Fetch(_ context.Context, rawURL string) (*scraper.Result, error) {
	f.lastURL = rawURL
	f.callCount++
	if f.err != nil {
		return nil, f.err
	}
	if f.result != nil {
		return f.result, nil
	}
	return &scraper.Result{
		FinalURL: rawURL,
		Title:    "One Piece",
		CoverURL: "https://cdn.example.com/cover.jpg",
		Chapter: scraper.Chapter{
			Text:   "Chapter 1050",
			Number: floatPointer(1050),
			Source: scraper.SourceSlug,
		},
	}, nil
}

// testService builds a service over a temporary database.
type testService struct {
	*Service
	repo    *Repository
	fetcher *stubFetcher
}

func newTestService(t *testing.T, fetcher *stubFetcher) *testService {
	t.Helper()

	if fetcher == nil {
		fetcher = &stubFetcher{}
	}
	repo, db := newTestRepository(t)

	// Every test reader the suite uses has to exist for the foreign key.
	seedUser(t, db, readerID, "reader1@example.com")
	seedUser(t, db, "reader-2", "reader2@example.com")

	svc, err := NewService(Options{
		Repository: repo,
		Fetcher:    fetcher,
		Logger:     discardLogger(),
	})
	require.NoError(t, err)

	return &testService{Service: svc, repo: repo, fetcher: fetcher}
}

func discardLogger() *slog.Logger { return slog.New(slog.DiscardHandler) }

func floatPointer(value float64) *float64 { return &value }

// readerID is a stand-in for an authenticated reader.
const readerID = "reader-1"

func createInput(title, url, chapter string, number *float64) CreateInput {
	return CreateInput{
		Title:         title,
		CurrentURL:    url,
		Chapter:       chapter,
		ChapterNumber: number,
		CoverURL:      "https://cdn.example.com/cover.jpg",
	}
}

// mustCreate saves a bookmark and returns it.
func mustCreate(t *testing.T, svc *testService, in CreateInput) *Bookmark {
	t.Helper()

	b, err := svc.Create(context.Background(), readerID, in)
	require.NoError(t, err)
	return b
}

// --- create -----------------------------------------------------------------

func TestCreateStoresABookmark(t *testing.T) {
	svc := newTestService(t, nil)

	b := mustCreate(t, svc, createInput("One Piece", "https://reader.example.com/one-piece-chapter-1050",
		"Chapter 1050", floatPointer(1050)))

	assert.NotEmpty(t, b.ID)
	assert.Equal(t, readerID, b.UserID)
	assert.Equal(t, "One Piece", b.Title)
	assert.Equal(t, "reader.example.com", b.SourceHost, "the host is derived from the link")
	assert.Equal(t, StatusReading, b.Status, "a new bookmark is being read")
	assert.False(t, b.CreatedAt.IsZero())
	assert.Nil(t, b.LastReadAt, "nothing has been opened yet")
	require.NotNil(t, b.ChapterNumber)
	assert.Equal(t, 1050.0, *b.ChapterNumber)
}

func TestCreateRejectsInputProblems(t *testing.T) {
	svc := newTestService(t, nil)
	ctx := context.Background()

	cases := map[string]struct {
		in    CreateInput
		field string
	}{
		"no title": {
			in:    createInput("   ", "https://example.com/1", "", nil),
			field: "title",
		},
		"no url": {
			in:    createInput("One Piece", "  ", "", nil),
			field: "current_url",
		},
		"relative url": {
			in:    createInput("One Piece", "not-a-url", "", nil),
			field: "current_url",
		},
		// A javascript: or file: link would be handed straight to the reader's
		// device by the open button.
		"javascript url": {
			in:    createInput("One Piece", "javascript:alert(1)", "", nil),
			field: "current_url",
		},
		"file url": {
			in:    createInput("One Piece", "file:///etc/passwd", "", nil),
			field: "current_url",
		},
		"unknown status": {
			in: func() CreateInput {
				in := createInput("One Piece", "https://example.com/1", "", nil)
				in.Status = "on-hold"
				return in
			}(),
			field: "status",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := svc.Create(ctx, readerID, tc.in)
			require.Error(t, err)

			var fieldErr *FieldError
			require.ErrorAs(t, err, &fieldErr)
			assert.Equal(t, tc.field, fieldErr.Field)
		})
	}
}

func TestCreateRejectsADuplicateLink(t *testing.T) {
	svc := newTestService(t, nil)
	ctx := context.Background()

	mustCreate(t, svc, createInput("One Piece", "https://reader.example.com/one-piece-chapter-1050", "Chapter 1050", floatPointer(1050)))

	_, err := svc.Create(ctx, readerID, createInput("One Piece again", "https://reader.example.com/one-piece-chapter-1050", "", nil))
	require.ErrorIs(t, err, ErrDuplicate)
}

// Two readers can each save the same link; the unique index is per reader.
func TestCreateAllowsTheSameLinkForADifferentReader(t *testing.T) {
	svc := newTestService(t, nil)
	ctx := context.Background()

	const url = "https://reader.example.com/one-piece-chapter-1050"
	mustCreate(t, svc, createInput("One Piece", url, "Chapter 1050", floatPointer(1050)))

	_, err := svc.Create(ctx, "reader-2", createInput("One Piece", url, "Chapter 1050", floatPointer(1050)))
	require.NoError(t, err)
}

func TestCreateDefaultsTheStatus(t *testing.T) {
	svc := newTestService(t, nil)

	in := createInput("One Piece", "https://example.com/1", "", nil)
	in.Status = ""
	b := mustCreate(t, svc, in)

	assert.Equal(t, StatusReading, b.Status)
}

// --- read and isolation -----------------------------------------------------

func TestGetMappingScopesToTheReader(t *testing.T) {
	svc := newTestService(t, nil)
	ctx := context.Background()

	mine := mustCreate(t, svc, createInput("One Piece", "https://example.com/1", "Chapter 1", floatPointer(1)))

	got, err := svc.Get(ctx, readerID, mine.ID)
	require.NoError(t, err)
	assert.Equal(t, mine.ID, got.ID)

	// Another reader asking for the same id gets "not found", not "forbidden":
	// a distinct error would confirm the bookmark exists.
	_, err = svc.Get(ctx, "reader-2", mine.ID)
	require.ErrorIs(t, err, ErrNotFound)

	_, err = svc.Get(ctx, readerID, "no-such-bookmark")
	require.ErrorIs(t, err, ErrNotFound)
}

func TestListIsScopedToTheReader(t *testing.T) {
	svc := newTestService(t, nil)
	ctx := context.Background()

	mustCreate(t, svc, createInput("Mine", "https://example.com/mine", "", nil))
	_, err := svc.Create(ctx, "reader-2", createInput("Theirs", "https://example.com/theirs", "", nil))
	require.NoError(t, err)

	mine, err := svc.List(ctx, readerID, Filter{})
	require.NoError(t, err)
	require.Len(t, mine, 1)
	assert.Equal(t, "Mine", mine[0].Title)
}

func TestUpdateAndDeleteCannotTouchAnotherReadersBookmark(t *testing.T) {
	svc := newTestService(t, nil)
	ctx := context.Background()

	mine := mustCreate(t, svc, createInput("One Piece", "https://example.com/1", "Chapter 1", floatPointer(1)))

	title := "Hijacked"
	_, err := svc.Update(ctx, "reader-2", mine.ID, UpdateInput{Title: &title})
	require.ErrorIs(t, err, ErrNotFound)

	require.ErrorIs(t, svc.Delete(ctx, "reader-2", mine.ID), ErrNotFound)

	// And it is untouched.
	got, err := svc.Get(ctx, readerID, mine.ID)
	require.NoError(t, err)
	assert.Equal(t, "One Piece", got.Title)
}

// --- ordering and filtering -------------------------------------------------

// The library is ordered for "continue reading": what was opened most recently
// comes first, and anything never opened sits at the end.
func TestListOrdersByLastReadThenRecency(t *testing.T) {
	svc := newTestService(t, nil)
	ctx := context.Background()

	first := mustCreate(t, svc, createInput("First", "https://example.com/1", "", nil))
	second := mustCreate(t, svc, createInput("Second", "https://example.com/2", "", nil))
	third := mustCreate(t, svc, createInput("Third", "https://example.com/3", "", nil))

	// Nothing read yet: newest first.
	got := mustList(t, svc, Filter{})
	assert.Equal(t, []string{"Third", "Second", "First"}, titles(got))

	// Reading the oldest moves it to the front of "continue reading".
	_, err := svc.MarkRead(ctx, readerID, first.ID)
	require.NoError(t, err)

	got = mustList(t, svc, Filter{})
	assert.Equal(t, []string{"First", "Third", "Second"}, titles(got))

	// And reading the newest puts it in front of that.
	time.Sleep(2 * time.Millisecond)
	_, err = svc.MarkRead(ctx, readerID, third.ID)
	require.NoError(t, err)

	got = mustList(t, svc, Filter{})
	assert.Equal(t, []string{"Third", "First", "Second"}, titles(got))
	_ = second
}

func TestListFiltersByStatus(t *testing.T) {
	svc := newTestService(t, nil)
	ctx := context.Background()

	paused := createInput("Paused", "https://example.com/paused", "", nil)
	paused.Status = string(StatusPaused)
	mustCreate(t, svc, paused)

	completed := createInput("Completed", "https://example.com/completed", "", nil)
	completed.Status = string(StatusCompleted)
	mustCreate(t, svc, completed)

	got := mustList(t, svc, Filter{Status: StatusPaused})
	require.Len(t, got, 1)
	assert.Equal(t, "Paused", got[0].Title)

	all := mustList(t, svc, Filter{})
	assert.Len(t, all, 2)

	_, err := svc.List(ctx, readerID, Filter{Status: "on-hold"})
	require.ErrorIs(t, err, ErrInvalidStatus)
}

func TestListSearchesTitleHostAndChapter(t *testing.T) {
	svc := newTestService(t, nil)

	mustCreate(t, svc, createInput("One Piece", "https://mangareader.example/one-piece-chapter-1050", "Chapter 1050", floatPointer(1050)))
	mustCreate(t, svc, createInput("Berserk", "https://othersite.example/berserk-1", "Prologue", nil))

	for name, tc := range map[string]struct {
		query string
		want  []string
	}{
		"title":            {"berserk", []string{"Berserk"}},
		"host":             {"mangareader", []string{"One Piece"}},
		"chapter":          {"prologue", []string{"Berserk"}},
		"case insensitive": {"BERSERK", []string{"Berserk"}},
		"no match":         {"nothing here", nil},
	} {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tc.want, titles(mustList(t, svc, Filter{Query: tc.query})))
		})
	}
}

// A search term containing a LIKE wildcard must be treated as text, not as a
// pattern that matches everything.
func TestListEscapesLikeWildcards(t *testing.T) {
	svc := newTestService(t, nil)

	mustCreate(t, svc, createInput("One Piece", "https://example.com/1", "", nil))
	mustCreate(t, svc, createInput("50% Off Comics", "https://example.com/2", "", nil))

	// The wildcard is matched as text, so it finds the title that contains one
	// and does not match everything else.
	assert.Equal(t, []string{"50% Off Comics"}, titles(mustList(t, svc, Filter{Query: "%"})),
		"a wildcard must be matched literally")
	assert.Equal(t, []string{"50% Off Comics"}, titles(mustList(t, svc, Filter{Query: "50%"})))
	assert.Empty(t, titles(mustList(t, svc, Filter{Query: "_"})),
		"an underscore is matched literally, and no title contains one")
}

func TestListPaginates(t *testing.T) {
	svc := newTestService(t, nil)
	ctx := context.Background()

	for _, title := range []string{"A", "B", "C", "D", "E"} {
		mustCreate(t, svc, createInput(title, "https://example.com/"+title, "", nil))
	}

	// Newest first, so the first page is the last two created.
	page1 := mustList(t, svc, Filter{Limit: 2})
	assert.Len(t, page1, 2)
	assert.Equal(t, []string{"E", "D"}, titles(page1))

	page2 := mustList(t, svc, Filter{Limit: 2, Offset: 2})
	assert.Equal(t, []string{"C", "B"}, titles(page2))

	page3 := mustList(t, svc, Filter{Limit: 2, Offset: 4})
	assert.Equal(t, []string{"A"}, titles(page3))

	// A limit beyond the cap is clamped rather than rejected.
	clamped := mustList(t, svc, Filter{Limit: 10_000})
	assert.Len(t, clamped, 5)

	total, err := svc.repo.Count(ctx, readerID)
	require.NoError(t, err)
	assert.Equal(t, 5, total)
}

func TestListRejectsAnOverlongSearchTerm(t *testing.T) {
	svc := newTestService(t, nil)

	_, err := svc.List(context.Background(), readerID, Filter{Query: strings.Repeat("x", MaxQueryLength+1)})
	require.Error(t, err)

	var fieldErr *FieldError
	require.ErrorAs(t, err, &fieldErr)
	assert.Equal(t, "q", fieldErr.Field)
}

// --- update -----------------------------------------------------------------

func TestUpdateAppliesOnlyTheFieldsGiven(t *testing.T) {
	svc := newTestService(t, nil)
	ctx := context.Background()

	b := mustCreate(t, svc, createInput("One Piece", "https://example.com/1", "Chapter 1050", floatPointer(1050)))

	title := "One Piece (coloured)"
	status := string(StatusCompleted)
	updated, err := svc.Update(ctx, readerID, b.ID, UpdateInput{Title: &title, Status: &status})
	require.NoError(t, err)

	assert.Equal(t, "One Piece (coloured)", updated.Title)
	assert.Equal(t, StatusCompleted, updated.Status)

	// Untouched fields survive.
	assert.Equal(t, "https://example.com/1", updated.CurrentURL)
	assert.Equal(t, "Chapter 1050", updated.Chapter)
	require.NotNil(t, updated.ChapterNumber)
	assert.Equal(t, 1050.0, *updated.ChapterNumber)
	assert.NotNil(t, updated.UpdatedAt)
}

// Clearing the number is how a reader says "this chapter is not a number", which
// is what puts the +1 button into its edit state.
func TestUpdateCanClearTheChapterNumber(t *testing.T) {
	svc := newTestService(t, nil)
	ctx := context.Background()

	b := mustCreate(t, svc, createInput("One Piece", "https://example.com/1", "Chapter 1050", floatPointer(1050)))

	chapter := "Special"
	updated, err := svc.Update(ctx, readerID, b.ID, UpdateInput{
		Chapter:            &chapter,
		ClearChapterNumber: true,
	})
	require.NoError(t, err)

	assert.Nil(t, updated.ChapterNumber)
	assert.Equal(t, "Special", updated.Chapter)
	assert.False(t, updated.ChapterKnown())

	// And now the +1 action refuses rather than guessing.
	_, err = svc.ReadNext(ctx, readerID, b.ID)
	require.ErrorIs(t, err, ErrChapterNotNumeric)
}

// A request that changes nothing is reported, so a client sending the wrong field
// name finds out instead of believing it worked.
func TestUpdateReportsNothingToChange(t *testing.T) {
	svc := newTestService(t, nil)
	ctx := context.Background()

	b := mustCreate(t, svc, createInput("One Piece", "https://example.com/1", "Chapter 1050", floatPointer(1050)))

	_, err := svc.Update(ctx, readerID, b.ID, UpdateInput{})
	require.ErrorIs(t, err, ErrNothingToUpdate)

	same := "One Piece"
	_, err = svc.Update(ctx, readerID, b.ID, UpdateInput{Title: &same})
	require.ErrorIs(t, err, ErrNothingToUpdate)
}

func TestUpdateRejectsBadValues(t *testing.T) {
	svc := newTestService(t, nil)
	ctx := context.Background()

	b := mustCreate(t, svc, createInput("One Piece", "https://example.com/1", "Chapter 1050", floatPointer(1050)))

	empty := "   "
	_, err := svc.Update(ctx, readerID, b.ID, UpdateInput{Title: &empty})
	require.ErrorIs(t, err, ErrTitleRequired)

	bad := "on-hold"
	_, err = svc.Update(ctx, readerID, b.ID, UpdateInput{Status: &bad})
	require.ErrorIs(t, err, ErrInvalidStatus)
}

// --- the +1 action ----------------------------------------------------------

func TestReadNextAdvancesTheChapter(t *testing.T) {
	svc := newTestService(t, nil)
	ctx := context.Background()

	b := mustCreate(t, svc, createInput("One Piece", "https://example.com/one-piece-chapter-1050",
		"Chapter 1050", floatPointer(1050)))

	next, err := svc.ReadNext(ctx, readerID, b.ID)
	require.NoError(t, err)

	require.NotNil(t, next.ChapterNumber)
	assert.Equal(t, 1051.0, *next.ChapterNumber)
	assert.Equal(t, "Chapter 1051", next.Chapter, "the site's wording is preserved")
	assert.NotNil(t, next.LastReadAt, "advancing counts as reading")

	// It persists, rather than only changing the response.
	stored, err := svc.Get(ctx, readerID, b.ID)
	require.NoError(t, err)
	assert.Equal(t, "Chapter 1051", stored.Chapter)
	require.NotNil(t, stored.ChapterNumber)
	assert.Equal(t, 1051.0, *stored.ChapterNumber)
}

// A chapter with no number cannot be advanced, and guessing would silently move
// the reader somewhere wrong.
func TestReadNextRefusesANonNumericChapter(t *testing.T) {
	svc := newTestService(t, nil)
	ctx := context.Background()

	b := mustCreate(t, svc, createInput("One Piece", "https://example.com/one-piece-prologue", "Prologue", nil))

	_, err := svc.ReadNext(ctx, readerID, b.ID)
	require.ErrorIs(t, err, ErrChapterNotNumeric)

	var fieldErr *FieldError
	require.ErrorAs(t, err, &fieldErr)
	assert.Equal(t, "chapter", fieldErr.Field)

	// The bookmark is untouched.
	stored, err := svc.Get(ctx, readerID, b.ID)
	require.NoError(t, err)
	assert.Equal(t, "Prologue", stored.Chapter)
	assert.Nil(t, stored.ChapterNumber)
}

func TestReadNextAdvancesDecimals(t *testing.T) {
	svc := newTestService(t, nil)
	ctx := context.Background()

	b := mustCreate(t, svc, createInput("Some Comic", "https://example.com/some-comic-chapter-12.5",
		"Ch. 12.5", floatPointer(12.5)))

	next, err := svc.ReadNext(ctx, readerID, b.ID)
	require.NoError(t, err)

	require.NotNil(t, next.ChapterNumber)
	assert.Equal(t, 13.5, *next.ChapterNumber)
	assert.Equal(t, "Ch. 13.5", next.Chapter)
}

func TestReadNextCannotTouchAnotherReadersBookmark(t *testing.T) {
	svc := newTestService(t, nil)
	ctx := context.Background()

	b := mustCreate(t, svc, createInput("One Piece", "https://example.com/1", "Chapter 1", floatPointer(1)))

	_, err := svc.ReadNext(ctx, "reader-2", b.ID)
	require.ErrorIs(t, err, ErrNotFound)
}

func TestMarkReadLeavesTheChapterAlone(t *testing.T) {
	svc := newTestService(t, nil)
	ctx := context.Background()

	b := mustCreate(t, svc, createInput("One Piece", "https://example.com/1", "Chapter 1050", floatPointer(1050)))

	read, err := svc.MarkRead(ctx, readerID, b.ID)
	require.NoError(t, err)

	assert.Equal(t, "Chapter 1050", read.Chapter)
	require.NotNil(t, read.ChapterNumber)
	assert.Equal(t, 1050.0, *read.ChapterNumber)
	assert.NotNil(t, read.LastReadAt)
}

// --- delete -----------------------------------------------------------------

func TestDeleteRemovesTheBookmark(t *testing.T) {
	svc := newTestService(t, nil)
	ctx := context.Background()

	b := mustCreate(t, svc, createInput("One Piece", "https://example.com/1", "", nil))

	require.NoError(t, svc.Delete(ctx, readerID, b.ID))

	_, err := svc.Get(ctx, readerID, b.ID)
	require.ErrorIs(t, err, ErrNotFound)

	// Deleting twice reports the second attempt rather than pretending.
	require.ErrorIs(t, svc.Delete(ctx, readerID, b.ID), ErrNotFound)
}

// --- preview ----------------------------------------------------------------

func TestParseReturnsAPreviewWithoutSavingAnything(t *testing.T) {
	fetcher := &stubFetcher{}
	svc := newTestService(t, fetcher)
	ctx := context.Background()

	preview, err := svc.Parse(ctx, "https://reader.example.com/one-piece-chapter-1050")
	require.NoError(t, err)

	assert.Equal(t, "One Piece", preview.Title)
	assert.Equal(t, "https://cdn.example.com/cover.jpg", preview.CoverURL)
	assert.Equal(t, "Chapter 1050", preview.Chapter)
	require.NotNil(t, preview.ChapterNumber)
	assert.Equal(t, 1050.0, *preview.ChapterNumber)
	assert.Equal(t, "reader.example.com", preview.SourceHost)
	assert.False(t, preview.Partial)

	// Nothing was written.
	stored, err := svc.List(ctx, readerID, Filter{})
	require.NoError(t, err)
	assert.Empty(t, stored, "parsing a link must not save it")
	assert.Equal(t, 1, fetcher.callCount)
}

// A URL the guard refuses is reported as an error, because the reader can fix it.
func TestParseReportsARefusedURL(t *testing.T) {
	svc := newTestService(t, &stubFetcher{err: scraper.ErrURLNotAllowed})

	_, err := svc.Parse(context.Background(), "http://169.254.169.254/latest/meta-data/")
	require.ErrorIs(t, err, scraper.ErrURLNotAllowed)
}

// A site that blocks the bot yields a partial preview, not a failure: the reader
// can still save the bookmark by typing the title.
func TestParseReturnsAPartialPreview(t *testing.T) {
	svc := newTestService(t, &stubFetcher{result: &scraper.Result{
		FinalURL: "https://reader.example.com/one-piece-chapter-1050",
		Partial:  true,
		Warnings: []string{"the site replied with 403"},
	}})

	preview, err := svc.Parse(context.Background(), "https://reader.example.com/one-piece-chapter-1050")
	require.NoError(t, err)

	assert.True(t, preview.Partial)
	assert.Contains(t, preview.Warnings, "the site replied with 403")
	// The host is a better default than an empty title box.
	assert.Equal(t, "reader.example.com", preview.Title)
}

// The parser leaves the chapter empty when the number came from the URL slug.
// The preview fills it in so the client has something to show and edit.
func TestParseGivesARedableChapterWhenTheTitleHasNone(t *testing.T) {
	svc := newTestService(t, &stubFetcher{result: &scraper.Result{
		FinalURL: "https://reader.example.com/one-piece-chapter-1050",
		Title:    "One Piece",
		Chapter: scraper.Chapter{
			Number: floatPointer(1050),
			Source: scraper.SourceSlug,
		},
	}})

	preview, err := svc.Parse(context.Background(), "https://reader.example.com/one-piece-chapter-1050")
	require.NoError(t, err)

	assert.Equal(t, "1050", preview.Chapter)
	require.NotNil(t, preview.ChapterNumber)
}

func TestParseAlwaysReportsWarningsAsAList(t *testing.T) {
	svc := newTestService(t, &stubFetcher{result: &scraper.Result{
		FinalURL: "https://reader.example.com/one-piece-chapter-1050",
		Title:    "One Piece",
		Chapter:  scraper.Chapter{Number: floatPointer(1050), Source: scraper.SourceSlug},
	}})

	preview, err := svc.Parse(context.Background(), "https://reader.example.com/one-piece-chapter-1050")
	require.NoError(t, err)

	// A client should not have to distinguish null from an empty list.
	assert.NotNil(t, preview.Warnings)
}

// --- a full reader journey --------------------------------------------------

// The sequence a real reader performs, against a real database, so the pieces are
// proven to work together rather than only in isolation.
func TestReaderJourney(t *testing.T) {
	svc := newTestService(t, nil)
	ctx := context.Background()

	// 1. Paste a link and look at the preview.
	preview, err := svc.Parse(ctx, "https://reader.example.com/one-piece-chapter-1050")
	require.NoError(t, err)
	assert.Equal(t, "One Piece", preview.Title)

	// 2. Save it.
	saved, err := svc.Create(ctx, readerID, CreateInput{
		Title:         preview.Title,
		CurrentURL:    preview.CurrentURL,
		Chapter:       preview.Chapter,
		ChapterNumber: preview.ChapterNumber,
		CoverURL:      preview.CoverURL,
	})
	require.NoError(t, err)
	assert.Equal(t, "Chapter 1050", saved.Chapter)

	// 3. Open the reader, which records the visit.
	opened, err := svc.MarkRead(ctx, readerID, saved.ID)
	require.NoError(t, err)
	assert.NotNil(t, opened.LastReadAt)

	// 4. Finish the chapter and tap +1.
	advanced, err := svc.ReadNext(ctx, readerID, saved.ID)
	require.NoError(t, err)
	assert.Equal(t, "Chapter 1051", advanced.Chapter)

	// 5. Come back later: it is at the top of the library.
	library, err := svc.List(ctx, readerID, Filter{})
	require.NoError(t, err)
	require.Len(t, library, 1)
	assert.Equal(t, "Chapter 1051", library[0].Chapter)

	// 6. Remove it.
	require.NoError(t, svc.Delete(ctx, readerID, saved.ID))

	library, err = svc.List(ctx, readerID, Filter{})
	require.NoError(t, err)
	assert.Empty(t, library)
}

// --- helpers ----------------------------------------------------------------

func mustList(t *testing.T, svc *testService, filter Filter) []*Bookmark {
	t.Helper()

	got, err := svc.List(context.Background(), readerID, filter)
	require.NoError(t, err)
	return got
}

// titles extracts the titles for a readable assertion. It returns nil for an
// empty input so comparisons read naturally.
func titles(bookmarks []*Bookmark) []string {
	if len(bookmarks) == 0 {
		return nil
	}

	out := make([]string, 0, len(bookmarks))
	for _, b := range bookmarks {
		out = append(out, b.Title)
	}
	return out
}
