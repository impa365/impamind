package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"impamind/internal/auth"
)

const reviewCols = `id, scope_id, type, dedupe_key, memory_ids, payload, status, created_at, COALESCE(resolved_by,''), COALESCE(resolution,''), COALESCE(resolved_at,'')`

var (
	qReviewUpsert = scoped("review.upsert", `INSERT INTO review_items(id, scope_id, type, dedupe_key, memory_ids, payload, status, created_at)
		SELECT ?1, ?2, ?3, ?4, ?5, ?6, 'open', ?7 WHERE {{in:?2}}
		ON CONFLICT(dedupe_key) DO UPDATE SET payload = excluded.payload, memory_ids = excluded.memory_ids WHERE review_items.status = 'open'`)
	qReviewClose = scoped("review.close_key", `UPDATE review_items SET status = ?2, resolution = ?3, resolved_by = ?4, resolved_at = ?5
		WHERE dedupe_key = ?1 AND status = 'open' AND {{scope}}`)
	qReviewList = scoped("review.list", `SELECT `+reviewCols+` FROM review_items WHERE {{scope}} AND (? = '' OR type = ?) AND status = ? ORDER BY created_at DESC LIMIT ?`)
	qReviewGet  = scoped("review.get", `SELECT `+reviewCols+` FROM review_items WHERE id = ? AND {{scope}}`)
	qReviewSet  = scoped("review.resolve", `UPDATE review_items SET status = ?2, resolution = ?3, resolved_by = ?4, resolved_at = ?5
		WHERE id = ?1 AND status = 'open' AND {{scope}}`)
	qReviewCount = scoped("review.count_open", `SELECT type, count(*) FROM review_items WHERE {{scope}} AND status = 'open' GROUP BY type`)
)

var ReviewTypes = []string{"proposed", "conflict", "duplicate", "stale", "expired", "tier_suggestion"}

func scanReview(r rowScanner) (ReviewItem, error) {
	var it ReviewItem
	var mids, payload string
	err := r.Scan(&it.ID, &it.ScopeID, &it.Type, &it.DedupeKey, &mids, &payload, &it.Status, &it.CreatedAt, &it.ResolvedBy, &it.Resolution, &it.ResolvedAt)
	_ = json.Unmarshal([]byte(mids), &it.MemoryIDs)
	if it.MemoryIDs == nil {
		it.MemoryIDs = []string{}
	}
	it.Payload = json.RawMessage(payload)
	return it, err
}

func (w wx) upsertReview(scopeID, typ, key string, memoryIDs []string, payload []byte) error {
	if payload == nil {
		payload = []byte("{}")
	}
	mids, _ := json.Marshal(memoryIDs)
	_, err := w.exec(qReviewUpsert, newID(), scopeID, typ, key, string(mids), string(payload), now(), ids([]string{scopeID}))
	return err
}

func (w wx) closeReview(scopeID, key, status, resolution, by string) error {
	_, err := w.exec(qReviewClose, key, status, resolution, by, now(), ids([]string{scopeID}))
	return err
}

// AddReviewItem files an item in the review inbox of a scope the caller can write to.
func (s *Store) AddReviewItem(ctx context.Context, ss auth.ScopeSet, scopeID, typ, key string, memoryIDs []string, payload any) error {
	if !ss.CanPropose(scopeID) {
		return denyWrite(ss, scopeID)
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	return s.write(ctx, func(w wx) error { return w.upsertReview(scopeID, typ, key, memoryIDs, b) })
}

// ListReview lists review items in the caller's readable scopes.
func (s *Store) ListReview(ctx context.Context, ss auth.ScopeSet, typ, status string, limit int) ([]ReviewItem, error) {
	if status == "" {
		status = "open"
	}
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	items, err := collectRows(s.reader(ctx).q(qReviewList, readable(ss), typ, typ, status, limit), scanReview)
	if err != nil {
		return nil, err
	}
	var all []string
	for _, it := range items {
		all = append(all, it.MemoryIDs...)
	}
	ms, err := s.GetMemories(ctx, ss, all)
	if err != nil {
		return nil, err
	}
	byID := map[string]Memory{}
	for _, m := range ms {
		byID[m.ID] = m
	}
	for i := range items {
		for _, id := range items[i].MemoryIDs {
			if m, ok := byID[id]; ok {
				items[i].Memories = append(items[i].Memories, m)
			}
		}
	}
	if items == nil {
		items = []ReviewItem{}
	}
	return items, nil
}

func (s *Store) GetReviewItem(ctx context.Context, ss auth.ScopeSet, id string) (ReviewItem, error) {
	it, err := scanReview(s.reader(ctx).row(qReviewGet, id, readable(ss)))
	if err != nil {
		return it, notFound(err)
	}
	ms, err := s.GetMemories(ctx, ss, it.MemoryIDs)
	it.Memories = ms
	return it, err
}

// ResolveReviewItem closes an item. status is resolved or dismissed.
func (s *Store) ResolveReviewItem(ctx context.Context, ss auth.ScopeSet, id, status, resolution string) error {
	if status != "resolved" && status != "dismissed" {
		return fmt.Errorf("%w: status must be resolved or dismissed", ErrInvalid)
	}
	it, err := s.GetReviewItem(ctx, ss, id)
	if err != nil {
		return err
	}
	if !ss.CanPropose(it.ScopeID) {
		return denyWrite(ss, it.ScopeID)
	}
	var n int64
	err = s.write(ctx, func(w wx) (err error) {
		n, err = w.execN(qReviewSet, id, status, clip(resolution, 500), ss.Principal().ID, now(), ids([]string{it.ScopeID}))
		return err
	})
	if err == nil && n == 0 {
		return fmt.Errorf("%w: review item already closed", ErrConflict)
	}
	return err
}

// ReviewCounts returns open item counts by type over readable scopes.
func (s *Store) ReviewCounts(ctx context.Context, ss auth.ScopeSet, scopeIDs []string) (map[string]int, error) {
	allowed := []string{}
	for _, id := range scopeIDs {
		if ss.CanRead(id) {
			allowed = append(allowed, id)
		}
	}
	rows, err := s.reader(ctx).query(qReviewCount, ids(allowed))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var t string
		var n int
		if err := rows.Scan(&t, &n); err != nil {
			return nil, err
		}
		out[strings.TrimSpace(t)] = n
	}
	return out, rows.Err()
}
