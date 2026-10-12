package protocol

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"impamind/internal/auth"
	"impamind/internal/store"
)

func TestDecisionsAndSupersede(t *testing.T) {
	ctx := context.Background()
	st, svc := newSvc(t)
	admin := adminSS(t, st, false)
	open := openProject(t, st, admin, "queue")
	balanced := openProject(t, st, admin, "billing")
	// CreateProject forces the policy argument; reopen the scope set after both exist.
	admin = adminSS(t, st, true)
	if _, err := st.UpdateScope(ctx, admin, balanced.ID, store.ProjectInput{Name: "billing", ReviewPolicy: "balanced"}); err != nil {
		t.Fatal(err)
	}
	g, _, err := st.ScopeRefs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	agent := token(t, st, "decider", []store.Grant{
		{ScopeID: open.ID, CanRead: true, CanWrite: true, CanPropose: true},
		{ScopeID: balanced.ID, CanRead: true, CanWrite: true, CanPropose: true},
		{ScopeID: g.ID, CanRead: true},
	})
	ss := mustResolve(t, st, agent, "queue")

	first, err := svc.Decide(ctx, call(ss), DecideIn{Title: "Use a single process", Decision: "Keep one writer for the queue", Rationale: "the workload fits one node"})
	if err != nil || first.Result != "created" {
		t.Fatalf("first decide: %v %+v", err, first)
	}
	second, err := svc.Decide(ctx, call(ss), DecideIn{Title: "Cache rendered pages", Decision: "Cache pages in memory for a minute", Rationale: "renders dominate the profile"})
	if err != nil || second.Result != "created" {
		t.Fatalf("second decide: %v %+v", err, second)
	}
	a, _ := st.GetMemory(ctx, ss, first.Memory.ID)
	b, _ := st.GetMemory(ctx, ss, second.Memory.ID)
	if a.Status != "active" || b.Status != "active" || a.LineageID == b.LineageID || a.LineageID == "" {
		t.Fatalf("independent decisions share a fate: %+v %+v", a, b)
	}

	viaDecide, err := svc.Decide(ctx, call(ss), DecideIn{
		Title: "Use a pool of writers", Decision: "Allow several writers behind one mutex",
		Rationale: "one writer now stalls imports", Supersedes: a.ID, Reason: "import volume outgrew a single writer",
	})
	if err != nil || viaDecide.Result != "superseded" {
		t.Fatalf("decide supersede: %v %+v", err, viaDecide)
	}
	viaTool, err := svc.Supersede(ctx, call(ss), SupersedeIn{
		OldID: b.ID, Reason: "a minute is too long for edits",
		New:   &NewContent{Title: "Cache pages for five seconds", Content: "Keep the page cache but expire it in five seconds", Rationale: "editors were seeing stale pages"},
	})
	if err != nil || viaTool.Result != "superseded" {
		t.Fatalf("supersede tool: %v %+v", err, viaTool)
	}
	for _, pair := range [][2]string{{a.ID, viaDecide.Memory.ID}, {b.ID, viaTool.Memory.ID}} {
		old, err := st.GetMemory(ctx, ss, pair[0])
		if err != nil {
			t.Fatal(err)
		}
		neu, err := st.GetMemory(ctx, ss, pair[1])
		if err != nil {
			t.Fatal(err)
		}
		if old.Status != "superseded" || old.Content == "" || neu.Status != "active" || neu.LineageID != old.LineageID {
			t.Fatalf("lineage broken old=%s/%s new=%s/%s", old.Status, old.LineageID, neu.Status, neu.LineageID)
		}
		h, err := st.History(ctx, ss, neu.ID)
		if err != nil {
			t.Fatal(err)
		}
		active := 0
		for _, m := range h.Lineage {
			if m.Status == "active" {
				active++
			}
		}
		if active != 1 {
			t.Fatalf("lineage has %d active", active)
		}
	}

	out, err := svc.Recall(ctx, call(ss), RecallIn{Task: "writers and page cache"})
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range out.Decisions {
		if it.ID == a.ID || it.ID == b.ID {
			t.Fatal("recall returned a superseded decision")
		}
		if it.ID == viaDecide.Memory.ID && (it.Supersedes == nil || it.Supersedes.Reason == "") {
			t.Fatalf("missing supersedes reason: %+v", it.Supersedes)
		}
	}
	full, err := svc.Read(ctx, call(ss), ReadIn{ID: viaDecide.Memory.ID, IncludeHistory: true})
	if err != nil || full.History == nil || len(full.History.Lineage) < 2 {
		t.Fatalf("history: %v %+v", err, full.History)
	}
}

func TestProposalKeepsOldActiveUntilApprove(t *testing.T) {
	ctx := context.Background()
	st, svc := newSvc(t)
	admin := adminSS(t, st, false)
	sc := openProject(t, st, admin, "billing")
	admin = adminSS(t, st, true)
	if _, err := st.UpdateScope(ctx, admin, sc.ID, store.ProjectInput{Name: "billing", ReviewPolicy: "balanced"}); err != nil {
		t.Fatal(err)
	}
	g, _, err := st.ScopeRefs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	agent := token(t, st, "architect", []store.Grant{
		{ScopeID: sc.ID, CanRead: true, CanWrite: true, CanPropose: true},
		{ScopeID: g.ID, CanRead: true},
	})
	ss := mustResolve(t, st, agent, "billing")
	old := memory(t, st, admin, sc.ID, "monolith handles billing")
	// memory() stores a note, which is not protected. Replace it with architecture.
	out, err := svc.Decide(ctx, call(ss), DecideIn{
		Kind: "architecture", Title: "Split billing into its own module",
		Decision: "Billing becomes a module with its own schema", Rationale: "release cadence of billing diverged",
		Supersedes: old.ID, Reason: "billing now ships on its own schedule",
	})
	if err != nil || out.Result != "proposed" {
		t.Fatalf("proposal: %v %+v", err, out)
	}
	still, err := st.GetMemory(ctx, ss, old.ID)
	if err != nil || still.Status != "active" {
		t.Fatalf("old status: %v %+v", err, still.Status)
	}
	proposed, err := st.GetMemory(ctx, ss, out.Memory.ID)
	if err != nil || proposed.Status != "proposed" || proposed.LineageID == still.LineageID {
		t.Fatalf("proposal lineage: %+v", proposed)
	}
	approved, err := st.Approve(ctx, admin, proposed.ID, store.ApproveInput{Note: "agreed"})
	if err != nil {
		t.Fatal(err)
	}
	still, _ = st.GetMemory(ctx, admin, old.ID)
	if still.Status != "superseded" || approved.Status != "active" || approved.LineageID != still.LineageID {
		t.Fatalf("after approve old=%s new=%s lineages %s %s", still.Status, approved.Status, still.LineageID, approved.LineageID)
	}
}

func TestReplacementRollback(t *testing.T) {
	ctx := context.Background()
	st, _ := newSvc(t)
	admin := adminSS(t, st, false)
	sc := openProject(t, st, admin, "queue")
	admin = adminSS(t, st, true)
	for _, stage := range []string{"replace.after_old", "replace.after_insert", "replace.after_versions"} {
		old := memory(t, st, admin, sc.ID, "stage "+stage)
		st.SetFailpoint(func(got string) error {
			if got == stage {
				return errors.New("boom")
			}
			return nil
		})
		_, _, err := st.ReplaceMemory(ctx, admin, old.ID, store.NewMemory{
			Kind: "note", Title: "replacement " + stage, Content: "rollbackmarker body for " + stage + " must disappear",
			SourceType: "agent_inferred", Tier: 2,
		}, "trying a replacement")
		st.SetFailpoint(nil)
		if err == nil {
			t.Fatalf("%s did not fail", stage)
		}
		got, err := st.GetMemory(ctx, admin, old.ID)
		if err != nil || got.Status != "active" || got.Title != old.Title {
			t.Fatalf("%s rolled to %+v %v", stage, got, err)
		}
		hits, err := st.SearchFTS(ctx, admin, "rollbackmarker", store.SearchFilter{ScopeIDs: []string{sc.ID}})
		if err != nil || len(hits) != 0 {
			t.Fatalf("%s fts hits %d %v", stage, len(hits), err)
		}
		if err := st.CheckIntegrity(ctx); err != nil {
			t.Fatalf("%s integrity: %v", stage, err)
		}
	}
}

func TestConcurrentReplacementConflicts(t *testing.T) {
	ctx := context.Background()
	st, _ := newSvc(t)
	admin := adminSS(t, st, false)
	sc := openProject(t, st, admin, "queue")
	admin = adminSS(t, st, true)
	old := memory(t, st, admin, sc.ID, "one writer only")
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, _, errs[i] = st.ReplaceMemory(ctx, admin, old.ID, store.NewMemory{
				Kind: "note", Title: "writer pool " + string(rune('a'+i)), Content: "several writers sharing one mutex and one log",
				SourceType: "agent_inferred", Tier: 2,
			}, "concurrency")
		}(i)
	}
	wg.Wait()
	ok, conflict := 0, 0
	for _, err := range errs {
		switch {
		case err == nil:
			ok++
		case errors.Is(err, store.ErrConflict):
			conflict++
		default:
			t.Fatalf("unexpected: %v", err)
		}
	}
	if ok != 1 || conflict != 1 {
		t.Fatalf("ok=%d conflict=%d", ok, conflict)
	}
}

func TestFreshnessAndAccessDoNotPromote(t *testing.T) {
	ctx := context.Background()
	st, svc := newSvc(t)
	admin := adminSS(t, st, false)
	sc := openProject(t, st, admin, "queue")
	admin = adminSS(t, st, true)
	past := store.FormatTime(time.Now().Add(-48 * time.Hour))
	stale, err := st.CreateMemory(ctx, admin, store.NewMemory{
		ScopeID: sc.ID, Kind: "decision", Title: "stale retention choice", Content: "keep logs for ninety days in the archive",
		Status: "active", SourceType: "agent_inferred", Tier: 2, Rationale: "compliance asked for ninety days", ReviewAfter: past,
	})
	if err != nil {
		t.Fatal(err)
	}
	expired, err := st.CreateMemory(ctx, admin, store.NewMemory{
		ScopeID: sc.ID, Kind: "decision", Title: "expired retention choice", Content: "keep logs for nine days only",
		Status: "active", SourceType: "agent_inferred", Tier: 2, Rationale: "a short window was enough then", ValidUntil: past,
	})
	if err != nil {
		t.Fatal(err)
	}
	g, _, err := st.ScopeRefs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	agent := token(t, st, "reader", []store.Grant{{ScopeID: sc.ID, CanRead: true, CanWrite: true}, {ScopeID: g.ID, CanRead: true}})
	ss := mustResolve(t, st, agent, "queue")
	out, err := svc.Recall(ctx, call(ss), RecallIn{Task: "retention choice"})
	if err != nil {
		t.Fatal(err)
	}
	sawStale := false
	for _, it := range append(out.Decisions, out.Relevant...) {
		if it.ID == expired.ID {
			t.Fatal("expired memory stayed in recall")
		}
		if it.ID == stale.ID {
			sawStale = it.Stale
		}
	}
	if !sawStale {
		t.Fatal("stale memory was not flagged")
	}
	found, err := svc.Search(ctx, call(ss), SearchIn{Query: "expired retention"})
	if err != nil {
		t.Fatal(err)
	}
	flagged := false
	for _, it := range found.Items {
		if it.ID == expired.ID && it.Expired {
			flagged = true
		}
	}
	if !flagged {
		t.Fatalf("search did not flag expired: %+v", found.Items)
	}
	for i := 0; i < 1000; i++ {
		if _, err := st.GetMemory(ctx, ss, stale.ID); err != nil {
			t.Fatal(err)
		}
	}
	st.FlushAccess()
	got, err := st.GetMemory(ctx, admin, stale.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Tier != 2 || got.Status != "active" {
		t.Fatalf("access changed the memory: tier %d status %s", got.Tier, got.Status)
	}
	items, err := st.ListReview(ctx, admin, "tier_suggestion", "open", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) > 1 {
		t.Fatalf("%d tier suggestions", len(items))
	}
}

func openProject(t *testing.T, st *store.Store, admin auth.ScopeSet, slug string) store.Scope {
	t.Helper()
	sc, err := st.CreateProject(context.Background(), admin, store.ProjectInput{Slug: slug, Name: slug, ReviewPolicy: "open"})
	if err != nil {
		t.Fatal(err)
	}
	return sc
}
