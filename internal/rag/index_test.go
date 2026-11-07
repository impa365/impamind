package rag

import (
	"context"
	"path/filepath"
	"testing"

	"impamind/internal/config"
	"impamind/internal/store"
)

func TestSearchStaysInsideTheScope(t *testing.T) {
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
	a, err := st.CreateProject(ctx, admin, store.ProjectInput{Slug: "alpha", Name: "alpha"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := st.CreateProject(ctx, admin, store.ProjectInput{Slug: "beta", Name: "beta"})
	if err != nil {
		t.Fatal(err)
	}
	admin, _ = st.AdminScope(ctx)
	body := "the lighthouse protocol keeps the same sentence in both projects"
	ma, err := st.CreateMemory(ctx, admin, store.NewMemory{ScopeID: a.ID, Kind: "note", Title: "lighthouse", Content: body, Status: "active", SourceType: "agent_inferred"})
	if err != nil {
		t.Fatal(err)
	}
	mb, err := st.CreateMemory(ctx, admin, store.NewMemory{ScopeID: b.ID, Kind: "note", Title: "lighthouse", Content: body, Status: "active", SourceType: "agent_inferred"})
	if err != nil {
		t.Fatal(err)
	}
	ix, err := NewWith(st, config.Config{EmbedModel: "hash", EmbedDims: 64}, nil, HashEmbedder{Dims: 64})
	if err != nil {
		t.Fatal(err)
	}
	ix.Enqueue(ma)
	ix.Enqueue(mb)
	ix.Flush(ctx)
	hits, err := ix.Search(ctx, []string{a.ID}, body, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].Seq != ma.Seq {
		t.Fatalf("scope A saw %+v", hits)
	}
}
