package main

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"comicmark/go-modular/internal/config"
	"comicmark/go-modular/internal/pkg/database"
	"comicmark/go-modular/internal/pkg/jwt"
	"comicmark/go-modular/internal/pkg/mailer"
	"comicmark/go-modular/internal/pkg/password"
	"comicmark/go-modular/internal/pkg/scraper"
)

// testSecret is long enough to satisfy jwt.MinSecretLength.
const testSecret = "test-secret-that-is-at-least-32-bytes-long"

// discardLogger keeps logs out of the test output.
func discardLogger() *slog.Logger { return slog.New(slog.DiscardHandler) }

// newRequest builds a request with a real RemoteAddr so rate limiting has
// something to key on.
func newRequest(method, target string, body io.Reader) *http.Request {
	r := httptest.NewRequest(method, target, body)
	r.RemoteAddr = "203.0.113.7:54321"
	return r
}

// testConfig returns a development config with a temporary database and rate
// limits high enough that tests are not throttled.
func testConfig(t *testing.T) *config.Config {
	t.Helper()

	cfg := config.Defaults()
	cfg.App.JWTSecret = testSecret
	cfg.App.DatabasePath = filepath.Join(t.TempDir(), "comicmark.db")
	cfg.App.BaseURL = "http://localhost:5173"
	cfg.App.CORSOrigins = []string{"http://localhost:5173"}

	const effectivelyUnlimited = 100_000
	cfg.App.RateLimitRPS = effectivelyUnlimited
	cfg.App.RateLimitBurst = effectivelyUnlimited
	cfg.App.AuthRateLimitPerMin = effectivelyUnlimited
	cfg.App.SensitiveRateLimitPerMin = effectivelyUnlimited

	return cfg
}

func testJWTManager(t *testing.T) *jwt.Manager {
	t.Helper()

	m, err := jwt.NewManager(
		[]byte(testSecret),
		"comicmark",
		15*time.Minute,
		30*24*time.Hour,
	)
	require.NoError(t, err)
	return m
}

// testHasher uses far cheaper parameters than production: hundreds of argon2id
// hashes run in the suite, and the production cost is covered by the password
// package's own tests.
func testHasher() *password.Hasher {
	return password.NewHasherWithParams(password.Params{
		Memory:      8,
		Iterations:  1,
		Parallelism: 1,
		SaltLength:  8,
		KeyLength:   16,
	})
}

// recordingMailer captures messages so tests can read verification and reset
// links. It also satisfies AsyncMailer, so tests can hand it straight to the
// server: delivery stays synchronous and assertions cannot race a queue.
type recordingMailer struct {
	mu       sync.Mutex
	messages []mailer.Message
}

// Shutdown satisfies mailer.AsyncMailer; nothing is buffered.
func (r *recordingMailer) Shutdown(context.Context) error { return nil }

func (r *recordingMailer) Send(_ context.Context, msg mailer.Message) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.messages = append(r.messages, msg)
	return nil
}

// all returns a copy of every captured message.
func (r *recordingMailer) all() []mailer.Message {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]mailer.Message(nil), r.messages...)
}

// lastLink returns the link of the most recent message.
func (r *recordingMailer) lastLink(t *testing.T) string {
	t.Helper()

	msgs := r.all()
	require.NotEmpty(t, msgs, "expected at least one email to have been sent")
	return msgs[len(msgs)-1].Link
}

// newAPIServerWithMailer builds a server whose outgoing email is captured, so a
// test can follow the links that would have been sent.
func newAPIServerWithMailer(t *testing.T) (*apiServer, *recordingMailer) {
	t.Helper()

	rec := &recordingMailer{}
	srv := newTestServer(t, rec)
	return srv, rec
}

// newTestServer builds a fully wired server over a migrated temporary database.
func newTestServer(t *testing.T, m mailer.AsyncMailer) *apiServer {
	t.Helper()
	return newTestServerWithConfig(t, nil, m)
}

// newTestServerWithConfig builds a server whose config a test can adjust before
// it is wired, for behaviours that only exist in a particular configuration.
func newTestServerWithConfig(t *testing.T, adjust func(*config.Config), m mailer.AsyncMailer) *apiServer {
	t.Helper()

	cfg := testConfig(t)
	if adjust != nil {
		adjust(cfg)
	}

	db := newTestDB(t)
	srv, err := newAPIServer(cfg, discardLogger(), db, testJWTManager(t), testHasher(), m, nil, testFetcher(t))
	require.NoError(t, err)
	t.Cleanup(srv.close)
	return srv
}

// newTestDB opens a temporary SQLite database with the schema applied.
func newTestDB(t *testing.T) *sql.DB {
	t.Helper()

	db, err := database.Open(context.Background(), filepath.Join(t.TempDir(), "comicmark.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })

	_, err = database.NewMigrator(db).Up(context.Background())
	require.NoError(t, err)
	return db
}

// stubFetcher stands in for the scraper in tests that are not about scraping.
// It reports a page it never fetched, so the bookmark endpoints can be exercised
// without a network.
type stubFetcher struct {
	result  *scraper.Result
	err     error
	lastURL string
}

func (f *stubFetcher) Fetch(_ context.Context, rawURL string) (*scraper.Result, error) {
	f.lastURL = rawURL
	if f.err != nil {
		return nil, f.err
	}
	if f.result != nil {
		return f.result, nil
	}
	return &scraper.Result{
		FinalURL: rawURL,
		Title:    "Stubbed Comic",
		CoverURL: "https://cdn.example.com/cover.jpg",
		Chapter: scraper.Chapter{
			Text:   "Chapter 1050",
			Number: floatPointer(1050),
			Source: scraper.SourceSlug,
		},
	}, nil
}

func floatPointer(value float64) *float64 { return &value }

// testFetcher returns a fetcher that answers without touching the network.
func testFetcher(t *testing.T) *stubFetcher {
	t.Helper()
	return &stubFetcher{}
}

// testMailer returns a console mailer whose output is discarded.
func testMailer(t *testing.T) mailer.AsyncMailer {
	t.Helper()

	driver, err := mailer.New(mailer.Options{
		Driver: mailer.DriverConsole,
		From:   "ComicMark <no-reply@comicmark.local>",
		Logger: slog.New(slog.DiscardHandler),
	})
	require.NoError(t, err)
	return mailer.Async(driver, slog.New(slog.DiscardHandler), 8)
}
