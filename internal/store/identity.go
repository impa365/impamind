package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"

	"impamind/internal/auth"
)

var (
	qScopeInsertGlobal    = system("scope.insert_global", "schema", `INSERT INTO scopes(id, kind, slug, name, description, hidden, review_policy, created_at, updated_at) VALUES (?, 'global', 'global', 'Global', 'Preferências, padrões e conhecimento reutilizável entre projetos', 0, 'strict', ?, ?)`)
	qPrincipalInsertAdmin = system("principal.insert_admin", "schema", `INSERT INTO principals(id, class, label, created_at) VALUES (?, 'admin_session', 'admin', ?)`)
	qGrantInsertAdmin     = system("grant.insert_admin", "schema", `INSERT INTO grants(principal_id, scope_id, can_read, can_write, can_propose, can_approve, cross_project)
		SELECT ?1, id, 1, 1, 1, 1, 1 FROM scopes WHERE kind = 'global'
		UNION ALL SELECT ?1, '*', 1, 1, 1, 1, 1`)

	qScopeRefs = system("scope.refs", "auth", `SELECT id, slug, kind FROM scopes WHERE deleted_at IS NULL`)

	qAdminPrincipal = system("principal.admin", "auth", `SELECT id, label FROM principals WHERE class = 'admin_session'`)
	qPrincipalByTok = system("principal.by_token_hash", "auth", `SELECT p.id, p.class, p.label, t.id FROM api_tokens t JOIN principals p ON p.token_id = t.id WHERE t.hash = ? AND t.revoked_at IS NULL`)
	qPrincipalByID  = system("principal.by_id", "auth", `SELECT id, class, label FROM principals WHERE id = ?`)
	qGrantsFor      = system("grant.for_principal", "auth", `SELECT scope_id, can_read, can_write, can_propose, can_approve, cross_project FROM grants WHERE principal_id = ?`)
	qGrantsDelete   = system("grant.delete_for_principal", "auth", `DELETE FROM grants WHERE principal_id = ?`)
	qGrantInsert    = system("grant.insert", "auth", `INSERT INTO grants(principal_id, scope_id, can_read, can_write, can_propose, can_approve, cross_project) VALUES (?, ?, ?, ?, ?, ?, ?)`)
	qTokenInsert    = system("token.insert", "auth", `INSERT INTO api_tokens(id, name, hash, class, client_type, created_at) VALUES (?, ?, ?, ?, ?, ?)`)
	qPrincipalTok   = system("principal.insert_token", "auth", `INSERT INTO principals(id, class, token_id, label, created_at) VALUES (?, ?, ?, ?, ?)`)
	qTokenList      = system("token.list", "auth", `SELECT t.id, t.name, t.class, t.client_type, p.id, t.created_at, COALESCE(t.last_used_at,''), COALESCE(t.revoked_at,'') FROM api_tokens t JOIN principals p ON p.token_id = t.id ORDER BY t.created_at DESC`)
	qTokenGet       = system("token.get", "auth", `SELECT t.id, t.name, t.class, t.client_type, p.id, t.created_at, COALESCE(t.last_used_at,''), COALESCE(t.revoked_at,'') FROM api_tokens t JOIN principals p ON p.token_id = t.id WHERE t.id = ?`)
	qTokenRevoke    = system("token.revoke", "auth", `UPDATE api_tokens SET revoked_at = ? WHERE id = ? AND revoked_at IS NULL`)
	qTokenTouch     = system("token.touch", "auth", `UPDATE api_tokens SET last_used_at = ? WHERE id = ?`)
	qTokenByName    = system("token.by_name", "auth", `SELECT id FROM api_tokens WHERE name = ? AND revoked_at IS NULL`)
	qTokenByHash    = system("token.exists_hash", "auth", `SELECT count(*) FROM api_tokens WHERE hash = ?`)

	qWebSessionInsert = system("websession.insert", "auth", `INSERT INTO web_sessions(id, principal_id, created_at, expires_at) VALUES (?, ?, ?, ?)`)
	qWebSessionGet    = system("websession.get", "auth", `SELECT principal_id FROM web_sessions WHERE id = ? AND revoked_at IS NULL AND expires_at > ?`)
	qWebSessionRevoke = system("websession.revoke", "auth", `UPDATE web_sessions SET revoked_at = ? WHERE id = ?`)
	qWebSessionPurge  = system("websession.purge", "job", `DELETE FROM web_sessions WHERE expires_at < ? OR revoked_at IS NOT NULL`)

	qScopeInsert = system("scope.insert_project", "auth", `INSERT INTO scopes(id, kind, slug, name, description, hidden, review_policy, created_at, updated_at)
		VALUES (?, 'project', ?, ?, ?, ?, ?, ?, ?)`)
	qScopeList = scoped("scope.list", `SELECT s.id, s.kind, s.slug, s.name, s.description, s.hidden, s.review_policy, s.created_at, s.updated_at, COALESCE(s.deleted_at,''),
		(SELECT count(*) FROM memories m WHERE m.scope_id = s.id AND m.status = 'active'),
		(SELECT count(*) FROM memories m WHERE m.scope_id = s.id AND m.status = 'proposed'),
		(SELECT count(*) FROM memories m WHERE m.scope_id = s.id)
		FROM scopes s WHERE s.deleted_at IS NULL AND {{in:s.id}} ORDER BY s.kind DESC, s.name COLLATE NOCASE`)
	qScopeGet = scoped("scope.get", `SELECT s.id, s.kind, s.slug, s.name, s.description, s.hidden, s.review_policy, s.created_at, s.updated_at, COALESCE(s.deleted_at,''),
		(SELECT count(*) FROM memories m WHERE m.scope_id = s.id AND m.status = 'active'),
		(SELECT count(*) FROM memories m WHERE m.scope_id = s.id AND m.status = 'proposed'),
		(SELECT count(*) FROM memories m WHERE m.scope_id = s.id)
		FROM scopes s WHERE s.id = ? AND s.deleted_at IS NULL AND {{in:s.id}}`)
	qScopeUpdate       = scoped("scope.update", `UPDATE scopes SET name = ?, description = ?, hidden = ?, review_policy = ?, updated_at = ? WHERE id = ? AND kind = 'project' AND deleted_at IS NULL AND {{in:id}}`)
	qScopeUpdateGlobal = scoped("scope.update_global", `UPDATE scopes SET description = ?, updated_at = ? WHERE id = ? AND kind = 'global' AND {{in:id}}`)
	qScopeSoftDelete   = scoped("scope.soft_delete", `UPDATE scopes SET deleted_at = ?, slug = slug || '~deleted~' || ?, updated_at = ? WHERE id = ? AND kind = 'project' AND deleted_at IS NULL AND {{in:id}}`)
	qScopeTags         = scoped("scope.tags", `SELECT scope_id, tag FROM scope_tags WHERE {{scope}} ORDER BY tag`)
	qScopeTagsDelete   = scoped("scope.tags_delete", `DELETE FROM scope_tags WHERE scope_id = ? AND {{scope}}`)
	qScopeTagInsert    = scoped("scope.tag_insert", `INSERT OR IGNORE INTO scope_tags(scope_id, tag) SELECT ?1, ?2 WHERE {{in:?1}}`)

	qAliasList   = scoped("alias.list", `SELECT alias, scope_id, kind, created_at FROM project_aliases WHERE {{scope}} ORDER BY alias`)
	qAliasInsert = scoped("alias.insert", `INSERT INTO project_aliases(alias, scope_id, kind, created_at) SELECT ?1, ?2, ?3, ?4 WHERE {{in:?2}}`)
	qAliasDelete = scoped("alias.delete", `DELETE FROM project_aliases WHERE alias = ? AND {{scope}}`)
	qAliasLookup = system("alias.lookup", "auth", `SELECT a.scope_id, s.slug FROM project_aliases a JOIN scopes s ON s.id = a.scope_id WHERE a.alias = ? AND s.deleted_at IS NULL`)
)

var slugRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,62}$`)

// NormalizeSlug lowercases and validates a project slug.
func NormalizeSlug(s string) (string, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "global" || s == "*" || !slugRe.MatchString(s) || strings.Contains(s, "~") {
		return "", fmt.Errorf("%w: slug must match %s and not be reserved", ErrInvalid, slugRe.String())
	}
	return s, nil
}

// Auth ------------------------------------------------------------------------

type scopeRefCache struct {
	mu      sync.Mutex
	version int64
	global  auth.ScopeRef
	refs    []auth.ScopeRef
	ok      bool
}

// ScopeRefs returns the global scope and all live project scopes.
func (s *Store) ScopeRefs(ctx context.Context) (auth.ScopeRef, []auth.ScopeRef, error) {
	c := &s.refs
	ver := s.AuthVersion()
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ok && c.version == ver {
		return c.global, c.refs, nil
	}
	refs, err := collectRows(s.reader(ctx).q(qScopeRefs), func(r rowScanner) (auth.ScopeRef, error) {
		var x auth.ScopeRef
		return x, r.Scan(&x.ID, &x.Slug, &x.Kind)
	})
	if err != nil {
		return auth.ScopeRef{}, nil, err
	}
	var global auth.ScopeRef
	projects := refs[:0:0]
	for _, r := range refs {
		if r.Kind == "global" {
			global = r
		} else {
			projects = append(projects, r)
		}
	}
	if global.ID == "" {
		return auth.ScopeRef{}, nil, errors.New("global scope missing")
	}
	c.global, c.refs, c.version, c.ok = global, projects, ver, true
	return global, projects, nil
}

// Identity is a principal with its stored grants.
type Identity struct {
	Principal auth.Principal
	Grants    []auth.Grant
	TokenID   string
}

func scanGrant(r rowScanner) (auth.Grant, error) {
	var g auth.Grant
	return g, r.Scan(&g.ScopeID, &g.CanRead, &g.CanWrite, &g.CanPropose, &g.CanApprove, &g.CrossProject)
}

func (s *Store) grantsFor(ctx context.Context, principalID string) ([]auth.Grant, error) {
	return collectRows(s.reader(ctx).q(qGrantsFor, principalID), scanGrant)
}

// IdentityByTokenHash resolves an API token. Revoked or unknown tokens return ErrNotFound.
func (s *Store) IdentityByTokenHash(ctx context.Context, hash string) (Identity, error) {
	var id Identity
	var class string
	err := s.reader(ctx).row(qPrincipalByTok, hash).Scan(&id.Principal.ID, &class, &id.Principal.Label, &id.TokenID)
	if err != nil {
		return id, notFound(err)
	}
	id.Principal.Class = auth.Class(class)
	id.Grants, err = s.grantsFor(ctx, id.Principal.ID)
	return id, err
}

// IdentityByPrincipal loads a principal (used for OAuth clients and the admin).
func (s *Store) IdentityByPrincipal(ctx context.Context, principalID string) (Identity, error) {
	var id Identity
	var class string
	err := s.reader(ctx).row(qPrincipalByID, principalID).Scan(&id.Principal.ID, &class, &id.Principal.Label)
	if err != nil {
		return id, notFound(err)
	}
	id.Principal.Class = auth.Class(class)
	id.Grants, err = s.grantsFor(ctx, id.Principal.ID)
	return id, err
}

func (s *Store) AdminIdentity(ctx context.Context) (Identity, error) {
	var pid, label string
	if err := s.reader(ctx).row(qAdminPrincipal).Scan(&pid, &label); err != nil {
		return Identity{}, notFound(err)
	}
	return s.IdentityByPrincipal(ctx, pid)
}

// TouchToken records last use; callers rate-limit how often they call it.
func (s *Store) TouchToken(ctx context.Context, tokenID string) error {
	return s.write(ctx, func(w wx) error {
		_, err := w.exec(qTokenTouch, now(), tokenID)
		return err
	})
}

// CreateToken stores a new API token with its principal and grants and returns
// the plaintext once. class is "agent" or "human".
func (s *Store) CreateToken(ctx context.Context, name, class, clientType string, grants []Grant) (string, Token, error) {
	plain, hash := auth.NewToken()
	t, err := s.createTokenWithHash(ctx, name, class, clientType, hash, grants)
	return plain, t, err
}

// EnsureTokenHash creates a token for a known hash if none exists (bootstrap).
func (s *Store) EnsureTokenHash(ctx context.Context, name, class, hash string, grants []Grant) (bool, error) {
	var n int
	if err := s.reader(ctx).row(qTokenByHash, hash).Scan(&n); err != nil {
		return false, err
	}
	if n > 0 {
		return false, nil
	}
	_, err := s.createTokenWithHash(ctx, name, class, "bootstrap", hash, grants)
	return err == nil, err
}

func (s *Store) createTokenWithHash(ctx context.Context, name, class, clientType, hash string, grants []Grant) (Token, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 120 {
		return Token{}, fmt.Errorf("%w: token name required (max 120)", ErrInvalid)
	}
	if class != "agent" && class != "human" {
		return Token{}, fmt.Errorf("%w: class must be agent or human", ErrInvalid)
	}
	pclass := class + "_token"
	for _, g := range grants {
		if g.CanApprove && class == "agent" {
			return Token{}, fmt.Errorf("%w: agent tokens cannot hold can_approve", ErrForbidden)
		}
	}
	t := Token{ID: newID(), Name: name, Class: class, ClientType: clientType, PrincipalID: newID(), CreatedAt: now()}
	err := s.write(ctx, func(w wx) error {
		if _, err := w.exec(qTokenInsert, t.ID, t.Name, hash, class, clientType, t.CreatedAt); err != nil {
			return err
		}
		if _, err := w.exec(qPrincipalTok, t.PrincipalID, pclass, t.ID, name, t.CreatedAt); err != nil {
			return err
		}
		return insertGrants(w, t.PrincipalID, grants)
	})
	if err != nil {
		return Token{}, grantErr(err)
	}
	s.bumpAuth()
	t.Grants = grants
	return t, nil
}

func grantErr(err error) error {
	switch {
	case isAbort(err, "cannot hold can_approve"):
		return fmt.Errorf("%w: agent principals cannot hold can_approve", ErrForbidden)
	case isAbort(err, "grant scope does not exist"):
		return fmt.Errorf("%w: grant scope does not exist", ErrInvalid)
	}
	return err
}

func insertGrants(w wx, principalID string, grants []Grant) error {
	seen := map[string]bool{}
	for _, g := range grants {
		if seen[g.ScopeID] {
			return fmt.Errorf("%w: duplicate grant for scope %s", ErrInvalid, g.ScopeID)
		}
		seen[g.ScopeID] = true
		if _, err := w.exec(qGrantInsert, principalID, g.ScopeID, b2i(g.CanRead || g.CanWrite), b2i(g.CanWrite), b2i(g.CanPropose || g.CanWrite), b2i(g.CanApprove), b2i(g.CrossProject)); err != nil {
			return err
		}
	}
	return nil
}

// SetGrants replaces every grant of a principal.
func (s *Store) SetGrants(ctx context.Context, principalID string, grants []Grant) error {
	err := s.write(ctx, func(w wx) error {
		if _, err := w.exec(qGrantsDelete, principalID); err != nil {
			return err
		}
		return insertGrants(w, principalID, grants)
	})
	if err != nil {
		return grantErr(err)
	}
	s.bumpAuth()
	return nil
}

func scanToken(r rowScanner) (Token, error) {
	var t Token
	return t, r.Scan(&t.ID, &t.Name, &t.Class, &t.ClientType, &t.PrincipalID, &t.CreatedAt, &t.LastUsedAt, &t.RevokedAt)
}

func (s *Store) storedGrants(ctx context.Context, principalID string) ([]Grant, error) {
	gs, err := s.grantsFor(ctx, principalID)
	if err != nil {
		return nil, err
	}
	global, refs, err := s.ScopeRefs(ctx)
	if err != nil {
		return nil, err
	}
	slug := map[string]string{global.ID: "global", "*": "*"}
	for _, r := range refs {
		slug[r.ID] = r.Slug
	}
	out := make([]Grant, 0, len(gs))
	for _, g := range gs {
		out = append(out, Grant{ScopeID: g.ScopeID, ScopeSlug: slug[g.ScopeID], CanRead: g.CanRead, CanWrite: g.CanWrite, CanPropose: g.CanPropose, CanApprove: g.CanApprove, CrossProject: g.CrossProject})
	}
	return out, nil
}

func (s *Store) ListTokens(ctx context.Context) ([]Token, error) {
	toks, err := collectRows(s.reader(ctx).q(qTokenList), scanToken)
	if err != nil {
		return nil, err
	}
	for i := range toks {
		if toks[i].Grants, err = s.storedGrants(ctx, toks[i].PrincipalID); err != nil {
			return nil, err
		}
	}
	return toks, nil
}

func (s *Store) GetToken(ctx context.Context, id string) (Token, error) {
	t, err := scanToken(s.reader(ctx).row(qTokenGet, id))
	if err != nil {
		return t, notFound(err)
	}
	t.Grants, err = s.storedGrants(ctx, t.PrincipalID)
	return t, err
}

func (s *Store) RevokeToken(ctx context.Context, id string) error {
	var n int64
	err := s.write(ctx, func(w wx) (err error) {
		n, err = w.execN(qTokenRevoke, now(), id)
		return err
	})
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	s.bumpAuth()
	return nil
}

// Web sessions ------------------------------------------------------------------

func (s *Store) CreateWebSession(ctx context.Context, principalID string, expires string) (string, error) {
	id := auth.RandomID(32)
	err := s.write(ctx, func(w wx) error {
		_, err := w.exec(qWebSessionInsert, id, principalID, now(), expires)
		return err
	})
	return id, err
}

func (s *Store) WebSessionPrincipal(ctx context.Context, id string) (string, error) {
	var pid string
	err := s.reader(ctx).row(qWebSessionGet, id, now()).Scan(&pid)
	return pid, notFound(err)
}

func (s *Store) RevokeWebSession(ctx context.Context, id string) error {
	return s.write(ctx, func(w wx) error {
		_, err := w.exec(qWebSessionRevoke, now(), id)
		return err
	})
}

// Scopes ------------------------------------------------------------------------

func scanScope(r rowScanner) (Scope, error) {
	var sc Scope
	var c Counts
	err := r.Scan(&sc.ID, &sc.Kind, &sc.Slug, &sc.Name, &sc.Description, &sc.Hidden, &sc.ReviewPolicy, &sc.CreatedAt, &sc.UpdatedAt, &sc.DeletedAt, &c.Active, &c.Proposed, &c.Total)
	sc.Counts = &c
	sc.Tags = []string{}
	return sc, err
}

func validPolicy(p string) bool { return p == "strict" || p == "balanced" || p == "open" }

type ProjectInput struct {
	Slug         string   `json:"slug"`
	Name         string   `json:"name"`
	Description  string   `json:"description"`
	Hidden       bool     `json:"hidden"`
	ReviewPolicy string   `json:"review_policy"`
	Tags         []string `json:"tags"`
}

func (s *Store) CreateProject(ctx context.Context, ss auth.ScopeSet, in ProjectInput) (Scope, error) {
	if !ss.CanCreateProjects() {
		return Scope{}, fmt.Errorf("%w: creating projects needs a write grant on every project", ErrForbidden)
	}
	slug, err := NormalizeSlug(in.Slug)
	if err != nil {
		return Scope{}, err
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		name = slug
	}
	if len(name) > 200 || len(in.Description) > 4000 {
		return Scope{}, fmt.Errorf("%w: name or description too long", ErrInvalid)
	}
	policy := in.ReviewPolicy
	if policy == "" {
		policy = "balanced"
	}
	if !validPolicy(policy) {
		return Scope{}, fmt.Errorf("%w: review_policy must be strict, balanced or open", ErrInvalid)
	}
	id := newID()
	ts := now()
	err = s.write(ctx, func(w wx) error {
		if _, err := w.exec(qScopeInsert, id, slug, name, strings.TrimSpace(in.Description), b2i(in.Hidden), policy, ts, ts); err != nil {
			return err
		}
		allowed := ids([]string{id})
		for _, t := range normTags(in.Tags) {
			if _, err := w.exec(qScopeTagInsert, id, t, allowed); err != nil {
				return err
			}
		}
		return nil
	})
	if isUniqueViolation(err) {
		return Scope{}, fmt.Errorf("%w: project %q already exists", ErrConflict, slug)
	}
	if err != nil {
		return Scope{}, err
	}
	s.bumpAuth()
	return s.scopeByID(ctx, id, []string{id})
}

func (s *Store) scopeByID(ctx context.Context, id string, allowed []string) (Scope, error) {
	sc, err := scanScope(s.reader(ctx).row(qScopeGet, id, ids(allowed)))
	if err != nil {
		return sc, notFound(err)
	}
	tags, err := s.scopeTags(ctx, []string{id})
	if err != nil {
		return sc, err
	}
	if t := tags[id]; t != nil {
		sc.Tags = t
	}
	return sc, nil
}

func (s *Store) scopeTags(ctx context.Context, scopeIDs []string) (map[string][]string, error) {
	rows, err := s.reader(ctx).query(qScopeTags, ids(scopeIDs))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]string{}
	for rows.Next() {
		var id, t string
		if err := rows.Scan(&id, &t); err != nil {
			return nil, err
		}
		out[id] = append(out[id], t)
	}
	return out, rows.Err()
}

// ListScopes lists the global scope and every project the principal holds a grant on.
func (s *Store) ListScopes(ctx context.Context, ss auth.ScopeSet) ([]Scope, error) {
	granted := ss.GrantedIDs()
	list, err := collectRows(s.reader(ctx).q(qScopeList, ids(granted)), scanScope)
	if err != nil {
		return nil, err
	}
	tags, err := s.scopeTags(ctx, granted)
	if err != nil {
		return nil, err
	}
	for i := range list {
		if t := tags[list[i].ID]; t != nil {
			list[i].Tags = t
		}
	}
	return list, nil
}

// GetScope returns a scope the principal holds a grant on.
func (s *Store) GetScope(ctx context.Context, ss auth.ScopeSet, id string) (Scope, error) {
	return s.scopeByID(ctx, id, ss.GrantedIDs())
}

// UpdateScope edits a project. Changing the review policy needs approve rights.
func (s *Store) UpdateScope(ctx context.Context, ss auth.ScopeSet, id string, in ProjectInput) (Scope, error) {
	cur, err := s.GetScope(ctx, ss, id)
	if err != nil {
		return cur, err
	}
	if !ss.CanWrite(id) {
		return cur, ErrNotFound
	}
	if cur.Kind == "global" {
		err = s.write(ctx, func(w wx) error {
			_, err := w.exec(qScopeUpdateGlobal, strings.TrimSpace(in.Description), now(), id, ids([]string{id}))
			return err
		})
		if err != nil {
			return cur, err
		}
		return s.GetScope(ctx, ss, id)
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		name = cur.Name
	}
	policy := in.ReviewPolicy
	if policy == "" {
		policy = cur.ReviewPolicy
	}
	if !validPolicy(policy) {
		return cur, fmt.Errorf("%w: review_policy must be strict, balanced or open", ErrInvalid)
	}
	if policy != cur.ReviewPolicy && !ss.CanApprove(id) {
		return cur, fmt.Errorf("%w: changing the review policy needs approve rights", ErrForbidden)
	}
	allowed := ids([]string{id})
	err = s.write(ctx, func(w wx) error {
		n, err := w.execN(qScopeUpdate, name, strings.TrimSpace(in.Description), b2i(in.Hidden), policy, now(), id, allowed)
		if err != nil {
			return err
		}
		if n == 0 {
			return ErrNotFound
		}
		if in.Tags != nil {
			if _, err := w.exec(qScopeTagsDelete, id, allowed); err != nil {
				return err
			}
			for _, t := range normTags(in.Tags) {
				if _, err := w.exec(qScopeTagInsert, id, t, allowed); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		return cur, err
	}
	s.bumpAuth()
	return s.GetScope(ctx, ss, id)
}

// SoftDeleteScope hides a project. The caller takes a backup first.
func (s *Store) SoftDeleteScope(ctx context.Context, ss auth.ScopeSet, id string) error {
	if !ss.CanApprove(id) {
		return ErrNotFound
	}
	ts := now()
	var n int64
	err := s.write(ctx, func(w wx) (err error) {
		n, err = w.execN(qScopeSoftDelete, ts, ts, ts, id, ids([]string{id}))
		return err
	})
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	s.bumpAuth()
	return nil
}

// Aliases -----------------------------------------------------------------------

// NormalizeAlias canonicalizes git remotes and paths so the same repository maps
// to one alias regardless of transport.
func NormalizeAlias(kind, v string) string {
	v = strings.TrimSpace(v)
	switch kind {
	case "git_remote":
		v = strings.TrimSuffix(strings.TrimSuffix(v, "/"), ".git")
		for _, p := range []string{"https://", "http://", "ssh://", "git://"} {
			v = strings.TrimPrefix(v, p)
		}
		if i := strings.Index(v, "@"); i >= 0 && i < strings.IndexAny(v+":/", ":/") {
			v = v[i+1:]
		}
		v = strings.Replace(v, ":", "/", 1)
		return "git:" + strings.ToLower(v)
	case "path":
		v = strings.ReplaceAll(v, "\\", "/")
		return "path:" + strings.ToLower(strings.TrimSuffix(v, "/"))
	default:
		return "name:" + strings.ToLower(v)
	}
}

// AliasKind guesses the alias kind from a free-form hint.
func AliasKind(hint string) string {
	h := strings.TrimSpace(hint)
	switch {
	case strings.Contains(h, "://") || strings.HasPrefix(h, "git@") || strings.HasSuffix(h, ".git"):
		return "git_remote"
	case strings.ContainsAny(h, "/\\"):
		return "path"
	}
	return "name"
}

func (s *Store) ListAliases(ctx context.Context, ss auth.ScopeSet, scopeID string) ([]Alias, error) {
	if !ss.CanRead(scopeID) {
		return nil, ErrNotFound
	}
	return collectRows(s.reader(ctx).q(qAliasList, ids([]string{scopeID})), func(r rowScanner) (Alias, error) {
		var a Alias
		return a, r.Scan(&a.Alias, &a.ScopeID, &a.Kind, &a.CreatedAt)
	})
}

func (s *Store) AddAlias(ctx context.Context, ss auth.ScopeSet, scopeID, kind, value string) (Alias, error) {
	if !ss.CanWrite(scopeID) {
		return Alias{}, ErrNotFound
	}
	if kind == "" {
		kind = AliasKind(value)
	}
	if kind != "git_remote" && kind != "path" && kind != "name" {
		return Alias{}, fmt.Errorf("%w: alias kind must be git_remote, path or name", ErrInvalid)
	}
	if strings.TrimSpace(value) == "" || len(value) > 500 {
		return Alias{}, fmt.Errorf("%w: alias value required", ErrInvalid)
	}
	a := Alias{Alias: NormalizeAlias(kind, value), ScopeID: scopeID, Kind: kind, CreatedAt: now()}
	err := s.write(ctx, func(w wx) error {
		n, err := w.execN(qAliasInsert, a.Alias, scopeID, kind, a.CreatedAt, writable(ss))
		if err == nil && n == 0 {
			err = ErrNotFound
		}
		return err
	})
	if isUniqueViolation(err) {
		return a, fmt.Errorf("%w: alias already mapped", ErrConflict)
	}
	if isAbort(err, "aliases only point to project scopes") {
		return a, fmt.Errorf("%w: aliases only point to projects", ErrInvalid)
	}
	return a, err
}

func (s *Store) DeleteAlias(ctx context.Context, ss auth.ScopeSet, scopeID, alias string) error {
	if !ss.CanWrite(scopeID) {
		return ErrNotFound
	}
	var n int64
	err := s.write(ctx, func(w wx) (err error) {
		n, err = w.execN(qAliasDelete, alias, ids([]string{scopeID}))
		return err
	})
	if err == nil && n == 0 {
		return ErrNotFound
	}
	return err
}

// LookupAlias resolves a hint to a project slug. The caller still has to pass
// the slug through auth.Resolve, which decides whether it is granted.
func (s *Store) LookupAlias(ctx context.Context, hint string) (string, bool, error) {
	hint = strings.TrimSpace(hint)
	if hint == "" {
		return "", false, nil
	}
	kinds := []string{AliasKind(hint)}
	if kinds[0] != "name" {
		kinds = append(kinds, "name")
	}
	for _, k := range kinds {
		val := hint
		if k == "name" && kinds[0] != "name" {
			val = lastSegment(hint)
		}
		var sid, slug string
		err := s.reader(ctx).row(qAliasLookup, NormalizeAlias(k, val)).Scan(&sid, &slug)
		if err == nil {
			return slug, true, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return "", false, err
		}
	}
	return "", false, nil
}

func lastSegment(h string) string {
	h = strings.TrimSuffix(strings.TrimSuffix(strings.ReplaceAll(h, "\\", "/"), "/"), ".git")
	if i := strings.LastIndexAny(h, "/:"); i >= 0 {
		return h[i+1:]
	}
	return h
}

func normTags(tags []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, t := range tags {
		t = strings.ToLower(strings.TrimSpace(t))
		if t == "" || len(t) > 64 || seen[t] {
			continue
		}
		seen[t] = true
		out = append(out, t)
	}
	if len(out) > 32 {
		out = out[:32]
	}
	return out
}
