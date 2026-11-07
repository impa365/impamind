package protocol

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"impamind/internal/core"
	"impamind/internal/store"
)

const warnRecallMissing = "protocol_violation=recall_missing: call memory_recall at the start of a task and pass its session_id"

// writeSession loads the caller's session for a write, or creates one when the
// agent skipped recall (recording the violation).
func (s *Service) writeSession(ctx context.Context, call Call, id string, warnings *[]string) (store.WorkSession, error) {
	ss := call.SS
	if !ss.HasProject() {
		return store.WorkSession{}, nil
	}
	if id != "" {
		w, err := s.St.SessionForWrite(ctx, ss, id)
		if err != nil {
			return w, err
		}
		if !w.Recalled {
			*warnings = append(*warnings, warnRecallMissing)
			if err := s.St.BumpSession(ctx, ss, w.ID, store.SessionDelta{Violations: 1}); err != nil {
				return w, err
			}
		}
		return w, nil
	}
	if call.Profile == ProfileUI {
		return store.WorkSession{}, nil
	}
	w, err := s.St.StartSession(ctx, ss, store.NewSession{Client: call.Client, Agent: call.Agent, Model: call.Model})
	if err != nil {
		return w, err
	}
	*warnings = append(*warnings, warnRecallMissing)
	return w, s.St.BumpSession(ctx, ss, w.ID, store.SessionDelta{Violations: 1})
}

// latestOwnSession finds the caller's most recent open session in the current
// project, for checkpoint and handoff calls that lost the session_id.
func (s *Service) latestOwnSession(ctx context.Context, call Call, warnings *[]string) string {
	if !call.SS.HasProject() || call.Profile == ProfileUI {
		return ""
	}
	list, err := s.St.ListSessions(ctx, call.SS, "", 50)
	if err != nil {
		return ""
	}
	for _, w := range list {
		if w.ScopeID == call.SS.Project().ID && w.CreatedBy == call.SS.Principal().ID && w.Status != "closed" {
			*warnings = append(*warnings, "session_id missing; used your latest open session "+w.ID)
			return w.ID
		}
	}
	return ""
}

func (s *Service) bump(ctx context.Context, call Call, w store.WorkSession, d store.SessionDelta) {
	if w.ID == "" {
		return
	}
	_ = s.St.BumpSession(ctx, call.SS, w.ID, d)
}

// targetScope picks the scope of a write: the current project by default, the
// global scope only when asked.
func (s *Service) targetScope(call Call, scope string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(scope)) {
	case "global":
		return call.SS.GlobalID(), nil
	case "", "project":
		if !call.SS.HasProject() {
			return "", errProjectRequired()
		}
		return call.SS.Project().ID, nil
	}
	return "", newErr(http.StatusBadRequest, "invalid", "scope must be project or global")
}

func (s *Service) secretsBlocked(ctx context.Context, call Call, w store.WorkSession, texts ...string) error {
	found := core.ScanSecrets(texts...)
	if len(found) == 0 {
		return nil
	}
	s.bump(ctx, call, w, store.SessionDelta{Secrets: 1})
	s.audit(call, "secret_blocked", "", http.StatusUnprocessableEntity, strings.Join(found, ","))
	return newErr(http.StatusUnprocessableEntity, "secret_detected", "content looks like it contains credentials (%s); nothing was stored", strings.Join(found, ", ")).
		withHint("put the secret in the vault (memory_vault_put / UI Cofre) and reference vault:<scope>/<key> in the memory text")
}

func evidenceTexts(ev []store.EvidenceIn) []string {
	var out []string
	for _, e := range ev {
		out = append(out, e.Ref, e.Note)
	}
	return out
}

func (s *Service) audit(call Call, op, scopeID string, status int, detail string) {
	if s.Log == nil {
		return
	}
	level := "info"
	if status >= 400 {
		level = "warn"
	}
	s.Log.Log(store.LogEntry{Source: call.Profile, PrincipalID: call.SS.Principal().ID, ScopeID: scopeID, Operation: op, Level: level, Status: status, Detail: detail})
}

func (s *Service) itemOf(call Call, m store.Memory, max int) *Item {
	sl := slugs{call.SS.GlobalID(): "global"}
	if call.SS.HasProject() {
		sl[call.SS.Project().ID] = call.SS.Project().Slug
	}
	it := project(m, sl, max, time.Now().UTC())
	return &it
}

func (s *Service) index(m store.Memory) {
	if s.Index != nil {
		s.Index.Enqueue(m)
	}
}

func clampConfidence(c float64) float64 {
	if c <= 0 {
		return 0.5
	}
	if c > 1 {
		return 1
	}
	return c
}

var observeKinds = map[string]bool{"preference": true, "pattern": true, "knowledge": true, "observation": true, "note": true, "todo": true, "requirement": true, "business_rule": true, "architecture": true}

// Observe records a learning after dedupe, secret and budget checks.
func (s *Service) Observe(ctx context.Context, call Call, in ObserveIn) (WriteOut, error) {
	var out WriteOut
	kind := strings.TrimSpace(in.Kind)
	if kind == "" {
		kind = "observation"
	}
	if !observeKinds[kind] {
		return out, newErr(http.StatusBadRequest, "invalid", "kind %q is not accepted by observe", kind).withHint("use memory_decide for decisions and memory_checkpoint for session state")
	}
	scopeID, err := s.targetScope(call, in.Scope)
	if err != nil {
		return out, err
	}
	if core.Trivial(in.Title, in.Content) {
		return out, newErr(http.StatusBadRequest, "too_trivial", "content is too short or generic to be worth remembering")
	}
	w, err := s.writeSession(ctx, call, in.SessionID, &out.Warnings)
	if err != nil {
		return out, err
	}
	out.SessionID = w.ID
	if err := s.secretsBlocked(ctx, call, w, append([]string{in.Title, in.Content}, evidenceTexts(in.Evidence)...)...); err != nil {
		return out, err
	}
	if w.ID != "" && w.Observations >= s.Cfg.ObservationBudget {
		return out, newErr(http.StatusTooManyRequests, "observation_budget", "this session already recorded %d observations (limit %d)", w.Observations, s.Cfg.ObservationBudget).
			withHint("consolidate what you learned into fewer, better memories, or write a memory_checkpoint / memory_handoff")
	}
	if dup, ok, err := s.St.FindByHash(ctx, call.SS, scopeID, kind, in.Content); err != nil {
		return out, err
	} else if ok {
		m, err := s.St.Confirm(ctx, call.SS, dup.ID, in.Confidence, in.Evidence, call.Agent)
		if err != nil {
			return out, err
		}
		s.bump(ctx, call, w, store.SessionDelta{Duplicates: 1})
		out.Result, out.DuplicateOf, out.Memory = "duplicate", m.ID, s.itemOf(call, m, 300)
		out.Message = fmt.Sprintf("already known: confirmation %d added to %s", m.Confirmations, m.ID)
		return out, nil
	}
	cands, err := s.nearDuplicates(ctx, call, scopeID, in.Title, in.Content, in.DistinctFrom)
	if err != nil {
		return out, err
	}
	if len(cands) > 0 {
		s.bump(ctx, call, w, store.SessionDelta{Duplicates: 1})
		out.Result = "duplicate_candidate"
		for _, m := range cands {
			out.DuplicateCandidates = append(out.DuplicateCandidates, *s.itemOf(call, m, 300))
		}
		out.Message = "very similar memories exist; nothing was stored. If yours is different, call again with distinct_from set to their ids; if it adds evidence, it was a duplicate."
		return out, nil
	}
	kindScope, policy, err := s.St.ScopePolicy(ctx, call.SS, scopeID)
	if err != nil {
		return out, err
	}
	status := core.InitialStatus(kindScope, policy, kind)
	if status == "active" && !call.SS.CanWrite(scopeID) && call.SS.CanPropose(scopeID) {
		status = "proposed"
	}
	reviewAfter := ""
	if d := core.ReviewAfter(kind); d > 0 {
		reviewAfter = store.FormatTime(time.Now().Add(d))
	}
	nm := store.NewMemory{
		ScopeID: scopeID, CreatedSessionID: w.ID, Kind: kind, Title: in.Title, Content: in.Content,
		Tier: core.DefaultTier(kind), Status: status, Confidence: clampConfidence(in.Confidence),
		SourceType: core.SourceType(call.fromUI(), in.Evidence), Client: call.Client, Agent: call.Agent, Model: call.Model,
		UserConfirmedClaim: in.UserConfirmed, ValidUntil: in.ValidUntil, ReviewAfter: reviewAfter, Tags: in.Tags, Evidence: in.Evidence,
	}
	m, err := s.St.CreateMemory(ctx, call.SS, nm)
	if err != nil {
		return out, err
	}
	s.bump(ctx, call, w, store.SessionDelta{Observations: 1, Writes: 1})
	s.index(m)
	s.audit(call, "observe", scopeID, 200, m.ID)
	s.scheduleSuggestLinks(call, m.ID)
	out.Memory = s.itemOf(call, m, -1)
	if m.Status == "proposed" {
		out.Result = "proposed"
		out.Message = "stored as a proposal; it becomes current only after a human approves it in the impamind UI"
	} else {
		out.Result = "created"
		out.Message = "stored"
	}
	return out, nil
}

// nearDuplicates finds active or proposed memories in the scope that are
// near-identical, excluding ids the caller declared distinct.
func (s *Service) nearDuplicates(ctx context.Context, call Call, scopeID, title, content string, distinct []string) ([]store.Memory, error) {
	skip := map[string]bool{}
	for _, id := range distinct {
		skip[id] = true
	}
	hits, err := s.St.SearchFTS(ctx, call.SS, title+" "+content, store.SearchFilter{ScopeIDs: []string{scopeID}, Statuses: []string{"active", "proposed"}, Limit: 8})
	if err != nil {
		return nil, err
	}
	var out []store.Memory
	found := map[string]bool{}
	for _, h := range hits {
		if skip[h.Memory.ID] || h.Memory.Kind == "checkpoint" || h.Memory.Kind == "session_summary" {
			continue
		}
		if core.Similarity(title, content, h.Memory.Title, h.Memory.Content) >= core.NearDuplicate {
			out = append(out, h.Memory)
			found[h.Memory.ID] = true
		}
	}
	if s.Index != nil {
		if vs, err := s.Index.Similar(ctx, scopeID, title+"\n"+content, 5); err == nil {
			ms, err := s.memoriesBySeq(ctx, call, vs, 0.92)
			if err != nil {
				return nil, err
			}
			for _, m := range ms {
				if !found[m.ID] && !skip[m.ID] && m.Kind != "checkpoint" && m.Kind != "session_summary" && (m.Status == "active" || m.Status == "proposed") {
					out = append(out, m)
					found[m.ID] = true
				}
			}
		}
	}
	if len(out) > 3 {
		out = out[:3]
	}
	return out, nil
}

var decideKinds = map[string]bool{"decision": true, "architecture": true, "business_rule": true, "requirement": true}

// Decide records a decision. With supersedes it replaces an active decision
// atomically (or proposes the replacement when the policy requires approval).
func (s *Service) Decide(ctx context.Context, call Call, in DecideIn) (WriteOut, error) {
	var out WriteOut
	kind := strings.TrimSpace(in.Kind)
	if kind == "" {
		kind = "decision"
	}
	if !decideKinds[kind] {
		return out, newErr(http.StatusBadRequest, "invalid", "kind must be decision, architecture, business_rule or requirement")
	}
	if strings.TrimSpace(in.Rationale) == "" {
		return out, newErr(http.StatusBadRequest, "rationale_required", "a decision needs a rationale: why was it decided?")
	}
	if strings.TrimSpace(in.Decision) == "" || strings.TrimSpace(in.Title) == "" {
		return out, newErr(http.StatusBadRequest, "invalid", "title and decision are required")
	}
	if !call.SS.HasProject() {
		return out, errProjectRequired()
	}
	scopeID := call.SS.Project().ID
	w, err := s.writeSession(ctx, call, in.SessionID, &out.Warnings)
	if err != nil {
		return out, err
	}
	out.SessionID = w.ID
	texts := append([]string{in.Title, in.Decision, in.Rationale}, in.Alternatives...)
	texts = append(texts, in.Consequences...)
	if err := s.secretsBlocked(ctx, call, w, append(texts, evidenceTexts(in.Evidence)...)...); err != nil {
		return out, err
	}
	details, _ := json.Marshal(map[string]any{"alternatives": nonNil(in.Alternatives), "consequences": nonNil(in.Consequences), "coexists": nonNil(in.Coexists)})
	tier := in.Tier
	if tier == 0 {
		tier = core.DefaultTier(kind)
	}
	nm := store.NewMemory{
		ScopeID: scopeID, CreatedSessionID: w.ID, Kind: kind, Title: in.Title, Content: in.Decision, Rationale: in.Rationale,
		Details: details, Tier: tier, Confidence: clampConfidence(in.Confidence), SourceType: core.SourceType(call.fromUI(), in.Evidence),
		Client: call.Client, Agent: call.Agent, Model: call.Model, UserConfirmedClaim: in.UserConfirmed,
		ReviewAfter: store.FormatTime(time.Now().Add(core.ReviewAfter(kind))), Tags: in.Tags, Evidence: in.Evidence,
	}
	if in.Supersedes != "" {
		reason := strings.TrimSpace(in.Reason)
		if reason == "" {
			reason = in.Rationale
		}
		return s.replace(ctx, call, w, in.Supersedes, nm, reason, out)
	}
	conflicts, err := s.conflicts(ctx, call, scopeID, in.Title, in.Decision, in.Coexists)
	if err != nil {
		return out, err
	}
	if len(conflicts) > 0 {
		ids := make([]string, 0, len(conflicts))
		for _, m := range conflicts {
			ids = append(ids, m.ID)
			out.PossibleConflicts = append(out.PossibleConflicts, *s.itemOf(call, m, 300))
		}
		sort.Strings(ids)
		key := "conflict:" + store.ContentHash(in.Title+" "+in.Decision) + ":" + strings.Join(ids, ",")
		if err := s.St.AddReviewItem(ctx, call.SS, scopeID, "conflict", key, ids, map[string]any{"proposed_title": in.Title, "proposed_decision": in.Decision, "agent": call.Agent}); err != nil {
			return out, err
		}
		out.Result = "needs_resolution"
		out.Message = "active decisions on the same subject exist. Call memory_decide again with supersedes=<id> to replace one, or coexists=[ids] if both stay valid."
		return out, nil
	}
	kindScope, policy, err := s.St.ScopePolicy(ctx, call.SS, scopeID)
	if err != nil {
		return out, err
	}
	nm.Status = core.InitialStatus(kindScope, policy, kind)
	if nm.Status == "active" && !call.SS.CanWrite(scopeID) {
		nm.Status = "proposed"
	}
	m, err := s.St.CreateMemory(ctx, call.SS, nm)
	if err != nil {
		return out, err
	}
	s.bump(ctx, call, w, store.SessionDelta{Writes: 1})
	s.index(m)
	s.audit(call, "decide", scopeID, 200, m.ID)
	out.Memory = s.itemOf(call, m, -1)
	if m.Status == "proposed" {
		out.Result, out.Message = "proposed", "recorded as a proposal; a human approves it in the impamind UI"
	} else {
		out.Result, out.Message = "created", "decision recorded"
	}
	return out, nil
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// conflicts returns active decision-like memories on the same subject.
func (s *Service) conflicts(ctx context.Context, call Call, scopeID, title, body string, coexists []string) ([]store.Memory, error) {
	skip := map[string]bool{}
	for _, id := range coexists {
		skip[id] = true
	}
	hits, err := s.St.SearchFTS(ctx, call.SS, title+" "+body, store.SearchFilter{ScopeIDs: []string{scopeID}, Statuses: []string{"active"}, Limit: 10})
	if err != nil {
		return nil, err
	}
	var out []store.Memory
	for _, h := range hits {
		m := h.Memory
		if skip[m.ID] || !decideKinds[m.Kind] {
			continue
		}
		if core.Similarity(title, "", m.Title, "") >= 0.5 || core.Similarity(title, body, m.Title, m.Content) >= core.ConflictCandidate {
			out = append(out, m)
		}
		if len(out) == 3 {
			break
		}
	}
	return out, nil
}

// replace is the shared path of decide-with-supersedes and supersede.
func (s *Service) replace(ctx context.Context, call Call, w store.WorkSession, oldID string, nm store.NewMemory, reason string, out WriteOut) (WriteOut, error) {
	old, err := s.St.GetMemory(ctx, call.SS, oldID)
	if err != nil {
		return out, err
	}
	if old.Status != "active" {
		return out, newErr(http.StatusConflict, "conflict", "memory %s is %s; only active memories can be superseded", old.ID, old.Status)
	}
	if nm.Kind == "" {
		nm.Kind = old.Kind
	}
	nm.ScopeID = old.ScopeID
	kindScope, policy, err := s.St.ScopePolicy(ctx, call.SS, old.ScopeID)
	if err != nil {
		return out, err
	}
	protected := store.Protected(kindScope, policy, nm.Kind) || store.Protected(kindScope, policy, old.Kind)
	if protected || !call.SS.CanWrite(old.ScopeID) {
		nm.Status = "proposed"
		nm.SupersedesID = old.ID
		nm.SupersedeReason = reason
		m, err := s.St.CreateMemory(ctx, call.SS, nm)
		if err != nil {
			return out, err
		}
		s.bump(ctx, call, w, store.SessionDelta{Writes: 1})
		s.index(m)
		s.audit(call, "supersede_proposed", old.ScopeID, 200, m.ID)
		out.Result = "proposed"
		out.Memory = s.itemOf(call, m, -1)
		out.Replaced = s.itemOf(call, old, 200)
		out.Message = "replacement recorded as a proposal; the current memory stays active until a human approves it"
		return out, nil
	}
	m, prev, err := s.St.ReplaceMemory(ctx, call.SS, old.ID, nm, reason)
	if err != nil {
		return out, err
	}
	s.bump(ctx, call, w, store.SessionDelta{Writes: 1})
	s.index(m)
	s.index(prev)
	s.audit(call, "supersede", old.ScopeID, 200, m.ID)
	out.Result = "superseded"
	out.Memory = s.itemOf(call, m, -1)
	out.Replaced = s.itemOf(call, prev, 200)
	out.Message = "replaced atomically; the old memory is superseded and kept in history"
	return out, nil
}

// Supersede replaces an active memory with new content (or an existing memory).
func (s *Service) Supersede(ctx context.Context, call Call, in SupersedeIn) (WriteOut, error) {
	var out WriteOut
	reason := strings.TrimSpace(in.Reason)
	if in.OldID == "" || reason == "" {
		return out, newErr(http.StatusBadRequest, "invalid", "old_id and reason are required")
	}
	if (in.New == nil) == (in.NewID == "") {
		return out, newErr(http.StatusBadRequest, "invalid", "pass exactly one of new or new_id")
	}
	w, err := s.writeSession(ctx, call, in.SessionID, &out.Warnings)
	if err != nil {
		return out, err
	}
	out.SessionID = w.ID
	if in.NewID != "" {
		m, prev, err := s.St.SupersedeWithExisting(ctx, call.SS, in.OldID, in.NewID, reason)
		if err != nil {
			return out, err
		}
		s.bump(ctx, call, w, store.SessionDelta{Writes: 1})
		s.index(m)
		s.index(prev)
		out.Result, out.Memory, out.Replaced = "superseded", s.itemOf(call, m, -1), s.itemOf(call, prev, 200)
		out.Message = "replaced; the old memory is superseded and kept in history"
		return out, nil
	}
	n := in.New
	if err := s.secretsBlocked(ctx, call, w, append([]string{n.Title, n.Content, n.Rationale, reason}, evidenceTexts(n.Evidence)...)...); err != nil {
		return out, err
	}
	if decideKinds[n.Kind] && strings.TrimSpace(n.Rationale) == "" {
		n.Rationale = reason
	}
	nm := store.NewMemory{
		Kind: n.Kind, Title: n.Title, Content: n.Content, Rationale: n.Rationale, Tier: n.Tier, Tags: n.Tags, Evidence: n.Evidence,
		CreatedSessionID: w.ID, Confidence: 0.6, SourceType: core.SourceType(call.fromUI(), n.Evidence),
		Client: call.Client, Agent: call.Agent, Model: call.Model,
	}
	if nm.Kind != "" {
		if d := core.ReviewAfter(nm.Kind); d > 0 {
			nm.ReviewAfter = store.FormatTime(time.Now().Add(d))
		}
	}
	return s.replace(ctx, call, w, in.OldID, nm, reason, out)
}

// Checkpoint stores the versioned state of the work session.
func (s *Service) Checkpoint(ctx context.Context, call Call, in CheckpointIn) (CheckpointOut, error) {
	var out CheckpointOut
	if !call.SS.HasProject() {
		return out, errProjectRequired()
	}
	if strings.TrimSpace(in.Task) == "" {
		return out, newErr(http.StatusBadRequest, "invalid", "task is required")
	}
	status := in.Status
	if status == "" {
		status = "in_progress"
	}
	if status != "in_progress" && status != "paused" && status != "done" {
		return out, newErr(http.StatusBadRequest, "invalid", "status must be in_progress, paused or done")
	}
	if in.SessionID == "" {
		in.SessionID = s.latestOwnSession(ctx, call, &out.Warnings)
	}
	w, err := s.writeSession(ctx, call, in.SessionID, &out.Warnings)
	if err != nil {
		return out, err
	}
	out.SessionID = w.ID
	texts := append(append(append(append([]string{in.Task, in.Notes}, in.Files...), in.Errors...), in.Tests...), in.NextSteps...)
	if err := s.secretsBlocked(ctx, call, w, texts...); err != nil {
		return out, err
	}
	d := checkpointDetails{Task: in.Task, Status: status, Files: nonNil(in.Files), Errors: nonNil(in.Errors), Tests: nonNil(in.Tests), NextSteps: nonNil(in.NextSteps), Notes: in.Notes}
	details, _ := json.Marshal(d)
	content := renderCheckpoint(d)
	title := "Checkpoint: " + clipTitle(in.Task)
	cur, ok, err := s.St.SessionCheckpoint(ctx, call.SS, w.ID)
	if err != nil {
		return out, err
	}
	var m store.Memory
	if ok {
		raw := json.RawMessage(details)
		m, err = s.St.UpdateMemory(ctx, call.SS, cur.ID, store.MemoryPatch{Title: &title, Content: &content, Details: &raw, Reason: "checkpoint"})
	} else {
		m, err = s.St.CreateMemory(ctx, call.SS, store.NewMemory{
			ScopeID: call.SS.Project().ID, SessionID: w.ID, CreatedSessionID: w.ID, Kind: "checkpoint", Title: title, Content: content,
			Details: details, Tier: 3, Status: "active", Confidence: 0.8, SourceType: "agent_inferred",
			Client: call.Client, Agent: call.Agent, Model: call.Model,
		})
	}
	if err != nil {
		return out, err
	}
	s.bump(ctx, call, w, store.SessionDelta{Checkpoints: 1})
	sessStatus := "active"
	if status == "paused" {
		sessStatus = "paused"
	}
	if err := s.St.UpdateSessionTask(ctx, call.SS, w.ID, in.Task, in.Files, sessStatus); err != nil {
		return out, err
	}
	if status == "done" {
		if err := s.St.CloseSession(ctx, call.SS, w.ID, false); err != nil {
			return out, err
		}
		out.Closed = true
	}
	s.audit(call, "checkpoint", call.SS.Project().ID, 200, m.ID)
	out.CheckpointID, out.Version = m.ID, m.Version
	out.Message = fmt.Sprintf("checkpoint v%d saved", m.Version)
	if out.Closed {
		out.Message += "; session closed"
	}
	return out, nil
}

func clipTitle(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	c, _ := clipText(s, 120)
	return c
}

func renderCheckpoint(d checkpointDetails) string {
	var b strings.Builder
	b.WriteString("Task: " + d.Task + "\nStatus: " + d.Status + "\n")
	list := func(name string, items []string) {
		if len(items) == 0 {
			return
		}
		b.WriteString(name + ":\n")
		for _, it := range items {
			b.WriteString("- " + it + "\n")
		}
	}
	list("Files", d.Files)
	list("Errors", d.Errors)
	list("Tests", d.Tests)
	list("Next steps", d.NextSteps)
	if d.Notes != "" {
		b.WriteString("Notes: " + d.Notes + "\n")
	}
	return b.String()
}

// Handoff closes the session with a summary and returns learning candidates for
// the agent to confirm with observe or decide.
func (s *Service) Handoff(ctx context.Context, call Call, in HandoffIn) (HandoffOut, error) {
	var out HandoffOut
	if !call.SS.HasProject() {
		return out, errProjectRequired()
	}
	if len(strings.TrimSpace(in.Summary)) < 20 {
		return out, newErr(http.StatusBadRequest, "invalid", "summary is required (at least a couple of sentences)")
	}
	var warnings []string
	if in.SessionID == "" {
		in.SessionID = s.latestOwnSession(ctx, call, &warnings)
	}
	w, err := s.writeSession(ctx, call, in.SessionID, &warnings)
	if err != nil {
		return out, err
	}
	out.Warnings = warnings
	texts := append(append([]string{in.Summary}, in.NextSteps...), in.LearningsCandidates...)
	if err := s.secretsBlocked(ctx, call, w, texts...); err != nil {
		return out, err
	}
	content := in.Summary
	if len(in.NextSteps) > 0 {
		content += "\n\nNext steps:\n- " + strings.Join(in.NextSteps, "\n- ")
	}
	details, _ := json.Marshal(map[string]any{"next_steps": nonNil(in.NextSteps), "learnings_candidates": nonNil(in.LearningsCandidates)})
	m, err := s.St.CreateMemory(ctx, call.SS, store.NewMemory{
		ScopeID: call.SS.Project().ID, SessionID: w.ID, CreatedSessionID: w.ID, Kind: "session_summary",
		Title: "Session summary: " + clipTitle(firstLine(in.Summary)), Content: content, Details: details, Tier: 3, Status: "active",
		Confidence: 0.8, SourceType: "agent_inferred", Client: call.Client, Agent: call.Agent, Model: call.Model,
	})
	if err != nil {
		return out, err
	}
	s.bump(ctx, call, w, store.SessionDelta{Writes: 1})
	if err := s.St.CloseSession(ctx, call.SS, w.ID, true); err != nil && !errors.Is(err, store.ErrClosed) {
		return out, err
	}
	s.index(m)
	s.audit(call, "handoff", call.SS.Project().ID, 200, m.ID)
	out.SessionID, out.SummaryID, out.Candidates = w.ID, m.ID, nonNil(in.LearningsCandidates)
	out.Message = "session closed with a summary. For each learning candidate worth keeping, call memory_observe (facts, patterns) or memory_decide (decisions)."
	return out, nil
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, ".\n"); i > 0 {
		return s[:i]
	}
	return s
}
