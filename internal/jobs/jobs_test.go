package jobs

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"impamind/internal/config"
	"impamind/internal/store"
)

func TestSweeps(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open(ctx, filepath.Join(dir, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	admin, err := st.AdminScope(ctx)
	if err != nil {
		t.Fatal(err)
	}
	sc, err := st.CreateProject(ctx, admin, store.ProjectInput{Slug: "queue", Name: "queue", ReviewPolicy: "open"})
	if err != nil {
		t.Fatal(err)
	}
	admin, _ = st.AdminScope(ctx)
	past := store.FormatTime(time.Now().Add(-48 * time.Hour))
	proposed, err := st.CreateMemory(ctx, admin, store.NewMemory{ScopeID: sc.ID, Kind: "note", Title: "old proposal about retries", Content: "an old proposal that nobody reviewed in time", Status: "proposed", SourceType: "agent_inferred"})
	if err != nil {
		t.Fatal(err)
	}
	expiring, err := st.CreateMemory(ctx, admin, store.NewMemory{ScopeID: sc.ID, Kind: "note", Title: "expiring note about logs", Content: "this note stops being true after the window", Status: "active", SourceType: "agent_inferred", ValidUntil: past})
	if err != nil {
		t.Fatal(err)
	}
	stale, err := st.CreateMemory(ctx, admin, store.NewMemory{ScopeID: sc.ID, Kind: "note", Title: "stale note about caches", Content: "this note should be reviewed again soon", Status: "active", SourceType: "agent_inferred", ReviewAfter: past})
	if err != nil {
		t.Fatal(err)
	}
	busy, err := st.CreateMemory(ctx, admin, store.NewMemory{ScopeID: sc.ID, Kind: "note", Title: "busy note about pools", Content: "this note is read often and might deserve tier one", Status: "active", SourceType: "agent_inferred", Tier: 2})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		_, _ = st.GetMemory(ctx, admin, busy.ID)
	}
	st.FlushAccess()
	j := New(st, nil, config.Config{ProposalTTL: time.Millisecond, SessionArchive: time.Hour, BackupInterval: 0, BackupDir: filepath.Join(dir, "bak"), BackupKeep: 2}, nil)
	time.Sleep(20 * time.Millisecond)
	j.Once(ctx)
	if got, _ := st.GetMemory(ctx, admin, proposed.ID); got.Status != "rejected" {
		t.Fatalf("proposal %s", got.Status)
	}
	if got, _ := st.GetMemory(ctx, admin, expiring.ID); got.Status != "expired" {
		t.Fatalf("expiry %s", got.Status)
	}
	items, err := st.ListReview(ctx, admin, "stale", "open", 10)
	if err != nil || len(items) == 0 {
		t.Fatalf("stale items %v %v", items, err)
	}
	if got, _ := st.GetMemory(ctx, admin, stale.ID); got.Status != "active" {
		t.Fatal("stale changed status")
	}
	if got, _ := st.GetMemory(ctx, admin, busy.ID); got.Tier != 2 {
		t.Fatalf("tier changed to %d", got.Tier)
	}
	tips, err := st.ListReview(ctx, admin, "tier_suggestion", "open", 10)
	if err != nil || len(tips) != 1 {
		t.Fatalf("tier suggestions %+v %v", tips, err)
	}
	_ = proposed
}
