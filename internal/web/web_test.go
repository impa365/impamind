package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRoutesReturnTheApp(t *testing.T) {
	h := Handler()
	for _, path := range []string{"/", "/memorias", "/memorias/abc?project=fila", "/conectar", "/app.js", "/app.css", "/constellation.js", "/obsidian-graph.js"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s -> %d", path, rec.Code)
		}
		if strings.HasSuffix(path, ".js") || strings.HasSuffix(path, ".css") {
			continue
		}
		if !strings.Contains(rec.Body.String(), "<title>impamind</title>") {
			t.Fatalf("%s did not return the app", path)
		}
	}
}
