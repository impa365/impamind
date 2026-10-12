package store

import (
	"context"
	"path/filepath"
	"testing"
)

func TestOpenProbePersist(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "impamind.db")

	s, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	p, err := s.Probe(ctx)
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if !p.FTS5 || !p.FTS5Porter {
		t.Fatalf("fts5 probe failed: %+v", p)
	}
	if p.JournalMode != "wal" {
		t.Fatalf("journal mode %q, want wal", p.JournalMode)
	}
	n, err := s.ProbeWrite(ctx, "primeira subida")
	if err != nil || n != 1 {
		t.Fatalf("probe write: n=%d err=%v", n, err)
	}
	g, _, err := s.ScopeRefs(ctx)
	if err != nil || g.Kind != "global" {
		t.Fatalf("global scope: %+v err=%v", g, err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	s, err = Open(ctx, path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s.Close()
	n, err = s.ProbeCount(ctx)
	if err != nil || n != 1 {
		t.Fatalf("after restart: n=%d err=%v", n, err)
	}
	if _, err := s.ProbeWrite(ctx, "segunda subida"); err != nil {
		t.Fatalf("second write: %v", err)
	}
	if n, _ = s.ProbeCount(ctx); n != 2 {
		t.Fatalf("count %d, want 2", n)
	}
}

func TestFTSRanksTitleAboveBody(t *testing.T) {
	ctx := context.Background()
	st := openTest(t)
	admin := adminScope(t, st, false)
	sc := makeProject(t, st, admin, "queue")
	admin = adminScope(t, st, true)
	body := makeMemory(t, st, admin, sc.ID, "notes mention lighthouse only in passing here")
	title := makeMemory(t, st, admin, sc.ID, "lighthouse protocol")
	_ = body
	hits, err := st.SearchFTS(ctx, admin, "lighthouse", SearchFilter{ScopeIDs: []string{sc.ID}})
	if err != nil || len(hits) < 2 {
		t.Fatalf("hits %+v %v", hits, err)
	}
	if hits[0].Memory.ID != title.ID {
		t.Fatalf("first hit %s, want the title match", hits[0].Memory.Title)
	}
}

func TestRegistry(t *testing.T) {
	seen := map[string]bool{}
	for _, q := range Registry() {
		if seen[q.Name] {
			t.Fatalf("duplicate query %s", q.Name)
		}
		seen[q.Name] = true
		if q.Scoped() && q.SQL == q.Raw {
			t.Fatalf("scoped query %s was not expanded", q.Name)
		}
		if !q.Scoped() && !SystemReasons[q.Reason] {
			t.Fatalf("query %s has neither a scope predicate nor a system reason", q.Name)
		}
	}
	if len(seen) < 40 {
		t.Fatalf("only %d queries registered", len(seen))
	}
}
