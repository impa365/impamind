// Package web serves the embedded interface and the client downloads.
package web

import (
	"embed"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"impamind/internal/api"
)

//go:embed static/*
var static embed.FS

// Handler serves the interface. Unknown paths return the app shell so the
// browser keeps the URL and the script can route it.
func Handler() http.Handler {
	sub, err := fs.Sub(static, "static")
	if err != nil {
		panic(err)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/")
		if name == "" {
			name = "index.html"
		}
		body, err := fs.ReadFile(sub, name)
		if err != nil {
			name = "index.html"
			body, err = fs.ReadFile(sub, name)
			if err != nil {
				http.NotFound(w, r)
				return
			}
		}
		switch {
		case strings.HasSuffix(name, ".css"):
			w.Header().Set("Content-Type", "text/css; charset=utf-8")
		case strings.HasSuffix(name, ".js"):
			w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		default:
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
		}
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = w.Write(body)
	})
}

// MountDownloads exposes the client binaries copied into the image.
func MountDownloads(a *api.Server) {
	dir := os.Getenv("IMPAMIND_DOWNLOAD_DIR")
	if dir == "" {
		dir = "/downloads"
	}
	a.Mount("GET /downloads/{name}", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := filepath.Base(r.PathValue("name"))
		if name == "." || name == string(filepath.Separator) {
			http.NotFound(w, r)
			return
		}
		http.ServeFile(w, r, filepath.Join(dir, name))
	}))
}
