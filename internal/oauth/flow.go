// Package oauth implements the authorization-code flow with PKCE that chat
// apps use to reach impamind. The consent screen chooses which projects the
// client may touch. An OAuth client can never approve memories.
package oauth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"html"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"impamind/internal/api"
	"impamind/internal/auth"
	"impamind/internal/authn"
	"impamind/internal/config"
	"impamind/internal/store"
)

type Server struct {
	st  *store.Store
	an  *authn.Authenticator
	cfg config.Config
	lg  *slog.Logger
}

func New(st *store.Store, an *authn.Authenticator, cfg config.Config, lg *slog.Logger) *Server {
	if lg == nil {
		lg = slog.Default()
	}
	return &Server{st: st, an: an, cfg: cfg, lg: lg}
}

func (s *Server) Mount(a *api.Server) {
	a.Mount("GET /.well-known/oauth-protected-resource", http.HandlerFunc(s.protected))
	a.Mount("GET /.well-known/oauth-authorization-server", http.HandlerFunc(s.metadata))
	a.Mount("POST /oauth/register", http.HandlerFunc(s.register))
	a.Mount("GET /oauth/authorize", http.HandlerFunc(s.authorize))
	a.Mount("POST /oauth/authorize", http.HandlerFunc(s.consent))
	a.Mount("POST /oauth/token", http.HandlerFunc(s.token))
	a.Mount("POST /oauth/revoke", http.HandlerFunc(s.revoke))
}

func (s *Server) issuer() string {
	if s.cfg.PublicURL != "" {
		return strings.TrimRight(s.cfg.PublicURL, "/")
	}
	return ""
}

func (s *Server) protected(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"resource":                 s.issuer(),
		"authorization_servers":    []string{s.issuer()},
		"bearer_methods_supported": []string{"header"},
		"scopes_supported":         []string{"read", "write"},
	})
}

func (s *Server) metadata(w http.ResponseWriter, r *http.Request) {
	base := s.issuer()
	writeJSON(w, http.StatusOK, map[string]any{
		"issuer":                                base,
		"authorization_endpoint":                base + "/oauth/authorize",
		"token_endpoint":                        base + "/oauth/token",
		"registration_endpoint":                 base + "/oauth/register",
		"revocation_endpoint":                   base + "/oauth/revoke",
		"response_types_supported":              []string{"code"},
		"grant_types_supported":                 []string{"authorization_code", "refresh_token"},
		"code_challenge_methods_supported":      []string{"S256"},
		"token_endpoint_auth_methods_supported": []string{"none"},
	})
}

func (s *Server) register(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ClientName   string   `json:"client_name"`
		RedirectURIs []string `json:"redirect_uris"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || len(in.RedirectURIs) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_client_metadata"})
		return
	}
	for _, u := range in.RedirectURIs {
		if _, err := url.ParseRequestURI(u); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_redirect_uri"})
			return
		}
	}
	name := strings.TrimSpace(in.ClientName)
	if name == "" {
		name = "oauth client"
	}
	raw, _ := json.Marshal(in.RedirectURIs)
	c, err := s.st.CreateOAuthClient(r.Context(), name, string(raw))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "server_error"})
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"client_id": c.ID, "client_name": c.Name, "redirect_uris": in.RedirectURIs, "token_endpoint_auth_method": "none",
	})
}

func (s *Server) authorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if _, err := s.an.SessionCookie(r.Context(), r); err != nil {
		http.Redirect(w, r, "/?next="+url.QueryEscape(r.URL.RequestURI()), http.StatusFound)
		return
	}
	client, err := s.st.OAuthClient(r.Context(), q.Get("client_id"))
	if err != nil || !redirectAllowed(client.RedirectURIs, q.Get("redirect_uri")) || q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" {
		http.Error(w, "pedido de autorização inválido", http.StatusBadRequest)
		return
	}
	caller, _ := s.an.SessionCookie(r.Context(), r)
	ss, err := s.an.Scope(r.Context(), caller, "", "", true)
	if err != nil {
		http.Error(w, "sem sessão", http.StatusUnauthorized)
		return
	}
	scopes, err := s.st.ListScopes(r.Context(), ss)
	if err != nil {
		http.Error(w, "falha ao listar projetos", http.StatusInternalServerError)
		return
	}
	var b strings.Builder
	b.WriteString(`<!doctype html><meta charset="utf-8"><title>Autorizar · impamind</title><style>body{font:16px/1.5 system-ui;background:#10211c;color:#f4efe4;margin:0}main{max-width:32rem;margin:4rem auto;padding:1.5rem}label{display:block;margin:.4rem 0}button{background:#e2a84b;color:#1b1408;border:0;padding:.6rem 1rem;border-radius:.4rem;font-weight:700}</style><main>`)
	b.WriteString("<h1>Autorizar " + html.EscapeString(client.Name) + "</h1><p>Escolha os projetos que este aplicativo pode ler e escrever. Ele não poderá aprovar memórias.</p><form method=post>")
	for _, k := range []string{"client_id", "redirect_uri", "code_challenge", "code_challenge_method", "state", "resource"} {
		b.WriteString(`<input type=hidden name="` + k + `" value="` + html.EscapeString(q.Get(k)) + `">`)
	}
	for _, sc := range scopes {
		if sc.Kind != "project" {
			continue
		}
		b.WriteString(`<label><input type=checkbox name=project value="` + html.EscapeString(sc.ID) + `"> ` + html.EscapeString(sc.Name) + `</label>`)
	}
	b.WriteString(`<button>Autorizar</button></form></main>`)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(b.String()))
}

func (s *Server) consent(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "formulário inválido", http.StatusBadRequest)
		return
	}
	caller, err := s.an.SessionCookie(r.Context(), r)
	if err != nil {
		http.Error(w, "entre na interface antes de autorizar", http.StatusUnauthorized)
		return
	}
	client, err := s.st.OAuthClient(r.Context(), r.Form.Get("client_id"))
	if err != nil || !redirectAllowed(client.RedirectURIs, r.Form.Get("redirect_uri")) || r.Form.Get("code_challenge_method") != "S256" {
		http.Error(w, "cliente ou redirect inválido", http.StatusBadRequest)
		return
	}
	pid, err := s.st.EnsureOAuthPrincipal(r.Context(), client.ID, client.Name)
	if err != nil {
		http.Error(w, "falha ao criar o principal", http.StatusInternalServerError)
		return
	}
	ss, err := s.an.Scope(r.Context(), caller, "", "", true)
	if err != nil {
		http.Error(w, "sem permissão", http.StatusForbidden)
		return
	}
	grants := []store.Grant{{ScopeID: ss.GlobalID(), CanRead: true, CanPropose: true}}
	for _, id := range r.Form["project"] {
		if !ss.CanRead(id) {
			continue
		}
		grants = append(grants, store.Grant{ScopeID: id, CanRead: true, CanWrite: true, CanPropose: true})
	}
	if err := s.st.SetGrants(r.Context(), pid, grants); err != nil {
		http.Error(w, "falha ao gravar os grants", http.StatusInternalServerError)
		return
	}
	raw := make([]byte, 32)
	_, _ = rand.Read(raw)
	code := base64.RawURLEncoding.EncodeToString(raw)
	if err := s.st.SaveOAuthCode(r.Context(), auth.HashToken(code), store.OAuthCode{
		ClientID: client.ID, RedirectURI: r.Form.Get("redirect_uri"), CodeChallenge: r.Form.Get("code_challenge"),
		PrincipalID: pid, ExpiresAt: store.FormatTime(time.Now().Add(2 * time.Minute)),
	}); err != nil {
		http.Error(w, "falha ao emitir o código", http.StatusInternalServerError)
		return
	}
	dest, _ := url.Parse(r.Form.Get("redirect_uri"))
	q := dest.Query()
	q.Set("code", code)
	if state := r.Form.Get("state"); state != "" {
		q.Set("state", state)
	}
	dest.RawQuery = q.Encode()
	http.Redirect(w, r, dest.String(), http.StatusFound)
}

func (s *Server) token(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request"})
		return
	}
	switch r.Form.Get("grant_type") {
	case "authorization_code":
		s.exchange(w, r)
	case "refresh_token":
		s.refresh(w, r)
	default:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unsupported_grant_type"})
	}
}

func (s *Server) exchange(w http.ResponseWriter, r *http.Request) {
	code, err := s.st.TakeOAuthCode(r.Context(), auth.HashToken(r.Form.Get("code")))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_grant"})
		return
	}
	if code.ClientID != r.Form.Get("client_id") || code.RedirectURI != r.Form.Get("redirect_uri") || !pkce(r.Form.Get("code_verifier"), code.CodeChallenge) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_grant"})
		return
	}
	s.issue(w, r, code.ClientID, code.PrincipalID, newID())
}

func (s *Server) refresh(w http.ResponseWriter, r *http.Request) {
	presented := r.Form.Get("refresh_token")
	tok, err := s.st.OAuthToken(r.Context(), auth.HashToken(presented))
	if err != nil || tok.Kind != "refresh" || tok.ClientID != r.Form.Get("client_id") {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_grant"})
		return
	}
	if tok.RevokedAt != "" {
		_ = s.st.RevokeOAuthFamily(r.Context(), tok.Family)
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_grant"})
		return
	}
	if exp, ok := store.ParseTime(tok.ExpiresAt); !ok || time.Now().After(exp) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_grant"})
		return
	}
	_ = s.st.RevokeOAuthToken(r.Context(), auth.HashToken(presented))
	s.issue(w, r, tok.ClientID, tok.PrincipalID, tok.Family)
}

func (s *Server) issue(w http.ResponseWriter, r *http.Request, clientID, principal, family string) {
	access, refresh := authn.OAuthPrefix+randomToken(), authn.OAuthPrefix+randomToken()
	now := time.Now()
	if err := s.st.SaveOAuthToken(r.Context(), auth.HashToken(access), store.OAuthToken{
		ClientID: clientID, Kind: "access", Family: family, PrincipalID: principal, ExpiresAt: store.FormatTime(now.Add(time.Hour)),
	}); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "server_error"})
		return
	}
	if err := s.st.SaveOAuthToken(r.Context(), auth.HashToken(refresh), store.OAuthToken{
		ClientID: clientID, Kind: "refresh", Family: family, PrincipalID: principal, ExpiresAt: store.FormatTime(now.Add(30 * 24 * time.Hour)),
	}); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "server_error"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"access_token": access, "refresh_token": refresh, "token_type": "Bearer", "expires_in": 3600,
	})
}

func (s *Server) revoke(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	tok, err := s.st.OAuthToken(r.Context(), auth.HashToken(r.Form.Get("token")))
	if err == nil {
		_ = s.st.RevokeOAuthFamily(r.Context(), tok.Family)
	}
	w.WriteHeader(http.StatusOK)
}

// Verify resolves an access token for the authenticator.
func (s *Server) Verify(ctx context.Context, token string) (string, time.Time, error) {
	tok, err := s.st.OAuthToken(ctx, auth.HashToken(token))
	if err != nil || tok.Kind != "access" || tok.RevokedAt != "" {
		return "", time.Time{}, errors.New("invalid token")
	}
	exp, ok := store.ParseTime(tok.ExpiresAt)
	if !ok || time.Now().After(exp) {
		return "", time.Time{}, errors.New("expired")
	}
	return tok.PrincipalID, exp, nil
}

func redirectAllowed(raw, want string) bool {
	var list []string
	if json.Unmarshal([]byte(raw), &list) != nil {
		return false
	}
	for _, u := range list {
		if u == want {
			return true
		}
	}
	return false
}

func pkce(verifier, challenge string) bool {
	if len(verifier) < 43 || len(verifier) > 128 {
		return false
	}
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:]) == challenge
}

func randomToken() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func newID() string { return store.NewID() }

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
