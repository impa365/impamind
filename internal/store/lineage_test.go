package store

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"impamind/internal/auth"
)

func TestVersionsAreAppendOnly(t *testing.T) {
	ctx := context.Background()
	st := openTest(t)
	admin := adminScope(t, st, false)
	sc := makeProject(t, st, admin, "queue")
	admin = adminScope(t, st, true)
	m := makeMemory(t, st, admin, sc.ID, "append only fact")
	if m.CreatedByPrincipalID == "" || m.LineageID == "" {
		t.Fatalf("missing authorship: %+v", m)
	}
	_, err := st.w.ExecContext(ctx, `UPDATE memory_versions SET change_reason = 'rewritten' WHERE memory_id = ?`, m.ID)
	if err == nil || !strings.Contains(err.Error(), "append-only") {
		t.Fatalf("update versions: %v", err)
	}
	_, err = st.w.ExecContext(ctx, `DELETE FROM memory_versions WHERE memory_id = ?`, m.ID)
	if err == nil || !strings.Contains(err.Error(), "append-only") {
		t.Fatalf("delete versions: %v", err)
	}
}

func TestOneActivePerLineage(t *testing.T) {
	ctx := context.Background()
	st := openTest(t)
	admin := adminScope(t, st, false)
	sc := makeProject(t, st, admin, "queue")
	admin = adminScope(t, st, true)
	a := makeMemory(t, st, admin, sc.ID, "first active fact")
	b := makeMemory(t, st, admin, sc.ID, "second active fact")
	_, err := st.w.ExecContext(ctx, `UPDATE memories SET lineage_id = ? WHERE id = ?`, a.LineageID, b.ID)
	if err == nil || !strings.Contains(err.Error(), "UNIQUE") {
		t.Fatalf("second active in lineage: %v", err)
	}
}

func TestHardDeleteLeavesTombstone(t *testing.T) {
	ctx := context.Background()
	st := openTest(t)
	admin := adminScope(t, st, false)
	sc := makeProject(t, st, admin, "queue")
	admin = adminScope(t, st, true)
	m := makeMemory(t, st, admin, sc.ID, "remove this fact")
	g, _, err := st.ScopeRefs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	agent := tokenIdentity(t, st, "worker", []Grant{{ScopeID: sc.ID, CanRead: true, CanWrite: true}, {ScopeID: g.ID, CanRead: true}})
	ss := resolveID(t, st, agent, "queue")
	if err := st.HardDelete(ctx, ss, m.ID, "agents do not get to erase"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("agent hard delete: %v", err)
	}
	archived, err := st.Transition(ctx, admin, m.ID, "archived", "kept for the record")
	if err != nil || archived.Status != "archived" {
		t.Fatalf("soft delete: %v %+v", err, archived.Status)
	}
	other := makeMemory(t, st, admin, sc.ID, "erase this other fact")
	if err := st.HardDelete(ctx, admin, other.ID, "it was entered by mistake"); err != nil {
		t.Fatal(err)
	}
	var title string
	if err := st.r.QueryRowContext(ctx, `SELECT title FROM tombstones WHERE memory_id = ?`, other.ID).Scan(&title); err != nil || title != other.Title {
		t.Fatalf("tombstone: %v %q", err, title)
	}
	if _, err := st.GetMemory(ctx, admin, other.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted memory still readable: %v", err)
	}
}

func TestBackupRestoresLineage(t *testing.T) {
	ctx := context.Background()
	st := openTest(t)
	admin := adminScope(t, st, false)
	sc := makeProject(t, st, admin, "queue")
	admin = adminScope(t, st, true)
	old := makeMemory(t, st, admin, sc.ID, "original decision text")
	neu, prev, err := st.ReplaceMemory(ctx, admin, old.ID, NewMemory{
		Kind: "note", Title: "revised decision text", Content: "the revised decision keeps the same lineage",
		SourceType: "agent_inferred", Tier: 2,
	}, "the premise changed")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddEvidence(ctx, admin, neu.ID, EvidenceIn{Type: "file", Ref: "doc/decision.md"}); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(t.TempDir(), "snap.db")
	if err := st.Backup(ctx, dst); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.ReplaceMemory(ctx, admin, neu.ID, NewMemory{
		Kind: "note", Title: "mutated after the snapshot", Content: "this mutation must not be in the backup",
		SourceType: "agent_inferred", Tier: 2,
	}, "later edit"); err != nil {
		t.Fatal(err)
	}
	bak, err := Open(ctx, dst)
	if err != nil {
		t.Fatal(err)
	}
	defer bak.Close()
	view := adminScope(t, bak, true)
	got, err := bak.GetMemory(ctx, view, neu.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.LineageID != prev.LineageID || got.Status != "active" || got.Title != neu.Title {
		t.Fatalf("backup head: %+v", got)
	}
	h, err := bak.History(ctx, view, neu.ID)
	if err != nil || len(h.Lineage) != 2 || len(h.Versions) < 2 {
		t.Fatalf("backup history lineage=%d versions=%d err=%v", len(h.Lineage), len(h.Versions), err)
	}
	if len(got.Evidence) != 1 || got.Evidence[0].Ref != "doc/decision.md" {
		t.Fatalf("backup evidence: %+v", got.Evidence)
	}
}

func tokenIdentity(t *testing.T, st *Store, name string, grants []Grant) Identity {
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

func resolveID(t *testing.T, st *Store, id Identity, project string) auth.ScopeSet {
	t.Helper()
	g, projects, err := st.ScopeRefs(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ss, err := auth.Resolve(id.Principal, id.Grants, g, projects, auth.Request{Arg: project})
	if err != nil {
		t.Fatal(err)
	}
	return ss
}
