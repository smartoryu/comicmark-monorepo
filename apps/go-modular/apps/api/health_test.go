package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"comicmark/go-modular/internal/pkg/database"
)

func TestHealthReportsOKWithAReachableDatabase(t *testing.T) {
	db, err := database.Open(context.Background(), filepath.Join(t.TempDir(), "health.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })

	srv := &apiServer{db: db, logger: discardLogger()}

	w := httptest.NewRecorder()
	srv.handleHealth(w, newRequest(http.MethodGet, "/healthz", nil))

	require.Equal(t, http.StatusOK, w.Code)

	var body healthResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	assert.Equal(t, "ok", body.Status)
	assert.Equal(t, "ok", body.DB)
}

func TestHealthReportsUnavailableWhenTheDatabaseIsGone(t *testing.T) {
	db, err := database.Open(context.Background(), filepath.Join(t.TempDir(), "health.db"))
	require.NoError(t, err)
	// Closing the pool makes every ping fail, which is what an unreachable or
	// wedged database looks like from here.
	require.NoError(t, db.Close())

	srv := &apiServer{db: db, logger: discardLogger()}

	w := httptest.NewRecorder()
	srv.handleHealth(w, newRequest(http.MethodGet, "/healthz", nil))

	assert.Equal(t, http.StatusServiceUnavailable, w.Code)

	var body healthResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	assert.Equal(t, "degraded", body.Status)
	assert.Equal(t, "unreachable", body.DB)
}

func TestHealthIsMountedAndUnauthenticated(t *testing.T) {
	srv := newTestServer(t, testMailer(t))

	// No Authorization header: the health check must not require a token.
	w := httptest.NewRecorder()
	srv.handler().ServeHTTP(w, newRequest(http.MethodGet, "/healthz", nil))

	assert.Equal(t, http.StatusOK, w.Code)
	assert.NotEqual(t, "text/html", w.Header().Get("Content-Type"),
		"health must not be answered by the SPA shell")
}
