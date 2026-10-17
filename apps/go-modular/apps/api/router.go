package main

import (
	"database/sql"
	"log/slog"
	"net/http"

	"comicmark/go-modular/internal/config"
	"comicmark/go-modular/internal/httpx"
	"comicmark/go-modular/internal/modules/auth"
	"comicmark/go-modular/internal/modules/bookmark"
	"comicmark/go-modular/internal/pkg/jwt"
	"comicmark/go-modular/internal/pkg/mailer"
	"comicmark/go-modular/internal/pkg/password"
)

// apiServer is the composition root of the HTTP layer. Feature modules receive
// the pieces they need from here rather than reaching for globals.
type apiServer struct {
	cfg    *config.Config
	logger *slog.Logger
	db     *sql.DB

	jwt       *jwt.Manager
	passwords *password.Hasher
	mailer    mailer.AsyncMailer

	// auth and bookmarks are the mounted feature modules. A nil module is
	// simply not mounted.
	auth      *auth.Module
	bookmarks *bookmark.Module

	// globalLimiter is the coarse per-client limit applied to every request.
	globalLimiter *httpx.RateLimiter
	// authLimiter guards sign-in and registration.
	authLimiter *httpx.RateLimiter
	// sensitiveLimiter guards endpoints that send email, where each request
	// costs a message and a token.
	sensitiveLimiter *httpx.RateLimiter

	static http.Handler
}

// newAPIServer wires the HTTP layer.
func newAPIServer(
	cfg *config.Config,
	logger *slog.Logger,
	db *sql.DB,
	jwtManager *jwt.Manager,
	passwords *password.Hasher,
	mailerDriver mailer.AsyncMailer,
	firebaseVerifier auth.FirebaseVerifier,
	fetcher bookmark.Fetcher,
) (*apiServer, error) {
	static, err := newStaticHandler()
	if err != nil {
		// A missing web build is a degraded but recoverable state: the JSON API
		// is still useful, so log and serve 404s for the shell rather than
		// refusing to start.
		logger.Warn("web build not embedded; the SPA will not be served",
			"error", err, "hint", "run `moon run comic-mobile:web-embed`")
		static = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "The web app is not embedded in this build. Run `moon run comic-mobile:web-embed` and restart.",
				http.StatusNotFound)
		})
	}

	server := &apiServer{
		cfg:              cfg,
		logger:           logger,
		db:               db,
		jwt:              jwtManager,
		passwords:        passwords,
		mailer:           mailerDriver,
		globalLimiter:    httpx.NewRateLimiter(float64(cfg.App.RateLimitRPS), cfg.App.RateLimitBurst),
		authLimiter:      httpx.NewPerMinuteLimiter(cfg.App.AuthRateLimitPerMin, cfg.App.AuthRateLimitPerMin),
		sensitiveLimiter: httpx.NewPerMinuteLimiter(cfg.App.SensitiveRateLimitPerMin, cfg.App.SensitiveRateLimitPerMin),
		static:           static,
	}

	authModule, err := auth.New(auth.Deps{
		Config:           cfg,
		Logger:           logger,
		DB:               db,
		JWT:              jwtManager,
		Passwords:        passwords,
		Mailer:           mailerDriver,
		AuthLimiter:      server.authLimiter.Middleware(),
		SensitiveLimiter: server.sensitiveLimiter.Middleware(),
		Firebase:         firebaseVerifier,
	})
	if err != nil {
		return nil, err
	}
	server.auth = authModule

	bookmarks, err := bookmark.New(bookmark.Deps{
		Config:      cfg,
		Logger:      logger,
		DB:          db,
		Fetcher:     fetcher,
		RequireAuth: authModule.RequireAuth,
	})
	if err != nil {
		return nil, err
	}
	server.bookmarks = bookmarks

	return server, nil
}

// close releases the background workers the server owns.
func (s *apiServer) close() {
	s.globalLimiter.Stop()
	s.authLimiter.Stop()
	s.sensitiveLimiter.Stop()
}

// registerCoreRoutes mounts endpoints that belong to the server itself rather
// than to a feature module.
func (s *apiServer) registerCoreRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /healthz", s.handleHealth)
}

// registerModuleRoutes mounts each feature module on the mux.
//
// Feature modules live in internal/modules and export a Register function that
// takes the dependencies it needs. Keeping the wiring here means the whole route
// table can be read in one place.
func (s *apiServer) registerModuleRoutes(mux *http.ServeMux) {
	if s.auth != nil {
		s.auth.RegisterRoutes(mux)
	}
	if s.bookmarks != nil {
		s.bookmarks.RegisterRoutes(mux)
	}
}

// handler assembles the middleware chain and the route table.
//
// Order matters and reads outside-in:
//   - RequestID first, so every later log line and error body can quote an id
//   - AccessLog next, so a request that panics still produces a log line
//   - Recover inside the log, so the recorded status is the 500 we return
//   - CORS before the limiter, so a rejected request is still readable by the browser
//   - the limiter outermost of the handlers, so it protects the route table
func (s *apiServer) handler() http.Handler {
	mux := http.NewServeMux()

	s.registerCoreRoutes(mux)
	s.registerModuleRoutes(mux)

	// The catch-all must be registered last: a more specific pattern always
	// wins in net/http's ServeMux, but registering "/" makes that explicit.
	mux.Handle("/", s.static)

	return httpx.Chain(mux,
		httpx.RequestID(),
		httpx.AccessLog(s.logger),
		httpx.Recover(s.logger),
		httpx.CORS(s.cfg.App.CORSOrigins),
		s.globalLimiter.Middleware(),
	)
}
