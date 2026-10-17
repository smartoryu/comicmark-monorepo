package bookmark

import (
	"database/sql"
	"errors"
	"log/slog"
	"net/http"

	"comicmark/go-modular/internal/config"
)

// Deps is what the bookmark module needs from the application.
type Deps struct {
	Config *config.Config
	Logger *slog.Logger
	DB     *sql.DB

	// Fetcher retrieves and parses pages.
	Fetcher Fetcher
	// RequireAuth is the auth module's middleware. Bookmark routes are all
	// scoped to a signed-in reader, so every one of them needs it.
	RequireAuth func(http.Handler) http.Handler
}

// Module is the wired bookmark feature.
type Module struct {
	service *Service
	handler *handler
	deps    Deps
}

// New builds the bookmark module.
func New(deps Deps) (*Module, error) {
	switch {
	case deps.Config == nil:
		return nil, errors.New("bookmark: config is required")
	case deps.DB == nil:
		return nil, errors.New("bookmark: database is required")
	case deps.Fetcher == nil:
		return nil, errors.New("bookmark: fetcher is required")
	case deps.RequireAuth == nil:
		return nil, errors.New("bookmark: require-auth middleware is required")
	}

	logger := deps.Logger
	if logger == nil {
		logger = slog.Default()
	}

	service, err := NewService(Options{
		Repository: NewRepository(deps.DB),
		Fetcher:    deps.Fetcher,
		Logger:     logger,
	})
	if err != nil {
		return nil, err
	}

	return &Module{
		service: service,
		handler: newHandler(service),
		deps:    deps,
	}, nil
}

// Service exposes the use cases.
func (m *Module) Service() *Service { return m.service }

// RegisterRoutes mounts the bookmark endpoints on the API's v1 mux.
//
// Every route is behind RequireAuth: a bookmark belongs to a reader, and the
// parse endpoint in particular should never be an open fetch proxy.
func (m *Module) RegisterRoutes(api *http.ServeMux) {
	h := m.handler

	protected := func(pattern string, handler http.HandlerFunc) {
		api.Handle(pattern, m.deps.RequireAuth(handler))
	}

	// Order matters only for readability; net/http's mux picks the most
	// specific pattern, so /{id}/read-next is not shadowed by /{id}.
	protected("POST /api/v1/bookmarks/parse", h.Parse)
	protected("POST /api/v1/bookmarks", h.Create)
	protected("GET /api/v1/bookmarks", h.List)
	protected("GET /api/v1/bookmarks/{id}", h.Get)
	protected("PATCH /api/v1/bookmarks/{id}", h.Update)
	protected("PATCH /api/v1/bookmarks/{id}/read-next", h.ReadNext)
	protected("POST /api/v1/bookmarks/{id}/read", h.MarkRead)
	protected("DELETE /api/v1/bookmarks/{id}", h.Delete)
}
