package store

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"time"

	"impamind/internal/auth"
)

var (
	qDueProposals = system("job.due_proposals", "job", `SELECT id, scope_id FROM memories WHERE status = 'proposed' AND created_at < ?`)
	qDueExpiry    = system("job.due_expiry", "job", `SELECT id, scope_id FROM memories WHERE status = 'active' AND valid_until IS NOT NULL AND valid_until <= ?`)
	qDueStale     = system("job.due_stale", "job", `SELECT id, scope_id FROM memories WHERE status = 'active' AND review_after IS NOT NULL AND review_after <= ?
		AND NOT EXISTS (SELECT 1 FROM review_items r WHERE r.dedupe_key = 'stale:' || memories.id AND r.status = 'open')`)
	qDueTier = system("job.due_tier", "job", `SELECT id, scope_id, tier, access_count FROM memories WHERE status = 'active' AND tier > 1 AND access_count >= 20
		AND NOT EXISTS (SELECT 1 FROM review_items r WHERE r.dedupe_key = 'tier:' || memories.id AND r.status = 'open')`)
	qDueSession = system("job.due_session", "job", `SELECT m.id, m.scope_id FROM memories m JOIN work_sessions s ON s.id = m.session_id
		WHERE s.status = 'closed' AND s.closed_at IS NOT NULL AND s.closed_at < ? AND m.status = 'active' AND m.kind <> 'session_summary'`)
	qEmbeddingUpsert = system("embedding.upsert", "index", `INSERT INTO embeddings(memory_seq, scope_id, model, dims, vector, updated_at) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(memory_seq) DO UPDATE SET scope_id = excluded.scope_id, model = excluded.model, dims = excluded.dims, vector = excluded.vector, updated_at = excluded.updated_at`)
	qEmbeddingAll    = system("embedding.all", "index", `SELECT memory_seq, scope_id, model, dims, vector FROM embeddings`)
	qEmbeddingDelete = system("embedding.delete", "index", `DELETE FROM embeddings WHERE memory_seq = ?`)
)

// DueProposals lists proposals older than the cutoff. The caller rejects them.
func (s *Store) DueProposals(ctx context.Context, before string) ([]Memory, error) {
	return s.due(ctx, qDueProposals, before)
}

func (s *Store) due(ctx context.Context, q *Query, before string) ([]Memory, error) {
	rows, err := s.reader(ctx).query(q, before)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Memory
	for rows.Next() {
		var m Memory
		if err := rows.Scan(&m.ID, &m.ScopeID); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// ExpireDue marks active memories past valid_until as expired and records a version.
func (s *Store) ExpireDue(ctx context.Context, principal, now string) (int, error) {
	list, err := s.due(ctx, qDueExpiry, now)
	if err != nil || len(list) == 0 {
		return 0, err
	}
	n := 0
	err = s.write(ctx, func(w wx) error {
		for _, m := range list {
			scope := []string{m.ScopeID}
			changed, err := w.execN(qMemSetStatus, m.ID, "expired", "needs_review", now, "active", ids(scope))
			if err != nil || changed != 1 {
				return err
			}
			if err := w.addVersion(m.ID, scope, actor{principal: principal}, "expired", "valid_until passed"); err != nil {
				return err
			}
			n++
		}
		return nil
	})
	return n, err
}

// FlagStale files one open review item per memory past review_after.
func (s *Store) FlagStale(ctx context.Context, now string) (int, error) {
	list, err := s.due(ctx, qDueStale, now)
	if err != nil || len(list) == 0 {
		return 0, err
	}
	n := 0
	err = s.write(ctx, func(w wx) error {
		for _, m := range list {
			if err := w.upsertReview(m.ScopeID, "stale", "stale:"+m.ID, []string{m.ID}, []byte(`{"reason":"review_after passed"}`)); err != nil {
				return err
			}
			n++
		}
		return nil
	})
	return n, err
}

// SuggestTiers files tier suggestions for busy memories. It never changes the tier.
func (s *Store) SuggestTiers(ctx context.Context) (int, error) {
	rows, err := s.reader(ctx).query(qDueTier)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	type hit struct {
		id, scope string
		tier, n   int
	}
	var list []hit
	for rows.Next() {
		var h hit
		if err := rows.Scan(&h.id, &h.scope, &h.tier, &h.n); err != nil {
			return 0, err
		}
		list = append(list, h)
	}
	if err := rows.Err(); err != nil || len(list) == 0 {
		return 0, err
	}
	count := 0
	err = s.write(ctx, func(w wx) error {
		for _, h := range list {
			payload := []byte(`{"from":` + itoa(h.tier) + `,"to":` + itoa(h.tier-1) + `,"access_count":` + itoa(h.n) + `}`)
			if err := w.upsertReview(h.scope, "tier_suggestion", "tier:"+h.id, []string{h.id}, payload); err != nil {
				return err
			}
			count++
		}
		return nil
	})
	return count, err
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [12]byte
	i := len(b)
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

// ArchiveClosedSessions archives session-scoped memories after the session has been closed for long enough.
// The final session summary stays.
func (s *Store) ArchiveClosedSessions(ctx context.Context, principal, closedBefore string) (int, error) {
	list, err := s.due(ctx, qDueSession, closedBefore)
	if err != nil || len(list) == 0 {
		return 0, err
	}
	n := 0
	err = s.write(ctx, func(w wx) error {
		for _, m := range list {
			scope := []string{m.ScopeID}
			changed, err := w.execN(qMemSetStatus, m.ID, "archived", "unreviewed", now(), "active", ids(scope))
			if err != nil || changed != 1 {
				continue
			}
			if err := w.addVersion(m.ID, scope, actor{principal: principal}, "archived", "session closed"); err != nil {
				return err
			}
			n++
		}
		return nil
	})
	return n, err
}

// RotateBackups writes a VACUUM INTO snapshot and deletes older files beyond keep.
func (s *Store) RotateBackups(ctx context.Context, dir string, keep int) (string, error) {
	if keep < 1 {
		keep = 1
	}
	dst := filepath.Join(dir, "impamind-"+time.Now().UTC().Format("20060102-150405")+".db")
	if err := s.Backup(ctx, dst); err != nil {
		return "", err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return dst, err
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && filepath.Ext(e.Name()) == ".db" {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	for len(names) > keep {
		_ = os.Remove(filepath.Join(dir, names[0]))
		names = names[1:]
	}
	return dst, nil
}

// SaveEmbedding stores the vector of one memory.
func (s *Store) SaveEmbedding(ctx context.Context, seq int64, scopeID, model string, vector []byte, dims int) error {
	return s.write(ctx, func(w wx) error {
		_, err := w.exec(qEmbeddingUpsert, seq, scopeID, model, dims, vector, now())
		return err
	})
}

// Embedding is one stored vector.
type Embedding struct {
	Seq     int64
	ScopeID string
	Model   string
	Dims    int
	Vector  []byte
}

// LoadEmbeddings reads every stored vector for the in-memory index.
func (s *Store) LoadEmbeddings(ctx context.Context) ([]Embedding, error) {
	rows, err := s.reader(ctx).query(qEmbeddingAll)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Embedding
	for rows.Next() {
		var e Embedding
		if err := rows.Scan(&e.Seq, &e.ScopeID, &e.Model, &e.Dims, &e.Vector); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// DeleteEmbedding removes a vector after a hard delete that did not cascade.
func (s *Store) DeleteEmbedding(ctx context.Context, seq int64) error {
	return s.write(ctx, func(w wx) error {
		_, err := w.exec(qEmbeddingDelete, seq)
		return err
	})
}

// AdminScope is the all-projects scope of the UI admin, for background jobs.
func (s *Store) AdminScope(ctx context.Context) (auth.ScopeSet, error) {
	id, err := s.AdminIdentity(ctx)
	if err != nil {
		return auth.ScopeSet{}, err
	}
	g, projects, err := s.ScopeRefs(ctx)
	if err != nil {
		return auth.ScopeSet{}, err
	}
	return auth.Resolve(id.Principal, id.Grants, g, projects, auth.Request{AllProjects: true})
}
