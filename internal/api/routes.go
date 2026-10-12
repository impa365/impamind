package api

import (
	"io"
	"net/http"
	"strings"
	"time"

	"impamind/internal/auth"
	"impamind/internal/authn"
	"impamind/internal/protocol"
	"impamind/internal/store"
)

func (s *Server) routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /health", s.health)
	mux.HandleFunc("GET /ready", s.health)
	mux.HandleFunc("POST /auth/login", s.loginPost)
	mux.HandleFunc("POST /auth/logout", s.authed(s.logoutPost))
	mux.HandleFunc("GET /auth/me", s.authed(s.me))

	mux.HandleFunc("GET /api/status", s.authed(s.status))
	mux.HandleFunc("GET /api/scopes", s.authed(s.scopesList))
	mux.HandleFunc("POST /api/projects", s.authed(s.projectsCreate))
	mux.HandleFunc("GET /api/projects/{slug}", s.authed(s.projectsGet))
	mux.HandleFunc("PATCH /api/projects/{slug}", s.authed(s.projectsUpdate))
	mux.HandleFunc("DELETE /api/projects/{slug}", s.authed(s.projectsDelete))
	mux.HandleFunc("GET /api/projects/{slug}/aliases", s.authed(s.aliasesList))
	mux.HandleFunc("POST /api/projects/{slug}/aliases", s.authed(s.aliasesCreate))
	mux.HandleFunc("DELETE /api/projects/{slug}/aliases/{alias}", s.authed(s.aliasesDelete))

	mux.HandleFunc("GET /api/memories", s.authed(s.memoriesList))
	mux.HandleFunc("POST /api/memories", s.authed(s.memoriesCreate))
	mux.HandleFunc("GET /api/memories/{id}", s.authed(s.memoriesGet))
	mux.HandleFunc("PATCH /api/memories/{id}", s.authed(s.memoriesUpdate))
	mux.HandleFunc("DELETE /api/memories/{id}", s.authed(s.memoriesDelete))
	mux.HandleFunc("GET /api/memories/{id}/history", s.authed(s.memoriesHistory))
	mux.HandleFunc("POST /api/memories/{id}/approve", s.authed(s.memoriesApprove))
	mux.HandleFunc("POST /api/memories/{id}/reject", s.authed(s.memoriesReject))
	mux.HandleFunc("POST /api/memories/{id}/transition", s.authed(s.memoriesTransition))
	mux.HandleFunc("POST /api/memories/{id}/supersede", s.authed(s.memoriesSupersede))
	mux.HandleFunc("POST /api/memories/{id}/promote", s.authed(s.memoriesPromote))
	mux.HandleFunc("POST /api/memories/{id}/evidence", s.authed(s.memoriesEvidence))
	mux.HandleFunc("POST /api/memories/{id}/suggest-links", s.authed(s.memoriesSuggestLinks))

	mux.HandleFunc("GET /api/links", s.authed(s.linksList))
	mux.HandleFunc("POST /api/links", s.authed(s.linksCreate))
	mux.HandleFunc("DELETE /api/links", s.authed(s.linksDelete))
	mux.HandleFunc("GET /api/graph", s.authed(s.graph))
	mux.HandleFunc("POST /api/graph/suggest-links", s.authed(s.graphSuggestLinks))

	mux.HandleFunc("GET /api/vault", s.authed(s.vaultList))
	mux.HandleFunc("PUT /api/vault/{key}", s.authed(s.vaultPut))
	mux.HandleFunc("GET /api/vault/{key}", s.authed(s.vaultGet))
	mux.HandleFunc("DELETE /api/vault/{key}", s.authed(s.vaultDelete))

	mux.HandleFunc("POST /api/recall", s.authed(s.recall))
	mux.HandleFunc("POST /api/observe", s.authed(s.observe))
	mux.HandleFunc("POST /api/decide", s.authed(s.decide))
	mux.HandleFunc("POST /api/checkpoint", s.authed(s.checkpoint))
	mux.HandleFunc("POST /api/handoff", s.authed(s.handoff))
	mux.HandleFunc("POST /api/search", s.authed(s.search))
	mux.HandleFunc("GET /api/review", s.authed(s.reviewList))
	mux.HandleFunc("POST /api/review/{id}/resolve", s.authed(s.reviewResolve))
	mux.HandleFunc("GET /api/sessions", s.authed(s.sessionsList))
	mux.HandleFunc("GET /api/sessions/{id}", s.authed(s.sessionsGet))
	mux.HandleFunc("GET /api/health/protocol", s.authed(s.protocolHealth))

	mux.HandleFunc("GET /api/sync/export", s.authed(s.syncExport))
	mux.HandleFunc("POST /api/sync/import", s.authed(s.syncImport))

	mux.HandleFunc("GET /api/tokens", s.authed(s.tokensList))
	mux.HandleFunc("POST /api/tokens", s.authed(s.tokensCreate))
	mux.HandleFunc("PATCH /api/tokens/{id}", s.authed(s.tokensUpdate))
	mux.HandleFunc("DELETE /api/tokens/{id}", s.authed(s.tokensRevoke))
	mux.HandleFunc("GET /api/logs", s.authed(s.logsList))
	mux.HandleFunc("POST /api/backup", s.authed(s.backup))
	mux.HandleFunc("POST /api/bundle/export", s.authed(s.bundleExport))
	mux.HandleFunc("POST /api/bundle/import", s.authed(s.bundleImport))

	mux.HandleFunc("GET /openapi.json", s.openAPI)
	if s.mcp != nil {
		mux.Handle("/mcp", s.mcp)
		mux.Handle("/mcp/", s.mcp)
	}
	if s.mcpAdm != nil {
		mux.Handle("/mcp/admin", s.mcpAdm)
		mux.Handle("/mcp/admin/", s.mcpAdm)
	}
	if s.web != nil {
		mux.Handle("/", s.web)
	}
}

// Health and auth ----------------------------------------------------------------

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	n, err := s.st.ProbeCount(ctx)
	if err != nil {
		writeErr(w, err)
		return
	}
	if r.URL.Path == "/ready" {
		writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "probe": n})
		return
	}
	p, err := s.st.Probe(ctx)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "version": protocol.Version, "sqlite": p, "probe": n, "rag": s.svc.Index != nil})
}

type loginIn struct {
	Password string `json:"password"`
}

func (s *Server) loginPost(w http.ResponseWriter, r *http.Request) {
	if s.cfg.AdminPassword == "" {
		writeErr(w, &protocol.Error{Code: "disabled", Status: http.StatusForbidden, Msg: "web login is disabled: set IMPAMIND_ADMIN_PASSWORD"})
		return
	}
	if !s.login.allow(clientIP(r)) {
		writeErr(w, &protocol.Error{Code: "rate_limited", Status: http.StatusTooManyRequests, Msg: "too many login attempts; try again in a minute"})
		return
	}
	var in loginIn
	if err := readJSON(r, &in); err != nil {
		writeErr(w, err)
		return
	}
	if subtleEqual(in.Password, s.cfg.AdminPassword) {
		admin, err := s.st.AdminIdentity(r.Context())
		if err != nil {
			writeErr(w, err)
			return
		}
		exp := store.FormatTime(time.Now().Add(12 * time.Hour))
		sid, err := s.st.CreateWebSession(r.Context(), admin.Principal.ID, exp)
		if err != nil {
			writeErr(w, err)
			return
		}
		http.SetCookie(w, &http.Cookie{Name: authn.CookieName, Value: s.an.SignSession(sid), Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: s.cfg.CookieSecure, MaxAge: 12 * 3600})
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
		return
	}
	writeErr(w, &protocol.Error{Code: "unauthorized", Status: http.StatusUnauthorized, Msg: "wrong password"})
}

func subtleEqual(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	var v byte
	for i := range []byte(a) {
		v |= a[i] ^ b[i]
	}
	return v == 0
}

func (s *Server) logoutPost(w http.ResponseWriter, r *http.Request, c *authn.Caller) {
	if sid := s.an.SessionID(r); sid != "" {
		_ = s.st.RevokeWebSession(r.Context(), sid)
	}
	http.SetCookie(w, &http.Cookie{Name: authn.CookieName, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: s.cfg.CookieSecure})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) me(w http.ResponseWriter, r *http.Request, c *authn.Caller) {
	call, err := s.call(r, c, "", false)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"principal": c.Principal(), "via": c.Via, "project": call.SS.Project(),
		"can_create_projects": call.SS.CanCreateProjects(), "is_admin": call.SS.IsAdmin(),
	})
}

// Scopes and projects ------------------------------------------------------------

func (s *Server) status(w http.ResponseWriter, r *http.Request, c *authn.Caller) {
	call, err := s.call(r, c, "", false)
	if err != nil {
		writeErr(w, err)
		return
	}
	st, err := s.svc.Status(r.Context(), call.SS)
	if err != nil {
		writeErr(w, err)
		return
	}
	out := map[string]any{"status": st}
	if s.svc.Index != nil {
		out["rag"] = s.svc.Index.Stats()
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) scopesList(w http.ResponseWriter, r *http.Request, c *authn.Caller) {
	call, err := s.call(r, c, "", false)
	if err != nil {
		writeErr(w, err)
		return
	}
	list, err := s.svc.ListScopes(r.Context(), call)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"scopes": list})
}

func (s *Server) projectsCreate(w http.ResponseWriter, r *http.Request, c *authn.Caller) {
	call, err := s.call(r, c, "", false)
	if err != nil {
		writeErr(w, err)
		return
	}
	var in store.ProjectInput
	if err := readJSON(r, &in); err != nil {
		writeErr(w, err)
		return
	}
	sc, err := s.svc.CreateProject(r.Context(), call, in)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, sc)
}

func (s *Server) projectOf(r *http.Request, c *authn.Caller) (protocol.Call, store.Scope, error) {
	call, err := s.call(r, c, r.PathValue("slug"), false)
	if err != nil {
		return call, store.Scope{}, err
	}
	sc, err := s.svc.ScopeBySlug(r.Context(), call, r.PathValue("slug"))
	return call, sc, err
}

func (s *Server) projectsGet(w http.ResponseWriter, r *http.Request, c *authn.Caller) {
	_, sc, err := s.projectOf(r, c)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, sc)
}

func (s *Server) projectsUpdate(w http.ResponseWriter, r *http.Request, c *authn.Caller) {
	call, sc, err := s.projectOf(r, c)
	if err != nil {
		writeErr(w, err)
		return
	}
	var in store.ProjectInput
	if err := readJSON(r, &in); err != nil {
		writeErr(w, err)
		return
	}
	out, err := s.svc.UpdateProject(r.Context(), call, sc.ID, in)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) projectsDelete(w http.ResponseWriter, r *http.Request, c *authn.Caller) {
	call, sc, err := s.projectOf(r, c)
	if err != nil {
		writeErr(w, err)
		return
	}
	backup, err := s.svc.DeleteProject(r.Context(), call, sc.ID)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"deleted": sc.Slug, "backup": backup})
}

func (s *Server) aliasesList(w http.ResponseWriter, r *http.Request, c *authn.Caller) {
	call, sc, err := s.projectOf(r, c)
	if err != nil {
		writeErr(w, err)
		return
	}
	list, err := s.st.ListAliases(r.Context(), call.SS, sc.ID)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"aliases": list})
}

func (s *Server) aliasesCreate(w http.ResponseWriter, r *http.Request, c *authn.Caller) {
	call, sc, err := s.projectOf(r, c)
	if err != nil {
		writeErr(w, err)
		return
	}
	var in struct {
		Kind  string `json:"kind"`
		Value string `json:"value"`
	}
	if err := readJSON(r, &in); err != nil {
		writeErr(w, err)
		return
	}
	a, err := s.st.AddAlias(r.Context(), call.SS, sc.ID, in.Kind, in.Value)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, a)
}

func (s *Server) aliasesDelete(w http.ResponseWriter, r *http.Request, c *authn.Caller) {
	call, sc, err := s.projectOf(r, c)
	if err != nil {
		writeErr(w, err)
		return
	}
	if err := s.st.DeleteAlias(r.Context(), call.SS, sc.ID, r.PathValue("alias")); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"deleted": true})
}

// Memories -----------------------------------------------------------------------

func (s *Server) memoriesList(w http.ResponseWriter, r *http.Request, c *authn.Caller) {
	call, err := s.call(r, c, "", qBool(r, "all_projects"))
	if err != nil {
		writeErr(w, err)
		return
	}
	out, err := s.svc.List(r.Context(), call, protocol.ListIn{
		Scope: r.URL.Query().Get("scope"), Status: r.URL.Query().Get("status"), Kind: r.URL.Query().Get("kind"),
		Tier: qInt(r, "tier", 0), Tag: r.URL.Query().Get("tag"), SessionID: r.URL.Query().Get("session_id"),
		Limit: qInt(r, "limit", 50), Offset: qInt(r, "offset", 0),
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) memoriesCreate(w http.ResponseWriter, r *http.Request, c *authn.Caller) {
	var in protocol.AddIn
	if err := readJSON(r, &in); err != nil {
		writeErr(w, err)
		return
	}
	call, err := s.call(r, c, in.Project, false)
	if err != nil {
		writeErr(w, err)
		return
	}
	m, err := s.svc.AddMemory(r.Context(), call, in)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, m)
}

func (s *Server) memoriesGet(w http.ResponseWriter, r *http.Request, c *authn.Caller) {
	call, err := s.call(r, c, "", false)
	if err != nil {
		writeErr(w, err)
		return
	}
	out, err := s.svc.Read(r.Context(), call, protocol.ReadIn{ID: r.PathValue("id"), IncludeHistory: qBool(r, "history")})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) memoriesUpdate(w http.ResponseWriter, r *http.Request, c *authn.Caller) {
	call, err := s.call(r, c, "", false)
	if err != nil {
		writeErr(w, err)
		return
	}
	var in store.MemoryPatch
	if err := readJSON(r, &in); err != nil {
		writeErr(w, err)
		return
	}
	m, err := s.svc.UpdateMemory(r.Context(), call, r.PathValue("id"), in)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, m)
}

func (s *Server) memoriesDelete(w http.ResponseWriter, r *http.Request, c *authn.Caller) {
	call, err := s.call(r, c, "", false)
	if err != nil {
		writeErr(w, err)
		return
	}
	var in struct {
		Reason string `json:"reason"`
	}
	if err := readJSON(r, &in); err != nil {
		writeErr(w, err)
		return
	}
	if err := s.svc.DeleteMemory(r.Context(), call, r.PathValue("id"), in.Reason); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"deleted": true})
}

func (s *Server) memoriesHistory(w http.ResponseWriter, r *http.Request, c *authn.Caller) {
	call, err := s.call(r, c, "", false)
	if err != nil {
		writeErr(w, err)
		return
	}
	h, err := s.svc.History(r.Context(), call, r.PathValue("id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, h)
}

func (s *Server) memoriesApprove(w http.ResponseWriter, r *http.Request, c *authn.Caller) {
	call, err := s.call(r, c, "", false)
	if err != nil {
		writeErr(w, err)
		return
	}
	var in store.ApproveInput
	if err := readJSON(r, &in); err != nil {
		writeErr(w, err)
		return
	}
	m, err := s.svc.Approve(r.Context(), call, r.PathValue("id"), in)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, m)
}

func (s *Server) memoriesReject(w http.ResponseWriter, r *http.Request, c *authn.Caller) {
	call, err := s.call(r, c, "", false)
	if err != nil {
		writeErr(w, err)
		return
	}
	var in struct {
		Note string `json:"note"`
	}
	if err := readJSON(r, &in); err != nil {
		writeErr(w, err)
		return
	}
	m, err := s.svc.Reject(r.Context(), call, r.PathValue("id"), in.Note)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, m)
}

func (s *Server) memoriesTransition(w http.ResponseWriter, r *http.Request, c *authn.Caller) {
	call, err := s.call(r, c, "", false)
	if err != nil {
		writeErr(w, err)
		return
	}
	var in struct {
		To     string `json:"to"`
		Reason string `json:"reason"`
	}
	if err := readJSON(r, &in); err != nil {
		writeErr(w, err)
		return
	}
	m, err := s.svc.Transition(r.Context(), call, r.PathValue("id"), in.To, in.Reason)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, m)
}

func (s *Server) memoriesSupersede(w http.ResponseWriter, r *http.Request, c *authn.Caller) {
	var in protocol.SupersedeIn
	if err := readJSON(r, &in); err != nil {
		writeErr(w, err)
		return
	}
	call, err := s.call(r, c, in.Project, false)
	if err != nil {
		writeErr(w, err)
		return
	}
	in.OldID = r.PathValue("id")
	out, err := s.svc.Supersede(r.Context(), call, in)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) memoriesPromote(w http.ResponseWriter, r *http.Request, c *authn.Caller) {
	call, err := s.call(r, c, "", false)
	if err != nil {
		writeErr(w, err)
		return
	}
	var in struct {
		Approve bool `json:"approve"`
	}
	if err := readJSON(r, &in); err != nil {
		writeErr(w, err)
		return
	}
	m, err := s.svc.Promote(r.Context(), call, r.PathValue("id"), in.Approve)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, m)
}

func (s *Server) memoriesEvidence(w http.ResponseWriter, r *http.Request, c *authn.Caller) {
	call, err := s.call(r, c, "", false)
	if err != nil {
		writeErr(w, err)
		return
	}
	var in store.EvidenceIn
	if err := readJSON(r, &in); err != nil {
		writeErr(w, err)
		return
	}
	ev, err := s.st.AddEvidence(r.Context(), call.SS, r.PathValue("id"), in)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, ev)
}

// Links and graph ----------------------------------------------------------------

func (s *Server) linksList(w http.ResponseWriter, r *http.Request, c *authn.Caller) {
	call, err := s.call(r, c, "", false)
	if err != nil {
		writeErr(w, err)
		return
	}
	ids := strings.Split(r.URL.Query().Get("memory_ids"), ",")
	list, err := s.st.LinksFor(r.Context(), call.SS, ids)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"links": list})
}

func (s *Server) linksCreate(w http.ResponseWriter, r *http.Request, c *authn.Caller) {
	call, err := s.call(r, c, "", false)
	if err != nil {
		writeErr(w, err)
		return
	}
	var in struct {
		SourceID string `json:"source_id"`
		TargetID string `json:"target_id"`
		Label    string `json:"label"`
	}
	if err := readJSON(r, &in); err != nil {
		writeErr(w, err)
		return
	}
	l, err := s.st.CreateLink(r.Context(), call.SS, in.SourceID, in.TargetID, in.Label)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, l)
}

func (s *Server) linksDelete(w http.ResponseWriter, r *http.Request, c *authn.Caller) {
	call, err := s.call(r, c, "", false)
	if err != nil {
		writeErr(w, err)
		return
	}
	if err := s.st.DeleteLink(r.Context(), call.SS, r.URL.Query().Get("source_id"), r.URL.Query().Get("target_id")); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"deleted": true})
}

func (s *Server) graph(w http.ResponseWriter, r *http.Request, c *authn.Caller) {
	scopeQ := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("scope")))
	all := scopeQ == "" || scopeQ == "all" || scopeQ == "both" || scopeQ == "todos"
	call, err := s.call(r, c, "", all)
	if err != nil {
		writeErr(w, err)
		return
	}
	scopes, err := s.svc.ScopeIDs(r.Context(), call, r.URL.Query().Get("scope"))
	if err != nil {
		writeErr(w, err)
		return
	}
	g, err := s.st.Graph(r.Context(), call.SS, scopes, qBool(r, "include_superseded"), qInt(r, "limit", 500))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, g)
}

func (s *Server) memoriesSuggestLinks(w http.ResponseWriter, r *http.Request, c *authn.Caller) {
	call, err := s.call(r, c, "", false)
	if err != nil {
		writeErr(w, err)
		return
	}
	out, err := s.svc.SuggestSimilarLinks(r.Context(), call, r.PathValue("id"), protocol.SimilarMaxPerMemory)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) graphSuggestLinks(w http.ResponseWriter, r *http.Request, c *authn.Caller) {
	slug := r.URL.Query().Get("scope")
	if slug == "" {
		slug = r.URL.Query().Get("project")
	}
	scopeQ := strings.ToLower(strings.TrimSpace(slug))
	all := scopeQ == "all" || scopeQ == "both" || scopeQ == "todos"
	call, err := s.call(r, c, "", all)
	if err != nil {
		writeErr(w, err)
		return
	}
	if slug == "" && call.SS.HasProject() {
		slug = call.SS.Project().Slug
	}
	out, err := s.svc.SuggestSimilarForScope(r.Context(), call, slug)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) vaultList(w http.ResponseWriter, r *http.Request, c *authn.Caller) {
	call, err := s.call(r, c, "", false)
	if err != nil {
		writeErr(w, err)
		return
	}
	list, err := s.svc.VaultList(r.Context(), call, protocol.VaultListIn{Project: r.URL.Query().Get("project"), Scope: r.URL.Query().Get("scope")})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"entries": list})
}

func (s *Server) vaultPut(w http.ResponseWriter, r *http.Request, c *authn.Caller) {
	call, err := s.call(r, c, "", false)
	if err != nil {
		writeErr(w, err)
		return
	}
	var in protocol.VaultPutIn
	if err := readJSON(r, &in); err != nil {
		writeErr(w, err)
		return
	}
	in.Key = r.PathValue("key")
	if in.Project == "" {
		in.Project = r.URL.Query().Get("project")
	}
	if in.Scope == "" {
		in.Scope = r.URL.Query().Get("scope")
	}
	out, err := s.svc.VaultPut(r.Context(), call, in)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) vaultGet(w http.ResponseWriter, r *http.Request, c *authn.Caller) {
	call, err := s.call(r, c, "", false)
	if err != nil {
		writeErr(w, err)
		return
	}
	out, err := s.svc.VaultGet(r.Context(), call, protocol.VaultKeyIn{
		Key: r.PathValue("key"), Project: r.URL.Query().Get("project"), Scope: r.URL.Query().Get("scope"),
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) vaultDelete(w http.ResponseWriter, r *http.Request, c *authn.Caller) {
	call, err := s.call(r, c, "", false)
	if err != nil {
		writeErr(w, err)
		return
	}
	if err := s.svc.VaultDelete(r.Context(), call, protocol.VaultKeyIn{
		Key: r.PathValue("key"), Project: r.URL.Query().Get("project"), Scope: r.URL.Query().Get("scope"),
	}); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"deleted": true})
}

// Protocol operations ------------------------------------------------------------

func postOp[In, Out any](s *Server, w http.ResponseWriter, r *http.Request, c *authn.Caller, project func(*In) string, fn func(protocol.Call, In) (Out, error)) {
	var in In
	if err := readJSON(r, &in); err != nil {
		writeErr(w, err)
		return
	}
	call, err := s.call(r, c, project(&in), false)
	if err != nil {
		writeErr(w, err)
		return
	}
	out, err := fn(call, in)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) recall(w http.ResponseWriter, r *http.Request, c *authn.Caller) {
	postOp(s, w, r, c, func(in *protocol.RecallIn) string { return in.Project }, func(call protocol.Call, in protocol.RecallIn) (protocol.RecallOut, error) {
		return s.svc.Recall(r.Context(), call, in)
	})
}

func (s *Server) observe(w http.ResponseWriter, r *http.Request, c *authn.Caller) {
	postOp(s, w, r, c, func(in *protocol.ObserveIn) string { return in.Project }, func(call protocol.Call, in protocol.ObserveIn) (protocol.WriteOut, error) {
		return s.svc.Observe(r.Context(), call, in)
	})
}

func (s *Server) decide(w http.ResponseWriter, r *http.Request, c *authn.Caller) {
	postOp(s, w, r, c, func(in *protocol.DecideIn) string { return in.Project }, func(call protocol.Call, in protocol.DecideIn) (protocol.WriteOut, error) {
		return s.svc.Decide(r.Context(), call, in)
	})
}

func (s *Server) checkpoint(w http.ResponseWriter, r *http.Request, c *authn.Caller) {
	postOp(s, w, r, c, func(in *protocol.CheckpointIn) string { return in.Project }, func(call protocol.Call, in protocol.CheckpointIn) (protocol.CheckpointOut, error) {
		return s.svc.Checkpoint(r.Context(), call, in)
	})
}

func (s *Server) handoff(w http.ResponseWriter, r *http.Request, c *authn.Caller) {
	postOp(s, w, r, c, func(in *protocol.HandoffIn) string { return in.Project }, func(call protocol.Call, in protocol.HandoffIn) (protocol.HandoffOut, error) {
		return s.svc.Handoff(r.Context(), call, in)
	})
}

func (s *Server) search(w http.ResponseWriter, r *http.Request, c *authn.Caller) {
	var in protocol.SearchIn
	if err := readJSON(r, &in); err != nil {
		writeErr(w, err)
		return
	}
	call, err := s.call(r, c, in.Project, in.AllProjects)
	if err != nil {
		writeErr(w, err)
		return
	}
	out, err := s.svc.Search(r.Context(), call, in)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) reviewList(w http.ResponseWriter, r *http.Request, c *authn.Caller) {
	call, err := s.call(r, c, "", qBool(r, "all_projects"))
	if err != nil {
		writeErr(w, err)
		return
	}
	out, err := s.svc.Review(r.Context(), call, protocol.ReviewIn{Mode: r.URL.Query().Get("mode"), Scope: r.URL.Query().Get("scope"), Limit: qInt(r, "limit", 50)})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) reviewResolve(w http.ResponseWriter, r *http.Request, c *authn.Caller) {
	var in protocol.ResolveIn
	if err := readJSON(r, &in); err != nil {
		writeErr(w, err)
		return
	}
	call, err := s.call(r, c, in.Project, false)
	if err != nil {
		writeErr(w, err)
		return
	}
	in.ItemID = r.PathValue("id")
	out, err := s.svc.ResolveReview(r.Context(), call, in)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) sessionsList(w http.ResponseWriter, r *http.Request, c *authn.Caller) {
	call, err := s.call(r, c, "", false)
	if err != nil {
		writeErr(w, err)
		return
	}
	list, err := s.st.ListSessions(r.Context(), call.SS, r.URL.Query().Get("status"), qInt(r, "limit", 100))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"sessions": list})
}

func (s *Server) sessionsGet(w http.ResponseWriter, r *http.Request, c *authn.Caller) {
	call, err := s.call(r, c, "", false)
	if err != nil {
		writeErr(w, err)
		return
	}
	sess, err := s.st.GetSession(r.Context(), call.SS, r.PathValue("id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	cp, _, err := s.st.SessionCheckpoint(r.Context(), call.SS, sess.ID)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"session": sess, "checkpoint": cp})
}

func (s *Server) protocolHealth(w http.ResponseWriter, r *http.Request, c *authn.Caller) {
	call, err := s.call(r, c, "", qBool(r, "all_projects"))
	if err != nil {
		writeErr(w, err)
		return
	}
	h, err := s.svc.Health(r.Context(), call, qInt(r, "days", 30))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"health": h})
}

// Sync ---------------------------------------------------------------------------

func (s *Server) syncExport(w http.ResponseWriter, r *http.Request, c *authn.Caller) {
	call, err := s.call(r, c, "", false)
	if err != nil {
		writeErr(w, err)
		return
	}
	out, err := s.svc.Export(r.Context(), call, qBool(r, "include_global"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) syncImport(w http.ResponseWriter, r *http.Request, c *authn.Caller) {
	var in struct {
		Project string                `json:"project"`
		Items   []protocol.ImportItem `json:"items"`
	}
	if err := readJSON(r, &in); err != nil {
		writeErr(w, err)
		return
	}
	call, err := s.call(r, c, in.Project, false)
	if err != nil {
		writeErr(w, err)
		return
	}
	res, err := s.svc.Import(r.Context(), call, in.Items)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"results": res})
}

// Tokens, logs, backup -----------------------------------------------------------

func (s *Server) adminOnly(w http.ResponseWriter, c *authn.Caller) bool {
	if c.Via != "cookie" {
		writeErr(w, &protocol.Error{Code: "forbidden", Status: http.StatusForbidden, Msg: "only the admin session can manage tokens"})
		return false
	}
	return true
}

type tokenIn struct {
	Name       string        `json:"name"`
	Class      string        `json:"class"`
	ClientType string        `json:"client_type"`
	Grants     []store.Grant `json:"grants"`
}

func (s *Server) tokensList(w http.ResponseWriter, r *http.Request, c *authn.Caller) {
	if !s.adminOnly(w, c) {
		return
	}
	list, err := s.st.ListTokens(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"tokens": list})
}

func (s *Server) tokensCreate(w http.ResponseWriter, r *http.Request, c *authn.Caller) {
	if !s.adminOnly(w, c) {
		return
	}
	var in tokenIn
	if err := readJSON(r, &in); err != nil {
		writeErr(w, err)
		return
	}
	plain, t, err := s.st.CreateToken(r.Context(), in.Name, in.Class, in.ClientType, in.Grants)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"token": plain, "record": t})
}

func (s *Server) tokensUpdate(w http.ResponseWriter, r *http.Request, c *authn.Caller) {
	if !s.adminOnly(w, c) {
		return
	}
	t, err := s.st.GetToken(r.Context(), r.PathValue("id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	var in struct {
		Grants []store.Grant `json:"grants"`
	}
	if err := readJSON(r, &in); err != nil {
		writeErr(w, err)
		return
	}
	if err := s.st.SetGrants(r.Context(), t.PrincipalID, in.Grants); err != nil {
		writeErr(w, err)
		return
	}
	t, err = s.st.GetToken(r.Context(), t.ID)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, t)
}

func (s *Server) tokensRevoke(w http.ResponseWriter, r *http.Request, c *authn.Caller) {
	if !s.adminOnly(w, c) {
		return
	}
	if err := s.st.RevokeToken(r.Context(), r.PathValue("id")); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"revoked": true})
}

func (s *Server) logsList(w http.ResponseWriter, r *http.Request, c *authn.Caller) {
	if c.Principal().Class != auth.AdminSession && c.Principal().Class != auth.HumanToken {
		writeErr(w, &protocol.Error{Code: "forbidden", Status: http.StatusForbidden, Msg: "logs need a human principal"})
		return
	}
	list, err := s.st.ListLogs(r.Context(), store.LogFilter{Operation: r.URL.Query().Get("operation"), Level: r.URL.Query().Get("level"), Source: r.URL.Query().Get("source"), BeforeID: int64(qInt(r, "before", 0)), Limit: qInt(r, "limit", 200)})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"logs": list})
}

func (s *Server) backup(w http.ResponseWriter, r *http.Request, c *authn.Caller) {
	if !s.adminOnly(w, c) {
		return
	}
	dst := s.cfg.BackupDir + "/impamind-" + time.Now().UTC().Format("20060102-150405") + ".db"
	if err := s.st.Backup(r.Context(), dst); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"backup": dst})
}

func (s *Server) bundleExport(w http.ResponseWriter, r *http.Request, c *authn.Caller) {
	if !s.adminOnly(w, c) {
		return
	}
	call, err := s.call(r, c, "", true)
	if err != nil {
		writeErr(w, err)
		return
	}
	var in struct {
		Passphrase      string `json:"passphrase"`
		IncludeGlobal   bool   `json:"include_global"`
		IncludeSessions bool   `json:"include_sessions"`
		Project         string `json:"project"`
	}
	if err := readJSON(r, &in); err != nil {
		writeErr(w, err)
		return
	}
	raw, name, err := s.svc.ExportBundle(r.Context(), call, protocol.BundleExportOpts{
		Passphrase: in.Passphrase, IncludeGlobal: in.IncludeGlobal,
		IncludeSessions: in.IncludeSessions, Project: in.Project,
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(raw)
}

func (s *Server) bundleImport(w http.ResponseWriter, r *http.Request, c *authn.Caller) {
	if !s.adminOnly(w, c) {
		return
	}
	call, err := s.call(r, c, "", true)
	if err != nil {
		writeErr(w, err)
		return
	}
	if err := r.ParseMultipartForm(128 << 20); err != nil {
		writeErr(w, &protocol.Error{Code: "invalid", Status: http.StatusBadRequest, Msg: "multipart form required (file + passphrase)"})
		return
	}
	pass := strings.TrimSpace(r.FormValue("passphrase"))
	overwrite := r.FormValue("overwrite_vault") == "1" || strings.EqualFold(r.FormValue("overwrite_vault"), "true")
	skipLinks := r.FormValue("skip_links") == "1" || strings.EqualFold(r.FormValue("skip_links"), "true")
	f, _, err := r.FormFile("file")
	if err != nil {
		writeErr(w, &protocol.Error{Code: "invalid", Status: http.StatusBadRequest, Msg: "file field required"})
		return
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, 128<<20))
	if err != nil {
		writeErr(w, err)
		return
	}
	rep, err := s.svc.ImportBundle(r.Context(), call, raw, protocol.BundleImportOpts{
		Passphrase: pass, OverwriteVault: overwrite, SkipLinks: skipLinks,
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, rep)
}
