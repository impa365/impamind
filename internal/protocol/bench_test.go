package protocol

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"impamind/internal/store"
)

func TestRecallBenchmarkGate(t *testing.T) {
	if os.Getenv("IMPAMIND_BENCH_GATE") != "1" {
		t.Skip("set IMPAMIND_BENCH_GATE=1 to run the 20ms recall gate")
	}
	ctx := context.Background()
	st, svc := newSvc(t)
	admin := adminSS(t, st, false)
	sc := openProject(t, st, admin, "bench")
	admin = adminSS(t, st, true)
	g := admin.GlobalID()
	for i := 0; i < 200; i++ {
		tier := 2
		if i < 50 {
			tier = 1
		}
		m, err := st.CreateMemory(ctx, admin, store.NewMemory{ScopeID: g, Kind: "preference", Title: fmt.Sprintf("global bench %d", i), Content: fmt.Sprintf("global bench preference %d stays available to every project", i), Status: "proposed", SourceType: "human_ui", Tier: tier})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := st.Approve(ctx, admin, m.ID, store.ApproveInput{Note: "bench"}); err != nil {
			t.Fatal(err)
		}
	}
	var ids []string
	for i := 0; i < 2000; i++ {
		m := memory(t, st, admin, sc.ID, fmt.Sprintf("project bench fact %d about queues caches and logs", i))
		ids = append(ids, m.ID)
	}
	for i := 0; i < 200; i++ {
		if _, err := st.CreateLink(ctx, admin, ids[i], ids[i+200], "related"); err != nil {
			t.Fatal(err)
		}
	}
	agent := token(t, st, "bench", []store.Grant{{ScopeID: sc.ID, CanRead: true, CanWrite: true}, {ScopeID: g, CanRead: true}})
	ss := mustResolve(t, st, agent, "bench")
	in := RecallIn{Task: "investigate the queue cache and the log pipeline in this service", Files: []string{"queue.go", "cache.go", "log.go"}, BudgetTokens: 1500}
	var samples []time.Duration
	for i := 0; i < 220; i++ {
		start := time.Now()
		if _, err := svc.Recall(ctx, call(ss), in); err != nil {
			t.Fatal(err)
		}
		if i >= 20 {
			samples = append(samples, time.Since(start))
		}
	}
	sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
	p95 := samples[len(samples)*95/100]
	t.Logf("recall p95 %s n=%d", p95, len(samples))
	if p95 > 20*time.Millisecond {
		t.Fatalf("p95 %s exceeds 20ms", p95)
	}
	_ = filepath.Separator
}
