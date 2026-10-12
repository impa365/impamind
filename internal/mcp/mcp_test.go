package mcp

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"impamind/internal/authn"
	"impamind/internal/config"
	"impamind/internal/protocol"
	"impamind/internal/store"
)

func newTest(t *testing.T) (*store.Store, *authn.Authenticator, string, http.Handler, http.Handler) {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	global, _, err := st.ScopeRefs(ctx)
	if err != nil {
		t.Fatalf("scopes: %v", err)
	}
	plain, _, err := st.CreateToken(ctx, "agent", "agent", "test", []store.Grant{
		{ScopeID: global.ID, CanRead: true, CanPropose: true},
		{ScopeID: "*", CanRead: true, CanWrite: true, CanPropose: true},
	})
	if err != nil {
		t.Fatalf("token: %v", err)
	}
	an := authn.New(st, []byte("test-secret"))
	svc := protocol.New(st, config.Config{MCPVersions: []string{"2026-07-28", "2025-11-25"}}, nil)
	agent, admin := Handlers(svc, an, Options{Versions: svc.Cfg.MCPVersions})
	return st, an, plain, agent, admin
}

func rpc(t *testing.T, h http.Handler, token, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Accept", "application/json, text/event-stream")
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestMCPAuth(t *testing.T) {
	_, _, token, agent, _ := newTest(t)
	init := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"t","version":"0"}}}`

	if res := rpc(t, agent, "", init); res.Code != http.StatusUnauthorized {
		t.Fatalf("no token: status %d, want 401 (%s)", res.Code, res.Body.String())
	}
	res := rpc(t, agent, token, init)
	if res.Code != http.StatusOK {
		t.Fatalf("with token: status %d body %s", res.Code, res.Body.String())
	}
	var out struct {
		Result struct {
			ProtocolVersion string `json:"protocolVersion"`
			Instructions    string `json:"instructions"`
		} `json:"result"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Result.ProtocolVersion == "" || !strings.Contains(out.Result.Instructions, "memory_recall") {
		t.Fatalf("initialize result: %+v", out.Result)
	}
}

func TestMCPContentType(t *testing.T) {
	_, _, token, agent, _ := newTest(t)
	r := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`))
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("Accept", "application/json, text/event-stream")
	w := httptest.NewRecorder()
	agent.ServeHTTP(w, r)
	if w.Code < 400 {
		t.Fatalf("missing Content-Type accepted: %d", w.Code)
	}
}

func TestMCPAdminProfile(t *testing.T) {
	_, _, token, agent, admin := newTest(t)
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"impamind_status","arguments":{}}}`
	if res := rpc(t, agent, token, body); !strings.Contains(res.Body.String(), "error") {
		t.Fatalf("agent endpoint should not serve admin tools: %s", res.Body.String())
	}
	if res := rpc(t, admin, token, body); res.Code != http.StatusForbidden {
		t.Fatalf("agent token on /mcp/admin: %d, want 403 (%s)", res.Code, res.Body.String())
	}
}

func TestMCPClientCall(t *testing.T) {
	_, _, token, agent, _ := newTest(t)
	srv := httptest.NewServer(agent)
	defer srv.Close()
	ctx := context.Background()
	client := mcp.NewClient(&mcp.Implementation{Name: "impamind-test", Version: "0"}, nil)
	tr := &mcp.StreamableClientTransport{Endpoint: srv.URL, HTTPClient: &http.Client{Transport: bearerRoundTripper{token, http.DefaultTransport}}}
	cs, err := client.Connect(ctx, tr, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer cs.Close()
	if instr := cs.InitializeResult().Instructions; !strings.Contains(instr, "memory_decide") {
		t.Fatalf("instructions missing: %q", instr)
	}
	tools, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	names := map[string]bool{}
	for _, tl := range tools.Tools {
		names[tl.Name] = true
	}
	for _, want := range []string{"memory_recall", "memory_observe", "memory_decide", "memory_checkpoint", "memory_handoff", "memory_supersede", "memory_review", "memory_review_resolve", "memory_read", "memory_search"} {
		if !names[want] {
			t.Errorf("missing tool %s", want)
		}
	}
	if names["impamind_status"] || names["memory_approve"] {
		t.Fatal("admin tools leaked into the agent profile")
	}
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "memory_recall", Arguments: map[string]any{"task": "verificar o servidor", "project_hint": "projeto-teste"}})
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if res.IsError {
		t.Fatalf("tool error: %+v", res)
	}
	var out protocol.RecallOut
	if err := json.Unmarshal([]byte(textOf(res)), &out); err != nil {
		t.Fatalf("decode recall: %v (%s)", err, textOf(res))
	}
	if out.ProjectUnresolved == nil {
		t.Fatal("expected project_unresolved for an unknown hint")
	}
}

type bearerRoundTripper struct {
	token string
	base  http.RoundTripper
}

func (b bearerRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return b.base.RoundTrip(r)
}

func textOf(res *mcp.CallToolResult) string {
	var b strings.Builder
	for _, c := range res.Content {
		if t, ok := c.(*mcp.TextContent); ok {
			b.WriteString(t.Text)
		}
	}
	if b.Len() == 0 && res.StructuredContent != nil {
		raw, _ := json.Marshal(res.StructuredContent)
		return string(raw)
	}
	return b.String()
}

// silence unused imports in case the SDK result shape changes
var _ = io.EOF
