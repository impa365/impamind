package protocol

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"time"

	"impamind/internal/core"
	"impamind/internal/store"
)

// Read returns one memory, optionally with its full history.
func (s *Service) Read(ctx context.Context, call Call, in ReadIn) (ReadOut, error) {
	var out ReadOut
	if strings.TrimSpace(in.ID) == "" {
		return out, newErr(http.StatusBadRequest, "invalid", "id is required")
	}
	m, err := s.St.GetMemory(ctx, call.SS, in.ID)
	if err != nil {
		return out, err
	}
	now := time.Now().UTC()
	out.Memory = m
	out.Scope = s.scopeName(ctx, call, m.ScopeID)
	out.Stale, out.Expired = isStale(m, now), isExpired(m, now)
	if out.Links, err = s.St.LinksFor(ctx, call.SS, []string{m.ID}); err != nil {
		return out, err
	}
	if in.IncludeHistory {
		h, err := s.St.History(ctx, call.SS, m.ID)
		if err != nil {
			return out, err
		}
		out.History = &h
	}
	return out, nil
}

func (s *Service) scopeName(ctx context.Context, call Call, id string) string {
	if id == call.SS.GlobalID() {
		return "global"
	}
	if call.SS.HasProject() && call.SS.Project().ID == id {
		return call.SS.Project().Slug
	}
	_, refs, err := s.St.ScopeRefs(ctx)
	if err == nil {
		for _, r := range refs {
			if r.ID == id {
				return r.Slug
			}
		}
	}
	return id
}

// ScopeIDs resolves the scope selector (project, global, both) to ids.
func (s *Service) ScopeIDs(ctx context.Context, call Call, scope string) ([]string, error) {
	return s.scopeFilter(call, scope)
}

func (s *Service) scopeFilter(call Call, scope string) ([]string, error) {
	ss := call.SS
	switch strings.ToLower(strings.TrimSpace(scope)) {
	case "global":
		return []string{ss.GlobalID()}, nil
	case "project":
		if !ss.HasProject() {
			return nil, errProjectRequired()
		}
		return []string{ss.Project().ID}, nil
	case "", "both", "all":
		return ss.ReadIDs(), nil
	}
	return nil, newErr(http.StatusBadRequest, "invalid", "scope must be project, global or both")
}

func (s *Service) slugMap(ctx context.Context, call Call) slugs {
	sl := slugs{call.SS.GlobalID(): "global"}
	if _, refs, err := s.St.ScopeRefs(ctx); err == nil {
		for _, r := range refs {
			if call.SS.CanRead(r.ID) {
				sl[r.ID] = r.Slug
			}
		}
	}
	return sl
}

// Search runs a full-text (or hybrid, when RAG is on) query over the call's scopes.
func (s *Service) Search(ctx context.Context, call Call, in SearchIn) (SearchOut, error) {
	out := SearchOut{Items: []Item{}, Mode: "fts"}
	if strings.TrimSpace(in.Query) == "" {
		return out, newErr(http.StatusBadRequest, "invalid", "query is required")
	}
	scopes, err := s.scopeFilter(call, in.Scope)
	if err != nil {
		return out, err
	}
	if in.Kind != "" && !store.ValidKind(in.Kind) {
		return out, newErr(http.StatusBadRequest, "invalid", "unknown kind %q", in.Kind)
	}
	limit := in.Limit
	if limit <= 0 || limit > 100 {
		limit = 10
	}
	statuses := []string{"active", "expired"}
	if in.IncludeProposed {
		statuses = append(statuses, "proposed")
	}
	if in.IncludeSuperseded {
		statuses = append(statuses, "superseded", "deprecated", "archived")
	}
	hits, err := s.St.SearchFTS(ctx, call.SS, in.Query, store.SearchFilter{ScopeIDs: scopes, Statuses: statuses, Kind: in.Kind, Limit: limit * 3})
	if err != nil {
		return out, err
	}
	now := time.Now().UTC()
	type cand struct {
		m   store.Memory
		rrf float64
	}
	byID := map[string]*cand{}
	var order []string
	for i, h := range hits {
		byID[h.Memory.ID] = &cand{m: h.Memory, rrf: 1 / float64(60+i+1)}
		order = append(order, h.Memory.ID)
	}
	if s.Index != nil {
		vs, err := s.Index.Search(ctx, scopes, in.Query, limit*3)
		if err == nil && len(vs) > 0 {
			out.Mode = "hybrid"
			ms, err := s.memoriesBySeq(ctx, call, vs, 0)
			if err != nil {
				return out, err
			}
			allowed := map[string]bool{}
			for _, st := range statuses {
				allowed[st] = true
			}
			for i, m := range ms {
				if !allowed[m.Status] || (in.Kind != "" && m.Kind != in.Kind) {
					continue
				}
				if c, ok := byID[m.ID]; ok {
					c.rrf += 1 / float64(60+i+1)
				} else {
					byID[m.ID] = &cand{m: m, rrf: 1 / float64(60+i+1)}
					order = append(order, m.ID)
				}
			}
		}
	}
	type scored struct {
		m store.Memory
		s float64
	}
	var list []scored
	best := 0.0
	for _, id := range order {
		if c := byID[id]; c.rrf > best {
			best = c.rrf
		}
	}
	for _, id := range order {
		c := byID[id]
		rel := c.rrf / best
		list = append(list, scored{c.m, core.Score(rel, c.m, now)})
	}
	sort.SliceStable(list, func(i, j int) bool { return list[i].s > list[j].s })
	sl := s.slugMap(ctx, call)
	var shown []store.Memory
	for i, x := range list {
		if i == limit {
			break
		}
		it := project(x.m, sl, 400, now)
		it.Score = round3(x.s)
		out.Items = append(out.Items, it)
		shown = append(shown, x.m)
	}
	s.St.TouchAccess(shown)
	return out, nil
}

// memoriesBySeq loads vector hits in rank order, dropping those under minScore.
func (s *Service) memoriesBySeq(ctx context.Context, call Call, vs []VectorHit, minScore float64) ([]store.Memory, error) {
	var seqs []int64
	for _, v := range vs {
		if v.Score >= minScore {
			seqs = append(seqs, v.Seq)
		}
	}
	ms, err := s.St.GetMemoriesBySeq(ctx, call.SS, seqs)
	if err != nil {
		return nil, err
	}
	pos := map[int64]int{}
	for i, q := range seqs {
		pos[q] = i
	}
	sort.SliceStable(ms, func(i, j int) bool { return pos[ms[i].Seq] < pos[ms[j].Seq] })
	return ms, nil
}

// Review lists open review items of the call's scopes.
func (s *Service) Review(ctx context.Context, call Call, in ReviewIn) (ReviewOut, error) {
	out := ReviewOut{Items: []ReviewView{}}
	if in.Mode != "" {
		ok := false
		for _, t := range store.ReviewTypes {
			ok = ok || t == in.Mode
		}
		if in.Mode == "conflicts" {
			in.Mode, ok = "conflict", true
		}
		if in.Mode == "duplicates" {
			in.Mode, ok = "duplicate", true
		}
		if in.Mode == "tier_suggestions" {
			in.Mode, ok = "tier_suggestion", true
		}
		if !ok {
			return out, newErr(http.StatusBadRequest, "invalid", "unknown review mode %q", in.Mode)
		}
	}
	scopes, err := s.scopeFilter(call, in.Scope)
	if err != nil {
		return out, err
	}
	want := map[string]bool{}
	for _, id := range scopes {
		want[id] = true
	}
	limit := in.Limit
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	items, err := s.St.ListReview(ctx, call.SS, in.Mode, "open", limit*2)
	if err != nil {
		return out, err
	}
	sl := s.slugMap(ctx, call)
	now := time.Now().UTC()
	for _, it := range items {
		if !want[it.ScopeID] {
			continue
		}
		v := ReviewView{ID: it.ID, Type: it.Type, Scope: sl.of(it.ScopeID), CreatedAt: it.CreatedAt, Memories: []Item{}}
		var payload any
		if json.Unmarshal(it.Payload, &payload) == nil {
			v.Payload = payload
		}
		kind, policy, _ := s.St.ScopePolicy(ctx, call.SS, it.ScopeID)
		for _, m := range it.Memories {
			v.Memories = append(v.Memories, project(m, sl, 300, now))
			if store.Protected(kind, policy, m.Kind) {
				v.Protected = true
			}
		}
		out.Items = append(out.Items, v)
		if len(out.Items) == limit {
			break
		}
	}
	return out, nil
}

// ResolveReview applies an action to a review item. The agent profile never
// approves; protected items need the UI or a human token on /mcp/admin.
func (s *Service) ResolveReview(ctx context.Context, call Call, in ResolveIn) (ResolveOut, error) {
	out := ResolveOut{ItemID: in.ItemID, Action: in.Action}
	it, err := s.St.GetReviewItem(ctx, call.SS, in.ItemID)
	if err != nil {
		return out, err
	}
	if it.Status != "open" {
		return out, newErr(http.StatusConflict, "conflict", "review item is already %s", it.Status)
	}
	note := strings.TrimSpace(in.Note)
	first := func() (store.Memory, error) {
		if len(it.Memories) == 0 {
			return store.Memory{}, newErr(http.StatusConflict, "conflict", "the memories of this item are gone")
		}
		return it.Memories[0], nil
	}
	resolution := in.Action
	switch in.Action {
	case "approve":
		if call.Profile == ProfileAgent {
			return out, newErr(http.StatusForbidden, "forbidden", "agents cannot approve").withHint("ask the user to approve it in the impamind UI")
		}
		m, err := first()
		if err != nil {
			return out, err
		}
		if it.Type == "tier_suggestion" {
			var p struct {
				To int `json:"to"`
			}
			_ = json.Unmarshal(it.Payload, &p)
			if !call.human() {
				return out, newErr(http.StatusForbidden, "forbidden", "tier changes need a human")
			}
			if _, err := s.St.UpdateMemory(ctx, call.SS, m.ID, store.MemoryPatch{Tier: &p.To, Reason: "tier suggestion accepted: " + note}); err != nil {
				return out, err
			}
			break
		}
		approved, err := s.St.Approve(ctx, call.SS, m.ID, store.ApproveInput{Note: note})
		if err != nil {
			return out, err
		}
		s.index(approved)
	case "reject":
		m, err := first()
		if err != nil {
			return out, err
		}
		if m.Status == "proposed" {
			if _, err := s.St.Reject(ctx, call.SS, m.ID, note); err != nil {
				return out, err
			}
		}
	case "archive":
		for _, m := range it.Memories {
			if m.Status == "active" {
				if _, err := s.St.Transition(ctx, call.SS, m.ID, "archived", note); err != nil {
					return out, err
				}
			}
		}
	case "reconfirm":
		for _, m := range it.Memories {
			if m.Status != "active" {
				continue
			}
			ra := ""
			if d := core.ReviewAfter(m.Kind); d > 0 {
				ra = store.FormatTime(time.Now().Add(d))
			}
			if _, err := s.St.UpdateMemory(ctx, call.SS, m.ID, store.MemoryPatch{ReviewAfter: &ra, Reason: "reconfirmed: " + note}); err != nil {
				return out, err
			}
			if _, err := s.St.Confirm(ctx, call.SS, m.ID, 0, nil, call.Agent); err != nil {
				return out, err
			}
		}
	case "merge":
		if in.TargetID == "" {
			return out, newErr(http.StatusBadRequest, "invalid", "merge needs target_id (the memory to keep)")
		}
		keep := false
		for _, m := range it.Memories {
			keep = keep || m.ID == in.TargetID
		}
		if !keep {
			return out, newErr(http.StatusBadRequest, "invalid", "target_id must be one of the item's memories")
		}
		for _, m := range it.Memories {
			if m.ID == in.TargetID || m.Status != "active" {
				continue
			}
			if _, err := s.St.Transition(ctx, call.SS, m.ID, "archived", "merged into "+in.TargetID); err != nil {
				return out, err
			}
			if _, err := s.St.Confirm(ctx, call.SS, in.TargetID, 0, nil, call.Agent); err != nil {
				return out, err
			}
		}
	case "supersede":
		if in.TargetID == "" {
			return out, newErr(http.StatusBadRequest, "invalid", "supersede needs target_id (the replacement)")
		}
		for _, m := range it.Memories {
			if m.ID == in.TargetID || m.Status != "active" {
				continue
			}
			if _, _, err := s.St.SupersedeWithExisting(ctx, call.SS, m.ID, in.TargetID, nonEmpty(note, "resolved in review")); err != nil {
				return out, err
			}
		}
	case "dismiss":
		if err := s.St.ResolveReviewItem(ctx, call.SS, it.ID, "dismissed", note); err != nil {
			return out, err
		}
		out.Result, out.Message = "dismissed", "item dismissed"
		return out, nil
	default:
		return out, newErr(http.StatusBadRequest, "invalid", "unknown action %q", in.Action)
	}
	if err := s.St.ResolveReviewItem(ctx, call.SS, it.ID, "resolved", resolution+": "+note); err != nil && AsError(err).Code != "conflict" {
		return out, err
	}
	s.audit(call, "review_resolve", it.ScopeID, 200, in.Action)
	out.Result, out.Message = "resolved", "item resolved with "+in.Action
	return out, nil
}

func nonEmpty(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}
