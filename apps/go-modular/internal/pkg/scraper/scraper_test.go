package scraper

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newFixtureFetcher builds a Fetcher pointed at a local test server.
//
// The fixture runs on loopback, so private addresses are allowed and the server's
// own client is injected. The parse path, the limits and the partial-result
// contract are all still exercised; only the guard is relaxed, and it has its own
// tests.
func newFixtureFetcher(t *testing.T, opts Options) *Fetcher {
	t.Helper()

	if opts.Guard == nil {
		opts.Guard = NewGuard(GuardOptions{AllowPrivate: true})
	}
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.DiscardHandler)
	}

	fetcher, err := New(opts)
	require.NoError(t, err)
	return fetcher
}

// fixtureServer serves one HTML body.
func fixtureServer(t *testing.T, status int, contentType, body string) *httptest.Server {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if contentType != "" {
			w.Header().Set("Content-Type", contentType)
		}
		w.WriteHeader(status)
		if r.Method != http.MethodHead {
			_, _ = io.WriteString(w, body)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// samplePage is a well-formed chapter page.
const samplePage = `<!doctype html>
<html>
<head>
  <meta property="og:title" content="One Piece">
  <meta property="og:image" content="/covers/one-piece.jpg">
  <meta property="og:site_name" content="Manga Reader">
  <title>Chapter 1050 - Manga Reader</title>
</head>
<body><h1>Chapter 1050</h1></body>
</html>`

// --- the happy path ---------------------------------------------------------

func TestFetchExtractsACompletePreview(t *testing.T) {
	srv := fixtureServer(t, http.StatusOK, "text/html; charset=utf-8", samplePage)
	fetcher := newFixtureFetcher(t, Options{
		UserAgent:  "ComicMarkBot/1.0",
		ContactURL: "https://example.com/bot",
		Guard:      NewGuard(GuardOptions{AllowPrivate: true}),
		Client:     srv.Client(),
	})

	// The chapter comes from the slug, which is what a real link looks like.
	result, err := fetcher.Fetch(context.Background(), srv.URL+"/one-piece-chapter-1050")
	require.NoError(t, err)

	assert.Equal(t, "One Piece", result.Title)
	assert.Equal(t, srv.URL+"/covers/one-piece.jpg", result.CoverURL,
		"a relative cover should resolve against the page it was found on")
	assert.Equal(t, "Manga Reader", result.SiteName)
	assert.Equal(t, http.StatusOK, result.StatusCode)

	require.True(t, result.Chapter.Found())
	assert.Equal(t, float64(1050), *result.Chapter.Number)

	assert.False(t, result.Partial, "a preview with a title, a cover and a chapter is complete")
	assert.Empty(t, result.Warnings)
}

func TestFetchSendsAnHonestUserAgent(t *testing.T) {
	var gotUA, gotAccept string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.UserAgent()
		gotAccept = r.Header.Get("Accept")
		w.Header().Set("Content-Type", "text/html")
		_, _ = io.WriteString(w, samplePage)
	}))
	t.Cleanup(srv.Close)

	fetcher := newFixtureFetcher(t, Options{
		UserAgent:  "ComicMarkBot/1.0",
		ContactURL: "https://example.com/bot",
		Guard:      NewGuard(GuardOptions{AllowPrivate: true}),
		Client:     srv.Client(),
	})

	assert.Equal(t, "ComicMarkBot/1.0 (+https://example.com/bot)", fetcher.UserAgent())

	_, err := fetcher.Fetch(context.Background(), srv.URL+"/one-piece-chapter-1050")
	require.NoError(t, err)

	assert.Equal(t, "ComicMarkBot/1.0 (+https://example.com/bot)", gotUA,
		"a site owner must be able to identify and contact the bot")
	assert.Contains(t, gotAccept, "text/html")
}

// --- refusals ---------------------------------------------------------------

// A refused URL is the user's mistake and is reported as one, not as a scrape
// failure they cannot act on.
func TestFetchRefusesUnsafeURLs(t *testing.T) {
	fetcher := newFixtureFetcher(t, Options{
		Guard:  NewGuard(GuardOptions{}), // private addresses refused
		Client: http.DefaultClient,
	})

	for name, raw := range map[string]string{
		"loopback":  "http://127.0.0.1:80/chapter/1",
		"metadata":  "http://169.254.169.254/latest/meta-data/",
		"file":      "file:///etc/passwd",
		"no scheme": "example.com/chapter/1",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := fetcher.Fetch(context.Background(), raw)

			require.Error(t, err)
			assert.True(t, IsRefusal(err), "expected a refusal, got %v", err)
		})
	}
}

// --- partial results --------------------------------------------------------

// A site that refuses the bot is exactly the case the manual fallback exists for:
// the user should still be able to save the bookmark.
func TestFetchReturnsPartialWhenTheSiteRefusesTheBot(t *testing.T) {
	srv := fixtureServer(t, http.StatusForbidden, "text/html", "<html><body>Access denied</body></html>")
	fetcher := newFixtureFetcher(t, Options{
		Guard:  NewGuard(GuardOptions{AllowPrivate: true}),
		Client: srv.Client(),
	})

	result, err := fetcher.Fetch(context.Background(), srv.URL+"/one-piece-chapter-1050")

	require.NoError(t, err, "a refused fetch is not an error the user can act on")
	require.NotNil(t, result)
	assert.True(t, result.Partial)
	assert.Equal(t, http.StatusForbidden, result.StatusCode)
	assert.Contains(t, strings.Join(result.Warnings, " "), "403")
}

func TestFetchReturnsPartialForANonHTMLResponse(t *testing.T) {
	srv := fixtureServer(t, http.StatusOK, "application/pdf", "%PDF-1.7")
	fetcher := newFixtureFetcher(t, Options{
		Guard:  NewGuard(GuardOptions{AllowPrivate: true}),
		Client: srv.Client(),
	})

	result, err := fetcher.Fetch(context.Background(), srv.URL+"/chapter/1050.pdf")

	require.NoError(t, err)
	assert.True(t, result.Partial)
	assert.Contains(t, strings.Join(result.Warnings, " "), "not a web page")
}

func TestFetchReturnsPartialWhenTheHostIsUnreachable(t *testing.T) {
	// A server that is closed immediately gives a connection refusal.
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL + "/one-piece-chapter-1050"
	srv.Close()

	fetcher := newFixtureFetcher(t, Options{
		Guard:   NewGuard(GuardOptions{AllowPrivate: true}),
		Timeout: time.Second,
		Client:  &http.Client{Timeout: time.Second},
	})

	result, err := fetcher.Fetch(context.Background(), url)

	require.NoError(t, err)
	assert.True(t, result.Partial)
	assert.NotEmpty(t, result.Warnings)
}

func TestFetchReturnsPartialForAPageWithNoMetadata(t *testing.T) {
	srv := fixtureServer(t, http.StatusOK, "text/html", "<html><head></head><body>hi</body></html>")
	fetcher := newFixtureFetcher(t, Options{
		Guard:  NewGuard(GuardOptions{AllowPrivate: true}),
		Client: srv.Client(),
	})

	result, err := fetcher.Fetch(context.Background(), srv.URL+"/chapter/1050")

	require.NoError(t, err)
	assert.True(t, result.Partial)
	assert.Empty(t, result.Title)
	assert.Empty(t, result.CoverURL)
	// The chapter still comes from the slug, so that much is available.
	require.True(t, result.Chapter.Found())
}

// A page with everything except a chapter number still yields a usable preview,
// but it is marked partial so the client asks for the chapter.
func TestFetchIsPartialWhenNoChapterIsFound(t *testing.T) {
	page := `<html><head>
	  <meta property="og:title" content="One Piece">
	  <meta property="og:image" content="https://cdn.example.com/cover.jpg">
	</head><body></body></html>`
	srv := fixtureServer(t, http.StatusOK, "text/html", page)
	fetcher := newFixtureFetcher(t, Options{
		Guard:  NewGuard(GuardOptions{AllowPrivate: true}),
		Client: srv.Client(),
	})

	result, err := fetcher.Fetch(context.Background(), srv.URL+"/one-piece/prologue")

	require.NoError(t, err)
	assert.True(t, result.Partial)
	assert.False(t, result.Chapter.Found())
	assert.Contains(t, strings.Join(result.Warnings, " "), "no chapter number")
}

// --- limits -----------------------------------------------------------------

func TestFetchTruncatesAnOversizedPage(t *testing.T) {
	// Metadata in the head, then a body far larger than the cap.
	huge := "<html><head>" +
		`<meta property="og:title" content="One Piece">` +
		`<meta property="og:image" content="https://cdn.example.com/cover.jpg">` +
		"</head><body>" + strings.Repeat("<p>filler</p>", 5000) + "</body></html>"

	srv := fixtureServer(t, http.StatusOK, "text/html", huge)
	fetcher := newFixtureFetcher(t, Options{
		MaxBytes: 1024,
		Guard:    NewGuard(GuardOptions{AllowPrivate: true}),
		Client:   srv.Client(),
	})

	result, err := fetcher.Fetch(context.Background(), srv.URL+"/one-piece-chapter-1050")

	require.NoError(t, err)
	assert.Equal(t, "One Piece", result.Title, "the head is inside the cap, so it is still read")
	assert.Contains(t, strings.Join(result.Warnings, " "), "larger than")
}

func TestFetchHonoursACallerCancellation(t *testing.T) {
	released := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-released:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(func() {
		close(released)
		srv.Close()
	})

	fetcher := newFixtureFetcher(t, Options{
		Timeout: 10 * time.Second,
		Guard:   NewGuard(GuardOptions{AllowPrivate: true}),
		Client:  srv.Client(),
	})

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	_, err := fetcher.Fetch(ctx, srv.URL+"/one-piece-chapter-1050")

	// A caller hanging up is not the site failing, so it propagates rather than
	// becoming a partial preview the client would show as a scrape failure.
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
}

func TestFetchHonoursItsOwnTimeout(t *testing.T) {
	released := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		// Unblock when the client gives up, so the test server can shut down.
		select {
		case <-released:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(func() {
		close(released)
		srv.Close()
	})

	fetcher := newFixtureFetcher(t, Options{
		Timeout: 100 * time.Millisecond,
		Guard:   NewGuard(GuardOptions{AllowPrivate: true}),
		// The injected client has no timeout of its own, which is the point:
		// the limit is the fetcher's to enforce.
		Client: srv.Client(),
	})

	start := time.Now()
	result, err := fetcher.Fetch(context.Background(), srv.URL+"/one-piece-chapter-1050")

	require.NoError(t, err, "a slow site is a partial result, not a caller error")
	assert.True(t, result.Partial)
	assert.Less(t, time.Since(start), 5*time.Second, "the timeout should have applied")
}

// --- redirects --------------------------------------------------------------

func TestFetchFollowsARedirectAndUsesTheFinalURl(t *testing.T) {
	page := `<html><head>
	  <meta property="og:title" content="One Piece">
	  <meta property="og:image" content="cover.jpg">
	</head><body></body></html>`

	mux := http.NewServeMux()
	mux.HandleFunc("/old-link", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/one-piece/chapter-1050", http.StatusFound)
	})
	mux.HandleFunc("/one-piece/chapter-1050", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = io.WriteString(w, page)
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	fetcher := newFixtureFetcher(t, Options{
		Guard:  NewGuard(GuardOptions{AllowPrivate: true}),
		Client: srv.Client(),
	})

	result, err := fetcher.Fetch(context.Background(), srv.URL+"/old-link")
	require.NoError(t, err)

	assert.Equal(t, srv.URL+"/one-piece/chapter-1050", result.FinalURL)

	// The chapter is read from where the page ended up, not from the link the
	// user pasted, and the relative cover resolves against the same place.
	require.True(t, result.Chapter.Found())
	assert.Equal(t, float64(1050), *result.Chapter.Number)
	assert.Equal(t, srv.URL+"/one-piece/cover.jpg", result.CoverURL)
}

// --- construction -----------------------------------------------------------

func TestNewAppliesDefaults(t *testing.T) {
	fetcher, err := New(Options{})
	require.NoError(t, err)

	assert.Equal(t, defaultUserAgent, fetcher.UserAgent())
	assert.Equal(t, int64(defaultMaxBytes), fetcher.maxBytes)
	require.NotNil(t, fetcher.client)
	require.NotNil(t, fetcher.guard)
	assert.False(t, fetcher.guard.AllowPrivate(), "private addresses are refused by default")
}

func TestNewOmitsTheContactURLWhenUnset(t *testing.T) {
	fetcher, err := New(Options{UserAgent: "ComicMarkBot/1.0"})
	require.NoError(t, err)

	assert.Equal(t, "ComicMarkBot/1.0", fetcher.UserAgent())
}

func TestIsHTML(t *testing.T) {
	for contentType, want := range map[string]bool{
		"text/html":                true,
		"text/html; charset=utf-8": true,
		"application/xhtml+xml":    true,
		"":                         true, // missing header: attempt a parse
		"application/pdf":          false,
		"image/jpeg":               false,
		"application/json":         false,
	} {
		assert.Equal(t, want, isHTML(contentType), "for %q", contentType)
	}
}

func TestIsRefusal(t *testing.T) {
	assert.True(t, IsRefusal(fmt.Errorf("wrapped: %w", ErrURLNotAllowed)))
	assert.True(t, IsRefusal(ErrResolutionFailed))
	assert.False(t, IsRefusal(errors.New("something else")))
	assert.False(t, IsRefusal(nil))
}
