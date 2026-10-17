package main

import (
	"embed"
	"errors"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

// webStatic holds the built Flutter web app. `all:` includes files whose names
// start with an underscore or dot, which the default pattern skips.
//
// The directory always exists (it is committed with a placeholder), so the
// binary builds before the first `moon run comic-mobile:web-embed`.
//
//go:embed all:web/static
var webStatic embed.FS

// staticRoot is where the Flutter build output is copied to.
const staticRoot = "web/static"

// apiPrefix marks paths owned by the JSON API. They must never fall through to
// the single-page-app shell, or a mistyped endpoint would return HTML.
const apiPrefix = "/api/"

// ErrNoWebBuild is returned when the embedded directory holds no index.html,
// which means the web build was never copied in.
var ErrNoWebBuild = errors.New("no web build embedded; run `moon run comic-mobile:web-embed`")

// newStaticHandler serves the embedded Flutter web build.
//
// Routing rules, in order:
//   - a path that maps to an embedded file is served directly
//   - API and health paths that reach this handler are 404s, not the SPA shell
//   - a path without a file extension is a client-side route, so it gets
//     index.html, which is what makes a hard refresh on /library work
//   - anything else (a missing .js or .png) is a genuine 404, so a broken asset
//     reference fails visibly instead of being answered with HTML
func newStaticHandler() (http.Handler, error) {
	sub, err := fs.Sub(webStatic, staticRoot)
	if err != nil {
		return nil, err
	}
	return newStaticHandlerFromFS(sub)
}

func newStaticHandlerFromFS(sub fs.FS) (http.Handler, error) {
	if _, err := fs.Stat(sub, "index.html"); err != nil {
		return nil, ErrNoWebBuild
	}

	files := http.FileServer(http.FS(sub))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upath := path.Clean("/" + r.URL.Path)

		// path.Clean drops the trailing slash, so both "/api" and "/api/..."
		// have to be recognised as API paths.
		if upath == "/api" || strings.HasPrefix(upath, apiPrefix) || upath == "/healthz" {
			http.NotFound(w, r)
			return
		}

		if isRoutePath(upath) {
			serveIndex(w, r, sub)
			return
		}

		if exists(sub, upath) {
			setCacheHeaders(w, upath)
			files.ServeHTTP(w, r)
			return
		}

		// A real file was requested and is missing.
		http.NotFound(w, r)
	}), nil
}

// isRoutePath reports whether a path should be answered with the SPA shell.
// A bare path like /library or /reset-password is a client-side route; a path
// with an extension is a file reference.
func isRoutePath(upath string) bool {
	if upath == "/" {
		return true
	}
	last := path.Base(upath)
	return !strings.Contains(last, ".")
}

func serveIndex(w http.ResponseWriter, r *http.Request, sub fs.FS) {
	f, err := sub.Open("index.html")
	if err != nil {
		http.Error(w, "index.html is missing from the embedded web build", http.StatusInternalServerError)
		return
	}
	defer func() { _ = f.Close() }()

	stat, err := f.Stat()
	if err != nil {
		http.Error(w, "cannot stat index.html", http.StatusInternalServerError)
		return
	}

	seeker, ok := f.(interface {
		Read([]byte) (int, error)
		Seek(int64, int) (int64, error)
	})
	if !ok {
		http.Error(w, "index.html is not seekable", http.StatusInternalServerError)
		return
	}

	setCacheHeaders(w, "/index.html")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// ServeContent handles conditional requests, which keeps the shell cheap to
	// revalidate.
	http.ServeContent(w, r, "index.html", stat.ModTime(), seeker)
}

func exists(sub fs.FS, upath string) bool {
	name := strings.TrimPrefix(upath, "/")
	if name == "" {
		return false
	}
	f, err := sub.Open(name)
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()

	stat, err := f.Stat()
	return err == nil && !stat.IsDir()
}

// noCacheFiles must be revalidated on every load. They are the entry points and
// the service worker: caching them is what makes a deployed app keep serving a
// stale main.dart.js.
var noCacheFiles = map[string]bool{
	"/index.html":                true,
	"/flutter_bootstrap.js":      true,
	"/flutter_service_worker.js": true,
	"/version.json":              true,
	"/manifest.json":             true,
}

// immutablePrefixes hold content-addressed or version-pinned build output.
var immutablePrefixes = []string{"/assets/", "/canvaskit/"}

func setCacheHeaders(w http.ResponseWriter, upath string) {
	switch {
	case noCacheFiles[upath]:
		w.Header().Set("Cache-Control", "no-cache, must-revalidate")
	case hasAnyPrefix(upath, immutablePrefixes):
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	default:
		w.Header().Set("Cache-Control", "public, max-age=3600")
	}
}

func hasAnyPrefix(s string, prefixes []string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}
