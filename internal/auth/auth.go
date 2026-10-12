// Package auth resolves who is calling and which scopes that caller may touch.
// It has no database access: callers load the principal and its grants and pass
// them to Resolve, which is the only constructor of ScopeSet.
package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"sort"
	"strings"
)

type Class string

const (
	AgentToken   Class = "agent_token"
	HumanToken   Class = "human_token"
	AdminSession Class = "admin_session"
	OAuthClient  Class = "oauth_client"
)

// IsHuman reports whether the class can approve protected memories.
func (c Class) IsHuman() bool { return c == HumanToken || c == AdminSession }

func (c Class) Valid() bool {
	switch c {
	case AgentToken, HumanToken, AdminSession, OAuthClient:
		return true
	}
	return false
}

type Principal struct {
	ID    string
	Class Class
	Label string
}

// Wildcard grants every project scope. It never covers the global scope.
const Wildcard = "*"

type Grant struct {
	ScopeID      string
	CanRead      bool
	CanWrite     bool
	CanPropose   bool
	CanApprove   bool
	CrossProject bool
}

type ScopeRef struct {
	ID   string
	Slug string
	Kind string
}

var (
	ErrNoProject       = errors.New("project not found or not granted")
	ErrProjectConflict = errors.New("project header and argument disagree")
	ErrForbidden       = errors.New("operation not permitted for this principal")
)

// Request is what the caller asked for. Header and Arg are project slugs.
type Request struct {
	Header      string
	Arg         string
	AllProjects bool
}

type perm uint8

const (
	pRead perm = 1 << iota
	pWrite
	pPropose
	pApprove
)

// ScopeSet is the resolved, per-request authorization. Its fields are unexported
// so it can only be produced by Resolve.
type ScopeSet struct {
	principal Principal
	global    string
	project   ScopeRef
	perms     map[string]perm
	readIDs   []string
	granted   []string
	create    bool
	cross     bool
	resolved  bool
}

// GrantedIDs lists every scope the principal holds any grant on. It is used to
// list scope names, never to read memories.
func (s ScopeSet) GrantedIDs() []string {
	if !s.resolved {
		return nil
	}
	out := make([]string, len(s.granted))
	copy(out, s.granted)
	return out
}

// CanCreateProjects is true for principals with a write grant on every project.
func (s ScopeSet) CanCreateProjects() bool { return s.resolved && s.create }

// CrossProject reports whether the principal may ask for all_projects reads.
func (s ScopeSet) CrossProject() bool { return s.resolved && s.cross }

// IsAdmin is true for the UI admin session.
func (s ScopeSet) IsAdmin() bool { return s.resolved && s.principal.Class == AdminSession }

func (s ScopeSet) Principal() Principal { return s.principal }
func (s ScopeSet) GlobalID() string     { return s.global }
func (s ScopeSet) Project() ScopeRef    { return s.project }
func (s ScopeSet) HasProject() bool     { return s.project.ID != "" }

// ReadIDs returns the scope IDs readable in this request, sorted. An unresolved
// (zero) ScopeSet returns none.
func (s ScopeSet) ReadIDs() []string {
	if !s.resolved {
		return nil
	}
	out := make([]string, len(s.readIDs))
	copy(out, s.readIDs)
	return out
}

// WritableIDs returns the scope IDs where the principal may write or propose.
func (s ScopeSet) WritableIDs() []string {
	if !s.resolved {
		return nil
	}
	var out []string
	for id, v := range s.perms {
		if v&(pWrite|pPropose) != 0 {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}

// ApprovableIDs returns the scope IDs where the principal may approve.
func (s ScopeSet) ApprovableIDs() []string {
	if !s.resolved || !s.principal.Class.IsHuman() {
		return nil
	}
	var out []string
	for id, v := range s.perms {
		if v&pApprove != 0 {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}

func (s ScopeSet) has(scopeID string, p perm) bool {
	if !s.resolved || scopeID == "" {
		return false
	}
	return s.perms[scopeID]&p != 0
}

func (s ScopeSet) CanRead(scopeID string) bool    { return s.has(scopeID, pRead) }
func (s ScopeSet) CanWrite(scopeID string) bool   { return s.has(scopeID, pWrite) }
func (s ScopeSet) CanPropose(scopeID string) bool { return s.has(scopeID, pPropose|pWrite) }

// CanApprove is false for every non-human principal regardless of grants.
func (s ScopeSet) CanApprove(scopeID string) bool {
	return s.principal.Class.IsHuman() && s.has(scopeID, pApprove)
}

// Resolve computes the ScopeSet for one request.
//
// Rules: header and argument may only narrow what the grants allow; if both are
// given they must agree; a principal granted exactly one project is pinned to it;
// AllProjects needs a cross_project grant and widens reads to every granted project.
func Resolve(p Principal, grants []Grant, global ScopeRef, projects []ScopeRef, req Request) (ScopeSet, error) {
	if p.ID == "" || !p.Class.Valid() {
		return ScopeSet{}, ErrForbidden
	}
	h, a := strings.TrimSpace(req.Header), strings.TrimSpace(req.Arg)
	if h != "" && a != "" && !strings.EqualFold(h, a) {
		return ScopeSet{}, ErrProjectConflict
	}
	want := a
	if want == "" {
		want = h
	}

	bySlug := make(map[string]ScopeRef, len(projects))
	byID := make(map[string]ScopeRef, len(projects))
	for _, pr := range projects {
		if pr.Kind != "project" {
			continue
		}
		bySlug[strings.ToLower(pr.Slug)] = pr
		byID[pr.ID] = pr
	}

	perms := map[string]perm{}
	var wildcard perm
	var explicit []string
	cross := false
	for _, g := range grants {
		var v perm
		if g.CanRead {
			v |= pRead
		}
		if g.CanWrite {
			v |= pWrite | pRead
		}
		if g.CanPropose {
			v |= pPropose
		}
		if g.CanApprove && p.Class.IsHuman() {
			v |= pApprove
		}
		if g.CrossProject {
			cross = true
		}
		switch {
		case g.ScopeID == Wildcard:
			wildcard |= v
		case g.ScopeID == global.ID:
			perms[global.ID] |= v
		default:
			if _, ok := byID[g.ScopeID]; ok {
				perms[g.ScopeID] |= v
				explicit = append(explicit, g.ScopeID)
			}
		}
	}
	projectPerm := func(id string) perm { return perms[id] | wildcard }

	var chosen ScopeRef
	switch {
	case want != "":
		pr, ok := bySlug[strings.ToLower(want)]
		if !ok || projectPerm(pr.ID) == 0 {
			return ScopeSet{}, ErrNoProject
		}
		if wildcard == 0 && len(explicit) == 1 && explicit[0] != pr.ID {
			return ScopeSet{}, ErrNoProject
		}
		chosen = pr
	case wildcard == 0 && len(explicit) == 1:
		chosen = byID[explicit[0]]
	}

	out := ScopeSet{principal: p, global: global.ID, project: chosen, perms: map[string]perm{}, resolved: true,
		create: wildcard&pWrite != 0, cross: cross}
	if perms[global.ID] != 0 {
		out.granted = append(out.granted, global.ID)
	}
	for _, pr := range projects {
		if pr.Kind == "project" && projectPerm(pr.ID) != 0 {
			out.granted = append(out.granted, pr.ID)
		}
	}
	sort.Strings(out.granted)
	if v := perms[global.ID]; v != 0 {
		out.perms[global.ID] = v
	}
	if chosen.ID != "" {
		out.perms[chosen.ID] = projectPerm(chosen.ID)
	}
	if req.AllProjects {
		if !cross {
			return ScopeSet{}, ErrForbidden
		}
		for _, pr := range projects {
			if v := projectPerm(pr.ID); v != 0 && pr.Kind == "project" {
				out.perms[pr.ID] = v
			}
		}
	}
	for id, v := range out.perms {
		if v&pRead != 0 {
			out.readIDs = append(out.readIDs, id)
		}
	}
	sort.Strings(out.readIDs)
	return out, nil
}

// WithProject re-resolves the same request pinned to a project slug. It is used
// when the project is discovered from an alias after the first resolution.
func WithProject(p Principal, grants []Grant, global ScopeRef, projects []ScopeRef, slug string) (ScopeSet, error) {
	return Resolve(p, grants, global, projects, Request{Arg: slug})
}

// Token helpers ---------------------------------------------------------------

const TokenPrefix = "imp_"

// NewToken returns a random bearer token and its storage hash.
func NewToken() (token, hash string) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	token = TokenPrefix + base64.RawURLEncoding.EncodeToString(b)
	return token, HashToken(token)
}

func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func RandomID(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// Sign and Verify implement the HMAC used on web session cookies.
func Sign(secret []byte, value string) string {
	m := hmac.New(sha256.New, secret)
	m.Write([]byte(value))
	return value + "." + base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

func Verify(secret []byte, signed string) (string, bool) {
	i := strings.LastIndexByte(signed, '.')
	if i <= 0 {
		return "", false
	}
	value := signed[:i]
	if !hmac.Equal([]byte(Sign(secret, value)), []byte(signed)) {
		return "", false
	}
	return value, true
}
