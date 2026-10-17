package scraper

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"
)

// Result is the outcome of fetching a page.
//
// Partial is the contract with the client: when it is true the preview is
// incomplete and every field must be editable, because the user is about to
// supply what the site did not publish. Fetch treats a site being unreachable as
// a partial result rather than an error, so a bookmark can still be saved.
type Result struct {
	// FinalURL is the address actually read, after redirects. It is what
	// relative cover images were resolved against.
	FinalURL string
	// Title is the comic or page title.
	Title string
	// CoverURL is the cover image, absolute.
	CoverURL string
	// SiteName is the publisher, when declared.
	SiteName string
	// Chapter is the extracted chapter reference.
	Chapter Chapter

	// Partial reports whether the preview is missing anything.
	Partial bool
	// Warnings explains what was missing, in the user's terms.
	Warnings []string
	// StatusCode is the response status, useful for logs.
	StatusCode int
}

// Options configures a Fetcher.
type Options struct {
	// UserAgent identifies the bot. Sites deserve an honest caller.
	UserAgent string
	// ContactURL is where a site owner can complain.
	ContactURL string
	// Timeout bounds the whole request.
	Timeout time.Duration
	// MaxBytes caps how much of a response is read.
	MaxBytes int64

	// Guard classifies destinations. Defaults to a guard that refuses private
	// addresses.
	Guard *Guard
	// Client overrides the HTTP client. Tests inject one whose transport has no
	// guard, so the parse path can be exercised against a local fixture.
	Client *http.Client

	Logger *slog.Logger
}

// Defaults, used when an option is left zero.
const (
	defaultTimeout   = 10 * time.Second
	defaultMaxBytes  = 2 << 20 // 2 MiB
	defaultUserAgent = "ComicMarkBot/1.0"
)

// Fetcher retrieves pages and extracts their metadata.
type Fetcher struct {
	guard     *Guard
	client    *http.Client
	userAgent string
	timeout   time.Duration
	maxBytes  int64
	logger    *slog.Logger
}

// New builds a Fetcher.
func New(opts Options) (*Fetcher, error) {
	guard := opts.Guard
	if guard == nil {
		guard = NewGuard(GuardOptions{})
	}

	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}

	maxBytes := opts.MaxBytes
	if maxBytes <= 0 {
		maxBytes = defaultMaxBytes
	}

	userAgent := strings.TrimSpace(opts.UserAgent)
	if userAgent == "" {
		userAgent = defaultUserAgent
	}
	if contact := strings.TrimSpace(opts.ContactURL); contact != "" {
		userAgent = fmt.Sprintf("%s (+%s)", userAgent, contact)
	}

	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}

	client := opts.Client
	if client == nil {
		client = newGuardedClient(guard, timeout)
	}

	return &Fetcher{
		guard:     guard,
		client:    client,
		userAgent: userAgent,
		timeout:   timeout,
		maxBytes:  maxBytes,
		logger:    logger,
	}, nil
}

// UserAgent returns the header the fetcher sends, so it can be logged and tested.
func (f *Fetcher) UserAgent() string { return f.userAgent }

// newGuardedClient builds the client used in production.
//
// The guard is installed in three places, and each covers a different gap: the
// dialer's Control hook sees the address actually connected to, CheckRedirect
// re-validates every hop, and Fetch validates the URL before anything is sent.
func newGuardedClient(guard *Guard, timeout time.Duration) *http.Client {
	dialer := &net.Dialer{
		Timeout:   5 * time.Second,
		KeepAlive: 30 * time.Second,
		Control:   guard.Control,
	}

	transport := &http.Transport{
		DialContext:           dialer.DialContext,
		MaxIdleConns:          10,
		MaxIdleConnsPerHost:   2,
		IdleConnTimeout:       30 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: timeout,
		ExpectContinueTimeout: time.Second,
		ForceAttemptHTTP2:     true,
	}

	return &http.Client{
		Transport:     transport,
		Timeout:       timeout,
		CheckRedirect: guard.CheckRedirect,
		// No cookie jar: this server has no business carrying a session for a
		// site it visits on a user's behalf.
	}
}

// Fetch retrieves a URL and extracts the metadata a preview needs.
//
// A refused URL returns ErrURLNotAllowed or ErrResolutionFailed, which the API
// reports as a bad request. Anything that goes wrong after that returns a partial
// Result and no error, because the user can still save the bookmark by typing
// what the site did not provide.
func (f *Fetcher) Fetch(ctx context.Context, rawURL string) (*Result, error) {
	validated, err := f.guard.ValidateURL(ctx, rawURL)
	if err != nil {
		return nil, err
	}

	// The timeout is applied to the request context rather than relying on the
	// client's own Timeout field, so an injected client still honours it and the
	// limit is a property of this package.
	caller := ctx
	ctx, cancel := context.WithTimeout(ctx, f.timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, validated.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrURLNotAllowed, err)
	}
	req.Header.Set("User-Agent", f.userAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en")

	resp, err := f.client.Do(req)
	if err != nil {
		// The caller hanging up is not the site's fault, so it propagates rather
		// than becoming a partial result the client would show as a scrape
		// failure. Our own timeout, on the other hand, is a slow site.
		if callerErr := caller.Err(); callerErr != nil {
			return nil, callerErr
		}
		return f.partial(validated.String(), "we could not reach that page"), nil
	}
	defer func() {
		// Drain a little so the connection can be reused, then close.
		_, _ = io.CopyN(io.Discard, resp.Body, 4<<10)
		_ = resp.Body.Close()
	}()

	finalURL := resp.Request.URL
	result := &Result{
		FinalURL:   finalURL.String(),
		StatusCode: resp.StatusCode,
	}

	if resp.StatusCode >= http.StatusBadRequest {
		result.Warnings = append(result.Warnings,
			fmt.Sprintf("the site replied with %d", resp.StatusCode))
		result.Partial = true
		// A site that refuses the bot is exactly the case the manual fallback
		// exists for.
		return f.finish(result), nil
	}

	if !isHTML(resp.Header.Get("Content-Type")) {
		result.Warnings = append(result.Warnings, "that link is not a web page")
		result.Partial = true
		return f.finish(result), nil
	}

	body, truncated, err := f.readBody(resp.Body)
	if err != nil {
		result.Warnings = append(result.Warnings, "the page could not be read completely")
		result.Partial = true
		return f.finish(result), nil
	}
	if truncated {
		result.Warnings = append(result.Warnings,
			fmt.Sprintf("the page was larger than %d KB, so only its start was read", f.maxBytes>>10))
	}

	og, err := ParseOpenGraph(bytes.NewReader(body), finalURL)
	if err != nil {
		result.Warnings = append(result.Warnings, "the page could not be understood")
		result.Partial = true
		return f.finish(result), nil
	}

	result.Title = og.Title
	result.CoverURL = og.Image
	result.SiteName = og.SiteName
	result.Warnings = append(result.Warnings, og.Warnings...)

	// The URL is consulted before the titles, so a slug wins over a decorated
	// page title.
	result.Chapter = ExtractChapter(finalURL.Path, og.Title, og.Metadata["og:title"], docTitle(og))
	if !result.Chapter.Found() {
		result.Warnings = append(result.Warnings, "no chapter number was found in that link")
	}

	return f.finish(result), nil
}

// partial builds a result for a failure that does not stop the user.
func (f *Fetcher) partial(url, warning string) *Result {
	return &Result{
		FinalURL: url,
		Partial:  true,
		Warnings: []string{warning},
	}
}

// finish sets Partial from what was actually found and logs the outcome once.
func (f *Fetcher) finish(result *Result) *Result {
	if result.Title == "" || result.CoverURL == "" || !result.Chapter.Found() {
		result.Partial = true
	}

	f.logger.Debug("scraped page",
		"url", result.FinalURL,
		"status", result.StatusCode,
		"partial", result.Partial,
		"title", result.Title != "",
		"cover", result.CoverURL != "",
		"chapter", result.Chapter.Humanise(),
		"warnings", result.Warnings,
	)

	return result
}

// readBody reads at most maxBytes, reporting whether the page was longer.
func (f *Fetcher) readBody(body io.Reader) ([]byte, bool, error) {
	// One byte over the limit is enough to know it was exceeded.
	limited := io.LimitReader(body, f.maxBytes+1)

	data, err := io.ReadAll(limited)
	if err != nil {
		return nil, false, err
	}

	if int64(len(data)) > f.maxBytes {
		return data[:f.maxBytes], true, nil
	}
	return data, false, nil
}

// isHTML reports whether a response is worth parsing.
func isHTML(contentType string) bool {
	if contentType == "" {
		// Some sites omit it; attempting a parse is harmless.
		return true
	}
	mediaType := strings.ToLower(strings.TrimSpace(strings.SplitN(contentType, ";", 2)[0]))
	switch mediaType {
	case "text/html", "application/xhtml+xml", "application/xml", "text/xml":
		return true
	default:
		return false
	}
}

// docTitle returns the OpenGraph title when one was found, so the chapter
// extractor sees each candidate exactly once.
func docTitle(og *OpenGraph) string {
	if og == nil {
		return ""
	}
	return og.Title
}

// IsRefusal reports whether an error means the URL was refused rather than that
// the fetch failed. The API uses this to answer with a bad request instead of a
// partial preview.
func IsRefusal(err error) bool {
	return errors.Is(err, ErrURLNotAllowed) || errors.Is(err, ErrResolutionFailed)
}
