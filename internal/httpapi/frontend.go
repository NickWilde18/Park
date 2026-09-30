package httpapi

import (
	"io/fs"
	"net/http"
	"path"
	"strings"
)

const noFrontend = "This build of Park has no frontend. Build it with `make web`, then build Park again.\n"

// frontend serves the built frontend from fsys. A path that names a file serves that file; any other path without
// a file extension is one of the frontend's own routes and serves index.html, so that the frontend draws the page.
// Files under assets/ have content hashes in their names and are cached for good; index.html is never cached.
// Only GET and HEAD are served.
func frontend(fsys fs.FS) http.Handler {
	return onlyRead(serveFrontend(fsys))
}

func onlyRead(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func serveFrontend(fsys fs.FS) http.Handler {
	index, err := fs.ReadFile(fsys, "index.html")
	if err != nil {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(noFrontend))
		})
	}
	files := http.FileServerFS(fsys)
	serveIndex := func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = w.Write(index)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if name == "" || name == "index.html" {
			serveIndex(w)
			return
		}
		if info, err := fs.Stat(fsys, name); err == nil && !info.IsDir() {
			if strings.HasPrefix(name, "assets/") {
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			}
			files.ServeHTTP(w, r)
			return
		}
		if path.Ext(name) != "" {
			http.NotFound(w, r)
			return
		}
		serveIndex(w)
	})
}
