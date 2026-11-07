package protocol

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"impamind/internal/store"
)

func TestProtocolGate(t *testing.T) {
	ctx := context.Background()
	st, svc := newSvc(t)
	admin := adminSS(t, st, false)
	sc := openProject(t, st, admin, "queue")
	admin = adminSS(t, st, true)
	g, _, err := st.ScopeRefs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	agent := token(t, st, "worker", []store.Grant{
		{ScopeID: sc.ID, CanRead: true, CanWrite: true, CanPropose: true},
		{ScopeID: g.ID, CanRead: true, CanPropose: true},
	})
	ss := mustResolve(t, st, agent, "queue")
	c := call(ss)

	pref, err := st.CreateMemory(ctx, admin, store.NewMemory{
		ScopeID: g.ID, Kind: "preference", Title: "GLOBALBEACON table driven", Content: strings.Repeat("prefer table driven tests. ", 40),
		Status: "proposed", SourceType: "human_ui", Tier: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Approve(ctx, admin, pref.ID, store.ApproveInput{Note: "é do usuário", MarksUserStated: true}); err != nil {
		t.Fatal(err)
	}
	approved, err := st.History(ctx, admin, pref.ID)
	if err != nil || len(approved.Approvals) == 0 || approved.Memory.Status != "active" || approved.Memory.SourceType != "user_stated" {
		t.Fatalf("approve: %+v %v", approved.Memory, err)
	}
	long := strings.Repeat("The queue keeps a durable note about backpressure, retries and the single writer. ", 8)
	for i := 0; i < 15; i++ {
		if _, err := st.CreateMemory(ctx, admin, store.NewMemory{ScopeID: sc.ID, Kind: "decision", Title: fmt.Sprintf("decision %d", i), Content: long, Rationale: "measured", Status: "active", SourceType: "agent_inferred", Tier: 2}); err != nil {
			t.Fatal(err)
		}
	}
	pack, err := svc.Recall(ctx, c, RecallIn{Task: strings.Repeat("a", 120), Files: []string{"a.go", "b.go", "c.go"}, BudgetTokens: 300})
	if err != nil {
		t.Fatal(err)
	}
	if pack.Budget.Tokens != 300 || !pack.Budget.Truncated {
		t.Fatalf("budget %+v", pack.Budget)
	}
	seen := false
	for _, it := range pack.Global {
		if it.ID == pref.ID {
			seen = true
		}
	}
	if !seen {
		t.Fatal("recall dropped the global preference")
	}

	body := "the queue retries with sqlite wal and a single writer"
	first, err := svc.Observe(ctx, c, ObserveIn{Kind: "knowledge", Title: "Queue retries", Content: body, Evidence: []store.EvidenceIn{{Type: "file", Ref: "queue.go"}}})
	if err != nil || first.Result != "created" || first.Memory.Source != "tool_observed" {
		t.Fatalf("observe: %v %+v", err, first)
	}
	second, err := svc.Observe(ctx, c, ObserveIn{Kind: "knowledge", Title: "Queue retries", Content: body, Evidence: []store.EvidenceIn{{Type: "commit", Ref: "abc123"}}})
	if err != nil || second.Result != "duplicate" || second.DuplicateOf != first.Memory.ID {
		t.Fatalf("dedupe: %v %+v", err, second)
	}
	got, _ := st.GetMemory(ctx, ss, first.Memory.ID)
	if got.Confirmations < 2 {
		t.Fatalf("confirmations %d", got.Confirmations)
	}
	plain, err := svc.Observe(ctx, c, ObserveIn{Kind: "knowledge", Title: "Logs live in the journal", Content: "application logs stay in the journal next to the code", Evidence: []store.EvidenceIn{{Type: "file", Ref: ""}}})
	if err != nil || plain.Memory.Source != "agent_inferred" {
		t.Fatalf("empty ref: %v %+v", err, plain.Memory)
	}

	globalWrite, err := svc.Observe(ctx, c, ObserveIn{Scope: "global", Kind: "preference", Title: "Prefer short functions", Content: "keep functions short enough to read without scrolling the editor", UserConfirmed: true})
	if err != nil || globalWrite.Result != "proposed" {
		t.Fatalf("global: %v %+v", err, globalWrite)
	}
	stored, _ := st.GetMemory(ctx, admin, globalWrite.Memory.ID)
	if stored.Status != "proposed" || stored.SourceType == "user_stated" {
		t.Fatalf("user_confirmed changed provenance: %+v", stored)
	}
	again, _ := svc.Recall(ctx, c, RecallIn{Task: "short functions", SessionID: pack.SessionID})
	for _, it := range again.Global {
		if it.ID == stored.ID {
			t.Fatal("proposed global memory was treated as current")
		}
	}

	if _, err := svc.Decide(ctx, c, DecideIn{Title: "No rationale", Decision: "ship it"}); statusOf(err) != 400 {
		t.Fatalf("missing rationale: %v", err)
	}
	ok, err := svc.Decide(ctx, c, DecideIn{Title: "Use postgres for the queue", Decision: "Use postgres for the queue as the only broker", Rationale: "we already operate it"})
	if err != nil || ok.Result != "created" {
		t.Fatalf("decide: %v %+v", err, ok)
	}
	conflict, err := svc.Decide(ctx, c, DecideIn{Title: "Use postgres for the queue", Decision: "Use postgres for the queue as the only broker now", Rationale: "same choice restated"})
	if err != nil || conflict.Result != "needs_resolution" || len(conflict.PossibleConflicts) == 0 {
		t.Fatalf("conflict: %v %+v", err, conflict)
	}

	if _, err := svc.Observe(ctx, c, ObserveIn{Title: "A key", Content: "the key is sk-proj-abcdefghijklmnopqrstuvwxyz and must not be stored"}); statusOf(err) != 422 {
		t.Fatalf("secret: %v", err)
	}

	svc.Cfg.ObservationBudget = 1
	if _, err := svc.Observe(ctx, c, ObserveIn{SessionID: first.SessionID, Title: "One more fact about retries", Content: "retries back off exponentially before they give up"}); err == nil || statusOf(err) != 429 {
		t.Fatalf("budget: %v", err)
	}

	cp, err := svc.Checkpoint(ctx, c, CheckpointIn{SessionID: pack.SessionID, Task: "ship the queue", NextSteps: []string{"write the benchmark"}, Files: []string{"queue.go"}})
	if err != nil {
		t.Fatal(err)
	}
	next, err := svc.Recall(ctx, c, RecallIn{Task: "continue the queue"})
	if err != nil || next.Checkpoint == nil || len(next.Checkpoint.NextSteps) == 0 || next.SessionID == cp.SessionID {
		t.Fatalf("new session checkpoint: %+v err=%v", next.Checkpoint, err)
	}

	warned, err := svc.Observe(ctx, c, ObserveIn{Title: "Wrote without recall", Content: "this note was saved before any recall in its own session"})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(warned.Warnings, " ")
	if !strings.Contains(joined, "recall_missing") {
		t.Fatalf("warnings %v", warned.Warnings)
	}

	hand, err := svc.Handoff(ctx, c, HandoffIn{SessionID: next.SessionID, Summary: "The queue work is done and the next agent should start from the benchmark.", NextSteps: []string{"run the bench"}, LearningsCandidates: []string{"one writer is enough"}})
	if err != nil {
		t.Fatal(err)
	}
	closed, err := st.GetSession(ctx, ss, hand.SessionID)
	if err != nil || closed.Status != "closed" || !closed.HandedOff || hand.SummaryID == "" {
		t.Fatalf("handoff %+v session %+v err %v", hand, closed, err)
	}
}
