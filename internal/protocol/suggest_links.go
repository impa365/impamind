package protocol

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"

	"impamind/internal/auth"
	"impamind/internal/core"
	"impamind/internal/store"
)

// SimilarThreshold is below NearDuplicate (0.72); pairs above it get a similar link.
// Tuned for dense Obsidian-like maps on small project banks.
const SimilarThreshold = 0.14

// OrphanSimilarThreshold is used on a second pass to attach degree-0 memories.
const OrphanSimilarThreshold = 0.08

// SimilarMaxPerMemory caps auto-created similar edges per source memory.
const SimilarMaxPerMemory = 16

// OrphanMaxLinks caps links created for an orphan in the second pass.
const OrphanMaxLinks = 3

// SuggestLinksOut is the result of a suggest-links scan.
type SuggestLinksOut struct {
	MemoryID   string  `json:"memory_id"`
	Created    int     `json:"created"`
	Candidates int     `json:"candidates"`
	Threshold  float64 `json:"threshold"`
}

// SuggestScopeOut is a scope-wide backfill result.
type SuggestScopeOut struct {
	Scope      string `json:"scope"`
	Scanned    int    `json:"scanned"`
	Created    int    `json:"created"`
	Threshold  float64 `json:"threshold"`
}

type scoredCand struct {
	id    string
	score float64
}

// SuggestSimilarLinks finds same-scope memories via FTS (+ list fallback, + optional embeddings)
// and persists idempotent links labeled "similar" when Jaccard ≥ threshold.
func (s *Service) SuggestSimilarLinks(ctx context.Context, call Call, memoryID string, limit int) (SuggestLinksOut, error) {
	return s.suggestSimilarLinksAt(ctx, call, memoryID, limit, SimilarThreshold)
}

func (s *Service) suggestSimilarLinksAt(ctx context.Context, call Call, memoryID string, limit int, threshold float64) (SuggestLinksOut, error) {
	out := SuggestLinksOut{MemoryID: memoryID, Threshold: threshold}
	if limit <= 0 || limit > SimilarMaxPerMemory {
		limit = SimilarMaxPerMemory
	}
	m, err := s.St.GetMemory(ctx, call.SS, memoryID)
	if err != nil {
		return out, err
	}
	if !call.SS.CanWrite(m.ScopeID) {
		return out, newErr(http.StatusForbidden, "forbidden", "need write on the memory scope to create links")
	}
	cands, err := s.similarCandidates(ctx, call, m, limit*6)
	if err != nil {
		return out, err
	}
	out.Candidates = len(cands)
	sort.Slice(cands, func(i, j int) bool { return cands[i].score > cands[j].score })
	created := 0
	for _, c := range cands {
		if created >= limit {
			break
		}
		if c.score < threshold {
			continue
		}
		src, dst := m.ID, c.id
		if src > dst {
			src, dst = dst, src
		}
		_, err := s.St.CreateLink(ctx, call.SS, src, dst, store.SimilarLinkLabel)
		if err != nil {
			if errors.Is(err, store.ErrConflict) {
				continue
			}
			return out, err
		}
		created++
	}
	out.Created = created
	return out, nil
}

func (s *Service) similarCandidates(ctx context.Context, call Call, m store.Memory, want int) ([]scoredCand, error) {
	if want <= 0 {
		want = 16
	}
	seen := map[string]float64{}
	text := strings.TrimSpace(m.Title + " " + m.Content)
	hits, err := s.St.SearchFTS(ctx, call.SS, text, store.SearchFilter{
		ScopeIDs: []string{m.ScopeID},
		Statuses: []string{"active", "proposed"},
		Limit:    want + 8,
	})
	if err != nil {
		return nil, err
	}
	for _, h := range hits {
		if h.Memory.ID == m.ID || h.Memory.ScopeID != m.ScopeID {
			continue
		}
		score := core.Similarity(m.Title, m.Content, h.Memory.Title, h.Memory.Content)
		score = boostTitleOverlap(score, m.Title, h.Memory.Title)
		score = boostSharedTags(score, m.Tags, h.Memory.Tags)
		if score > seen[h.Memory.ID] {
			seen[h.Memory.ID] = score
		}
	}
	// Fallback: FTS often returns nothing for short/PT titles (stopwords). Score peers directly.
	if len(seen) < want {
		list, _, err := s.St.ListMemories(ctx, call.SS, store.MemoryFilter{
			ScopeIDs: []string{m.ScopeID},
			Statuses: []string{"active", "proposed"},
			Limit:    250,
		})
		if err != nil {
			return nil, err
		}
		for _, other := range list {
			if other.ID == m.ID || other.Kind == "checkpoint" || other.Kind == "session_summary" {
				continue
			}
			score := core.Similarity(m.Title, m.Content, other.Title, other.Content)
			score = boostTitleOverlap(score, m.Title, other.Title)
			score = boostSharedTags(score, m.Tags, other.Tags)
			if score > seen[other.ID] {
				seen[other.ID] = score
			}
		}
	}
	if s.Index != nil {
		vhs, err := s.Index.Similar(ctx, m.ScopeID, text, want)
		if err == nil && len(vhs) > 0 {
			seqs := make([]int64, len(vhs))
			scoreBySeq := map[int64]float64{}
			for i, vh := range vhs {
				seqs[i] = vh.Seq
				scoreBySeq[vh.Seq] = vh.Score
			}
			others, err := s.St.GetMemoriesBySeq(ctx, call.SS, seqs)
			if err == nil {
				for _, other := range others {
					if other.ID == m.ID || other.ScopeID != m.ScopeID {
						continue
					}
					score := core.Similarity(m.Title, m.Content, other.Title, other.Content)
					score = boostTitleOverlap(score, m.Title, other.Title)
					score = boostSharedTags(score, m.Tags, other.Tags)
					if emb := scoreBySeq[other.Seq]; emb > 0 {
						score = score*0.85 + emb*0.15
					}
					if score > seen[other.ID] {
						seen[other.ID] = score
					}
				}
			}
		}
	}
	out := make([]scoredCand, 0, len(seen))
	for id, score := range seen {
		out = append(out, scoredCand{id: id, score: score})
	}
	return out, nil
}

func boostTitleOverlap(base float64, titleA, titleB string) float64 {
	ta, tb := core.Tokens(titleA), core.Tokens(titleB)
	if len(ta) == 0 || len(tb) == 0 {
		return base
	}
	tj := core.Jaccard(ta, tb)
	if tj > base {
		return tj
	}
	// Mild lift when titles share tokens even if bodies diverge.
	if tj >= 0.35 && base < SimilarThreshold {
		return SimilarThreshold + 0.01
	}
	return base
}

func boostSharedTags(base float64, a, b []string) float64 {
	if len(a) == 0 || len(b) == 0 {
		return base
	}
	set := make(map[string]struct{}, len(a))
	for _, t := range a {
		t = strings.ToLower(strings.TrimSpace(t))
		if t == "" || strings.HasPrefix(t, "topic:") {
			continue
		}
		set[t] = struct{}{}
	}
	shared := 0
	for _, t := range b {
		t = strings.ToLower(strings.TrimSpace(t))
		if _, ok := set[t]; ok {
			shared++
		}
	}
	if shared == 0 {
		return base
	}
	// One shared tag is enough to clear the dense threshold; more tags strengthen.
	lift := SimilarThreshold + 0.02*float64(shared)
	if lift > base {
		return lift
	}
	return base
}

// SuggestSimilarForScope rescans every active memory in one scope (UI backfill).
func (s *Service) SuggestSimilarForScope(ctx context.Context, call Call, scopeSlug string) (SuggestScopeOut, error) {
	out := SuggestScopeOut{Scope: scopeSlug, Threshold: SimilarThreshold}
	switch strings.ToLower(strings.TrimSpace(scopeSlug)) {
	case "all", "both", "todos":
		scopes, err := s.St.ListScopes(ctx, call.SS)
		if err != nil {
			return out, err
		}
		out.Scope = "all"
		for _, sc := range scopes {
			if !call.SS.CanWrite(sc.ID) {
				continue
			}
			scanned, created, err := s.suggestSimilarInScopeID(ctx, call, sc.ID)
			if err != nil {
				return out, err
			}
			out.Scanned += scanned
			out.Created += created
		}
		return out, nil
	case "", "global":
		out.Scope = "global"
		scanned, created, err := s.suggestSimilarInScopeID(ctx, call, call.SS.GlobalID())
		out.Scanned, out.Created = scanned, created
		return out, err
	case "project":
		if !call.SS.HasProject() {
			return out, errProjectRequired()
		}
		out.Scope = call.SS.Project().Slug
		scanned, created, err := s.suggestSimilarInScopeID(ctx, call, call.SS.Project().ID)
		out.Scanned, out.Created = scanned, created
		return out, err
	default:
		sc, err := s.ScopeBySlug(ctx, call, scopeSlug)
		if err != nil {
			return out, err
		}
		out.Scope = sc.Slug
		scanned, created, err := s.suggestSimilarInScopeID(ctx, call, sc.ID)
		out.Scanned, out.Created = scanned, created
		return out, err
	}
}

func (s *Service) suggestSimilarInScopeID(ctx context.Context, call Call, scopeID string) (scanned, created int, err error) {
	if !call.SS.CanWrite(scopeID) {
		return 0, 0, newErr(http.StatusForbidden, "forbidden", "need write on the scope")
	}
	if _, err := s.St.DeleteLinksByLabelPrefix(ctx, call.SS, scopeID, "topic:"); err != nil {
		// non-fatal
	}
	list, _, err := s.St.ListMemories(ctx, call.SS, store.MemoryFilter{
		ScopeIDs: []string{scopeID},
		Statuses: []string{"active", "proposed"},
		Limit:    500,
	})
	if err != nil {
		return 0, 0, err
	}
	ids := make([]string, 0, len(list))
	for _, m := range list {
		if m.Kind == "checkpoint" || m.Kind == "session_summary" {
			continue
		}
		ids = append(ids, m.ID)
		scanned++
		r, err := s.SuggestSimilarLinks(ctx, call, m.ID, SimilarMaxPerMemory)
		if err != nil {
			continue
		}
		created += r.Created
	}
	// Second pass: attach still-orphaned memories with a softer threshold.
	if len(ids) == 0 {
		return scanned, created, nil
	}
	links, err := s.St.LinksFor(ctx, call.SS, ids)
	if err != nil {
		return scanned, created, nil
	}
	linked := map[string]struct{}{}
	for _, l := range links {
		linked[l.SourceID] = struct{}{}
		linked[l.TargetID] = struct{}{}
	}
	for _, id := range ids {
		if _, ok := linked[id]; ok {
			continue
		}
		r, err := s.suggestSimilarLinksAt(ctx, call, id, OrphanMaxLinks, OrphanSimilarThreshold)
		if err != nil {
			continue
		}
		created += r.Created
		if r.Created > 0 {
			linked[id] = struct{}{}
		}
	}
	return scanned, created, nil
}

// SuggestSimilarBatch scans recent memories across readable scopes (job use).
func (s *Service) SuggestSimilarBatch(ctx context.Context, ss auth.ScopeSet, maxMemories int) (int, error) {
	if maxMemories <= 0 {
		maxMemories = 80
	}
	scopes, err := s.St.ListScopes(ctx, ss)
	if err != nil {
		return 0, err
	}
	call := Call{SS: ss, Profile: ProfileAdmin}
	created := 0
	scanned := 0
	perScope := maxMemories
	if len(scopes) > 1 {
		perScope = (maxMemories + len(scopes) - 1) / len(scopes)
		if perScope < 12 {
			perScope = 12
		}
	}
	for _, sc := range scopes {
		if scanned >= maxMemories {
			break
		}
		list, _, err := s.St.ListMemories(ctx, ss, store.MemoryFilter{
			ScopeIDs: []string{sc.ID},
			Statuses: []string{"active"},
			Limit:    perScope,
		})
		if err != nil {
			return created, err
		}
		for _, m := range list {
			if scanned >= maxMemories {
				break
			}
			if m.Kind == "checkpoint" || m.Kind == "session_summary" {
				continue
			}
			scanned++
			out, err := s.SuggestSimilarLinks(ctx, call, m.ID, SimilarMaxPerMemory)
			if err != nil {
				continue
			}
			created += out.Created
		}
	}
	return created, nil
}

// scheduleSuggestLinks runs a light async rescan after create/update.
func (s *Service) scheduleSuggestLinks(call Call, memoryID string) {
	if memoryID == "" {
		return
	}
	ss := call.SS
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
		defer cancel()
		_, _ = s.SuggestSimilarLinks(ctx, Call{SS: ss, Profile: call.Profile}, memoryID, SimilarMaxPerMemory)
	}()
}
