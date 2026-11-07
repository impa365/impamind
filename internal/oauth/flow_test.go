package oauth

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"impamind/internal/api"
	"impamind/internal/authn"
	"impamind/internal/config"
	"impamind/internal/protocol"
	"impamind/internal/store"
)

func TestAuthorizationCode(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	admin, err := st.AdminScope(ctx)
	if err != nil {
		t.Fatal(err)
	}
	proj, err := st.CreateProject(ctx, admin, store.ProjectInput{Slug: "queue", Name: "queue"})
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{PublicURL: "http://impamind.test", AdminPassword: "impamind-admin", MCPVersions: []string{"2025-11-25"}, ObservationBudget: 15}
	an := authn.New(st, []byte("test-secret"))
	oa := New(st, an, cfg, nil)
	an.OAuth = oa.Verify
	srv := api.New(api.Deps{Config: cfg, Store: st, Service: protocol.New(st, cfg, nil), Auth: an})
	oa.Mount(srv)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	jar, _ := cookiejar.New(nil)
	httpClient := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

	regBody := `{"client_name":"chat","redirect_uris":["http://127.0.0.1:9/callback"]}`
	res, err := httpClient.Post(ts.URL+"/oauth/register", "application/json", strings.NewReader(regBody))
	if err != nil {
		t.Fatal(err)
	}
	var reg struct {
		ClientID string `json:"client_id"`
	}
	if err := json.NewDecoder(res.Body).Decode(&reg); err != nil || reg.ClientID == "" || res.StatusCode != 201 {
		t.Fatalf("register %d %v", res.StatusCode, err)
	}
	res.Body.Close()
	meta, err := httpClient.Get(ts.URL + "/.well-known/oauth-authorization-server")
	if err != nil || meta.StatusCode != 200 {
		t.Fatalf("metadata %v", err)
	}
	meta.Body.Close()

	login, err := httpClient.Post(ts.URL+"/auth/login", "application/json", strings.NewReader(`{"password":"impamind-admin"}`))
	if err != nil || login.StatusCode != 200 {
		t.Fatalf("login %v %d", err, status(login))
	}
	login.Body.Close()

	verifier := strings.Repeat("b", 50)
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])
	form := url.Values{
		"client_id": {reg.ClientID}, "redirect_uri": {"http://127.0.0.1:9/callback"},
		"code_challenge": {challenge}, "code_challenge_method": {"S256"}, "state": {"xyz"}, "project": {proj.ID},
	}
	consent, err := httpClient.PostForm(ts.URL+"/oauth/authorize", form)
	if err != nil {
		t.Fatal(err)
	}
	loc := consent.Header.Get("Location")
	if consent.StatusCode != http.StatusFound || !strings.Contains(loc, "code=") || !strings.Contains(loc, "state=xyz") {
		b, _ := io.ReadAll(consent.Body)
		t.Fatalf("consent %d %s %s", consent.StatusCode, loc, b)
	}
	u, _ := url.Parse(loc)
	code := u.Query().Get("code")
	tokForm := url.Values{"grant_type": {"authorization_code"}, "code": {code}, "code_verifier": {verifier}, "client_id": {reg.ClientID}, "redirect_uri": {"http://127.0.0.1:9/callback"}}
	tokRes, err := httpClient.PostForm(ts.URL+"/oauth/token", tokForm)
	if err != nil {
		t.Fatal(err)
	}
	var issued struct {
		Access  string `json:"access_token"`
		Refresh string `json:"refresh_token"`
	}
	if err := json.NewDecoder(tokRes.Body).Decode(&issued); err != nil || !strings.HasPrefix(issued.Access, "impo_") {
		t.Fatalf("token %d %+v", tokRes.StatusCode, issued)
	}
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/scopes", nil)
	req.Header.Set("Authorization", "Bearer "+issued.Access)
	scopes, err := http.DefaultClient.Do(req)
	if err != nil || scopes.StatusCode != 200 {
		t.Fatalf("scopes %v %d", err, status(scopes))
	}
	body, _ := io.ReadAll(scopes.Body)
	if !strings.Contains(string(body), "queue") {
		t.Fatalf("scopes body %s", body)
	}
	ref := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {issued.Refresh}, "client_id": {reg.ClientID}}
	refRes, err := http.DefaultClient.PostForm(ts.URL+"/oauth/token", ref)
	if err != nil || refRes.StatusCode != 200 {
		t.Fatalf("refresh %v %d", err, status(refRes))
	}
	again, err := http.DefaultClient.PostForm(ts.URL+"/oauth/token", ref)
	if err != nil || again.StatusCode == 200 {
		t.Fatalf("reused refresh accepted: %d", status(again))
	}
}

func status(r *http.Response) int {
	if r == nil {
		return 0
	}
	return r.StatusCode
}
