package client

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHookInjectsRecall(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/recall" || r.Header.Get("Authorization") == "" {
			t.Errorf("request %s %s", r.URL.Path, r.Header.Get("Authorization"))
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"session_id": "s1", "protocol": "recall at start"})
	}))
	defer srv.Close()
	var buf strings.Builder
	in := strings.NewReader(`{"hook_event_name":"SessionStart","cwd":"/work/demo","source":"startup"}`)
	if err := Hook(nil, in, &buf, Env{URL: srv.URL, Token: "tok", Project: "demo"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "additionalContext") || !strings.Contains(buf.String(), "s1") {
		t.Fatalf("output %s", buf.String())
	}
	buf.Reset()
	if err := Hook([]string{"PreCompact"}, strings.NewReader(`{}`), &buf, Env{URL: srv.URL, Token: "tok", Project: "demo"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "memory_checkpoint") {
		t.Fatalf("precompact %s", buf.String())
	}
}

func TestSyncRoundTrip(t *testing.T) {
	var pushed []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/sync/export":
			_ = json.NewEncoder(w).Encode(map[string]any{"memories": []map[string]any{{
				"id": "m1", "kind": "decision", "title": "Use sqlite", "content": "One writer, many readers.", "status": "active", "tier": 1, "tags": []string{"db"},
			}}})
		case "/api/sync/import":
			var body struct {
				Items []map[string]any `json:"items"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			pushed = body.Items
			_ = json.NewEncoder(w).Encode(map[string]any{"results": []any{}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	dir := filepath.Join(t.TempDir(), "mem")
	env := Env{URL: srv.URL, Token: "tok", Project: "demo"}
	if err := Sync([]string{"pull", "--dir", dir}, env); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "decision", "use-sqlite.md")
	b, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(b), "id: m1") {
		t.Fatalf("file: %v %s", err, b)
	}
	if err := os.WriteFile(path, []byte(strings.Replace(string(b), "One writer, many readers.", "One writer behind a mutex.", 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Sync([]string{"push", "--dir", dir}, env); err != nil {
		t.Fatal(err)
	}
	if len(pushed) != 1 || pushed[0]["id"] != "m1" || !strings.Contains(pushed[0]["content"].(string), "mutex") {
		t.Fatalf("pushed %+v", pushed)
	}
}
