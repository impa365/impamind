// Package api serves the REST API, the web UI, the MCP endpoints and the
// OAuth endpoints from one http.Handler.
package api

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"

	"impamind/internal/auth"
	"impamind/internal/authn"
	"impamind/internal/config"
	"impamind/internal/protocol"
	"impamind/internal/store"
)

type Server struct {
	cfg   config.Config
	st    *store.Store
	svc   *protocol.Service
	an    *authn.Authenticator
	log   *store.Logger
	slog  *slog.Logger
	mux   *http.ServeMux
	login *limiter
	// Extra routes mounted by other packages (OAuth, downloads).
	mounts []mount
	web    http.Handler
	mcp    http.Handler
	mcpAdm http.Handler
}

type mount struct {
	pattern string
	h       http.Handler
}

type Deps struct {
	Config  config.Config
	Store   *store.Store
	Service *protocol.Service
	Auth    *authn.Authenticator
	Log     *store.Logger
	Slog    *slog.Logger
	Web     http.Handler
	MCP     http.Handler
	MCPAdm  http.Handler
}

func New(d Deps) *Server {
	s := &Server{cfg: d.Config, st: d.Store, svc: d.Service, an: d.Auth, log: d.Log, slog: d.Slog, web: d.Web, mcp: d.MCP, mcpAdm: d.MCPAdm,
		login: newLimiter(10, time.Minute)}
	if s.slog == nil {
		s.slog = slog.Default()
	}
	return s
}

// Mount adds a route served by another package.
func (s *Server) Mount(pattern string, h http.Handler) {
	s.mounts = append(s.mounts, mount{pattern, h})
}

// Handler builds the full HTTP handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	s.mux = mux
	s.routes(mux)
	for _, m := range s.mounts {
		mux.Handle(m.pattern, m.h)
	}
	cop := http.NewCrossOriginProtection()
	for _, p := range []string{"/oauth/token", "/oauth/register", "/oauth/revoke"} {
		cop.AddInsecureBypassPattern("POST " + p)
	}
	for _, o := range s.cfg.CORSOrigins {
		_ = cop.AddTrustedOrigin(o)
	}
	var h http.Handler = mux
	h = cop.Handler(h)
	h = s.cors(h)
	h = gzipJSON(h)
	h = s.common(h)
	return h
}

func (s *Server) common(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				if v == http.ErrAbortHandler {
					panic(v)
				}
				s.slog.Error("panic", "path", r.URL.Path, "err", v, "stack", string(debug.Stack()))
				writeErr(w, errors.New("panic"))
			}
		}()
		hd := w.Header()
		hd.Set("X-Content-Type-Options", "nosniff")
		hd.Set("Referrer-Policy", "no-referrer")
		hd.Set("X-Frame-Options", "DENY")
		hd.Set("Cross-Origin-Opener-Policy", "same-origin")
		if strings.HasPrefix(s.cfg.PublicURL, "https://") {
			hd.Set("Strict-Transport-Security", "max-age=31536000")
		}
		if r.ContentLength > 2<<20 {
			http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 2<<20)
		next.ServeHTTP(w, r)
	})
}

func (s *Server) cors(next http.Handler) http.Handler {
	allowed := map[string]bool{}
	for _, o := range s.cfg.CORSOrigins {
		allowed[strings.TrimRight(o, "/")] = true
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" && (allowed[origin] || allowed["*"] && !strings.HasPrefix(r.URL.Path, "/auth/")) {
			h := w.Header()
			h.Set("Access-Control-Allow-Origin", origin)
			h.Add("Vary", "Origin")
			h.Set("Access-Control-Allow-Headers", "Authorization, Content-Type, Mcp-Protocol-Version, Mcp-Session-Id, X-Impamind-Project, X-Impamind-Client, X-Impamind-Agent")
			h.Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
			h.Set("Access-Control-Expose-Headers", "WWW-Authenticate, Mcp-Session-Id")
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// gzip ------------------------------------------------------------------------

type gzipWriter struct {
	http.ResponseWriter
	gz      *gzip.Writer
	decided bool
	use     bool
}

var gzPool = sync.Pool{New: func() any { w, _ := gzip.NewWriterLevel(io.Discard, gzip.BestSpeed); return w }}

func (g *gzipWriter) WriteHeader(code int) {
	if !g.decided {
		g.decided = true
		ct := g.Header().Get("Content-Type")
		g.use = code == http.StatusOK && g.Header().Get("Content-Encoding") == "" &&
			(strings.HasPrefix(ct, "application/json") || strings.HasPrefix(ct, "text/") || strings.HasPrefix(ct, "application/javascript"))
		if g.use {
			g.Header().Del("Content-Length")
			g.Header().Set("Content-Encoding", "gzip")
			g.Header().Add("Vary", "Accept-Encoding")
			g.gz = gzPool.Get().(*gzip.Writer)
			g.gz.Reset(g.ResponseWriter)
		}
	}
	g.ResponseWriter.WriteHeader(code)
}

func (g *gzipWriter) Write(b []byte) (int, error) {
	if !g.decided {
		g.WriteHeader(http.StatusOK)
	}
	if g.use {
		return g.gz.Write(b)
	}
	return g.ResponseWriter.Write(b)
}

func (g *gzipWriter) Flush() {
	if g.use {
		_ = g.gz.Flush()
	}
	if f, ok := g.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (g *gzipWriter) close() {
	if g.use && g.gz != nil {
		_ = g.gz.Close()
		gzPool.Put(g.gz)
	}
}

func gzipJSON(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") || strings.HasPrefix(r.URL.Path, "/mcp") {
			next.ServeHTTP(w, r)
			return
		}
		g := &gzipWriter{ResponseWriter: w}
		defer g.close()
		next.ServeHTTP(g, r)
	})
}

// JSON helpers ------------------------------------------------------------------

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

func writeErr(w http.ResponseWriter, err error) {
	pe := protocol.AsError(err)
	if pe.Status == http.StatusUnauthorized {
		w.Header().Set("WWW-Authenticate", `Bearer realm="impamind"`)
	}
	writeJSON(w, pe.Status, map[string]any{"error": pe})
}

func readJSON(r *http.Request, v any) error {
	if ct := r.Header.Get("Content-Type"); ct != "" && !strings.HasPrefix(ct, "application/json") {
		return &protocol.Error{Code: "invalid", Status: http.StatusUnsupportedMediaType, Msg: "Content-Type must be application/json"}
	}
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(v); err != nil {
		if errors.Is(err, io.EOF) {
			return &protocol.Error{Code: "invalid", Status: http.StatusBadRequest, Msg: "request body is empty"}
		}
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			return &protocol.Error{Code: "too_large", Status: http.StatusRequestEntityTooLarge, Msg: "request body too large"}
		}
		return &protocol.Error{Code: "invalid", Status: http.StatusBadRequest, Msg: "invalid JSON: " + err.Error()}
	}
	return nil
}

func qInt(r *http.Request, k string, def int) int {
	if v := r.URL.Query().Get(k); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func qBool(r *http.Request, k string) bool {
	b, _ := strconv.ParseBool(r.URL.Query().Get(k))
	return b
}

func clientIP(r *http.Request) string {
	if xf := r.Header.Get("X-Forwarded-For"); xf != "" {
		if i := strings.IndexByte(xf, ','); i >= 0 {
			xf = xf[:i]
		}
		return strings.TrimSpace(xf)
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// Rate limiting -----------------------------------------------------------------

type limiter struct {
	mu     sync.Mutex
	max    int
	window time.Duration
	hits   map[string][]time.Time
}

func newLimiter(max int, window time.Duration) *limiter {
	return &limiter{max: max, window: window, hits: map[string][]time.Time{}}
}

func (l *limiter) allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	cut := now.Add(-l.window)
	list := l.hits[key][:0]
	for _, t := range l.hits[key] {
		if t.After(cut) {
			list = append(list, t)
		}
	}
	if len(list) >= l.max {
		l.hits[key] = list
		return false
	}
	l.hits[key] = append(list, now)
	if len(l.hits) > 10000 {
		l.hits = map[string][]time.Time{}
	}
	return true
}

// Caller resolution -------------------------------------------------------------

type ctxKey int

const callerCtx ctxKey = 1

// authed wraps a handler that needs a principal (Bearer or admin cookie).
func (s *Server) authed(fn func(http.ResponseWriter, *http.Request, *authn.Caller)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		c, err := s.an.Request(r)
		if err != nil {
			if errors.Is(err, authn.ErrNoCredential) || errors.Is(err, authn.ErrBadCredential) {
				writeErr(w, &protocol.Error{Code: "unauthorized", Status: http.StatusUnauthorized, Msg: "missing or invalid credential"})
				return
			}
			writeErr(w, err)
			return
		}
		rec := &statusRecorder{ResponseWriter: w, status: 200}
		fn(rec, r.WithContext(context.WithValue(r.Context(), callerCtx, c)), c)
		if s.log != nil {
			level := "info"
			if rec.status >= 500 {
				level = "error"
			} else if rec.status >= 400 {
				level = "warn"
			}
			s.log.Log(store.LogEntry{Source: "rest", PrincipalID: c.Principal().ID, Operation: r.Method + " " + r.Pattern, Level: level, Status: rec.status, DurationUS: time.Since(start).Microseconds()})
		}
	}
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) { s.status = code; s.ResponseWriter.WriteHeader(code) }

func (s *statusRecorder) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// call resolves the protocol call for a REST request. project is the explicit
// argument (query or body); the header can only agree with it.
func (s *Server) call(r *http.Request, c *authn.Caller, project string, all bool) (protocol.Call, error) {
	if project == "" {
		project = r.URL.Query().Get("project")
	}
	header := r.Header.Get(authn.ProjectHeader)
	ss, err := s.an.Scope(r.Context(), c, header, project, all)
	if err != nil {
		return protocol.Call{}, err
	}
	profile := protocol.ProfileREST
	if c.Via == "cookie" {
		profile = protocol.ProfileUI
	}
	call := protocol.Call{SS: ss, Profile: profile, Client: headerOr(r, "X-Impamind-Client", uaName(r)), Agent: headerOr(r, "X-Impamind-Agent", ""), Model: headerOr(r, "X-Impamind-Model", "")}
	if c.Via == "cookie" {
		call.Client = "impamind-ui"
	}
	call.Rescope = func(slug string) (auth.ScopeSet, error) {
		if header != "" || project != "" {
			return ss, auth.ErrNoProject
		}
		return s.an.Scope(r.Context(), c, "", slug, false)
	}
	return call, nil
}

func headerOr(r *http.Request, k, def string) string {
	v := strings.TrimSpace(r.Header.Get(k))
	if v == "" {
		return def
	}
	if len(v) > 80 {
		v = v[:80]
	}
	return v
}

func uaName(r *http.Request) string {
	ua := r.UserAgent()
	if i := strings.IndexAny(ua, " /("); i > 0 {
		ua = ua[:i]
	}
	if len(ua) > 80 {
		ua = ua[:80]
	}
	return ua
}
