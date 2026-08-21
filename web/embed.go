// Package webui embeds the built React application and serves it with SPA history fallback.
//
// # The bootstrap problem
//
// The go:embed directive refuses to compile when its pattern matches nothing, so a naive
// embed of dist would make "go build ./..." fail on a clean checkout — before anyone has run "make web". Two things
// avoid that without checking build output into git:
//
//   - web/dist/.gitkeep is committed and the embed pattern uses the all: prefix, which includes
//     dot-files. The pattern therefore always matches at least one file.
//   - fallback.html is committed next to this file and embedded separately. When dist holds no
//     index.html — a clean checkout, or a binary built without the frontend — the handler serves
//     that page instead, which says plainly that the frontend was not built and how to build it.
//
// A binary produced by "make build" always has the real application, because that target builds
// the frontend first.
package webui

import (
	"embed"
	"errors"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

//go:embed all:dist
var distFS embed.FS

//go:embed fallback.html
var fallbackHTML []byte

const indexFile = "index.html"

// Assets returns the built frontend rooted at dist, and reports whether a real build is present.
func Assets() (fs.FS, bool) {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		return nil, false
	}
	if _, err := fs.Stat(sub, indexFile); err != nil {
		return sub, false
	}
	return sub, true
}

// Built reports whether this binary carries a real frontend build.
func Built() bool {
	_, ok := Assets()
	return ok
}

// Handler serves the embedded SPA. Any GET or HEAD that does not name an embedded file returns
// index.html with status 200, so client-side routes survive a page reload. Callers are expected to
// route /api/ elsewhere before falling through to this handler.
func Handler() http.Handler {
	assets, built := Assets()
	if !built {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !readOnlyMethod(r.Method) {
				methodNotAllowed(w)
				return
			}
			writeHTML(w, r, fallbackHTML)
		})
	}

	index, err := fs.ReadFile(assets, indexFile)
	if err != nil {
		index = fallbackHTML
	}
	files := http.FileServerFS(assets)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !readOnlyMethod(r.Method) {
			methodNotAllowed(w)
			return
		}
		name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if name == "" || name == "." {
			writeHTML(w, r, index)
			return
		}
		info, err := fs.Stat(assets, name)
		if err != nil || info.IsDir() {
			if err != nil && !errors.Is(err, fs.ErrNotExist) {
				http.Error(w, "read embedded asset: "+err.Error(), http.StatusInternalServerError)
				return
			}
			writeHTML(w, r, index)
			return
		}
		// Vite fingerprints everything under assets/, so those may be cached forever.
		if strings.HasPrefix(name, "assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "no-cache")
		}
		files.ServeHTTP(w, r)
	})
}

func readOnlyMethod(m string) bool {
	return m == http.MethodGet || m == http.MethodHead
}

func methodNotAllowed(w http.ResponseWriter) {
	w.Header().Set("Allow", "GET, HEAD")
	http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
}

func writeHTML(w http.ResponseWriter, r *http.Request, body []byte) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodHead {
		return
	}
	_, _ = w.Write(body)
}
