package main

import (
	"context"
	"net/http"
	"time"

	"comicmark/go-modular/internal/httpx"
)

// healthPingTimeout bounds the database probe so a wedged database cannot make
// the health check itself hang.
const healthPingTimeout = 2 * time.Second

// Health status values.
const (
	healthOK       = "ok"
	healthDegraded = "degraded"
)

// healthResponse is the body of GET /healthz.
type healthResponse struct {
	Status string `json:"status"`
	DB     string `json:"db"`
}

// handleHealth reports whether the service can serve traffic.
//
// It returns 503 when the database is unreachable, so a load balancer or
// orchestrator stops sending requests here. The underlying error is logged but
// never returned: it can contain filesystem paths.
func (s *apiServer) handleHealth(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), healthPingTimeout)
	defer cancel()

	if err := s.db.PingContext(ctx); err != nil {
		s.logger.ErrorContext(r.Context(), "health check failed", "error", err)
		httpx.WriteJSON(w, r, http.StatusServiceUnavailable, healthResponse{
			Status: healthDegraded,
			DB:     "unreachable",
		})
		return
	}

	httpx.WriteJSON(w, r, http.StatusOK, healthResponse{Status: healthOK, DB: healthOK})
}
