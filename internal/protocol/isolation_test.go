package protocol

import (
	"context"
	"errors"
	"math/rand/v2"
	"net/http"
	"path/filepath"
	"testing"

	"impamind/internal/auth"
	"impamind/internal/config"
	"impamind/internal/store"
)

func TestA1TokenCannotSeeOtherProject(t *testing.T) {
	ctx := context.Background()
	st, svc := newSvc(t)
	admin := adminSS(t, st, true)
	alpha := newProject(t, st, admin, "alpha")
	beta := newProject(t, st, admin, "beta")
	admin = adminSS(t, st, true)
	secret := memory(t, st, admin, alpha.ID, "alpha secret beacon")
	visible := memory(t, st, admin, beta.ID, "beta visible beacon")
	globalMem := proposeGlobal(t, st, admin, "global beacon preference")
	tok := token(t, st, "beta-agent", []store.Grant{
		{ScopeID: beta.ID, CanRead: true, CanWrite: true, CanPropose: true},
		{ScopeID: admin.GlobalID(), CanRead: true, CanPropose: true},
	})
	ss := mustResolve(t, st, tok, "")
	if ss.Project().Slug != "beta" {
		t.Fatalf("pinned to %s", ss.Project().Slug)
	}
	if _, err := st.GetMemory(ctx, ss, secret.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("read alpha: %v", err)
	}
	if _, err := st.History(ctx, ss, secret.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("history alpha: %v", err)
	}
	for _, text := range []string{"beacon", "alpha secret", "beta visible"} {
		hits, err := st.SearchFTS(ctx, ss, text, store.SearchFilter{})
		if err != nil {
			t.Fatal(err)
		}
		for _, h := range hits {
			if h.Memory.ID == secret.ID || h.Memory.ScopeID == alpha.ID {
				t.Fatalf("search %q returned alpha %s", text, h.Memory.Title)
			}
		}
	}
	g, err := st.Graph(ctx, ss, nil, false, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range g.Nodes {
		if n.ID == secret.ID || n.ScopeID == alpha.ID {
			t.Fatal("graph contains alpha")
		}
	}
	links, err := st.LinksFor(ctx, ss, []string{secret.ID, visible.ID, globalMem.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(links) != 0 {
		t.Fatalf("links leaked: %+v", links)
	}
	items, err := st.ListReview(ctx, ss, "", "open", 50)
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range items {
		if it.ScopeID == alpha.ID {
			t.Fatal("review leaked alpha")
		}
	}
	out, err := svc.Recall(ctx, call(ss), RecallIn{Task: "beacon check"})
	if err != nil {
		t.Fatal(err)
	}
	seenGlobal := false
	for _, it := range append(append(append(out.Global, out.Decisions...), out.Relevant...), out.Pending...) {
		if it.ID == secret.ID {
			t.Fatal("recall returned the alpha memory")
		}
		if it.ID == globalMem.ID {
			seenGlobal = true
		}
	}
	if !seenGlobal {
		t.Fatal("recall missed the global memory")
	}
	res, err := svc.Search(ctx, call(ss), SearchIn{Query: "beacon"})
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range res.Items {
		if it.ID == secret.ID || it.Scope == "alpha" {
			t.Fatalf("protocol search leaked %+v", it)
		}
	}
}

func TestA2NoCrossProjectLeak(t *testing.T) {
	ctx := context.Background()
	st, svc := newSvc(t)
	admin := adminSS(t, st, false)
	const n = 20
	type proj struct {
		sc store.Scope
		ss auth.ScopeSet
	}
	ps := make([]proj, n)
	ids := map[string]string{}
	for i := 0; i < n; i++ {
		slug := "p" + string(rune('a'+i/26)) + string(rune('a'+i%26))
		sc := newProject(t, st, admin, slug)
		ps[i].sc = sc
	}
	admin = adminSS(t, st, true)
	for i := range ps {
		for k := 0; k < 3; k++ {
			title := "lighthouse protocol " + ps[i].sc.Slug + " fact"
			m := memory(t, st, admin, ps[i].sc.ID, title)
			ids[m.ID] = ps[i].sc.ID
		}
		tok := token(t, st, "agent-"+ps[i].sc.Slug, []store.Grant{
			{ScopeID: ps[i].sc.ID, CanRead: true, CanWrite: true, CanPropose: true},
			{ScopeID: admin.GlobalID(), CanRead: true},
		})
		ps[i].ss = mustResolve(t, st, tok, "")
	}
	rng := rand.New(rand.NewPCG(1, 2))
	sessions := map[string]string{}
	for i := 0; i < 1000; i++ {
		p := ps[rng.IntN(n)]
		if i%2 == 0 {
			hits, err := st.SearchFTS(ctx, p.ss, "lighthouse", store.SearchFilter{})
			if err != nil {
				t.Fatal(err)
			}
			for _, h := range hits {
				if ids[h.Memory.ID] != p.sc.ID && h.Memory.ScopeID != p.ss.GlobalID() {
					t.Fatalf("search leak %s into %s", h.Memory.ID, p.sc.Slug)
				}
			}
			continue
		}
		in := RecallIn{Task: "lighthouse protocol", SessionID: sessions[p.sc.ID]}
		out, err := svc.Recall(ctx, call(p.ss), in)
		if err != nil {
			t.Fatal(err)
		}
		if out.Project == nil || out.Project.Slug != p.sc.Slug {
			t.Fatalf("recall project %+v", out.Project)
		}
		sessions[p.sc.ID] = out.SessionID
		for _, it := range append(append(out.Global, out.Decisions...), out.Relevant...) {
			if it.Scope != "global" && it.Scope != p.sc.Slug {
				t.Fatalf("recall leak scope %s into %s", it.Scope, p.sc.Slug)
			}
		}
	}
}

func TestA5PinnedProjectRejectsOther(t *testing.T) {
	st, _ := newSvc(t)
	admin := adminSS(t, st, false)
	alpha := newProject(t, st, admin, "alpha")
	newProject(t, st, admin, "beta")
	tok := token(t, st, "alpha-only", []store.Grant{{ScopeID: alpha.ID, CanRead: true, CanWrite: true}, {ScopeID: adminSS(t, st, false).GlobalID(), CanRead: true}})
	if _, err := resolve(t, st, tok, "beta"); !errors.Is(err, auth.ErrNoProject) {
		t.Fatalf("pinned token accepted beta: %v", err)
	}
	ss := mustResolve(t, st, tok, "alpha")
	if ss.Project().Slug != "alpha" || ss.CanRead(projectID(t, st, "beta")) {
		t.Fatal("pin did not stick")
	}
}

func TestA6AndSessions(t *testing.T) {
	ctx := context.Background()
	st, svc := newSvc(t)
	admin := adminSS(t, st, false)
	alpha := newProject(t, st, admin, "alpha")
	beta := newProject(t, st, admin, "beta")
	g, _, err := st.ScopeRefs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	both := token(t, st, "both", []store.Grant{
		{ScopeID: alpha.ID, CanRead: true, CanWrite: true, CanPropose: true},
		{ScopeID: beta.ID, CanRead: true, CanWrite: true, CanPropose: true},
		{ScopeID: g.ID, CanRead: true},
	})
	other := token(t, st, "other", []store.Grant{
		{ScopeID: alpha.ID, CanRead: true, CanWrite: true, CanPropose: true},
		{ScopeID: g.ID, CanRead: true},
	})
	ssA := mustResolve(t, st, both, "alpha")
	sess, err := st.StartSession(ctx, ssA, store.NewSession{Client: "test", Task: "migrate the queue"})
	if err != nil {
		t.Fatal(err)
	}
	ssB := mustResolve(t, st, both, "beta")
	_, err = svc.Checkpoint(ctx, call(ssB), CheckpointIn{SessionID: sess.ID, Task: "continue on beta"})
	if statusOf(err) != http.StatusNotFound {
		t.Fatalf("A6 checkpoint on other project: %v", err)
	}
	ssOther := mustResolve(t, st, other, "alpha")
	before, err := st.GetSession(ctx, ssA, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = svc.Checkpoint(ctx, call(ssOther), CheckpointIn{SessionID: sess.ID, Task: "steal the session"})
	if statusOf(err) != http.StatusNotFound {
		t.Fatalf("S1 other token: %v", err)
	}
	after, err := st.GetSession(ctx, ssA, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Writes != before.Writes || after.Checkpoints != before.Checkpoints {
		t.Fatalf("S1 wrote anyway: %+v", after)
	}
	if _, err := st.SessionForWrite(ctx, ssA, store.NewID()); statusOf(err) != http.StatusNotFound && !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("S4: %v", err)
	}
	if err := st.CloseSession(ctx, ssA, sess.ID, false); err != nil {
		t.Fatal(err)
	}
	_, err = svc.Checkpoint(ctx, call(ssA), CheckpointIn{SessionID: sess.ID, Task: "after close"})
	if statusOf(err) != http.StatusConflict {
		t.Fatalf("S3: %v", err)
	}
	closed, err := st.GetSession(ctx, ssA, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if closed.Status != "closed" {
		t.Fatalf("session reopened: %s", closed.Status)
	}
	if err := st.UpdateSessionTask(ctx, ssA, sess.ID, "nope", nil, "active"); !errors.Is(err, store.ErrClosed) {
		t.Fatalf("reopen via update: %v", err)
	}
}

func TestA7SoftDeleteKeepsOthers(t *testing.T) {
	ctx := context.Background()
	st, svc := newSvc(t)
	admin := adminSS(t, st, true)
	alpha := newProject(t, st, admin, "alpha")
	beta := newProject(t, st, admin, "beta")
	admin = adminSS(t, st, true)
	a := memory(t, st, admin, alpha.ID, "alpha stays in the backup")
	b := memory(t, st, admin, beta.ID, "beta stays live")
	g := proposeGlobal(t, st, admin, "global stays live")
	backup, err := svc.DeleteProject(ctx, Call{SS: admin, Profile: ProfileUI}, alpha.ID)
	if err != nil {
		t.Fatal(err)
	}
	live := adminSS(t, st, true)
	if _, err := st.GetMemory(ctx, live, b.ID); err != nil {
		t.Fatalf("beta after delete: %v", err)
	}
	if _, err := st.GetMemory(ctx, live, g.ID); err != nil {
		t.Fatalf("global after delete: %v", err)
	}
	if _, err := st.GetScope(ctx, live, alpha.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("deleted project still listed: %v", err)
	}
	bak, err := store.Open(ctx, backup)
	if err != nil {
		t.Fatal(err)
	}
	defer bak.Close()
	old := adminSS(t, bak, true)
	got, err := bak.GetMemory(ctx, old, a.ID)
	if err != nil {
		t.Fatalf("backup missing alpha: %v", err)
	}
	if got.Content != a.Content || got.ScopeID != alpha.ID {
		t.Fatalf("backup memory changed: %+v", got)
	}
}

func TestAgentProfileCannotApprove(t *testing.T) {
	ctx := context.Background()
	st, svc := newSvc(t)
	admin := adminSS(t, st, false)
	mem, err := st.CreateMemory(ctx, admin, store.NewMemory{
		ScopeID: admin.GlobalID(), Kind: "preference", Title: "needs a human", Content: "needs a human before it applies anywhere",
		Status: "proposed", SourceType: "human_ui", Tier: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	items, err := st.ListReview(ctx, admin, "proposed", "open", 10)
	if err != nil || len(items) == 0 {
		t.Fatalf("review item: %v %+v", err, items)
	}
	tok := token(t, st, "agent", []store.Grant{{ScopeID: admin.GlobalID(), CanRead: true, CanPropose: true}})
	ss := mustResolve(t, st, tok, "")
	_, err = svc.ResolveReview(ctx, Call{SS: ss, Profile: ProfileAgent}, ResolveIn{ItemID: items[0].ID, Action: "approve"})
	if statusOf(err) != http.StatusForbidden {
		t.Fatalf("agent approve: %v", err)
	}
	got, err := st.GetMemory(ctx, admin, mem.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "proposed" {
		t.Fatalf("status %s", got.Status)
	}
}

func newSvc(t *testing.T) (*store.Store, *Service) {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	cfg := config.Config{BackupDir: t.TempDir(), ObservationBudget: 15, MCPVersions: []string{"2025-11-25"}}
	return st, New(st, cfg, nil)
}

func adminSS(t *testing.T, st *store.Store, all bool) auth.ScopeSet {
	t.Helper()
	id, err := st.AdminIdentity(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	g, projects, err := st.ScopeRefs(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ss, err := auth.Resolve(id.Principal, id.Grants, g, projects, auth.Request{AllProjects: all})
	if err != nil {
		t.Fatal(err)
	}
	return ss
}

func newProject(t *testing.T, st *store.Store, admin auth.ScopeSet, slug string) store.Scope {
	t.Helper()
	sc, err := st.CreateProject(context.Background(), admin, store.ProjectInput{Slug: slug, Name: slug, ReviewPolicy: "balanced"})
	if err != nil {
		t.Fatal(err)
	}
	return sc
}

func memory(t *testing.T, st *store.Store, ss auth.ScopeSet, scopeID, title string) store.Memory {
	t.Helper()
	m, err := st.CreateMemory(context.Background(), ss, store.NewMemory{
		ScopeID: scopeID, Kind: "note", Title: title, Content: title + " is durable knowledge worth keeping",
		Status: "active", SourceType: "agent_inferred", Tier: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func proposeGlobal(t *testing.T, st *store.Store, admin auth.ScopeSet, title string) store.Memory {
	t.Helper()
	m, err := st.CreateMemory(context.Background(), admin, store.NewMemory{
		ScopeID: admin.GlobalID(), Kind: "preference", Title: title, Content: title + " applies to every project",
		Status: "proposed", SourceType: "human_ui", Tier: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	m, err = st.Approve(context.Background(), admin, m.ID, store.ApproveInput{Note: "ok"})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func token(t *testing.T, st *store.Store, name string, grants []store.Grant) store.Identity {
	t.Helper()
	plain, _, err := st.CreateToken(context.Background(), name, "agent", "test", grants)
	if err != nil {
		t.Fatal(err)
	}
	id, err := st.IdentityByTokenHash(context.Background(), auth.HashToken(plain))
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func resolve(t *testing.T, st *store.Store, id store.Identity, project string) (auth.ScopeSet, error) {
	t.Helper()
	g, projects, err := st.ScopeRefs(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return auth.Resolve(id.Principal, id.Grants, g, projects, auth.Request{Arg: project})
}

func mustResolve(t *testing.T, st *store.Store, id store.Identity, project string) auth.ScopeSet {
	t.Helper()
	ss, err := resolve(t, st, id, project)
	if err != nil {
		t.Fatal(err)
	}
	return ss
}

func projectID(t *testing.T, st *store.Store, slug string) string {
	t.Helper()
	_, projects, err := st.ScopeRefs(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range projects {
		if p.Slug == slug {
			return p.ID
		}
	}
	t.Fatalf("no project %s", slug)
	return ""
}

func call(ss auth.ScopeSet) Call { return Call{SS: ss, Profile: ProfileAgent, Client: "test"} }

func statusOf(err error) int {
	if err == nil {
		return 200
	}
	return AsError(err).Status
}
