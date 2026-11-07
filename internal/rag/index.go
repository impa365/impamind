// Package rag keeps an optional in-memory vector index partitioned by scope.
// Exact search over one scope is enough at personal scale; the partition is
// also what stops one project from seeing another.
package rag

import (
	"bytes"
	"context"
	"encoding/binary"
	"hash/fnv"
	"log/slog"
	"math"
	"strings"
	"sync"
	"unicode"

	"impamind/internal/config"
	"impamind/internal/protocol"
	"impamind/internal/store"
)

// Embedder turns texts into vectors of a fixed dimension.
type Embedder interface {
	Embed(ctx context.Context, texts []string) ([][]float32, error)
}

type Index struct {
	st     *store.Store
	cfg    config.Config
	lg     *slog.Logger
	embed  Embedder
	queue  chan store.Memory
	stop   chan struct{}
	mu     sync.RWMutex
	parts  map[string][]item
	dims   int
	closed bool
}

type item struct {
	seq int64
	v   []float32
}

func New(st *store.Store, cfg config.Config, lg *slog.Logger) (*Index, error) {
	return NewWith(st, cfg, lg, httpEmbedder{url: cfg.EmbedURL, key: cfg.EmbedKey, model: cfg.EmbedModel, dims: cfg.EmbedDims, timeout: cfg.EmbedTimeout})
}

// NewWith builds an index that uses the given embedder. Tests pass a fake one.
func NewWith(st *store.Store, cfg config.Config, lg *slog.Logger, embed Embedder) (*Index, error) {
	if lg == nil {
		lg = slog.Default()
	}
	dims := cfg.EmbedDims
	if dims <= 0 {
		dims = 64
	}
	ix := &Index{st: st, cfg: cfg, lg: lg, embed: embed, queue: make(chan store.Memory, 1024), stop: make(chan struct{}), parts: map[string][]item{}, dims: dims}
	if st != nil {
		loaded, err := st.LoadEmbeddings(context.Background())
		if err != nil {
			return nil, err
		}
		for _, e := range loaded {
			ix.parts[e.ScopeID] = append(ix.parts[e.ScopeID], item{seq: e.Seq, v: decode(e.Vector)})
		}
	}
	return ix, nil
}

func (i *Index) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-i.stop:
			return
		case m := <-i.queue:
			i.embedOne(ctx, m)
		}
	}
}

func (i *Index) Stop() {
	i.mu.Lock()
	defer i.mu.Unlock()
	if !i.closed {
		i.closed = true
		close(i.stop)
	}
}

func (i *Index) Enqueue(m store.Memory) {
	if i == nil || m.Seq == 0 || strings.TrimSpace(m.Content) == "" {
		return
	}
	select {
	case i.queue <- m:
	default:
		go i.embedOne(context.Background(), m)
	}
}

// Flush embeds everything currently queued. Tests use it instead of waiting.
func (i *Index) Flush(ctx context.Context) {
	for {
		select {
		case m := <-i.queue:
			i.embedOne(ctx, m)
		default:
			return
		}
	}
}

func (i *Index) embedOne(ctx context.Context, m store.Memory) {
	vecs, err := i.embed.Embed(ctx, []string{m.Title + "\n" + m.Content})
	if err != nil || len(vecs) != 1 || len(vecs[0]) == 0 {
		if err != nil {
			i.lg.Warn("embed", "id", m.ID, "err", err)
		}
		return
	}
	v := normalize(vecs[0])
	if i.st != nil {
		if err := i.st.SaveEmbedding(ctx, m.Seq, m.ScopeID, i.cfg.EmbedModel, encode(v), len(v)); err != nil {
			i.lg.Warn("save embedding", "id", m.ID, "err", err)
			return
		}
	}
	i.mu.Lock()
	part := i.parts[m.ScopeID]
	replaced := false
	for n := range part {
		if part[n].seq == m.Seq {
			part[n].v = v
			replaced = true
			break
		}
	}
	if !replaced {
		part = append(part, item{seq: m.Seq, v: v})
	}
	i.parts[m.ScopeID] = part
	i.mu.Unlock()
}

func (i *Index) Remove(scopeID string, seq int64) {
	i.mu.Lock()
	part := i.parts[scopeID]
	out := part[:0]
	for _, it := range part {
		if it.seq != seq {
			out = append(out, it)
		}
	}
	i.parts[scopeID] = out
	i.mu.Unlock()
	if i.st != nil {
		_ = i.st.DeleteEmbedding(context.Background(), seq)
	}
}

func (i *Index) Search(ctx context.Context, scopeIDs []string, text string, k int) ([]protocol.VectorHit, error) {
	return i.rank(ctx, scopeIDs, text, k)
}

func (i *Index) Similar(ctx context.Context, scopeID, text string, k int) ([]protocol.VectorHit, error) {
	return i.rank(ctx, []string{scopeID}, text, k)
}

func (i *Index) rank(ctx context.Context, scopeIDs []string, text string, k int) ([]protocol.VectorHit, error) {
	if k <= 0 {
		k = 5
	}
	vecs, err := i.embed.Embed(ctx, []string{text})
	if err != nil || len(vecs) != 1 {
		return nil, err
	}
	q := normalize(vecs[0])
	i.mu.RLock()
	defer i.mu.RUnlock()
	var hits []protocol.VectorHit
	for _, scope := range scopeIDs {
		for _, it := range i.parts[scope] {
			score := dot(q, it.v)
			if score <= 0 {
				continue
			}
			hits = insertHit(hits, protocol.VectorHit{Seq: it.seq, Score: score}, k)
		}
	}
	return hits, nil
}

func insertHit(hits []protocol.VectorHit, h protocol.VectorHit, k int) []protocol.VectorHit {
	hits = append(hits, h)
	for n := len(hits) - 1; n > 0 && hits[n].Score > hits[n-1].Score; n-- {
		hits[n], hits[n-1] = hits[n-1], hits[n]
	}
	if len(hits) > k {
		hits = hits[:k]
	}
	return hits
}

func (i *Index) Stats() map[string]any {
	i.mu.RLock()
	defer i.mu.RUnlock()
	n := 0
	for _, p := range i.parts {
		n += len(p)
	}
	return map[string]any{"enabled": true, "vectors": n, "scopes": len(i.parts), "model": i.cfg.EmbedModel}
}

// HashEmbedder is a deterministic local embedder for tests and for running without a provider.
type HashEmbedder struct{ Dims int }

func (h HashEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	dims := h.Dims
	if dims <= 0 {
		dims = 64
	}
	out := make([][]float32, len(texts))
	for i, text := range texts {
		v := make([]float32, dims)
		for _, tok := range strings.Fields(strings.ToLower(text)) {
			tok = strings.TrimFunc(tok, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
			if tok == "" {
				continue
			}
			sum := fnv.New32a()
			_, _ = sum.Write([]byte(tok))
			v[int(sum.Sum32())%dims]++
		}
		out[i] = normalize(v)
	}
	return out, nil
}

func normalize(v []float32) []float32 {
	var n float64
	for _, x := range v {
		n += float64(x) * float64(x)
	}
	if n == 0 {
		return v
	}
	s := float32(1 / math.Sqrt(n))
	for i := range v {
		v[i] *= s
	}
	return v
}

func dot(a, b []float32) float64 {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	var s float64
	for i := 0; i < n; i++ {
		s += float64(a[i]) * float64(b[i])
	}
	return s
}

func encode(v []float32) []byte {
	var buf bytes.Buffer
	_ = binary.Write(&buf, binary.LittleEndian, v)
	return buf.Bytes()
}

func decode(b []byte) []float32 {
	out := make([]float32, len(b)/4)
	_ = binary.Read(bytes.NewReader(b), binary.LittleEndian, &out)
	return out
}
