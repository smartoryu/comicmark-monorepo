package main

// HTTP-level tests for the bookmark endpoints. They drive the real router, so the
// route table, the authentication guard, the status codes and the error envelope
// are all covered alongside the use cases.
//
// Cases the plan calls out for this step:
//
//	full CRUD                       TestBookmarkCRUDOverHTTP
//	cross-user 404 isolation        TestBookmarkIsolationBetweenReaders
//	duplicate 409                   TestDuplicateBookmarkIsRejected
//	filter / sort / pagination      TestBookmarkListFilterSortAndPage
//	+1 happy path                   TestReadNextOverHTTP
//	+1 with no number -> 409        TestReadNextRefusesANonNumericChapter

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"comicmark/go-modular/internal/httpx"
)

// --- fixtures ---------------------------------------------------------------

// bookmarkEnvelope mirrors the documented bookmark response.
type bookmarkEnvelope struct {
	ID            string   `json:"id"`
	Title         string   `json:"title"`
	CurrentURL    string   `json:"current_url"`
	Chapter       string   `json:"chapter"`
	ChapterNumber *float64 `json:"chapter_number"`
	CoverURL      string   `json:"cover_url"`
	SourceHost    string   `json:"source_host"`
	Status        string   `json:"status"`
	LastReadAt    *string  `json:"last_read_at"`
}

type listEnvelope struct {
	Bookmarks []bookmarkEnvelope `json:"bookmarks"`
	Total     int                `json:"total"`
	Limit     int                `json:"limit"`
	Offset    int                `json:"offset"`
}

// reader is a signed-in account plus the token to act as them.
type reader struct {
	token string
	id    string
}

// newReader registers an account and returns it.
func newReader(t *testing.T, srv *apiServer, email string) reader {
	t.Helper()

	var out authEnvelope
	w := callJSON(t, srv, http.MethodPost, "/api/v1/auth/register", map[string]string{
		"email": email, "password": "correct horse battery",
	}, &out, nil)
	require.Equal(t, http.StatusCreated, w.Code, "body: %s", w.Body.String())

	return reader{token: out.AccessToken, id: out.User.ID}
}

// saveBookmark creates a bookmark and returns it.
func saveBookmark(t *testing.T, srv *apiServer, r reader, body map[string]any) bookmarkEnvelope {
	t.Helper()

	var out bookmarkEnvelope
	w := callJSON(t, srv, http.MethodPost, "/api/v1/bookmarks", body, &out, bearer(r.token))
	require.Equal(t, http.StatusCreated, w.Code, "body: %s", w.Body.String())
	return out
}

// library lists a reader's bookmarks.
func library(t *testing.T, srv *apiServer, r reader, query string) listEnvelope {
	t.Helper()

	var out listEnvelope
	w := callJSON(t, srv, http.MethodGet, "/api/v1/bookmarks"+query, nil, &out, bearer(r.token))
	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
	return out
}

func bookmarkBody(title, url, chapter string) map[string]any {
	body := map[string]any{"title": title, "current_url": url}
	if chapter != "" {
		body["chapter"] = chapter
	}
	return body
}

// --- authentication ---------------------------------------------------------

// A bookmark belongs to a reader, so every route requires one. The parse endpoint
// especially must not be an open fetch proxy.
func TestEveryBookmarkRouteRequiresAuthentication(t *testing.T) {
	srv := newTestServer(t, testMailer(t))

	routes := []struct {
		method string
		path   string
		body   any
	}{
		{http.MethodPost, "/api/v1/bookmarks/parse", map[string]string{"url": "https://example.com/1"}},
		{http.MethodPost, "/api/v1/bookmarks", bookmarkBody("One Piece", "https://example.com/1", "")},
		{http.MethodGet, "/api/v1/bookmarks", nil},
		{http.MethodGet, "/api/v1/bookmarks/some-id", nil},
		{http.MethodPatch, "/api/v1/bookmarks/some-id", map[string]any{"status": "paused"}},
		{http.MethodPatch, "/api/v1/bookmarks/some-id/read-next", nil},
		{http.MethodPost, "/api/v1/bookmarks/some-id/read", nil},
		{http.MethodDelete, "/api/v1/bookmarks/some-id", nil},
	}

	for _, route := range routes {
		t.Run(route.method+" "+route.path, func(t *testing.T) {
			w := call(t, srv, route.method, route.path, route.body, nil)

			assert.Equal(t, http.StatusUnauthorized, w.Code,
				"an unauthenticated caller must not reach a bookmark")
		})
	}
}

// --- full CRUD --------------------------------------------------------------

func TestBookmarkCRUDOverHTTP(t *testing.T) {
	srv := newTestServer(t, testMailer(t))
	r := newReader(t, srv, "reader@example.com")

	// Create.
	created := saveBookmark(t, srv, r, map[string]any{
		"title":          "One Piece",
		"current_url":    "https://reader.example.com/one-piece-chapter-1050",
		"chapter":        "Chapter 1050",
		"chapter_number": 1050,
		"cover_url":      "https://cdn.example.com/cover.jpg",
	})
	require.NotEmpty(t, created.ID)
	assert.Equal(t, "One Piece", created.Title)
	assert.Equal(t, "reading", created.Status, "a new bookmark defaults to reading")
	assert.Equal(t, "reader.example.com", created.SourceHost)
	require.NotNil(t, created.ChapterNumber)
	assert.Equal(t, 1050.0, *created.ChapterNumber)
	assert.Nil(t, created.LastReadAt)

	// Read one.
	var fetched bookmarkEnvelope
	w := callJSON(t, srv, http.MethodGet, "/api/v1/bookmarks/"+created.ID, nil, &fetched, bearer(r.token))
	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
	assert.Equal(t, created.ID, fetched.ID)

	// Update, partially.
	var updated bookmarkEnvelope
	w = callJSON(t, srv, http.MethodPatch, "/api/v1/bookmarks/"+created.ID,
		map[string]any{"status": "completed", "title": "One Piece (coloured)"}, &updated, bearer(r.token))
	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
	assert.Equal(t, "completed", updated.Status)
	assert.Equal(t, "One Piece (coloured)", updated.Title)
	// The chapter is untouched by a PATCH that did not mention it.
	assert.Equal(t, "Chapter 1050", updated.Chapter)

	// Delete.
	w = call(t, srv, http.MethodDelete, "/api/v1/bookmarks/"+created.ID, nil, bearer(r.token))
	require.Equal(t, http.StatusNoContent, w.Code)

	// And it is gone.
	w = call(t, srv, http.MethodGet, "/api/v1/bookmarks/"+created.ID, nil, bearer(r.token))
	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestCreateBookmarkRejectsInputProblems(t *testing.T) {
	srv := newTestServer(t, testMailer(t))
	r := newReader(t, srv, "reader@example.com")

	cases := map[string]struct {
		body  map[string]any
		field string
	}{
		"no title": {
			body:  map[string]any{"current_url": "https://example.com/1"},
			field: "title",
		},
		"no url": {
			body:  map[string]any{"title": "One Piece"},
			field: "current_url",
		},
		"javascript url": {
			body:  map[string]any{"title": "One Piece", "current_url": "javascript:alert(1)"},
			field: "current_url",
		},
		"unknown status": {
			body: map[string]any{
				"title": "One Piece", "current_url": "https://example.com/1", "status": "on-hold",
			},
			field: "status",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			var env httpx.ErrorEnvelope
			w := callJSON(t, srv, http.MethodPost, "/api/v1/bookmarks", tc.body, &env, bearer(r.token))

			require.Equal(t, http.StatusBadRequest, w.Code, "body: %s", w.Body.String())
			require.NotNil(t, env.Error.Details, "a field problem should point at the field")
			assert.Contains(t, env.Error.Details, tc.field)
		})
	}
}

func TestDuplicateBookmarkIsRejected(t *testing.T) {
	srv := newTestServer(t, testMailer(t))
	r := newReader(t, srv, "reader@example.com")

	const url = "https://reader.example.com/one-piece-chapter-1050"
	saveBookmark(t, srv, r, bookmarkBody("One Piece", url, "Chapter 1050"))

	var env httpx.ErrorEnvelope
	w := callJSON(t, srv, http.MethodPost, "/api/v1/bookmarks",
		bookmarkBody("One Piece again", url, ""), &env, bearer(r.token))

	assert.Equal(t, http.StatusConflict, w.Code)
	assert.Equal(t, "duplicate_bookmark", env.Error.Code)
}

// --- isolation --------------------------------------------------------------

// Another reader's bookmark must be reported as not found rather than forbidden:
// a distinct error would confirm the id exists.
func TestBookmarkIsolationBetweenReaders(t *testing.T) {
	srv := newTestServer(t, testMailer(t))
	owner := newReader(t, srv, "owner@example.com")
	intruder := newReader(t, srv, "intruder@example.com")

	mine := saveBookmark(t, srv, owner, bookmarkBody("One Piece", "https://example.com/1", "Chapter 1"))

	t.Run("reading is refused", func(t *testing.T) {
		w := call(t, srv, http.MethodGet, "/api/v1/bookmarks/"+mine.ID, nil, bearer(intruder.token))
		assert.Equal(t, http.StatusNotFound, w.Code)
	})

	t.Run("editing is refused", func(t *testing.T) {
		w := call(t, srv, http.MethodPatch, "/api/v1/bookmarks/"+mine.ID,
			map[string]any{"title": "Hijacked"}, bearer(intruder.token))
		assert.Equal(t, http.StatusNotFound, w.Code)
	})

	t.Run("advancing is refused", func(t *testing.T) {
		w := call(t, srv, http.MethodPatch, "/api/v1/bookmarks/"+mine.ID+"/read-next", nil, bearer(intruder.token))
		assert.Equal(t, http.StatusNotFound, w.Code)
	})

	t.Run("deleting is refused", func(t *testing.T) {
		w := call(t, srv, http.MethodDelete, "/api/v1/bookmarks/"+mine.ID, nil, bearer(intruder.token))
		assert.Equal(t, http.StatusNotFound, w.Code)
	})

	t.Run("the library shows only your own", func(t *testing.T) {
		assert.Empty(t, library(t, srv, intruder, "").Bookmarks)
		assert.Len(t, library(t, srv, owner, "").Bookmarks, 1)
	})

	t.Run("the bookmark is untouched", func(t *testing.T) {
		var fetched bookmarkEnvelope
		w := callJSON(t, srv, http.MethodGet, "/api/v1/bookmarks/"+mine.ID, nil, &fetched, bearer(owner.token))
		require.Equal(t, http.StatusOK, w.Code)
		assert.Equal(t, "One Piece", fetched.Title)
		assert.Equal(t, "Chapter 1", fetched.Chapter)
	})
}

// --- listing ----------------------------------------------------------------

func TestBookmarkListFilterSortAndPage(t *testing.T) {
	srv := newTestServer(t, testMailer(t))
	r := newReader(t, srv, "reader@example.com")

	// Three reading, two paused.
	for _, title := range []string{"Alpha", "Bravo", "Charlie"} {
		saveBookmark(t, srv, r, bookmarkBody(title, "https://mangareader.example/"+title, "Chapter 1"))
	}
	for _, title := range []string{"Delta", "Echo"} {
		body := bookmarkBody(title, "https://othersite.example/"+title, "Chapter 2")
		body["status"] = "paused"
		saveBookmark(t, srv, r, body)
	}

	t.Run("everything by default, newest first", func(t *testing.T) {
		got := library(t, srv, r, "")
		assert.Equal(t, 5, got.Total)
		assert.Equal(t, 50, got.Limit, "the response echoes the page size actually applied")
		assert.Equal(t, []string{"Echo", "Delta", "Charlie", "Bravo", "Alpha"}, titlesOf(got))
	})

	t.Run("filtered by status", func(t *testing.T) {
		got := library(t, srv, r, "?status=paused")
		assert.Equal(t, []string{"Echo", "Delta"}, titlesOf(got))
	})

	t.Run("an unknown status is rejected", func(t *testing.T) {
		w := call(t, srv, http.MethodGet, "/api/v1/bookmarks?status=on-hold", nil, bearer(r.token))
		assert.Equal(t, http.StatusBadRequest, w.Code)
	})

	t.Run("filtered by host", func(t *testing.T) {
		got := library(t, srv, r, "?q=othersite")
		assert.Equal(t, []string{"Echo", "Delta"}, titlesOf(got))
	})

	t.Run("paged", func(t *testing.T) {
		page := library(t, srv, r, "?limit=2")
		assert.Len(t, page.Bookmarks, 2)
		assert.Equal(t, 2, page.Limit)
		assert.Equal(t, []string{"Echo", "Delta"}, titlesOf(page))

		next := library(t, srv, r, "?limit=2&offset=2")
		assert.Equal(t, []string{"Charlie", "Bravo"}, titlesOf(next))
		assert.Equal(t, 2, next.Offset)
	})

	t.Run("an oversized limit is clamped, not rejected", func(t *testing.T) {
		assert.Equal(t, 200, library(t, srv, r, "?limit=10000").Limit)
	})

	t.Run("a nonsense limit is rejected", func(t *testing.T) {
		w := call(t, srv, http.MethodGet, "/api/v1/bookmarks?limit=0", nil, bearer(r.token))
		assert.Equal(t, http.StatusBadRequest, w.Code)

		w = call(t, srv, http.MethodGet, "/api/v1/bookmarks?limit=lots", nil, bearer(r.token))
		assert.Equal(t, http.StatusBadRequest, w.Code)
	})

	t.Run("reading then reordering", func(t *testing.T) {
		// Opening a bookmark moves it to the front of "continue reading".
		target := library(t, srv, r, "?q=alpha").Bookmarks[0]
		w := call(t, srv, http.MethodPost, "/api/v1/bookmarks/"+target.ID+"/read", nil, bearer(r.token))
		require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())

		got := library(t, srv, r, "")
		assert.Equal(t, "Alpha", got.Bookmarks[0].Title, "the most recently opened comes first")
		assert.NotNil(t, got.Bookmarks[0].LastReadAt)
	})
}

// --- the +1 action ----------------------------------------------------------

func TestReadNextOverHTTP(t *testing.T) {
	srv := newTestServer(t, testMailer(t))
	r := newReader(t, srv, "reader@example.com")

	saved := saveBookmark(t, srv, r, map[string]any{
		"title":          "One Piece",
		"current_url":    "https://reader.example.com/one-piece-chapter-1050",
		"chapter":        "Chapter 1050",
		"chapter_number": 1050,
	})

	var advanced bookmarkEnvelope
	w := callJSON(t, srv, http.MethodPatch, "/api/v1/bookmarks/"+saved.ID+"/read-next", nil, &advanced, bearer(r.token))
	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())

	assert.Equal(t, "Chapter 1051", advanced.Chapter, "the site's wording is preserved")
	require.NotNil(t, advanced.ChapterNumber)
	assert.Equal(t, 1051.0, *advanced.ChapterNumber)
	assert.NotNil(t, advanced.LastReadAt, "advancing counts as reading")

	// It persisted, so a reload shows the same chapter.
	var reloaded bookmarkEnvelope
	w = callJSON(t, srv, http.MethodGet, "/api/v1/bookmarks/"+saved.ID, nil, &reloaded, bearer(r.token))
	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "Chapter 1051", reloaded.Chapter)
}

// The 409 is the contract that puts the client into its edit-the-chapter flow,
// so it is asserted along with the follow-up that clears the state.
func TestReadNextRefusesANonNumericChapter(t *testing.T) {
	srv := newTestServer(t, testMailer(t))
	r := newReader(t, srv, "reader@example.com")

	saved := saveBookmark(t, srv, r, bookmarkBody("One Piece", "https://example.com/one-piece-prologue", "Prologue"))
	require.Nil(t, saved.ChapterNumber)

	var env httpx.ErrorEnvelope
	w := callJSON(t, srv, http.MethodPatch, "/api/v1/bookmarks/"+saved.ID+"/read-next", nil, &env, bearer(r.token))

	require.Equal(t, http.StatusConflict, w.Code)
	assert.Equal(t, "chapter_not_numeric", env.Error.Code)

	// The bookmark is unchanged, so the reader has not been moved anywhere.
	var fetched bookmarkEnvelope
	w = callJSON(t, srv, http.MethodGet, "/api/v1/bookmarks/"+saved.ID, nil, &fetched, bearer(r.token))
	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "Prologue", fetched.Chapter)
	assert.Nil(t, fetched.ChapterNumber)

	t.Run("editing in the chapter unblocks it", func(t *testing.T) {
		var updated bookmarkEnvelope
		w := callJSON(t, srv, http.MethodPatch, "/api/v1/bookmarks/"+saved.ID,
			map[string]any{"chapter": "Chapter 10", "chapter_number": 10}, &updated, bearer(r.token))
		require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())

		var advanced bookmarkEnvelope
		w = callJSON(t, srv, http.MethodPatch, "/api/v1/bookmarks/"+saved.ID+"/read-next", nil, &advanced, bearer(r.token))
		require.Equal(t, http.StatusOK, w.Code)
		assert.Equal(t, "Chapter 11", advanced.Chapter)
	})
}

// An explicit null is how the client says "this chapter is not a number", which
// is the other half of the +1 contract.
func TestPatchCanClearTheChapterNumber(t *testing.T) {
	srv := newTestServer(t, testMailer(t))
	r := newReader(t, srv, "reader@example.com")

	saved := saveBookmark(t, srv, r, map[string]any{
		"title": "One Piece", "current_url": "https://example.com/1",
		"chapter": "Chapter 5", "chapter_number": 5,
	})
	require.NotNil(t, saved.ChapterNumber)

	// The raw JSON matters here: null must reach the server, not be dropped.
	raw := strings.NewReader(`{"chapter_number": null, "chapter": "Special"}`)
	req := newRequest(http.MethodPatch, "/api/v1/bookmarks/"+saved.ID, raw)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+r.token)

	w := httptest.NewRecorder()
	srv.handler().ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())

	var updated bookmarkEnvelope
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &updated))
	assert.Nil(t, updated.ChapterNumber, "an explicit null clears the number")
	assert.Equal(t, "Special", updated.Chapter)

	// And now +1 refuses, which is the state the client's editor handles.
	var env httpx.ErrorEnvelope
	w = callJSON(t, srv, http.MethodPatch, "/api/v1/bookmarks/"+saved.ID+"/read-next", nil, &env, bearer(r.token))
	assert.Equal(t, http.StatusConflict, w.Code)
	assert.Equal(t, "chapter_not_numeric", env.Error.Code)
}

// --- preview over HTTP ------------------------------------------------------

// The scraper is stubbed here; the scraper's own behaviour is covered by its
// package tests. What matters at this level is the response shape and that
// nothing is saved.
func TestParseOverHTTPReturnsAPreviewWithoutSaving(t *testing.T) {
	rec := &recordingMailer{}
	srv := newTestServer(t, rec)
	r := newReader(t, srv, "reader@example.com")

	var preview struct {
		Title         string   `json:"title"`
		CoverURL      string   `json:"cover_url"`
		CurrentURL    string   `json:"current_url"`
		Chapter       string   `json:"chapter"`
		ChapterNumber *float64 `json:"chapter_number"`
		SourceHost    string   `json:"source_host"`
		Partial       bool     `json:"partial"`
		Warnings      []string `json:"warnings"`
	}
	w := callJSON(t, srv, http.MethodPost, "/api/v1/bookmarks/parse",
		map[string]string{"url": "https://reader.example.com/one-piece-chapter-1050"}, &preview, bearer(r.token))

	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
	assert.Equal(t, "Stubbed Comic", preview.Title)
	assert.Equal(t, "https://cdn.example.com/cover.jpg", preview.CoverURL)
	assert.Equal(t, "Chapter 1050", preview.Chapter)
	require.NotNil(t, preview.ChapterNumber)
	assert.Equal(t, 1050.0, *preview.ChapterNumber)
	assert.False(t, preview.Partial)
	// Always a list, never null, so a client need not handle both.
	assert.NotNil(t, preview.Warnings)

	assert.Empty(t, library(t, srv, r, "").Bookmarks, "parsing must not save anything")
}

func TestParseRejectsAMissingURL(t *testing.T) {
	srv := newTestServer(t, testMailer(t))
	r := newReader(t, srv, "reader@example.com")

	var env httpx.ErrorEnvelope
	w := callJSON(t, srv, http.MethodPost, "/api/v1/bookmarks/parse",
		map[string]string{"url": ""}, &env, bearer(r.token))

	require.Equal(t, http.StatusBadRequest, w.Code)
	assert.Equal(t, httpx.CodeValidation, env.Error.Code)
}

// --- helpers ----------------------------------------------------------------

func titlesOf(list listEnvelope) []string {
	if len(list.Bookmarks) == 0 {
		return nil
	}

	out := make([]string, 0, len(list.Bookmarks))
	for _, b := range list.Bookmarks {
		out = append(out, b.Title)
	}
	return out
}
