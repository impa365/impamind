package protocol

import (
	"context"
	"encoding/json"
	"errors"
	"path"
	"sort"
	"strings"
	"sync"
	"time"

	"impamind/internal/auth"
	"impamind/internal/core"
	"impamind/internal/store"
)

const ProtocolText = "recall at start; observe or decide when you learn or decide; checkpoint before pausing; handoff when done"

// Pack limits (B1: up to 20 global, 15 decisions, 1 checkpoint, 5 hits).
const (
	recallGlobal    = 20
	recallDecisions = 15
	recallTodos     = 10
	recallHits      = 5
	recallProposed  = 5
	defaultBudget   = 1500
)

type recallCache struct {
	mu      sync.Mutex
	key     string
	version int64
	at      time.Time
	items   []store.Memory
}

func newRecallCache() *recallCache { return &recallCache{} }

func (c *recallCache) get(globalID string, version int64) ([]store.Memory, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.key == globalID && c.version == version && time.Since(c.at) < time.Minute && c.items != nil {
		return c.items, true
	}
	return nil, false
}

func (c *recallCache) put(globalID string, version int64, items []store.Memory) {
	c.mu.Lock()
	defer c.mu.Unlock()
	cp := make([]store.Memory, len(items))
	copy(cp, items)
	c.key, c.version, c.at, c.items = globalID, version, time.Now(), cp
}

// Recall starts or resumes a work session and returns a budgeted pack:
// global preferences, current project decisions, open todos, the last
// checkpoint, results relevant to the task and alerts.
func (s *Service) Recall(ctx context.Context, call Call, in RecallIn) (RecallOut, error) {
	ss := call.SS
	out := RecallOut{Protocol: ProtocolText, Global: []Item{}, Decisions: []Item{}, Todos: []Item{}, Relevant: []Item{}, Pending: []Item{}}
	hint := strings.TrimSpace(in.ProjectHint)
	if !ss.HasProject() && hint != "" {
		slug, ok, err := s.St.LookupAlias(ctx, hint)
		if err != nil {
			return out, err
		}
		if ok && call.Rescope != nil {
			if nss, err := call.Rescope(slug); err == nil {
				ss = nss
				call.SS = nss
			}
		}
	}
	if !ss.HasProject() {
		cands, err := s.projectCandidates(ctx, ss, hint)
		if err != nil {
			return out, err
		}
		out.ProjectUnresolved = &Unresolved{Hint: hint, Candidates: cands,
			Message: "No project is bound to this call. Ask the user which project this is, or whether to create it; then pass project (or set X-Impamind-Project)."}
	} else if hint != "" && ss.CanWrite(ss.Project().ID) {
		if _, err := s.St.AddAlias(ctx, ss, ss.Project().ID, "", hint); err != nil && !errors.Is(err, store.ErrConflict) && !errors.Is(err, store.ErrInvalid) {
			return out, err
		}
	}

	now := time.Now().UTC()
	var sess store.WorkSession
	if ss.HasProject() {
		p := ss.Project()
		out.Project = &ProjectRef{ID: p.ID, Slug: p.Slug}
		var err error
		sess, err = s.openSession(ctx, call, in, &out.Warnings)
		if err != nil {
			return out, err
		}
		out.SessionID = sess.ID
	}

	gid := ss.GlobalID()
	gver := s.St.ScopeVersion(gid)
	cachedGlobal, hit := s.recall.get(gid, gver)
	q := store.RecallQuery{
		GlobalID: gid, ProjectID: ss.Project().ID, SessionID: sess.ID,
		Text:          recallText(in),
		GlobalLimit:   recallGlobal,
		DecisionLimit: recallDecisions, TodoLimit: recallTodos, HitLimit: recallHits, ProposedLimit: recallProposed,
	}
	if hit {
		q.GlobalLimit = 0
	}
	data, err := s.St.RecallSnapshot(ctx, ss, q)
	if err != nil {
		return out, err
	}
	if hit {
		data.Global = cachedGlobal
	} else if ss.CanRead(gid) {
		s.recall.put(gid, gver, data.Global)
	}

	sl := slugs{gid: "global"}
	if ss.HasProject() {
		sl[ss.Project().ID] = ss.Project().Slug
	}
	seen := map[string]bool{}
	for _, m := range data.Global {
		if !isExpired(m, now) {
			out.Global = append(out.Global, project(m, sl, 240, now))
			seen[m.ID] = true
		}
	}
	for _, m := range data.Decisions {
		it := project(m, sl, 400, now)
		if b, ok := data.Superseded[m.SupersedesID]; ok && m.SupersedesID != "" {
			b.Reason = m.SupersedeReason
			it.Supersedes = &b
		}
		out.Decisions = append(out.Decisions, it)
		seen[m.ID] = true
	}
	for _, m := range data.Todos {
		out.Todos = append(out.Todos, project(m, sl, 160, now))
		seen[m.ID] = true
	}
	if data.Checkpoint != nil {
		out.Checkpoint = checkpointView(*data.Checkpoint)
		seen[data.Checkpoint.ID] = true
	}
	type scored struct {
		m store.Memory
		s float64
	}
	var hits []scored
	worst := 0.0
	for _, h := range data.Hits {
		if -h.Rank > worst {
			worst = -h.Rank
		}
	}
	for _, h := range data.Hits {
		if seen[h.Memory.ID] || h.Memory.Kind == "checkpoint" || isExpired(h.Memory, now) {
			continue
		}
		rel := 1.0
		if worst > 0 {
			rel = (-h.Rank) / worst
		}
		hits = append(hits, scored{h.Memory, core.Score(rel, h.Memory, now)})
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].s > hits[j].s })
	for i, h := range hits {
		if i == recallHits {
			break
		}
		it := project(h.m, sl, 300, now)
		it.Score = round3(h.s)
		out.Relevant = append(out.Relevant, it)
	}
	for _, m := range data.Proposed {
		it := project(m, sl, 160, now)
		if b, ok := data.Superseded[m.SupersedesID]; ok && m.SupersedesID != "" {
			b.Reason = m.SupersedeReason
			it.Supersedes = &b
		}
		out.Pending = append(out.Pending, it)
	}
	out.Alerts = Alerts{ProposedPending: data.ProposedCount, Stale: data.StaleCount, Conflicts: data.ConflictCount}
	budget := in.BudgetTokens
	if budget <= 0 {
		budget = defaultBudget
	}
	if budget < 300 {
		budget = 300
	}
	if budget > 8000 {
		budget = 8000
	}
	fitBudget(&out, budget)
	return out, nil
}

func round3(f float64) float64 { return float64(int(f*1000+0.5)) / 1000 }

func recallText(in RecallIn) string {
	var b strings.Builder
	b.WriteString(in.Task)
	b.WriteByte(' ')
	b.WriteString(in.Query)
	for _, f := range in.Files {
		base := path.Base(strings.ReplaceAll(f, "\\", "/"))
		base = strings.TrimSuffix(base, path.Ext(base))
		b.WriteByte(' ')
		b.WriteString(strings.NewReplacer("_", " ", "-", " ", ".", " ").Replace(base))
	}
	return b.String()
}

func (s *Service) openSession(ctx context.Context, call Call, in RecallIn, warnings *[]string) (store.WorkSession, error) {
	ss := call.SS
	if in.SessionID != "" {
		w, err := s.St.SessionForWrite(ctx, ss, in.SessionID)
		switch {
		case err == nil:
			if err := s.St.UpdateSessionTask(ctx, ss, w.ID, in.Task, in.Files, "active"); err != nil {
				return w, err
			}
			if err := s.St.BumpSession(ctx, ss, w.ID, store.SessionDelta{Recalled: true}); err != nil {
				return w, err
			}
			return w, nil
		case errors.Is(err, store.ErrClosed):
			*warnings = append(*warnings, "session "+in.SessionID+" is closed; a new session was started")
		default:
			return store.WorkSession{}, err
		}
	}
	agent := in.Agent
	if agent == "" {
		agent = call.Agent
	}
	model := in.Model
	if model == "" {
		model = call.Model
	}
	return s.St.StartSession(ctx, ss, store.NewSession{Client: call.Client, Agent: agent, Model: model, Task: in.Task, Files: in.Files, Recalled: true})
}

func (s *Service) projectCandidates(ctx context.Context, ss auth.ScopeSet, hint string) ([]ProjectRef, error) {
	scopes, err := s.St.ListScopes(ctx, ss)
	if err != nil {
		return nil, err
	}
	h := strings.ToLower(lastSeg(hint))
	type c struct {
		r ProjectRef
		s int
	}
	var list []c
	for _, sc := range scopes {
		if sc.Kind != "project" || sc.Hidden {
			continue
		}
		score := 0
		slug, name := strings.ToLower(sc.Slug), strings.ToLower(sc.Name)
		switch {
		case h != "" && (slug == h || name == h):
			score = 3
		case h != "" && (strings.Contains(slug, h) || strings.Contains(h, slug) || strings.Contains(name, h)):
			score = 2
		}
		list = append(list, c{ProjectRef{ID: sc.ID, Slug: sc.Slug, Name: sc.Name}, score})
	}
	sort.SliceStable(list, func(i, j int) bool { return list[i].s > list[j].s })
	out := []ProjectRef{}
	for i, x := range list {
		if i == 10 {
			break
		}
		out = append(out, x.r)
	}
	return out, nil
}

func lastSeg(h string) string {
	h = strings.TrimSuffix(strings.TrimSuffix(strings.ReplaceAll(strings.TrimSpace(h), "\\", "/"), "/"), ".git")
	if i := strings.LastIndexAny(h, "/:"); i >= 0 {
		return h[i+1:]
	}
	return h
}

type checkpointDetails struct {
	Task      string   `json:"task"`
	Status    string   `json:"status"`
	Files     []string `json:"files"`
	Errors    []string `json:"errors"`
	Tests     []string `json:"tests"`
	NextSteps []string `json:"next_steps"`
	Notes     string   `json:"notes"`
}

func checkpointView(m store.Memory) *CheckpointView {
	var d checkpointDetails
	_ = json.Unmarshal(m.Details, &d)
	return &CheckpointView{ID: m.ID, SessionID: m.SessionID, Task: d.Task, Status: d.Status, Files: d.Files, Errors: d.Errors,
		Tests: d.Tests, NextSteps: d.NextSteps, Notes: d.Notes, Agent: m.CreatedByAgent, ChangedAt: m.UpdatedAt, Version: m.Version}
}

// fitBudget shrinks the pack until its JSON fits budget*4 bytes, dropping the
// lowest-priority content first: relevant, pending, todos, decisions, global.
func fitBudget(out *RecallOut, budget int) {
	limit := budget * 4
	size := func() int {
		b, _ := json.Marshal(out)
		return len(b)
	}
	out.Budget = BudgetInfo{Tokens: budget}
	n := size()
	if n <= limit {
		out.Budget.Used = n / 4
		return
	}
	out.Budget.Truncated = true
	shrink := func(items []Item, max int) {
		for i := range items {
			if c, cut := clipText(items[i].Content, max); cut || max == 0 {
				if max == 0 {
					c = ""
				}
				items[i].Content, items[i].Truncated = c, true
			}
		}
	}
	steps := []func(){
		func() { shrink(out.Relevant, 160); shrink(out.Pending, 0); shrink(out.Todos, 0) },
		func() { shrink(out.Global, 120); shrink(out.Decisions, 220) },
		func() {
			if out.Checkpoint != nil && len(out.Checkpoint.Notes) > 200 {
				out.Checkpoint.Notes, _ = clipText(out.Checkpoint.Notes, 200)
			}
		},
	}
	for _, st := range steps {
		st()
		if n = size(); n <= limit {
			out.Budget.Used = n / 4
			return
		}
	}
	drops := []*[]Item{&out.Relevant, &out.Pending, &out.Todos}
	for _, d := range drops {
		for len(*d) > 0 {
			*d = (*d)[:len(*d)-1]
			if n = size(); n <= limit {
				out.Budget.Used = n / 4
				return
			}
		}
	}
	shrink(out.Decisions, 0)
	shrink(out.Global, 0)
	for _, d := range []*[]Item{&out.Decisions, &out.Global} {
		for len(*d) > 5 {
			*d = (*d)[:len(*d)-1]
			if n = size(); n <= limit {
				out.Budget.Used = n / 4
				return
			}
		}
	}
	out.Budget.Used = size() / 4
}
