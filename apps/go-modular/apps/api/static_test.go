package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeWebBuild models a Flutter web build output.
func fakeWebBuild() fstest.MapFS {
	return fstest.MapFS{
		"index.html":                {Data: []byte("<!doctype html><title>ComicMark</title>")},
		"flutter_bootstrap.js":      {Data: []byte("// bootstrap")},
		"flutter_service_worker.js": {Data: []byte("// service worker")},
		"main.dart.js":              {Data: []byte("// compiled app")},
		"manifest.json":             {Data: []byte("{}")},
		"assets/AssetManifest.json": {Data: []byte("{}")},
		"canvaskit/canvaskit.wasm":  {Data: []byte("\x00asm")},
	}
}

func newTestStaticHandler(t *testing.T, fsys fstest.MapFS) http.Handler {
	t.Helper()
	h, err := newStaticHandlerFromFS(fsys)
	require.NoError(t, err)
	return h
}

func get(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
	return w
}

func TestStaticServesEmbeddedFiles(t *testing.T) {
	h := newTestStaticHandler(t, fakeWebBuild())

	w := get(t, h, "/main.dart.js")
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "compiled app")
}

func TestStaticServesIndexForClientSideRoutes(t *testing.T) {
	h := newTestStaticHandler(t, fakeWebBuild())

	for _, route := range []string{"/", "/library", "/reset-password", "/settings/sessions"} {
		w := get(t, h, route)
		assert.Equal(t, http.StatusOK, w.Code, "route %s should serve the shell", route)
		assert.Contains(t, w.Body.String(), "ComicMark",
			"a hard refresh on %s must load the app rather than 404", route)
	}
}

// Serving HTML for a missing script would make Flutter fail with a confusing
// MIME error instead of an obvious 404.
func TestStaticReturns404ForMissingAssets(t *testing.T) {
	h := newTestStaticHandler(t, fakeWebBuild())

	for _, path := range []string{"/main.dart.js.map", "/missing.png", "/assets/gone.json"} {
		w := get(t, h, path)
		assert.Equal(t, http.StatusNotFound, w.Code, "%s should be a real 404", path)
		assert.NotContains(t, w.Body.String(), "<!doctype html>")
	}
}

// A mistyped endpoint must not answer with the app shell.
func TestStaticNeverServesAPIOrHealthPaths(t *testing.T) {
	h := newTestStaticHandler(t, fakeWebBuild())

	for _, path := range []string{"/api/v1/bookmarks", "/api/v1/typo", "/api/", "/healthz"} {
		w := get(t, h, path)
		assert.Equal(t, http.StatusNotFound, w.Code, "%s must not fall through to the SPA", path)
		assert.NotContains(t, w.Body.String(), "<!doctype html>")
	}
}

func TestStaticCacheHeaders(t *testing.T) {
	h := newTestStaticHandler(t, fakeWebBuild())

	t.Run("entry points are revalidated", func(t *testing.T) {
		for _, path := range []string{"/", "/index.html", "/flutter_bootstrap.js", "/flutter_service_worker.js"} {
			w := get(t, h, path)
			assert.Contains(t, w.Header().Get("Cache-Control"), "no-cache",
				"%s must not be cached, or a deploy would keep serving the old app", path)
		}
	})

	t.Run("build output is immutable", func(t *testing.T) {
		for _, path := range []string{"/assets/AssetManifest.json", "/canvaskit/canvaskit.wasm"} {
			w := get(t, h, path)
			assert.Contains(t, w.Header().Get("Cache-Control"), "immutable", path)
		}
	})

	t.Run("index is served as HTML", func(t *testing.T) {
		w := get(t, h, "/library")
		assert.Contains(t, w.Header().Get("Content-Type"), "text/html")
	})
}

func TestStaticReportsAMissingWebBuild(t *testing.T) {
	_, err := newStaticHandlerFromFS(fstest.MapFS{
		"placeholder": {Data: []byte("")},
	})
	require.ErrorIs(t, err, ErrNoWebBuild)
	assert.Contains(t, err.Error(), "web-embed")
}
