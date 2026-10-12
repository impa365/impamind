package protocol

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"impamind/internal/core"
	"impamind/internal/store"
)

// AddIn creates a memory directly (UI and admin tools).
type AddIn struct {
	Scope       string             `json:"scope,omitempty" jsonschema:"project (default) or global"`
	Project     string             `json:"project,omitempty"`
	Kind        string             `json:"kind"`
	Title       string             `json:"title"`
	Content     string             `json:"content"`
	Rationale   string             `json:"rationale,omitempty"`
	Tier        int                `json:"tier,omitempty"`
	Pinned      bool               `json:"pinned,omitempty"`
	Confidence  float64            `json:"confidence,omitempty"`
	Tags        []string           `json:"tags,omitempty"`
	Evidence    []store.EvidenceIn `json:"evidence,omitempty"`
	ValidUntil  string             `json:"valid_until,omitempty"`
	ReviewAfter string             `json:"review_after,omitempty"`
	Approve     bool               `json:"approve,omitempty" jsonschema:"approve immediately when the scope requires approval (human only)"`
	UserStated  bool               `json:"user_stated,omitempty" jsonschema:"with approve: the text is the user's own words"`
}

// AddMemory stores a memory written by a human (UI or human token). Protected
// memories are created as proposals and, if requested and allowed, approved in
// a second step through Approve.
func (s *Service) AddMemory(ctx context.Context, call Call, in AddIn) (store.Memory, error) {
	scopeID, err := s.targetScope(call, in.Scope)
	if err != nil {
		return store.Memory{}, err
	}
	if in.Kind == "" {
		in.Kind = "note"
	}
	if !store.ValidKind(in.Kind) {
		return store.Memory{}, newErr(http.StatusBadRequest, "invalid", "unknown kind %q", in.Kind)
	}
	if found := core.ScanSecrets(in.Title, in.Content, in.Rationale); len(found) > 0 {
		return store.Memory{}, newErr(http.StatusUnprocessableEntity, "secret_detected", "content looks like it contains credentials (%s); nothing was stored", strings.Join(found, ", "))
	}
	kind, policy, err := s.St.ScopePolicy(ctx, call.SS, scopeID)
	if err != nil {
		return store.Memory{}, err
	}
	status := core.InitialStatus(kind, policy, in.Kind)
	if status == "active" && !call.SS.CanWrite(scopeID) {
		status = "proposed"
	}
	tier := in.Tier
	if tier == 0 {
		tier = core.DefaultTier(in.Kind)
	}
	if (in.Pinned || in.Tier != 0) && !call.human() {
		return store.Memory{}, newErr(http.StatusForbidden, "forbidden", "tier and pin are set by humans")
	}
	ra := in.ReviewAfter
	if ra == "" {
		if d := core.ReviewAfter(in.Kind); d > 0 {
			ra = store.FormatTime(time.Now().Add(d))
		}
	}
	m, err := s.St.CreateMemory(ctx, call.SS, store.NewMemory{
		ScopeID: scopeID, Kind: in.Kind, Title: in.Title, Content: in.Content, Rationale: in.Rationale, Tier: tier, Pinned: in.Pinned,
		Status: status, Confidence: clampConfidence(in.Confidence), SourceType: core.SourceType(call.fromUI(), in.Evidence),
		Client: call.Client, Agent: call.Agent, Model: call.Model, ValidUntil: in.ValidUntil, ReviewAfter: ra, Tags: in.Tags, Evidence: in.Evidence,
	})
	if err != nil {
		return m, err
	}
	if m.Status == "proposed" && in.Approve && call.SS.CanApprove(scopeID) && call.Profile != ProfileAgent {
		m, err = s.St.Approve(ctx, call.SS, m.ID, store.ApproveInput{Note: "created and approved by a human", MarksUserStated: in.UserStated})
		if err != nil {
			return m, err
		}
	}
	s.index(m)
	s.audit(call, "memory_add", scopeID, 200, m.ID)
	s.scheduleSuggestLinks(call, m.ID)
	return m, nil
}

func (s *Service) humanOnly(call Call) error {
	if call.Profile == ProfileAgent || !call.human() {
		return newErr(http.StatusForbidden, "forbidden", "this operation needs a human (UI session or human token)")
	}
	return nil
}

func (s *Service) Approve(ctx context.Context, call Call, id string, in store.ApproveInput) (store.Memory, error) {
	if err := s.humanOnly(call); err != nil {
		return store.Memory{}, err
	}
	m, err := s.St.Approve(ctx, call.SS, id, in)
	if err != nil {
		return m, err
	}
	s.index(m)
	s.audit(call, "approve", m.ScopeID, 200, m.ID)
	return m, nil
}

func (s *Service) Reject(ctx context.Context, call Call, id, note string) (store.Memory, error) {
	m, err := s.St.Reject(ctx, call.SS, id, note)
	if err == nil {
		s.audit(call, "reject", m.ScopeID, 200, m.ID)
	}
	return m, err
}

func (s *Service) Transition(ctx context.Context, call Call, id, to, reason string) (store.Memory, error) {
	m, err := s.St.Transition(ctx, call.SS, id, to, reason)
	if err == nil {
		s.index(m)
		s.audit(call, "transition_"+to, m.ScopeID, 200, m.ID)
	}
	return m, err
}

func (s *Service) UpdateMemory(ctx context.Context, call Call, id string, p store.MemoryPatch) (store.Memory, error) {
	var texts []string
	if p.Title != nil {
		texts = append(texts, *p.Title)
	}
	if p.Content != nil {
		texts = append(texts, *p.Content)
	}
	if found := core.ScanSecrets(texts...); len(found) > 0 {
		return store.Memory{}, newErr(http.StatusUnprocessableEntity, "secret_detected", "content looks like it contains credentials (%s)", strings.Join(found, ", "))
	}
	m, err := s.St.UpdateMemory(ctx, call.SS, id, p)
	if err == nil {
		s.index(m)
		s.audit(call, "memory_update", m.ScopeID, 200, m.ID)
		if p.Title != nil || p.Content != nil {
			s.scheduleSuggestLinks(call, m.ID)
		}
	}
	return m, err
}

func (s *Service) DeleteMemory(ctx context.Context, call Call, id, reason string) error {
	if err := s.humanOnly(call); err != nil {
		return err
	}
	m, err := s.St.GetMemory(ctx, call.SS, id)
	if err != nil {
		return err
	}
	if err := s.St.HardDelete(ctx, call.SS, id, reason); err != nil {
		return err
	}
	if s.Index != nil {
		s.Index.Remove(m.ScopeID, m.Seq)
	}
	s.audit(call, "memory_delete", m.ScopeID, 200, m.ID)
	return nil
}

// Promote copies a project memory to the global scope as a new memory with
// derived_from_id. Only humans can do it; the project memory stays.
func (s *Service) Promote(ctx context.Context, call Call, id string, approve bool) (store.Memory, error) {
	if err := s.humanOnly(call); err != nil {
		return store.Memory{}, err
	}
	src, err := s.St.GetMemory(ctx, call.SS, id)
	if err != nil {
		return src, err
	}
	gid := call.SS.GlobalID()
	if src.ScopeID == gid {
		return src, newErr(http.StatusBadRequest, "invalid", "memory is already global")
	}
	if src.Status != "active" {
		return src, newErr(http.StatusConflict, "conflict", "only active memories can be promoted")
	}
	m, err := s.St.CreateMemory(ctx, call.SS, store.NewMemory{
		ScopeID: gid, Kind: src.Kind, Title: src.Title, Content: src.Content, Rationale: src.Rationale, Details: src.Details,
		Tier: src.Tier, Status: "proposed", Confidence: src.Confidence, SourceType: core.SourceType(call.fromUI(), nil),
		DerivedFromID: src.ID, Tags: src.Tags, Client: call.Client,
	})
	if err != nil {
		return m, err
	}
	if approve && call.SS.CanApprove(gid) {
		if m, err = s.St.Approve(ctx, call.SS, m.ID, store.ApproveInput{Note: "promoted from project memory " + src.ID}); err != nil {
			return m, err
		}
	}
	s.index(m)
	s.audit(call, "promote", gid, 200, m.ID)
	return m, nil
}

// Projects ---------------------------------------------------------------------

func (s *Service) ListScopes(ctx context.Context, call Call) ([]store.Scope, error) {
	return s.St.ListScopes(ctx, call.SS)
}

func (s *Service) CreateProject(ctx context.Context, call Call, in store.ProjectInput) (store.Scope, error) {
	sc, err := s.St.CreateProject(ctx, call.SS, in)
	if err == nil {
		s.audit(call, "project_create", sc.ID, 200, sc.Slug)
	}
	return sc, err
}

// ScopeBySlug finds a scope the caller holds a grant on.
func (s *Service) ScopeBySlug(ctx context.Context, call Call, slug string) (store.Scope, error) {
	slug = strings.ToLower(strings.TrimSpace(slug))
	if slug == "global" {
		return s.St.GetScope(ctx, call.SS, call.SS.GlobalID())
	}
	_, refs, err := s.St.ScopeRefs(ctx)
	if err != nil {
		return store.Scope{}, err
	}
	for _, r := range refs {
		if r.Slug == slug {
			return s.St.GetScope(ctx, call.SS, r.ID)
		}
	}
	return store.Scope{}, store.ErrNotFound
}

func (s *Service) UpdateProject(ctx context.Context, call Call, id string, in store.ProjectInput) (store.Scope, error) {
	sc, err := s.St.UpdateScope(ctx, call.SS, id, in)
	if err == nil {
		s.audit(call, "project_update", id, 200, sc.Slug)
	}
	return sc, err
}

// DeleteProject soft-deletes a project after taking a backup.
func (s *Service) DeleteProject(ctx context.Context, call Call, id string) (string, error) {
	if err := s.humanOnly(call); err != nil {
		return "", err
	}
	sc, err := s.St.GetScope(ctx, call.SS, id)
	if err != nil {
		return "", err
	}
	if sc.Kind != "project" {
		return "", newErr(http.StatusBadRequest, "invalid", "the global scope cannot be deleted")
	}
	if !call.SS.CanApprove(id) {
		return "", store.ErrNotFound
	}
	backup := filepath.Join(s.Cfg.BackupDir, fmt.Sprintf("impamind-%s-before-delete-%s.db", time.Now().UTC().Format("20060102-150405"), sc.Slug))
	if err := s.St.Backup(ctx, backup); err != nil {
		return "", fmt.Errorf("backup before delete: %w", err)
	}
	if err := s.St.SoftDeleteScope(ctx, call.SS, id); err != nil {
		return "", err
	}
	s.audit(call, "project_delete", id, 200, sc.Slug)
	return backup, nil
}

// History returns the full history of a memory.
func (s *Service) History(ctx context.Context, call Call, id string) (store.History, error) {
	return s.St.History(ctx, call.SS, id)
}

// Export dumps the active memories of the call's project for markdown sync.
type ExportOut struct {
	Project   ProjectRef     `json:"project"`
	Generated string         `json:"generated_at"`
	Memories  []store.Memory `json:"memories"`
}

func (s *Service) Export(ctx context.Context, call Call, includeGlobal bool) (ExportOut, error) {
	var out ExportOut
	if !call.SS.HasProject() {
		return out, errProjectRequired()
	}
	p := call.SS.Project()
	out.Project = ProjectRef{ID: p.ID, Slug: p.Slug}
	out.Generated = store.FormatTime(time.Now())
	scopes := []string{p.ID}
	if includeGlobal {
		scopes = append(scopes, call.SS.GlobalID())
	}
	offset := 0
	for {
		list, total, err := s.St.ListMemories(ctx, call.SS, store.MemoryFilter{ScopeIDs: scopes, Statuses: []string{"active"}, Limit: 500, Offset: offset})
		if err != nil {
			return out, err
		}
		for _, m := range list {
			if m.Kind == "checkpoint" {
				continue
			}
			out.Memories = append(out.Memories, m)
		}
		offset += len(list)
		if offset >= total || len(list) == 0 {
			break
		}
	}
	if out.Memories == nil {
		out.Memories = []store.Memory{}
	}
	return out, nil
}

// ImportItem is one memory coming from a markdown file.
type ImportItem struct {
	ID       string   `json:"id,omitempty"`
	Kind     string   `json:"kind"`
	Title    string   `json:"title"`
	Content  string   `json:"content"`
	Tags     []string `json:"tags,omitempty"`
	Tier     int      `json:"tier,omitempty"`
	File     string   `json:"file"`
	Hash     string   `json:"hash"`
	Manifest string   `json:"manifest,omitempty"`
}

type ImportResult struct {
	File   string `json:"file"`
	Result string `json:"result"`
	ID     string `json:"id,omitempty"`
	Error  string `json:"error,omitempty"`
}

// Import stores memories edited locally as markdown. New files become new
// memories (source imported); edited files become superseding versions, or
// proposals when the policy protects them.
func (s *Service) Import(ctx context.Context, call Call, items []ImportItem) ([]ImportResult, error) {
	if !call.SS.HasProject() {
		return nil, errProjectRequired()
	}
	if len(items) > 500 {
		return nil, newErr(http.StatusBadRequest, "invalid", "at most 500 items per import")
	}
	scopeID := call.SS.Project().ID
	kind, policy, err := s.St.ScopePolicy(ctx, call.SS, scopeID)
	if err != nil {
		return nil, err
	}
	var out []ImportResult
	for _, it := range items {
		r := ImportResult{File: it.File}
		if found := core.ScanSecrets(it.Title, it.Content); len(found) > 0 {
			r.Result, r.Error = "rejected", "secret_detected: "+strings.Join(found, ",")
			out = append(out, r)
			continue
		}
		ev := []store.EvidenceIn{{Type: "manifest", Ref: it.File, Note: "sha256:" + it.Hash}}
		if it.Kind == "" {
			it.Kind = "note"
		}
		nm := store.NewMemory{ScopeID: scopeID, Kind: it.Kind, Title: it.Title, Content: it.Content, Tier: it.Tier, Tags: it.Tags,
			Confidence: 0.7, SourceType: "imported", Evidence: ev, Client: "sync"}
		if decideKinds[it.Kind] {
			nm.Rationale = "imported from " + it.File
		}
		if it.ID != "" {
			cur, err := s.St.GetMemory(ctx, call.SS, it.ID)
			if err != nil {
				r.Result, r.Error = "error", AsError(err).Code
				out = append(out, r)
				continue
			}
			if cur.ContentHash == store.ContentHash(it.Content) && cur.Title == it.Title {
				r.Result, r.ID = "unchanged", cur.ID
				out = append(out, r)
				continue
			}
			if cur.Status != "active" {
				r.Result, r.Error, r.ID = "conflict", "server memory is "+cur.Status, cur.ID
				out = append(out, r)
				continue
			}
			nm.Kind = cur.Kind
			if store.Protected(kind, policy, cur.Kind) || !call.SS.CanWrite(scopeID) {
				nm.Status, nm.SupersedesID, nm.SupersedeReason = "proposed", cur.ID, "edited in "+it.File
				m, err := s.St.CreateMemory(ctx, call.SS, nm)
				if err != nil {
					r.Result, r.Error = "error", AsError(err).Msg
				} else {
					r.Result, r.ID = "proposed", m.ID
					s.index(m)
				}
				out = append(out, r)
				continue
			}
			m, _, err := s.St.ReplaceMemory(ctx, call.SS, cur.ID, nm, "edited in "+it.File)
			if err != nil {
				r.Result, r.Error = "error", AsError(err).Msg
			} else {
				r.Result, r.ID = "superseded", m.ID
				s.index(m)
			}
			out = append(out, r)
			continue
		}
		if dup, ok, err := s.St.FindByHash(ctx, call.SS, scopeID, it.Kind, it.Content); err == nil && ok {
			r.Result, r.ID = "duplicate", dup.ID
			out = append(out, r)
			continue
		}
		nm.Status = core.InitialStatus(kind, policy, it.Kind)
		if nm.Status == "active" && !call.SS.CanWrite(scopeID) {
			nm.Status = "proposed"
		}
		m, err := s.St.CreateMemory(ctx, call.SS, nm)
		if err != nil {
			r.Result, r.Error = "error", AsError(err).Msg
		} else {
			r.Result, r.ID = map[bool]string{true: "proposed", false: "created"}[m.Status == "proposed"], m.ID
			s.index(m)
		}
		out = append(out, r)
	}
	s.audit(call, "sync_import", scopeID, 200, fmt.Sprintf("%d items", len(items)))
	return out, nil
}

// Health aggregates protocol metrics of the readable scopes.
func (s *Service) Health(ctx context.Context, call Call, days int) ([]store.ProtocolHealth, error) {
	if days <= 0 || days > 365 {
		days = 30
	}
	return s.St.ProtocolHealth(ctx, call.SS, store.FormatTime(time.Now().AddDate(0, 0, -days)))
}

type ListIn struct {
	Scope     string `json:"scope,omitempty"`
	Status    string `json:"status,omitempty"`
	Kind      string `json:"kind,omitempty"`
	Tier      int    `json:"tier,omitempty"`
	Tag       string `json:"tag,omitempty"`
	SessionID string `json:"session_id,omitempty"`
	Limit     int    `json:"limit,omitempty"`
	Offset    int    `json:"offset,omitempty"`
}

type ListOut struct {
	Items []store.Memory `json:"items"`
	Total int            `json:"total"`
}

// List pages through memories of the call's scopes.
func (s *Service) List(ctx context.Context, call Call, in ListIn) (ListOut, error) {
	scopes, err := s.scopeFilter(call, in.Scope)
	if err != nil {
		return ListOut{}, err
	}
	var statuses []string
	for _, st := range strings.Split(in.Status, ",") {
		if st = strings.TrimSpace(st); st != "" {
			statuses = append(statuses, st)
		}
	}
	if in.Kind != "" && !store.ValidKind(in.Kind) {
		return ListOut{}, newErr(http.StatusBadRequest, "invalid", "unknown kind %q", in.Kind)
	}
	items, total, err := s.St.ListMemories(ctx, call.SS, store.MemoryFilter{ScopeIDs: scopes, Statuses: statuses, Kind: in.Kind, Tier: in.Tier, Tag: in.Tag, SessionID: in.SessionID, Limit: in.Limit, Offset: in.Offset})
	return ListOut{Items: items, Total: total}, err
}
