// Package mcp exposes the memory protocol over MCP Streamable HTTP. The server
// is stateless: every request authenticates again, and the Mcp-Session-Id
// header neither authenticates nor selects a project.
package mcp

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"

	sdkauth "github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"impamind/internal/auth"
	"impamind/internal/authn"
	"impamind/internal/protocol"
)

const callerKey = "impamind.caller"

type Options struct {
	Versions            []string
	ResourceMetadataURL string
	Logger              *slog.Logger
}

// Handlers returns the agent (/mcp) and admin (/mcp/admin) endpoints.
func Handlers(svc *protocol.Service, an *authn.Authenticator, opts Options) (agent, admin http.Handler) {
	agentSrv := newServer(svc, an, protocol.ProfileAgent, opts)
	adminSrv := newServer(svc, an, protocol.ProfileAdmin, opts)
	stream := func(s *mcp.Server) http.Handler {
		return mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return s }, &mcp.StreamableHTTPOptions{
			Stateless:           true,
			JSONResponse:        true,
			Logger:              opts.Logger,
			MaxRequestBodyBytes: 1 << 20,
		})
	}
	verifier := func(ctx context.Context, token string, r *http.Request) (*sdkauth.TokenInfo, error) {
		c, err := an.VerifyToken(ctx, token)
		if err != nil {
			return nil, sdkauth.ErrInvalidToken
		}
		exp := c.Expires
		return &sdkauth.TokenInfo{UserID: c.Principal().ID, Expiration: exp, Extra: map[string]any{callerKey: c}}, nil
	}
	bearer := sdkauth.RequireBearerToken(verifier, &sdkauth.RequireBearerTokenOptions{
		ResourceMetadataURL:    opts.ResourceMetadataURL,
		AllowMissingExpiration: true,
	})
	agent = bearer(stream(agentSrv))
	admin = bearer(requireHuman(stream(adminSrv)))
	return agent, admin
}

// requireHuman rejects every principal but human tokens before dispatch.
func requireHuman(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ti := sdkauth.TokenInfoFromContext(r.Context())
		c := callerOf(ti)
		if c == nil || c.Principal().Class != auth.HumanToken {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":{"code":"forbidden","message":"the admin MCP endpoint needs a human token"}}`))
			return
		}
		next.ServeHTTP(w, r)
	})
}

func callerOf(ti *sdkauth.TokenInfo) *authn.Caller {
	if ti == nil || ti.Extra == nil {
		return nil
	}
	c, _ := ti.Extra[callerKey].(*authn.Caller)
	return c
}

type handler struct {
	svc     *protocol.Service
	an      *authn.Authenticator
	profile string
}

func newServer(svc *protocol.Service, an *authn.Authenticator, profile string, opts Options) *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: "impamind", Title: "impamind memory", Version: protocol.Version}, &mcp.ServerOptions{
		Instructions:              Instructions,
		Logger:                    opts.Logger,
		SupportedProtocolVersions: opts.Versions,
	})
	h := &handler{svc: svc, an: an, profile: profile}
	registerAgentTools(s, h)
	if profile == protocol.ProfileAdmin {
		registerAdminTools(s, h)
	}
	registerPrompts(s)
	return s
}

// call builds the protocol call for one tool request, resolving the ScopeSet
// from the bearer's grants, the project header and the project argument.
func (h *handler) call(ctx context.Context, req *mcp.CallToolRequest, project string, all bool) (protocol.Call, error) {
	var hdr http.Header
	var c *authn.Caller
	if req != nil && req.Extra != nil {
		hdr = req.Extra.Header
		c = callerOf(req.Extra.TokenInfo)
	}
	if c == nil {
		return protocol.Call{}, &protocol.Error{Code: "unauthorized", Status: http.StatusUnauthorized, Msg: "missing credential"}
	}
	header := ""
	if hdr != nil {
		header = hdr.Get(authn.ProjectHeader)
	}
	ss, err := h.an.Scope(ctx, c, header, project, all)
	if err != nil {
		return protocol.Call{}, err
	}
	call := protocol.Call{SS: ss, Profile: h.profile, Client: clientName(hdr), Agent: headerVal(hdr, "X-Impamind-Agent"), Model: headerVal(hdr, "X-Impamind-Model")}
	call.Rescope = func(slug string) (auth.ScopeSet, error) {
		if header != "" || project != "" {
			return ss, auth.ErrNoProject
		}
		return h.an.Scope(ctx, c, "", slug, false)
	}
	return call, nil
}

func headerVal(h http.Header, k string) string {
	if h == nil {
		return ""
	}
	v := strings.TrimSpace(h.Get(k))
	if len(v) > 80 {
		v = v[:80]
	}
	return v
}

func clientName(h http.Header) string {
	if v := headerVal(h, "X-Impamind-Client"); v != "" {
		return v
	}
	ua := headerVal(h, "User-Agent")
	if i := strings.IndexAny(ua, " /("); i > 0 {
		ua = ua[:i]
	}
	return ua
}

// toolErr renders a protocol error as a tool result the model can read.
func toolErr(err error) *mcp.CallToolResult {
	pe := protocol.AsError(err)
	b, _ := json.Marshal(map[string]any{"error": pe})
	return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: string(b)}}}
}

// tool registers a typed tool whose handler receives a resolved protocol.Call.
func tool[In any](s *mcp.Server, h *handler, name, desc string, readOnly bool, project func(*In) string, fn func(context.Context, protocol.Call, In) (any, error)) {
	f := false
	ann := &mcp.ToolAnnotations{ReadOnlyHint: readOnly, OpenWorldHint: &f}
	if !readOnly {
		ann.DestructiveHint = &f
	}
	mcp.AddTool(s, &mcp.Tool{Name: name, Description: desc, Annotations: ann}, func(ctx context.Context, req *mcp.CallToolRequest, in In) (*mcp.CallToolResult, any, error) {
		start := time.Now()
		call, err := h.call(ctx, req, project(&in), false)
		if err != nil {
			h.logCall(call, name, err, start)
			return toolErr(err), nil, nil
		}
		out, err := fn(ctx, call, in)
		h.logCall(call, name, err, start)
		if err != nil {
			return toolErr(err), nil, nil
		}
		return nil, out, nil
	})
}

func (h *handler) logCall(call protocol.Call, name string, err error, start time.Time) {
	if h.svc.Log == nil {
		return
	}
	status, level, detail := 200, "info", ""
	if err != nil {
		pe := protocol.AsError(err)
		status, level, detail = pe.Status, "warn", pe.Code
		if status >= 500 {
			level = "error"
		}
	}
	h.svc.Log.Log(logEntry(call, "mcp."+name, status, level, detail, time.Since(start)))
}
