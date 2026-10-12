package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"impamind/internal/auth"
	"impamind/internal/authn"
	"impamind/internal/config"
	mcpsrv "impamind/internal/mcp"
	"impamind/internal/protocol"
	"impamind/internal/store"
)

func TestHeaderNarrowsAndForeignIDs(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	admin := adminSS(t, st)
	alpha := makeProject(t, st, admin, "alpha")
	beta := makeProject(t, st, admin, "beta")
	admin = adminSS(t, st)
	secret := makeMemory(t, st, admin, alpha.ID, "alpha secret beacon")
	makeMemory(t, st, admin, beta.ID, "beta visible beacon")
	g, _, err := st.ScopeRefs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	wide, _, err := st.CreateToken(ctx, "wide", "agent", "test", []store.Grant{
		{ScopeID: "*", CanRead: true, CanWrite: true, CanPropose: true},
		{ScopeID: g.ID, CanRead: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	pinned, _, err := st.CreateToken(ctx, "pinned", "agent", "test", []store.Grant{
		{ScopeID: beta.ID, CanRead: true, CanWrite: true, CanPropose: true},
		{ScopeID: g.ID, CanRead: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{MCPVersions: []string{"2025-11-25", "2026-07-28"}, ObservationBudget: 15}
	an := authn.New(st, []byte("secret"))
	svc := protocol.New(st, cfg, nil)
	agent, adm := mcpsrv.Handlers(svc, an, mcpsrv.Options{Versions: cfg.MCPVersions})
	h := New(Deps{Config: cfg, Store: st, Service: svc, Auth: an, MCP: agent, MCPAdm: adm}).Handler()

	res := do(t, h, http.MethodGet, "/api/memories", wide, "", "alpha")
	if res.Code != http.StatusOK {
		t.Fatalf("list: %d %s", res.Code, res.Body.String())
	}
	if strings.Contains(res.Body.String(), secret.ID) == false || strings.Contains(res.Body.String(), "beta visible") {
		t.Fatalf("header did not narrow the list: %s", res.Body.String())
	}
	if strings.Contains(res.Body.String(), alpha.ID) == false {
		t.Fatalf("alpha memory missing: %s", res.Body.String())
	}

	foreign := []struct{ method, path, body string }{
		{http.MethodGet, "/api/memories/" + secret.ID, ""},
		{http.MethodGet, "/api/memories/" + secret.ID + "/history", ""},
		{http.MethodPatch, "/api/memories/" + secret.ID, `{"title":"changed title"}`},
		{http.MethodDelete, "/api/memories/" + secret.ID, `{"reason":"cleanup"}`},
		{http.MethodPost, "/api/memories/" + secret.ID + "/approve", `{}`},
		{http.MethodPost, "/api/memories/" + secret.ID + "/reject", `{"note":"no"}`},
		{http.MethodPost, "/api/memories/" + secret.ID + "/transition", `{"to":"archived","reason":"old"}`},
		{http.MethodPost, "/api/memories/" + secret.ID + "/promote", `{"approve":false}`},
		{http.MethodPost, "/api/memories/" + secret.ID + "/evidence", `{"type":"file","ref":"main.go"}`},
		{http.MethodPost, "/api/memories/" + secret.ID + "/supersede", `{"reason":"replaced","new":{"title":"newer title","content":"a longer replacement body"}}`},
		{http.MethodGet, "/api/projects/alpha", ""},
		{http.MethodGet, "/api/projects/alpha/aliases", ""},
	}
	for _, c := range foreign {
		res := do(t, h, c.method, c.path, pinned, c.body, "")
		if res.Code != http.StatusNotFound && res.Code != http.StatusForbidden {
			t.Fatalf("%s %s -> %d %s", c.method, c.path, res.Code, res.Body.String())
		}
		if strings.Contains(res.Body.String(), "alpha secret") {
			t.Fatalf("%s %s leaked content", c.method, c.path)
		}
	}
	if res := do(t, h, http.MethodGet, "/api/memories?project=alpha", pinned, "", ""); res.Code != http.StatusNotFound {
		t.Fatalf("pinned token project=alpha: %d %s", res.Code, res.Body.String())
	}

	srv := httptest.NewServer(agent)
	t.Cleanup(srv.Close)
	client := mcp.NewClient(&mcp.Implementation{Name: "isolation", Version: "0"}, nil)
	cs, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: srv.URL, HTTPClient: &http.Client{Transport: bearer(pinned)}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	tool, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "memory_read", Arguments: map[string]any{"id": secret.ID}})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(tool)
	if !tool.IsError || !strings.Contains(string(raw), "not_found") || strings.Contains(string(raw), "alpha secret") {
		t.Fatalf("mcp read: %+v", tool)
	}
	if _, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "memory_approve", Arguments: map[string]any{"id": secret.ID}}); err == nil {
		t.Fatal("agent profile exposed memory_approve")
	}
}

func do(t *testing.T, h http.Handler, method, path, token, body, project string) *httptest.ResponseRecorder {
	t.Helper()
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, path, nil)
	} else {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
	}
	r.Header.Set("Authorization", "Bearer "+token)
	if project != "" {
		r.Header.Set(authn.ProjectHeader, project)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

type bearerRoundTripper struct {
	token string
	base  http.RoundTripper
}

func bearer(token string) bearerRoundTripper {
	return bearerRoundTripper{token, http.DefaultTransport}
}

func (b bearerRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return b.base.RoundTrip(r)
}

func adminSS(t *testing.T, st *store.Store) auth.ScopeSet {
	t.Helper()
	id, err := st.AdminIdentity(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	g, projects, err := st.ScopeRefs(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ss, err := auth.Resolve(id.Principal, id.Grants, g, projects, auth.Request{AllProjects: true})
	if err != nil {
		t.Fatal(err)
	}
	return ss
}

func makeProject(t *testing.T, st *store.Store, admin auth.ScopeSet, slug string) store.Scope {
	t.Helper()
	sc, err := st.CreateProject(context.Background(), admin, store.ProjectInput{Slug: slug, Name: slug, ReviewPolicy: "balanced"})
	if err != nil {
		t.Fatal(err)
	}
	return sc
}

func makeMemory(t *testing.T, st *store.Store, ss auth.ScopeSet, scopeID, title string) store.Memory {
	t.Helper()
	m, err := st.CreateMemory(context.Background(), ss, store.NewMemory{
		ScopeID: scopeID, Kind: "note", Title: title, Content: title + " is durable knowledge worth keeping",
		Status: "active", SourceType: "agent_inferred", Tier: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	return m
}
