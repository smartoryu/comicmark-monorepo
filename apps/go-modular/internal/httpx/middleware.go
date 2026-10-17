package httpx

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"runtime/debug"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"golang.org/x/time/rate"
)

// Middleware wraps a handler.
type Middleware func(http.Handler) http.Handler

// Chain applies middleware so that the first argument is the outermost layer,
// which is the order the layers are written in.
func Chain(h http.Handler, middlewares ...Middleware) http.Handler {
	for i := len(middlewares) - 1; i >= 0; i-- {
		h = middlewares[i](h)
	}
	return h
}

// --- request id -------------------------------------------------------------

// HeaderRequestID is the header carrying the correlation id.
const HeaderRequestID = "X-Request-ID"

// HeaderClientPlatform lets a client state which build it is (web, android, ios)
// instead of relying on User-Agent sniffing.
const HeaderClientPlatform = "X-Client-Platform"

type contextKey string

const requestIDKey contextKey = "request_id"

// RequestIDFromContext returns the request id, or "" outside a request.
func RequestIDFromContext(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey).(string)
	return id
}

// maxRequestIDLength bounds an echoed client-supplied id, which would otherwise
// appear in every log line for the request.
const maxRequestIDLength = 128

// RequestID echoes an incoming X-Request-ID or generates one, then puts it in
// the response header and the request context so log lines can be correlated
// with a report from a user.
func RequestID() Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id := strings.TrimSpace(r.Header.Get(HeaderRequestID))
			if id == "" || len(id) > maxRequestIDLength {
				id = uuid.Must(uuid.NewV7()).String()
			}

			w.Header().Set(HeaderRequestID, id)
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), requestIDKey, id)))
		})
	}
}

// --- panic recovery ---------------------------------------------------------

// Recover turns a panic into a 500 with the standard error envelope, logging the
// stack. Without it a single nil dereference would take the process down.
func Recover(logger *slog.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				rec := recover()
				if rec == nil {
					return
				}

				// A client disconnecting mid-write surfaces as this sentinel; it
				// is not a server fault and the response is already gone.
				if err, ok := rec.(error); ok && errors.Is(err, http.ErrAbortHandler) {
					return
				}

				logger.ErrorContext(r.Context(), "recovered from panic",
					"panic", rec,
					"method", r.Method,
					"path", r.URL.Path,
					"stack", string(debug.Stack()),
				)
				WriteError(w, r, http.StatusInternalServerError, CodeInternal, MsgInternal)
			}()

			next.ServeHTTP(w, r)
		})
	}
}

// --- access log -------------------------------------------------------------

// responseRecorder captures the status and size for the access log.
type responseRecorder struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (r *responseRecorder) WriteHeader(status int) {
	if r.status == 0 {
		r.status = status
	}
	r.ResponseWriter.WriteHeader(status)
}

func (r *responseRecorder) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	n, err := r.ResponseWriter.Write(b)
	r.bytes += n
	return n, err
}

// Unwrap lets http.ResponseController reach the underlying writer.
func (r *responseRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

// AccessLog records one line per request.
func AccessLog(logger *slog.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rec := &responseRecorder{ResponseWriter: w}

			next.ServeHTTP(rec, r)

			if rec.status == 0 {
				// No handler wrote anything: net/http sends 200 with an empty body.
				rec.status = http.StatusOK
			}

			logger.InfoContext(r.Context(), "request",
				"method", r.Method,
				"path", r.URL.Path,
				"status", rec.status,
				"bytes", rec.bytes,
				"duration_ms", time.Since(start).Milliseconds(),
				"ip", ClientIP(r),
			)
		})
	}
}

// --- CORS -------------------------------------------------------------------

// corsAllowedMethods is the fixed set of methods the API exposes.
var corsAllowedMethods = []string{
	http.MethodGet, http.MethodPost, http.MethodPut,
	http.MethodPatch, http.MethodDelete, http.MethodOptions,
}

// corsAllowedHeaders lists the request headers a browser may send. Anything not
// listed here fails preflight.
var corsAllowedHeaders = []string{
	"Authorization",
	"Content-Type",
	HeaderClientPlatform,
	HeaderRequestID,
}

// corsMaxAge caches a successful preflight for ten minutes.
const corsMaxAge = 10 * time.Minute

// CORS allows the configured browser origins, which is what makes
// `flutter run -d chrome` on a different port work during development.
//
// Requests from other origins are still served: CORS is enforced by the browser,
// and this API authenticates with a bearer token rather than cookies, so there is
// no cross-site request forgery to prevent here. The response simply omits the
// allow headers, so a browser refuses to hand the body to the page.
func CORS(origins []string) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			allowed := origin != "" && slices.Contains(origins, origin)

			// Caches must key on Origin, or one origin's response would be
			// replayed to another.
			w.Header().Add("Vary", "Origin")

			if allowed {
				w.Header().Set("Access-Control-Allow-Origin", origin)
				w.Header().Set("Access-Control-Allow-Credentials", "true")
			}

			if r.Method != http.MethodOptions {
				next.ServeHTTP(w, r)
				return
			}

			// Preflight.
			w.Header().Add("Vary", "Access-Control-Request-Method")
			w.Header().Add("Vary", "Access-Control-Request-Headers")

			if !allowed {
				WriteError(w, r, http.StatusForbidden, CodeForbidden, MsgForbidden)
				return
			}

			w.Header().Set("Access-Control-Allow-Methods", strings.Join(corsAllowedMethods, ", "))
			w.Header().Set("Access-Control-Allow-Headers", strings.Join(corsAllowedHeaders, ", "))
			w.Header().Set("Access-Control-Max-Age", strconv.Itoa(int(corsMaxAge.Seconds())))
			w.WriteHeader(http.StatusNoContent)
		})
	}
}

// --- client address ---------------------------------------------------------

// ClientIP returns the address a rate limit is keyed on and that is recorded on
// a new session.
//
// Only the connection's own address is used. X-Forwarded-For is attacker
// controlled unless a trusted proxy is rewriting it, and honouring it blindly
// would let anyone bypass every limit by sending a different fake value. A
// deployment behind a proxy should configure its replacement explicitly.
func ClientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// --- rate limiting ----------------------------------------------------------

// visitor is one client's bucket plus when it was last used.
type visitor struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

// visitorTTL is how long an idle visitor is remembered before its bucket is
// discarded, which is what keeps the map from growing without bound.
const visitorTTL = 10 * time.Minute

// RateLimiter applies a per-client token bucket.
type RateLimiter struct {
	mu       sync.Mutex
	visitors map[string]*visitor
	limit    rate.Limit
	burst    int
	stop     chan struct{}
	stopOnce sync.Once
	now      func() time.Time
}

// NewRateLimiter builds a limiter allowing perSecond requests with the given
// burst. A background collector removes idle entries.
func NewRateLimiter(perSecond float64, burst int) *RateLimiter {
	rl := &RateLimiter{
		visitors: make(map[string]*visitor),
		limit:    rate.Limit(perSecond),
		burst:    burst,
		stop:     make(chan struct{}),
		now:      time.Now,
	}
	go rl.collect()
	return rl
}

// NewPerMinuteLimiter is a convenience wrapper for limits expressed per minute,
// used for the sensitive account endpoints.
func NewPerMinuteLimiter(perMinute, burst int) *RateLimiter {
	return NewRateLimiter(float64(perMinute)/60.0, burst)
}

// Allow reports whether key may proceed now.
func (rl *RateLimiter) Allow(key string) bool {
	return rl.limiterFor(key).Allow()
}

func (rl *RateLimiter) limiterFor(key string) *rate.Limiter {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := rl.now()
	if v, ok := rl.visitors[key]; ok {
		v.lastSeen = now
		return v.limiter
	}

	// A new client starts with a full bucket, so a burst of legitimate first
	// requests is not rejected.
	v := &visitor{limiter: rate.NewLimiter(rl.limit, rl.burst), lastSeen: now}
	rl.visitors[key] = v
	return v.limiter
}

func (rl *RateLimiter) collect() {
	ticker := time.NewTicker(visitorTTL)
	defer ticker.Stop()

	for {
		select {
		case <-rl.stop:
			return
		case <-ticker.C:
			rl.collectOnce()
		}
	}
}

// collectOnce performs a single sweep of idle visitors. The background collector
// calls it on a ticker; tests call it directly rather than waiting.
func (rl *RateLimiter) collectOnce() {
	cutoff := rl.now().Add(-visitorTTL)

	rl.mu.Lock()
	defer rl.mu.Unlock()
	for key, v := range rl.visitors {
		if v.lastSeen.Before(cutoff) {
			delete(rl.visitors, key)
		}
	}
}

// Stop releases the collector goroutine.
func (rl *RateLimiter) Stop() {
	rl.stopOnce.Do(func() { close(rl.stop) })
}

// Middleware applies the limiter per client address.
func (rl *RateLimiter) Middleware() Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !rl.Allow(ClientIP(r)) {
				w.Header().Set("Retry-After", "1")
				WriteError(w, r, http.StatusTooManyRequests, CodeRateLimited, MsgRateLimited)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
