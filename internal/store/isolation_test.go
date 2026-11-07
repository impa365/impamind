package store

import (
	"context"
	"errors"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"impamind/internal/auth"
)

func TestNoSQLOutsideStore(t *testing.T) {
	root := moduleRoot(t)
	fset := token.NewFileSet()
	var bad []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case "vendor", ".git", "bin", "modcache":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		rel := filepath.ToSlash(strings.TrimPrefix(path, root+string(filepath.Separator)))
		if strings.HasPrefix(rel, "internal/store/") {
			return nil
		}
		f, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		for _, imp := range f.Imports {
			p, _ := strconv.Unquote(imp.Path.Value)
			if p == "database/sql" || p == "modernc.org/sqlite" {
				bad = append(bad, rel+" imports "+p)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(bad) > 0 {
		t.Fatalf("SQL leaves internal/store:\n%s", strings.Join(bad, "\n"))
	}
}

func TestScopedQueries(t *testing.T) {
	n := 0
	for _, q := range Registry() {
		n++
		switch {
		case q.Scoped():
			if !strings.Contains(q.Raw, "{{") || !strings.Contains(q.SQL, "json_each(?)") {
				t.Errorf("query %s lost its scope predicate", q.Name)
			}
		case !SystemReasons[q.Reason]:
			t.Errorf("query %s has no scope predicate and reason %q", q.Name, q.Reason)
		case strings.Contains(q.Raw, "{{"):
			t.Errorf("system query %s still has a scope marker", q.Name)
		}
	}
	if n < 40 {
		t.Fatalf("only %d queries registered", n)
	}
}

func TestI2EmptyScopeSet(t *testing.T) {
	ctx := context.Background()
	st := openTest(t)
	admin := adminScope(t, st, true)
	proj := makeProject(t, st, admin, "alpha")
	admin = adminScope(t, st, true)
	mem := makeMemory(t, st, admin, proj.ID, "alpha secret beacon")
	var empty auth.ScopeSet
	if _, err := st.GetMemory(ctx, empty, mem.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("empty scope read: %v", err)
	}
	list, total, err := st.ListMemories(ctx, empty, MemoryFilter{Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	if total != 0 || len(list) != 0 {
		t.Fatalf("empty scope listed %d memories", total)
	}
	hits, err := st.SearchFTS(ctx, empty, "beacon", SearchFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 0 {
		t.Fatalf("empty scope search returned %d", len(hits))
	}
}

func TestA3LinkScopeRule(t *testing.T) {
	ctx := context.Background()
	st := openTest(t)
	admin := adminScope(t, st, true)
	alpha := makeProject(t, st, admin, "alpha")
	beta := makeProject(t, st, admin, "beta")
	admin = adminScope(t, st, true)
	a := makeMemory(t, st, admin, alpha.ID, "alpha architecture note")
	b := makeMemory(t, st, admin, beta.ID, "beta architecture note")
	gmem, err := st.CreateMemory(ctx, admin, NewMemory{ScopeID: admin.GlobalID(), Kind: "preference", Title: "global preference beacon", Content: "teams prefer table driven tests always", Status: "proposed", SourceType: "human_ui", Tier: 1})
	if err != nil {
		t.Fatal(err)
	}
	gmem, err = st.Approve(ctx, admin, gmem.ID, ApproveInput{Note: "ok"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateLink(ctx, admin, a.ID, b.ID, "related"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("link A->B: %v", err)
	}
	if _, err := st.CreateLink(ctx, admin, gmem.ID, a.ID, "related"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("link global->A: %v", err)
	}
	if _, err := st.CreateLink(ctx, admin, a.ID, gmem.ID, "related"); err != nil {
		t.Fatalf("link A->global: %v", err)
	}
}

func TestG2AgentCannotHoldApprove(t *testing.T) {
	ctx := context.Background()
	st := openTest(t)
	g, _, err := st.ScopeRefs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = st.CreateToken(ctx, "sneaky", "agent", "test", []Grant{{ScopeID: g.ID, CanRead: true, CanApprove: true}})
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("create agent can_approve: %v", err)
	}
	_, tok, err := st.CreateToken(ctx, "worker", "agent", "test", []Grant{{ScopeID: g.ID, CanRead: true, CanPropose: true}})
	if err != nil {
		t.Fatal(err)
	}
	err = st.SetGrants(ctx, tok.PrincipalID, []Grant{{ScopeID: g.ID, CanRead: true, CanApprove: true}})
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("update agent can_approve: %v", err)
	}
}

func openTest(t *testing.T) *Store {
	t.Helper()
	st, err := Open(context.Background(), filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func adminScope(t *testing.T, st *Store, all bool) auth.ScopeSet {
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

func makeProject(t *testing.T, st *Store, admin auth.ScopeSet, slug string) Scope {
	t.Helper()
	sc, err := st.CreateProject(context.Background(), admin, ProjectInput{Slug: slug, Name: slug, ReviewPolicy: "balanced"})
	if err != nil {
		t.Fatal(err)
	}
	return sc
}

func makeMemory(t *testing.T, st *Store, ss auth.ScopeSet, scopeID, title string) Memory {
	t.Helper()
	m, err := st.CreateMemory(context.Background(), ss, NewMemory{
		ScopeID: scopeID, Kind: "note", Title: title, Content: title + " stored for the isolation suite",
		Status: "active", SourceType: "agent_inferred", Tier: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found")
		}
		dir = parent
	}
}
