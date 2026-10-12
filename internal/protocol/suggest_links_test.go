package protocol

import (
	"context"
	"testing"

	"impamind/internal/store"
)

func TestSuggestSimilarLinksAndGraphDegree(t *testing.T) {
	ctx := context.Background()
	st, svc := newSvc(t)
	admin := adminSS(t, st, true)
	sc := openProject(t, st, admin, "constellation")
	admin = adminSS(t, st, true)
	other := openProject(t, st, admin, "other-proj")
	admin = adminSS(t, st, true)
	c := Call{SS: admin, Profile: ProfileAdmin, Client: "test"}

	body := "retry queue with exponential backoff before giving up on failed jobs"
	a, err := st.CreateMemory(ctx, admin, store.NewMemory{
		ScopeID: sc.ID, Kind: "knowledge", Title: "Queue retries use exponential backoff",
		Content: body + " in the worker", Status: "active", SourceType: "agent_inferred", Tier: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	b, err := st.CreateMemory(ctx, admin, store.NewMemory{
		ScopeID: sc.ID, Kind: "knowledge", Title: "Worker retries with exponential backoff",
		Content: body + " inside the queue consumer", Status: "active", SourceType: "agent_inferred", Tier: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = st.CreateMemory(ctx, admin, store.NewMemory{
		ScopeID: other.ID, Kind: "knowledge", Title: "Queue retries use exponential backoff",
		Content: body + " in the worker", Status: "active", SourceType: "agent_inferred", Tier: 2,
	})
	if err != nil {
		t.Fatal(err)
	}

	out, err := svc.SuggestSimilarLinks(ctx, c, a.ID, 5)
	if err != nil {
		t.Fatalf("suggest: %v", err)
	}
	if out.Created < 1 {
		t.Fatalf("expected at least one similar link, got %+v", out)
	}
	out2, err := svc.SuggestSimilarLinks(ctx, c, a.ID, 5)
	if err != nil {
		t.Fatal(err)
	}
	if out2.Created != 0 {
		t.Fatalf("second suggest should be idempotent, created=%d", out2.Created)
	}

	graph, err := st.Graph(ctx, admin, []string{sc.ID}, false, 100)
	if err != nil {
		t.Fatal(err)
	}
	var similar int
	var linkedAB bool
	for _, e := range graph.Edges {
		if e.Type != store.EdgeTypeSimilar {
			continue
		}
		similar++
		if e.Weight <= 0 {
			t.Fatalf("similar edge weight: %+v", e)
		}
		if (e.Source == a.ID && e.Target == b.ID) || (e.Source == b.ID && e.Target == a.ID) {
			linkedAB = true
		}
	}
	if similar < 1 || !linkedAB {
		t.Fatalf("graph missing a↔b similar edge: edges=%+v", graph.Edges)
	}
	for _, n := range graph.Nodes {
		if n.ID == a.ID || n.ID == b.ID {
			if n.Degree < 1 {
				t.Fatalf("node %s degree=%d", n.ID, n.Degree)
			}
		}
	}

	links, err := st.LinksFor(ctx, admin, []string{a.ID})
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range links {
		src, err1 := st.GetMemory(ctx, admin, l.SourceID)
		dst, err2 := st.GetMemory(ctx, admin, l.TargetID)
		if err1 != nil || err2 != nil {
			continue
		}
		if (src.ScopeID == sc.ID && dst.ScopeID == other.ID) || (src.ScopeID == other.ID && dst.ScopeID == sc.ID) {
			t.Fatalf("cross-project similar link: %+v", l)
		}
	}
}

func TestSuggestSimilarForScopeAll(t *testing.T) {
	ctx := context.Background()
	st, svc := newSvc(t)
	admin := adminSS(t, st, true)
	sc := openProject(t, st, admin, "all-scope-a")
	admin = adminSS(t, st, true)
	c := Call{SS: admin, Profile: ProfileAdmin, Client: "test"}

	body := "shared vocabulary for scope all backfill test"
	_, err := st.CreateMemory(ctx, admin, store.NewMemory{
		ScopeID: sc.ID, Kind: "knowledge", Title: "Alpha backoff policy",
		Content: body, Status: "active", SourceType: "agent_inferred", Tier: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = st.CreateMemory(ctx, admin, store.NewMemory{
		ScopeID: sc.ID, Kind: "knowledge", Title: "Alpha backoff policy copy",
		Content: body + " extra", Status: "active", SourceType: "agent_inferred", Tier: 2,
	})
	if err != nil {
		t.Fatal(err)
	}

	out, err := svc.SuggestSimilarForScope(ctx, c, "all")
	if err != nil {
		t.Fatalf("all scope: %v", err)
	}
	if out.Scope != "all" {
		t.Fatalf("scope=%q", out.Scope)
	}
	if out.Scanned < 2 {
		t.Fatalf("scanned=%d", out.Scanned)
	}
}

func TestSuggestSimilarOrphanSecondPass(t *testing.T) {
	ctx := context.Background()
	st, svc := newSvc(t)
	admin := adminSS(t, st, true)
	sc := openProject(t, st, admin, "orphan-pass")
	admin = adminSS(t, st, true)
	c := Call{SS: admin, Profile: ProfileAdmin, Client: "test"}

	hub, err := st.CreateMemory(ctx, admin, store.NewMemory{
		ScopeID: sc.ID, Kind: "knowledge", Title: "Deploy pipeline notes",
		Content: "ci cd deploy staging production rollback checklist", Status: "active",
		SourceType: "agent_inferred", Tier: 2, Tags: []string{"deploy"},
	})
	if err != nil {
		t.Fatal(err)
	}
	peer, err := st.CreateMemory(ctx, admin, store.NewMemory{
		ScopeID: sc.ID, Kind: "knowledge", Title: "Staging deploy steps",
		Content: "deploy staging then production after checks", Status: "active",
		SourceType: "agent_inferred", Tier: 2, Tags: []string{"deploy"},
	})
	if err != nil {
		t.Fatal(err)
	}
	orphan, err := st.CreateMemory(ctx, admin, store.NewMemory{
		ScopeID: sc.ID, Kind: "note", Title: "zzz unrelated short",
		Content: "deploy mention only", Status: "active",
		SourceType: "agent_inferred", Tier: 3, Tags: []string{"deploy"},
	})
	if err != nil {
		t.Fatal(err)
	}

	out, err := svc.SuggestSimilarForScope(ctx, c, sc.Slug)
	if err != nil {
		t.Fatalf("scope: %v", err)
	}
	if out.Created < 1 {
		t.Fatalf("expected densify to create links, got %+v", out)
	}

	graph, err := st.Graph(ctx, admin, []string{sc.ID}, false, 100)
	if err != nil {
		t.Fatal(err)
	}
	deg := map[string]int{}
	for _, n := range graph.Nodes {
		deg[n.ID] = n.Degree
	}
	if deg[hub.ID] < 1 && deg[peer.ID] < 1 && deg[orphan.ID] < 1 {
		t.Fatalf("expected at least one linked node, degrees=%v edges=%+v", deg, graph.Edges)
	}
	// Orphan pass + tag boost should attach the weakly related note.
	if deg[orphan.ID] < 1 {
		t.Fatalf("orphan still degree 0; degrees=%v edges=%+v", deg, graph.Edges)
	}
}
