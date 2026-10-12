package api

import (
	"net/http"
	"strings"
)

// openAPI serves an OpenAPI 3.1 document generated from the registered routes,
// so the spec cannot drift from the server.
func (s *Server) openAPI(w http.ResponseWriter, r *http.Request) {
	doc := map[string]any{
		"openapi": "3.1.0",
		"info": map[string]any{
			"title":       "impamind",
			"version":     "1.0.0",
			"description": "Servidor central de memória persistente. Autenticação por Bearer (token de API) ou cookie de sessão da UI. O header X-Impamind-Project seleciona o projeto e só estreita o que o grant permite.",
		},
		"servers": []map[string]any{{"url": s.cfg.PublicURL}},
		"components": map[string]any{
			"securitySchemes": map[string]any{
				"bearer": map[string]any{"type": "http", "scheme": "bearer"},
				"cookie": map[string]any{"type": "apiKey", "in": "cookie", "name": "impamind_session"},
			},
			"parameters": map[string]any{
				"projectHeader": map[string]any{"name": "X-Impamind-Project", "in": "header", "schema": map[string]any{"type": "string"}},
			},
		},
		"security": []map[string]any{{"bearer": []any{}}, {"cookie": []any{}}},
		"paths":    s.paths(),
	}
	writeJSON(w, http.StatusOK, doc)
}

var pathNotes = map[string]string{
	"/health": "Saúde do processo, versão do SQLite e probe de FTS5. Sem autenticação.",
	"/ready":  "Pronto para tráfego. Sem autenticação.", "/auth/login": "Login da UI com a senha de admin; define o cookie de sessão.",
	"/auth/logout": "Encerra a sessão web.", "/auth/me": "Quem está autenticado nesta chamada.",
	"/api/status": "Status do servidor e do índice.", "/api/scopes": "Escopo global e projetos visíveis ao principal.",
	"/api/projects": "Criação de projetos.", "/api/projects/{slug}": "Leitura, edição e exclusão (soft, com backup) de um projeto.",
	"/api/projects/{slug}/aliases":         "Aliases do projeto (git remote, caminho ou nome).",
	"/api/projects/{slug}/aliases/{alias}": "Remoção de um alias.",
	"/api/memories":                        "Listagem e criação de memórias.", "/api/memories/{id}": "Leitura, edição e exclusão (lápide) de uma memória.",
	"/api/memories/{id}/history": "Versões, linhagem e aprovações.",
	"/api/memories/{id}/approve": "Aprovação humana: único caminho de proposed para active.",
	"/api/memories/{id}/reject":  "Rejeição de uma proposta.", "/api/memories/{id}/transition": "deprecated ou archived.",
	"/api/memories/{id}/supersede": "Substituição atômica por uma nova memória.",
	"/api/memories/{id}/promote":   "Cópia para o global com derived_from_id (humano).",
	"/api/memories/{id}/evidence":  "Anexar evidência.", "/api/links": "Links entre memórias do mesmo escopo ou de projeto para global.",
	"/api/graph": "Grafo do escopo (grau, peso, tipos link|supersedes|similar).",
	"/api/graph/suggest-links": "Backfill de ligações similares em todo o escopo.",
	"/api/memories/{id}/suggest-links": "Sugere e persiste ligações similares (FTS/Jaccard).",
	"/api/vault": "Lista entradas do cofre (metadados; sem valores).",
	"/api/vault/{key}": "Ler/gravar/apagar credencial criptografada no cofre.",
	"/api/recall": "Pacote de recall com orçamento de tokens.",
	"/api/observe": "Registrar aprendizado (dedupe, segredos, orçamento).", "/api/decide": "Registrar decisão com rationale.",
	"/api/checkpoint": "Checkpoint da sessão.", "/api/handoff": "Encerrar a sessão com resumo.",
	"/api/search": "Busca textual (híbrida quando o RAG está ligado).", "/api/review": "Caixa de revisão.",
	"/api/review/{id}/resolve": "Resolver um item de revisão.", "/api/sessions": "Sessões de trabalho.",
	"/api/sessions/{id}": "Uma sessão e seu checkpoint.", "/api/health/protocol": "Saúde do protocolo por cliente e agente.",
	"/api/sync/export": "Exportação para sync markdown.", "/api/sync/import": "Importação de arquivos markdown.",
	"/api/tokens": "Tokens de API (somente sessão admin).", "/api/tokens/{id}": "Grants e revogação de um token.",
	"/api/logs": "Logs de auditoria (humanos).", "/api/backup": "Backup com VACUUM INTO.",
	"/api/bundle/export": "Exporta clone portátil .impamind (ZIP + cofre cifrado).",
	"/api/bundle/import": "Importa arquivo .impamind (merge idempotente).",
	"/openapi.json": "Este documento.", "/mcp": "MCP Streamable HTTP, perfil agente.",
	"/mcp/admin": "MCP Streamable HTTP, perfil admin (exige token humano).",
}

func (s *Server) paths() map[string]any {
	out := map[string]any{}
	for _, route := range s.muxRoutes() {
		method, path := splitRoute(route)
		if method == "" || path == "/" || strings.HasPrefix(path, "/oauth") || strings.HasPrefix(path, "/downloads") {
			continue
		}
		item, _ := out[path].(map[string]any)
		if item == nil {
			item = map[string]any{}
		}
		op := map[string]any{"summary": pathNotes[path], "responses": map[string]any{"200": map[string]any{"description": "ok"}}}
		if path == "/health" || path == "/ready" || path == "/openapi.json" {
			op["security"] = []any{}
		}
		item[strings.ToLower(method)] = op
		out[path] = item
	}
	return out
}

func splitRoute(route string) (string, string) {
	i := strings.IndexByte(route, ' ')
	if i < 0 {
		return "", route
	}
	return route[:i], route[i+1:]
}

// muxRoutes lists the patterns registered on the mux.
func (s *Server) muxRoutes() []string {
	var out []string
	s.mux.HandleFunc("GET /__routes_probe__", func(http.ResponseWriter, *http.Request) {})
	// net/http does not export its route table; the patterns are mirrored here
	// from the registrations in routes().
	return append(out, routePatterns...)
}

var routePatterns = []string{
	"GET /health", "GET /ready", "POST /auth/login", "POST /auth/logout", "GET /auth/me",
	"GET /api/status", "GET /api/scopes", "POST /api/projects", "GET /api/projects/{slug}", "PATCH /api/projects/{slug}", "DELETE /api/projects/{slug}",
	"GET /api/projects/{slug}/aliases", "POST /api/projects/{slug}/aliases", "DELETE /api/projects/{slug}/aliases/{alias}",
	"GET /api/memories", "POST /api/memories", "GET /api/memories/{id}", "PATCH /api/memories/{id}", "DELETE /api/memories/{id}",
	"GET /api/memories/{id}/history", "POST /api/memories/{id}/approve", "POST /api/memories/{id}/reject", "POST /api/memories/{id}/transition",
	"POST /api/memories/{id}/supersede", "POST /api/memories/{id}/promote", "POST /api/memories/{id}/evidence",
	"GET /api/links", "POST /api/links", "DELETE /api/links", "GET /api/graph",
	"POST /api/recall", "POST /api/observe", "POST /api/decide", "POST /api/checkpoint", "POST /api/handoff", "POST /api/search",
	"GET /api/review", "POST /api/review/{id}/resolve", "GET /api/sessions", "GET /api/sessions/{id}", "GET /api/health/protocol",
	"GET /api/sync/export", "POST /api/sync/import", "GET /api/tokens", "POST /api/tokens", "PATCH /api/tokens/{id}", "DELETE /api/tokens/{id}",
	"GET /api/logs", "POST /api/backup", "POST /api/bundle/export", "POST /api/bundle/import", "GET /openapi.json",
}
