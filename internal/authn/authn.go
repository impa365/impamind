// Package authn turns request credentials into a principal and resolves the
// per-request ScopeSet. Every call re-checks the credential; nothing is
// inherited from a transport session.
package authn

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"impamind/internal/auth"
	"impamind/internal/store"
)

const (
	CookieName    = "impamind_session"
	ProjectHeader = "X-Impamind-Project"
	OAuthPrefix   = "impo_"
)

var (
	ErrNoCredential = errors.New("missing credential")
	ErrBadCredential = errors.New("invalid or revoked credential")
)

// OAuthVerifier resolves OAuth access tokens to a principal id. Set by the OAuth package.
type OAuthVerifier func(ctx context.Context, token string) (principalID string, expires time.Time, err error)

type Authenticator struct {
	st     *store.Store
	secret []byte
	OAuth  OAuthVerifier

	mu    sync.Mutex
	cache map[string]cached
	touch map[string]time.Time
}

type cached struct {
	id      store.Identity
	version int64
	at      time.Time
}

func New(st *store.Store, secret []byte) *Authenticator {
	return &Authenticator{st: st, secret: secret, cache: map[string]cached{}, touch: map[string]time.Time{}}
}

// Caller is the authenticated principal of one request.
type Caller struct {
	Identity store.Identity
	Via      string // token, cookie, oauth
	Expires  time.Time
}

func (c *Caller) Principal() auth.Principal { return c.Identity.Principal }

// Bearer extracts a bearer token from the Authorization header.
func Bearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if len(h) > 7 && strings.EqualFold(h[:7], "bearer ") {
		return strings.TrimSpace(h[7:])
	}
	return ""
}

// VerifyToken resolves an API or OAuth bearer token.
func (a *Authenticator) VerifyToken(ctx context.Context, token string) (*Caller, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return nil, ErrNoCredential
	}
	if len(token) > 512 {
		return nil, ErrBadCredential
	}
	if strings.HasPrefix(token, OAuthPrefix) {
		if a.OAuth == nil {
			return nil, ErrBadCredential
		}
		pid, exp, err := a.OAuth(ctx, token)
		if err != nil {
			return nil, ErrBadCredential
		}
		id, err := a.st.IdentityByPrincipal(ctx, pid)
		if err != nil {
			return nil, ErrBadCredential
		}
		return &Caller{Identity: id, Via: "oauth", Expires: exp}, nil
	}
	hash := auth.HashToken(token)
	ver := a.st.AuthVersion()
	a.mu.Lock()
	c, ok := a.cache[hash]
	a.mu.Unlock()
	if ok && c.version == ver && time.Since(c.at) < 5*time.Minute {
		a.markUsed(c.id.TokenID)
		return &Caller{Identity: c.id, Via: "token"}, nil
	}
	id, err := a.st.IdentityByTokenHash(ctx, hash)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			a.mu.Lock()
			delete(a.cache, hash)
			a.mu.Unlock()
			return nil, ErrBadCredential
		}
		return nil, err
	}
	a.mu.Lock()
	if len(a.cache) > 10000 {
		a.cache = map[string]cached{}
	}
	a.cache[hash] = cached{id: id, version: ver, at: time.Now()}
	a.mu.Unlock()
	a.markUsed(id.TokenID)
	return &Caller{Identity: id, Via: "token"}, nil
}

// markUsed updates last_used_at at most once a minute per token, off the request path.
func (a *Authenticator) markUsed(tokenID string) {
	if tokenID == "" {
		return
	}
	a.mu.Lock()
	last := a.touch[tokenID]
	due := time.Since(last) > time.Minute
	if due {
		a.touch[tokenID] = time.Now()
	}
	a.mu.Unlock()
	if due {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = a.st.TouchToken(ctx, tokenID)
		}()
	}
}

// SessionCookie verifies the signed admin cookie.
func (a *Authenticator) SessionCookie(ctx context.Context, r *http.Request) (*Caller, error) {
	ck, err := r.Cookie(CookieName)
	if err != nil || ck.Value == "" {
		return nil, ErrNoCredential
	}
	sid, ok := auth.Verify(a.secret, ck.Value)
	if !ok {
		return nil, ErrBadCredential
	}
	pid, err := a.st.WebSessionPrincipal(ctx, sid)
	if err != nil {
		return nil, ErrBadCredential
	}
	id, err := a.st.IdentityByPrincipal(ctx, pid)
	if err != nil || id.Principal.Class != auth.AdminSession {
		return nil, ErrBadCredential
	}
	return &Caller{Identity: id, Via: "cookie"}, nil
}

// SessionID returns the web session id carried by a valid cookie.
func (a *Authenticator) SessionID(r *http.Request) string {
	ck, err := r.Cookie(CookieName)
	if err != nil {
		return ""
	}
	sid, ok := auth.Verify(a.secret, ck.Value)
	if !ok {
		return ""
	}
	return sid
}

// SignSession returns the cookie value for a web session id.
func (a *Authenticator) SignSession(sid string) string { return auth.Sign(a.secret, sid) }

// Request authenticates an HTTP request: bearer first, then the admin cookie.
func (a *Authenticator) Request(r *http.Request) (*Caller, error) {
	if tok := Bearer(r); tok != "" {
		return a.VerifyToken(r.Context(), tok)
	}
	return a.SessionCookie(r.Context(), r)
}

// Scope resolves the ScopeSet for one call. header and arg are project slugs;
// they only narrow what the grants allow.
func (a *Authenticator) Scope(ctx context.Context, c *Caller, header, arg string, all bool) (auth.ScopeSet, error) {
	global, projects, err := a.st.ScopeRefs(ctx)
	if err != nil {
		return auth.ScopeSet{}, err
	}
	if strings.EqualFold(strings.TrimSpace(arg), "global") {
		arg = ""
	}
	if strings.EqualFold(strings.TrimSpace(header), "global") {
		header = ""
	}
	return auth.Resolve(c.Identity.Principal, c.Identity.Grants, global, projects, auth.Request{Header: header, Arg: arg, AllProjects: all})
}

// Store exposes the underlying store for packages that hold an Authenticator.
func (a *Authenticator) Store() *store.Store { return a.st }
