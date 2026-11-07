package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode"

	"impamind/internal/auth"
)

const memCols = `m.seq, m.id, m.scope_id, COALESCE(m.session_id,''), m.kind, m.title, m.content, m.tier, m.pinned, m.status, m.review_state,
	m.confidence, m.source_type, m.created_by_principal_id, m.created_by_client, m.created_by_agent, m.created_by_model,
	COALESCE(m.created_session_id,''), m.user_confirmed_claim, COALESCE(m.approved_by,''), COALESCE(m.approved_via,''), m.lineage_id,
	COALESCE(m.supersedes_id,''), COALESCE(m.target_lineage_id,''), COALESCE(m.superseded_by_id,''), m.supersede_reason,
	COALESCE(m.derived_from_id,''), m.rationale, m.details, COALESCE(m.valid_from,''), COALESCE(m.valid_until,''),
	COALESCE(m.review_after,''), m.content_hash, m.version, m.confirmations, COALESCE(m.last_confirmed_at,''), m.access_count,
	COALESCE(m.last_accessed_at,''), m.created_at, m.updated_at`

func scanMemory(r rowScanner) (Memory, error) {
	var m Memory
	var details string
	err := r.Scan(&m.Seq, &m.ID, &m.ScopeID, &m.SessionID, &m.Kind, &m.Title, &m.Content, &m.Tier, &m.Pinned, &m.Status, &m.ReviewState,
		&m.Confidence, &m.SourceType, &m.CreatedByPrincipalID, &m.CreatedByClient, &m.CreatedByAgent, &m.CreatedByModel,
		&m.CreatedSessionID, &m.UserConfirmedClaim, &m.ApprovedBy, &m.ApprovedVia, &m.LineageID,
		&m.SupersedesID, &m.TargetLineageID, &m.SupersededByID, &m.SupersedeReason,
		&m.DerivedFromID, &m.Rationale, &details, &m.ValidFrom, &m.ValidUntil,
		&m.ReviewAfter, &m.ContentHash, &m.Version, &m.Confirmations, &m.LastConfirmedAt, &m.AccessCount,
		&m.LastAccessedAt, &m.CreatedAt, &m.UpdatedAt)
	if details != "" && details != "{}" {
		m.Details = json.RawMessage(details)
	}
	m.Tags = []string{}
	return m, err
}

// versionSnapshot is the JSON stored in memory_versions for the current row.
const versionSnapshot = `json_object(
	'id', id, 'scope_id', scope_id, 'session_id', session_id, 'kind', kind, 'title', title, 'content', content,
	'tier', tier, 'pinned', pinned, 'status', status, 'review_state', review_state, 'confidence', confidence,
	'source_type', source_type, 'created_by_principal_id', created_by_principal_id, 'created_by_client', created_by_client,
	'created_by_agent', created_by_agent, 'created_by_model', created_by_model, 'user_confirmed_claim', user_confirmed_claim,
	'approved_by', approved_by, 'approved_via', approved_via, 'lineage_id', lineage_id, 'supersedes_id', supersedes_id,
	'superseded_by_id', superseded_by_id, 'supersede_reason', supersede_reason, 'derived_from_id', derived_from_id,
	'rationale', rationale, 'details', json(details), 'valid_from', valid_from, 'valid_until', valid_until,
	'review_after', review_after, 'content_hash', content_hash, 'confirmations', confirmations,
	'tags', json(COALESCE((SELECT json_group_array(tag) FROM (SELECT tag FROM memory_tags WHERE memory_id = memories.id ORDER BY tag)), '[]')),
	'updated_at', updated_at)`

var (
	qMemInsert = scoped("memory.insert", `INSERT INTO memories(id, scope_id, session_id, kind, title, content, tier, pinned, status, review_state,
		confidence, source_type, created_by_principal_id, created_by_client, created_by_agent, created_by_model, created_session_id,
		user_confirmed_claim, lineage_id, supersedes_id, target_lineage_id, derived_from_id, rationale, details, valid_from, valid_until,
		review_after, content_hash, version, confirmations, created_at, updated_at, supersede_reason)
		SELECT ?1, ?2, ?3, ?4, ?5, ?6, ?7, ?8, ?9, ?10, ?11, ?12, ?13, ?14, ?15, ?16, ?17, ?18, ?19, ?20, ?21, ?22, ?23, ?24, ?25, ?26, ?27, ?28, 1, 1, ?29, ?30, ?31
		WHERE {{in:?2}}`)
	qMemGet       = scoped("memory.get", `SELECT `+memCols+` FROM memories m WHERE m.id = ? AND {{scope:m}}`)
	qMemGetMany   = scoped("memory.get_many", `SELECT `+memCols+` FROM memories m WHERE m.id IN (SELECT value FROM json_each(?)) AND {{scope:m}}`)
	qMemByHash    = scoped("memory.by_hash", `SELECT `+memCols+` FROM memories m WHERE m.content_hash = ? AND m.kind = ? AND m.status IN ('active','proposed') AND {{scope:m}} ORDER BY m.status, m.created_at LIMIT 1`)
	qMemLineage   = scoped("memory.lineage", `SELECT `+memCols+` FROM memories m WHERE (m.lineage_id = ?1 OR m.target_lineage_id = ?1) AND {{scope:m}} ORDER BY m.created_at, m.seq`)
	qMemTags      = scoped("memory.tags", `SELECT t.memory_id, t.tag FROM memory_tags t JOIN memories m ON m.id = t.memory_id WHERE t.memory_id IN (SELECT value FROM json_each(?)) AND {{scope:m}} ORDER BY t.tag`)
	qMemTagInsert = scoped("memory.tag_insert", `INSERT OR IGNORE INTO memory_tags(memory_id, tag) SELECT id, ?2 FROM memories WHERE id = ?1 AND {{scope}}`)
	qMemTagsClear = scoped("memory.tags_clear", `DELETE FROM memory_tags WHERE memory_id = ?1 AND EXISTS (SELECT 1 FROM memories WHERE id = ?1 AND {{scope}})`)

	qEvidenceInsert = scoped("evidence.insert", `INSERT INTO memory_evidence(id, memory_id, type, ref, note, added_by_principal_id, created_at)
		SELECT ?1, id, ?3, ?4, ?5, ?6, ?7 FROM memories WHERE id = ?2 AND {{scope}}`)
	qEvidenceList = scoped("evidence.list", `SELECT e.id, e.memory_id, e.type, e.ref, e.note, e.added_by_principal_id, e.created_at
		FROM memory_evidence e JOIN memories m ON m.id = e.memory_id WHERE e.memory_id IN (SELECT value FROM json_each(?)) AND {{scope:m}} ORDER BY e.created_at`)

	qVersionInsert = scoped("version.insert", `INSERT INTO memory_versions(memory_id, version, snapshot, changed_by_principal_id, changed_by_agent, change_type, change_reason, created_at)
		SELECT id, version, `+versionSnapshot+`, ?2, ?3, ?4, ?5, ?6 FROM memories WHERE id = ?1 AND {{scope}}`)
	qVersionList = scoped("version.list", `SELECT v.memory_id, v.version, v.snapshot, v.changed_by_principal_id, v.changed_by_agent, v.change_type, v.change_reason, v.created_at
		FROM memory_versions v JOIN memories m ON m.id = v.memory_id WHERE v.memory_id IN (SELECT value FROM json_each(?)) AND {{scope:m}} ORDER BY v.memory_id, v.version`)

	qMemSupersede = scoped("memory.mark_superseded", `UPDATE memories SET status = 'superseded', superseded_by_id = ?2, supersede_reason = ?3, version = version + 1, updated_at = ?4
		WHERE id = ?1 AND status = 'active' AND lineage_id = ?5 AND scope_id = ?6 AND {{scope}}`)
	qMemActivate = scoped("memory.activate", `UPDATE memories SET status = 'active', review_state = 'approved', approved_by = ?2, approved_via = ?3, lineage_id = ?4,
		source_type = ?5, version = version + 1, updated_at = ?6 WHERE id = ?1 AND status = 'proposed' AND {{scope}}`)
	qMemSetStatus = scoped("memory.set_status", `UPDATE memories SET status = ?2, review_state = ?3, version = version + 1, updated_at = ?4
		WHERE id = ?1 AND status = ?5 AND {{scope}}`)
	qMemUpdate = scoped("memory.update", `UPDATE memories SET title = ?2, content = ?3, tier = ?4, pinned = ?5, confidence = ?6, rationale = ?7, details = ?8,
		valid_until = ?9, review_after = ?10, content_hash = ?11, review_state = ?12, version = version + 1, updated_at = ?13
		WHERE id = ?1 AND status IN ('active','proposed') AND {{scope}}`)
	qMemConfirm = scoped("memory.confirm", `UPDATE memories SET confirmations = confirmations + 1, last_confirmed_at = ?2, confidence = max(confidence, ?3),
		version = version + 1, updated_at = ?2 WHERE id = ?1 AND status IN ('active','proposed') AND {{scope}}`)
	qMemDelete = scoped("memory.delete", `DELETE FROM memories WHERE id = ? AND {{scope}}`)

	qTombstoneInsert = scoped("tombstone.insert", `INSERT INTO tombstones(id, memory_id, scope_id, title, deleted_by_principal_id, reason, created_at)
		SELECT ?1, id, scope_id, title, ?2, ?3, ?4 FROM memories WHERE id = ?5 AND {{scope}}`)

	qApprovalInsert = scoped("approval.insert", `INSERT INTO approvals(id, memory_id, from_status, to_status, approver_class, approver_id, marks_user_stated, note, created_at)
		SELECT ?1, id, status, ?2, ?3, ?4, ?5, ?6, ?7 FROM memories WHERE id = ?8 AND status = 'proposed' AND {{scope}}`)
	qApprovalList = scoped("approval.list", `SELECT a.id, a.memory_id, a.from_status, a.to_status, a.approver_class, a.approver_id, a.marks_user_stated, a.note, a.created_at
		FROM approvals a JOIN memories m ON m.id = a.memory_id WHERE a.memory_id IN (SELECT value FROM json_each(?)) AND {{scope:m}} ORDER BY a.created_at`)

	qLinkInsert = scoped("link.insert", `INSERT INTO links(source_id, target_id, label, created_by_principal_id, created_at)
		SELECT s.id, t.id, ?3, ?4, ?5 FROM memories s, memories t WHERE s.id = ?1 AND t.id = ?2 AND {{scope:s}} AND {{scope:t}}`)
	qLinkDelete = scoped("link.delete", `DELETE FROM links WHERE source_id = ?1 AND target_id = ?2 AND EXISTS (SELECT 1 FROM memories s WHERE s.id = ?1 AND {{scope:s}})`)
	qLinkDeletePrefix = scoped("link.delete_prefix", `DELETE FROM links WHERE label LIKE ?1 || '%' AND source_id IN (
		SELECT m.id FROM memories m WHERE m.scope_id = ?2 AND {{scope:m}})`)
	qLinksFor = scoped("link.for", `SELECT l.source_id, l.target_id, l.label, l.created_by_principal_id, l.created_at FROM links l
		JOIN memories s ON s.id = l.source_id JOIN memories t ON t.id = l.target_id
		WHERE (l.source_id IN (SELECT value FROM json_each(?1)) OR l.target_id IN (SELECT value FROM json_each(?1))) AND {{scope:s}} AND {{scope:t}}
		ORDER BY l.created_at`)
)

// Normalization and hashing --------------------------------------------------

// NormalizeText lowercases, strips punctuation and collapses whitespace.
func NormalizeText(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	space := true
	for _, r := range strings.ToLower(s) {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
			space = false
		default:
			if !space {
				b.WriteByte(' ')
				space = true
			}
		}
	}
	return strings.TrimSpace(b.String())
}

// ContentHash is the dedupe key of a memory body.
func ContentHash(content string) string {
	sum := sha256.Sum256([]byte(NormalizeText(content)))
	return hex.EncodeToString(sum[:16])
}

// Inputs ---------------------------------------------------------------------

type NewMemory struct {
	ScopeID            string
	SessionID          string
	CreatedSessionID   string
	Kind               string
	Title              string
	Content            string
	Tier               int
	Pinned             bool
	Status             string
	ReviewState        string
	Confidence         float64
	SourceType         string
	Client             string
	Agent              string
	Model              string
	UserConfirmedClaim bool
	SupersedesID       string
	SupersedeReason    string
	DerivedFromID      string
	Rationale          string
	Details            json.RawMessage
	ValidFrom          string
	ValidUntil         string
	ReviewAfter        string
	Tags               []string
	Evidence           []EvidenceIn
}

const (
	MaxTitle   = 300
	MaxContent = 64 * 1024
)

func (nm *NewMemory) validate() error {
	nm.Title = strings.TrimSpace(nm.Title)
	nm.Content = strings.TrimSpace(nm.Content)
	if nm.ScopeID == "" {
		return fmt.Errorf("%w: scope required", ErrInvalid)
	}
	if !ValidKind(nm.Kind) {
		return fmt.Errorf("%w: unknown kind %q", ErrInvalid, nm.Kind)
	}
	if nm.Title == "" || len(nm.Title) > MaxTitle {
		return fmt.Errorf("%w: title required (max %d bytes)", ErrInvalid, MaxTitle)
	}
	if nm.Content == "" || len(nm.Content) > MaxContent {
		return fmt.Errorf("%w: content required (max %d bytes)", ErrInvalid, MaxContent)
	}
	if nm.Tier == 0 {
		nm.Tier = 2
	}
	if nm.Tier < 1 || nm.Tier > 3 {
		return fmt.Errorf("%w: tier must be 1, 2 or 3", ErrInvalid)
	}
	if nm.Status != "active" && nm.Status != "proposed" {
		return fmt.Errorf("%w: new memories are active or proposed", ErrInvalid)
	}
	if nm.ReviewState == "" {
		nm.ReviewState = "unreviewed"
	}
	if nm.Confidence == 0 {
		nm.Confidence = 0.5
	}
	if nm.Confidence < 0 || nm.Confidence > 1 {
		return fmt.Errorf("%w: confidence must be between 0 and 1", ErrInvalid)
	}
	switch nm.SourceType {
	case "agent_inferred", "tool_observed", "imported", "human_ui":
	default:
		return fmt.Errorf("%w: source_type %q cannot be set on insert", ErrInvalid, nm.SourceType)
	}
	if len(nm.Details) == 0 {
		nm.Details = json.RawMessage("{}")
	} else if !json.Valid(nm.Details) {
		return fmt.Errorf("%w: details must be JSON", ErrInvalid)
	}
	var err error
	for _, p := range []*string{&nm.ValidFrom, &nm.ValidUntil, &nm.ReviewAfter} {
		if *p, err = NormalizeTime(*p); err != nil {
			return err
		}
	}
	for _, e := range nm.Evidence {
		if !ValidEvidenceType(e.Type) {
			return fmt.Errorf("%w: unknown evidence type %q", ErrInvalid, e.Type)
		}
		if len(e.Ref) > 2000 || len(e.Note) > 2000 {
			return fmt.Errorf("%w: evidence ref/note too long", ErrInvalid)
		}
	}
	if len(nm.Evidence) > 20 {
		return fmt.Errorf("%w: at most 20 evidence items", ErrInvalid)
	}
	return nil
}

// Write helpers ----------------------------------------------------------------

type actor struct {
	principal string
	agent     string
}

func actorOf(ss auth.ScopeSet, agent string) actor {
	return actor{principal: ss.Principal().ID, agent: agent}
}

func (w wx) getMemory(id string, allowed []string) (Memory, error) {
	m, err := scanMemory(w.row(qMemGet, id, ids(allowed)))
	return m, notFound(err)
}

func (w wx) addVersion(id string, allowed []string, a actor, changeType, reason string) error {
	n, err := w.execN(qVersionInsert, id, a.principal, a.agent, changeType, reason, now(), ids(allowed))
	if err == nil && n != 1 {
		err = fmt.Errorf("version insert for %s affected %d rows", id, n)
	}
	return err
}

func (w wx) insertMemory(id string, nm NewMemory, lineage, targetLineage string, a actor, allowed string) error {
	ts := now()
	n, err := w.execN(qMemInsert, id, nm.ScopeID, nullStr(nm.SessionID), nm.Kind, nm.Title, nm.Content, nm.Tier, b2i(nm.Pinned), nm.Status, nm.ReviewState,
		nm.Confidence, nm.SourceType, a.principal, nm.Client, nm.Agent, nm.Model, nullStr(nm.CreatedSessionID),
		b2i(nm.UserConfirmedClaim), lineage, nullStr(nm.SupersedesID), nullStr(targetLineage), nullStr(nm.DerivedFromID), nm.Rationale, string(nm.Details),
		nullStr(nm.ValidFrom), nullStr(nm.ValidUntil), nullStr(nm.ReviewAfter), ContentHash(nm.Content), ts, ts, strings.TrimSpace(nm.SupersedeReason), allowed)
	if err != nil {
		return mapTriggerErr(err)
	}
	if n != 1 {
		return ErrNotFound
	}
	scope := ids([]string{nm.ScopeID})
	for _, t := range normTags(nm.Tags) {
		if _, err := w.exec(qMemTagInsert, id, t, scope); err != nil {
			return err
		}
	}
	for _, e := range nm.Evidence {
		if _, err := w.exec(qEvidenceInsert, newID(), id, e.Type, strings.TrimSpace(e.Ref), strings.TrimSpace(e.Note), a.principal, ts, scope); err != nil {
			return err
		}
	}
	return nil
}

func mapTriggerErr(err error) error {
	switch {
	case err == nil:
		return nil
	case isAbort(err, "protected memory must be approved"):
		return fmt.Errorf("%w: protected memory must be approved before it becomes active", ErrForbidden)
	case isAbort(err, "user_stated requires a human approval"):
		return fmt.Errorf("%w: user_stated requires a human approval", ErrForbidden)
	case isAbort(err, "session belongs to another scope"):
		return ErrNotFound
	case isAbort(err, "memories_one_active_per_lineage") || (isUniqueViolation(err) && strings.Contains(err.Error(), "lineage_id")):
		return fmt.Errorf("%w: lineage already has an active memory", ErrConflict)
	case isAbort(err, "links cannot cross project scopes"), isAbort(err, "global memories cannot link to project memories"):
		return fmt.Errorf("%w: %s", ErrInvalid, triggerMsg(err))
	case isAbort(err, "hard delete requires a tombstone"):
		return fmt.Errorf("%w: hard delete requires a tombstone", ErrForbidden)
	}
	return err
}

func triggerMsg(err error) string {
	m := err.Error()
	if i := strings.LastIndex(m, ": "); i >= 0 {
		return strings.TrimSpace(m[i+2:])
	}
	return m
}

// permission errors: out-of-scope reads are 404, readable-but-not-writable is 403.
func denyWrite(ss auth.ScopeSet, scopeID string) error {
	if !ss.CanRead(scopeID) {
		return ErrNotFound
	}
	return ErrForbidden
}

// CreateMemory inserts a new memory (active or proposed) with its first version.
// A proposal with SupersedesID keeps its own lineage and records the lineage of
// the memory it wants to replace.
func (s *Store) CreateMemory(ctx context.Context, ss auth.ScopeSet, nm NewMemory) (Memory, error) {
	if err := nm.validate(); err != nil {
		return Memory{}, err
	}
	switch nm.Status {
	case "active":
		if !ss.CanWrite(nm.ScopeID) {
			return Memory{}, denyWrite(ss, nm.ScopeID)
		}
	case "proposed":
		if !ss.CanPropose(nm.ScopeID) {
			return Memory{}, denyWrite(ss, nm.ScopeID)
		}
	}
	if nm.SupersedesID != "" && nm.Status != "proposed" {
		return Memory{}, fmt.Errorf("%w: use ReplaceMemory for active replacements", ErrInvalid)
	}
	id := newID()
	scope := []string{nm.ScopeID}
	a := actorOf(ss, nm.Agent)
	var out Memory
	err := s.write(ctx, func(w wx) error {
		target := ""
		if nm.SupersedesID != "" {
			old, err := w.getMemory(nm.SupersedesID, scope)
			if err != nil {
				return err
			}
			if old.Status != "active" {
				return fmt.Errorf("%w: memory %s is %s, not active", ErrConflict, old.ID, old.Status)
			}
			target = old.LineageID
		}
		if err := w.insertMemory(id, nm, newID(), target, a, writable(ss)); err != nil {
			return err
		}
		if err := s.failpoint("create.after_insert"); err != nil {
			return err
		}
		if err := w.addVersion(id, scope, a, "created", nm.SupersedeReason); err != nil {
			return err
		}
		if nm.Status == "proposed" {
			payload, _ := json.Marshal(map[string]string{"title": nm.Title, "kind": nm.Kind, "supersedes_id": nm.SupersedesID})
			if err := w.upsertReview(nm.ScopeID, "proposed", "proposed:"+id, []string{id}, payload); err != nil {
				return err
			}
		}
		m, err := w.getMemory(id, scope)
		out = m
		return err
	})
	if err != nil {
		return Memory{}, err
	}
	s.bumpScope(nm.ScopeID)
	return s.decorate(ctx, ss, out)
}

// ReplaceMemory is commitReplacement for the case where the new memory may be
// active immediately: in one BEGIN IMMEDIATE transaction it marks the old memory
// superseded, inserts the new one in the same lineage, and writes both versions.
func (s *Store) ReplaceMemory(ctx context.Context, ss auth.ScopeSet, oldID string, nm NewMemory, reason string) (Memory, Memory, error) {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return Memory{}, Memory{}, fmt.Errorf("%w: reason required", ErrInvalid)
	}
	nm.Status = "active"
	nm.SupersedesID = ""
	var newMem, oldMem Memory
	err := s.write(ctx, func(w wx) error {
		old, err := w.getMemory(oldID, ss.ReadIDs())
		if err != nil {
			return err
		}
		if nm.ScopeID == "" {
			nm.ScopeID = old.ScopeID
		}
		if nm.Kind == "" {
			nm.Kind = old.Kind
		}
		if err := nm.validate(); err != nil {
			return err
		}
		if nm.ScopeID != old.ScopeID {
			return fmt.Errorf("%w: a replacement stays in the scope of the memory it replaces", ErrInvalid)
		}
		if !ss.CanWrite(old.ScopeID) {
			return denyWrite(ss, old.ScopeID)
		}
		if old.Status != "active" {
			return fmt.Errorf("%w: memory %s is %s, not active", ErrConflict, old.ID, old.Status)
		}
		newMem, oldMem, err = s.commitReplacement(w, ss, old, nm, reason, nil)
		return err
	})
	if err != nil {
		return Memory{}, Memory{}, err
	}
	s.bumpScope(oldMem.ScopeID)
	newMem, err = s.decorate(ctx, ss, newMem)
	return newMem, oldMem, err
}

// commitReplacement runs inside the caller's transaction. When approved is set
// the new memory already exists as a proposal and is activated instead of inserted.
func (s *Store) commitReplacement(w wx, ss auth.ScopeSet, old Memory, nm NewMemory, reason string, approved *approvalCtx) (Memory, Memory, error) {
	scope := []string{old.ScopeID}
	sc := ids(scope)
	a := actorOf(ss, nm.Agent)
	newID := newID()
	if approved != nil {
		newID = approved.memoryID
	}
	n, err := w.execN(qMemSupersede, old.ID, newID, reason, now(), old.LineageID, old.ScopeID, sc)
	if err != nil {
		return Memory{}, Memory{}, mapTriggerErr(err)
	}
	if n != 1 {
		return Memory{}, Memory{}, fmt.Errorf("%w: memory %s is no longer active in its lineage", ErrConflict, old.ID)
	}
	if err := s.failpoint("replace.after_old"); err != nil {
		return Memory{}, Memory{}, err
	}
	if approved == nil {
		nm.SupersedeReason = reason
		nm.SupersedesID = old.ID
		if err := w.insertMemory(newID, nm, old.LineageID, "", a, sc); err != nil {
			return Memory{}, Memory{}, err
		}
	} else {
		n, err := w.execN(qMemActivate, newID, approved.approver, approved.via, old.LineageID, approved.source, now(), sc)
		if err != nil {
			return Memory{}, Memory{}, mapTriggerErr(err)
		}
		if n != 1 {
			return Memory{}, Memory{}, fmt.Errorf("%w: proposal is no longer pending", ErrConflict)
		}
	}
	if err := s.failpoint("replace.after_insert"); err != nil {
		return Memory{}, Memory{}, err
	}
	if err := w.addVersion(old.ID, scope, a, "superseded", reason); err != nil {
		return Memory{}, Memory{}, err
	}
	changeType := "created"
	if approved != nil {
		changeType = "approved"
	}
	if err := w.addVersion(newID, scope, a, changeType, "supersedes "+old.ID+": "+reason); err != nil {
		return Memory{}, Memory{}, err
	}
	if err := s.failpoint("replace.after_versions"); err != nil {
		return Memory{}, Memory{}, err
	}
	newMem, err := w.getMemory(newID, scope)
	if err != nil {
		return Memory{}, Memory{}, err
	}
	oldMem, err := w.getMemory(old.ID, scope)
	return newMem, oldMem, err
}

type approvalCtx struct {
	memoryID string
	approver string
	via      string
	source   string
}

type ApproveInput struct {
	Note            string `json:"note"`
	MarksUserStated bool   `json:"marks_user_stated"`
}

// Approve is the only path from proposed to active. It accepts the UI admin
// session or a human token with can_approve on the memory's scope.
func (s *Store) Approve(ctx context.Context, ss auth.ScopeSet, id string, in ApproveInput) (Memory, error) {
	p := ss.Principal()
	if !p.Class.IsHuman() {
		return Memory{}, fmt.Errorf("%w: only a human principal can approve", ErrForbidden)
	}
	via := "token"
	if p.Class == auth.AdminSession {
		via = "ui"
	}
	var out Memory
	var scopeID string
	err := s.write(ctx, func(w wx) error {
		m, err := w.getMemory(id, ss.ReadIDs())
		if err != nil {
			return err
		}
		scopeID = m.ScopeID
		if !ss.CanApprove(m.ScopeID) {
			return fmt.Errorf("%w: no approve grant on this scope", ErrForbidden)
		}
		if m.Status != "proposed" {
			return fmt.Errorf("%w: memory is %s, not proposed", ErrConflict, m.Status)
		}
		scope := []string{m.ScopeID}
		sc := ids(scope)
		n, err := w.execN(qApprovalInsert, newID(), "active", string(p.Class), p.ID, b2i(in.MarksUserStated), strings.TrimSpace(in.Note), now(), m.ID, ids(ss.ApprovableIDs()))
		if err != nil {
			return mapTriggerErr(err)
		}
		if n != 1 {
			return ErrNotFound
		}
		if err := s.failpoint("approve.after_approval"); err != nil {
			return err
		}
		source := m.SourceType
		if in.MarksUserStated {
			source = "user_stated"
		}
		actx := &approvalCtx{memoryID: m.ID, approver: p.ID, via: via, source: source}
		if m.SupersedesID != "" {
			old, err := w.getMemory(m.SupersedesID, scope)
			if err != nil {
				return fmt.Errorf("%w: the memory this proposal replaces is gone", ErrConflict)
			}
			if old.Status != "active" || old.ScopeID != m.ScopeID || old.LineageID != m.TargetLineageID {
				return fmt.Errorf("%w: the memory this proposal replaces changed since it was proposed", ErrConflict)
			}
			nm := NewMemory{Agent: ""}
			newMem, _, err := s.commitReplacement(w, ss, old, nm, m.SupersedeReason, actx)
			if err != nil {
				return err
			}
			out = newMem
		} else {
			n, err := w.execN(qMemActivate, m.ID, p.ID, via, m.LineageID, source, now(), sc)
			if err != nil {
				return mapTriggerErr(err)
			}
			if n != 1 {
				return fmt.Errorf("%w: proposal is no longer pending", ErrConflict)
			}
			if err := w.addVersion(m.ID, scope, actorOf(ss, ""), "approved", strings.TrimSpace(in.Note)); err != nil {
				return err
			}
			if out, err = w.getMemory(m.ID, scope); err != nil {
				return err
			}
		}
		return w.closeReview(m.ScopeID, "proposed:"+m.ID, "resolved", "approved", p.ID)
	})
	if err != nil {
		return Memory{}, err
	}
	s.bumpScope(scopeID)
	return s.decorate(ctx, ss, out)
}

// Reject moves a proposal to rejected. Protected proposals need approve rights;
// otherwise a writer of the scope, or the principal that proposed it, may reject.
func (s *Store) Reject(ctx context.Context, ss auth.ScopeSet, id, note string) (Memory, error) {
	var out Memory
	err := s.write(ctx, func(w wx) error {
		m, err := w.getMemory(id, ss.ReadIDs())
		if err != nil {
			return err
		}
		if m.Status != "proposed" {
			return fmt.Errorf("%w: memory is %s, not proposed", ErrConflict, m.Status)
		}
		sc, err := w.scopeInfo(m.ScopeID)
		if err != nil {
			return err
		}
		own := m.CreatedByPrincipalID == ss.Principal().ID
		if Protected(sc.kind, sc.policy, m.Kind) && !ss.CanApprove(m.ScopeID) && !own {
			return fmt.Errorf("%w: rejecting a protected proposal needs approve rights", ErrForbidden)
		}
		if !ss.CanWrite(m.ScopeID) && !own && !ss.CanApprove(m.ScopeID) {
			return denyWrite(ss, m.ScopeID)
		}
		out, err = w.setStatus(ss, m, "rejected", "disputed", "rejected", note)
		if err != nil {
			return err
		}
		return w.closeReview(m.ScopeID, "proposed:"+m.ID, "resolved", "rejected", ss.Principal().ID)
	})
	if err != nil {
		return Memory{}, err
	}
	s.bumpScope(out.ScopeID)
	return s.decorate(ctx, ss, out)
}

func (w wx) setStatus(ss auth.ScopeSet, m Memory, to, reviewState, changeType, reason string) (Memory, error) {
	scope := []string{m.ScopeID}
	if reviewState == "" {
		reviewState = m.ReviewState
	}
	n, err := w.execN(qMemSetStatus, m.ID, to, reviewState, now(), m.Status, ids(scope))
	if err != nil {
		return Memory{}, mapTriggerErr(err)
	}
	if n != 1 {
		return Memory{}, fmt.Errorf("%w: memory changed concurrently", ErrConflict)
	}
	if err := w.addVersion(m.ID, scope, actorOf(ss, ""), changeType, strings.TrimSpace(reason)); err != nil {
		return Memory{}, err
	}
	return w.getMemory(m.ID, scope)
}

var allowedTransitions = map[string][]string{
	"active": {"deprecated", "archived"},
}

// Transition moves an active memory to deprecated or archived. Protected
// memories need approve rights.
func (s *Store) Transition(ctx context.Context, ss auth.ScopeSet, id, to, reason string) (Memory, error) {
	var out Memory
	err := s.write(ctx, func(w wx) error {
		m, err := w.getMemory(id, ss.ReadIDs())
		if err != nil {
			return err
		}
		ok := false
		for _, t := range allowedTransitions[m.Status] {
			ok = ok || t == to
		}
		if !ok {
			return fmt.Errorf("%w: cannot move %s memory to %s", ErrConflict, m.Status, to)
		}
		if !ss.CanWrite(m.ScopeID) {
			return denyWrite(ss, m.ScopeID)
		}
		sc, err := w.scopeInfo(m.ScopeID)
		if err != nil {
			return err
		}
		if Protected(sc.kind, sc.policy, m.Kind) && !ss.CanApprove(m.ScopeID) {
			return fmt.Errorf("%w: changing a protected memory needs approve rights", ErrForbidden)
		}
		out, err = w.setStatus(ss, m, to, "", to, reason)
		return err
	})
	if err != nil {
		return Memory{}, err
	}
	s.bumpScope(out.ScopeID)
	return s.decorate(ctx, ss, out)
}

// MemoryPatch edits fields in place. Nil fields are left unchanged.
type MemoryPatch struct {
	Title       *string          `json:"title"`
	Content     *string          `json:"content"`
	Tier        *int             `json:"tier"`
	Pinned      *bool            `json:"pinned"`
	Confidence  *float64         `json:"confidence"`
	Rationale   *string          `json:"rationale"`
	Details     *json.RawMessage `json:"details"`
	ValidUntil  *string          `json:"valid_until"`
	ReviewAfter *string          `json:"review_after"`
	ReviewState *string          `json:"review_state"`
	Tags        *[]string        `json:"tags"`
	Reason      string           `json:"reason"`
}

// UpdateMemory edits an active or proposed memory and records a version.
// Tier, pin and protected memories are human-only.
func (s *Store) UpdateMemory(ctx context.Context, ss auth.ScopeSet, id string, p MemoryPatch) (Memory, error) {
	var out Memory
	err := s.write(ctx, func(w wx) error {
		m, err := w.getMemory(id, ss.ReadIDs())
		if err != nil {
			return err
		}
		if !ss.CanWrite(m.ScopeID) {
			return denyWrite(ss, m.ScopeID)
		}
		if m.Status != "active" && m.Status != "proposed" {
			return fmt.Errorf("%w: memory is %s and can no longer change", ErrConflict, m.Status)
		}
		human := ss.Principal().Class.IsHuman()
		sc, err := w.scopeInfo(m.ScopeID)
		if err != nil {
			return err
		}
		if m.Status == "active" && Protected(sc.kind, sc.policy, m.Kind) && !ss.CanApprove(m.ScopeID) {
			return fmt.Errorf("%w: edit a protected memory through a superseding proposal", ErrForbidden)
		}
		if (p.Tier != nil || p.Pinned != nil) && !human {
			return fmt.Errorf("%w: tier and pin change only by a human", ErrForbidden)
		}
		nm := m
		if p.Title != nil {
			nm.Title = strings.TrimSpace(*p.Title)
		}
		if p.Content != nil {
			nm.Content = strings.TrimSpace(*p.Content)
		}
		if p.Tier != nil {
			nm.Tier = *p.Tier
		}
		if p.Pinned != nil {
			nm.Pinned = *p.Pinned
		}
		if p.Confidence != nil {
			nm.Confidence = *p.Confidence
		}
		if p.Rationale != nil {
			nm.Rationale = *p.Rationale
		}
		details := string(m.Details)
		if details == "" {
			details = "{}"
		}
		if p.Details != nil {
			if !json.Valid(*p.Details) {
				return fmt.Errorf("%w: details must be JSON", ErrInvalid)
			}
			details = string(*p.Details)
		}
		if p.ValidUntil != nil {
			if nm.ValidUntil, err = NormalizeTime(*p.ValidUntil); err != nil {
				return err
			}
		}
		if p.ReviewAfter != nil {
			if nm.ReviewAfter, err = NormalizeTime(*p.ReviewAfter); err != nil {
				return err
			}
		}
		if p.ReviewState != nil {
			switch *p.ReviewState {
			case "unreviewed", "needs_review", "disputed":
				nm.ReviewState = *p.ReviewState
			case "approved":
				return fmt.Errorf("%w: approval goes through Approve", ErrInvalid)
			default:
				return fmt.Errorf("%w: unknown review_state", ErrInvalid)
			}
		}
		if nm.Title == "" || len(nm.Title) > MaxTitle || nm.Content == "" || len(nm.Content) > MaxContent {
			return fmt.Errorf("%w: title and content required", ErrInvalid)
		}
		if nm.Tier < 1 || nm.Tier > 3 || nm.Confidence < 0 || nm.Confidence > 1 {
			return fmt.Errorf("%w: tier 1..3 and confidence 0..1", ErrInvalid)
		}
		scope := []string{m.ScopeID}
		sc2 := ids(scope)
		n, err := w.execN(qMemUpdate, m.ID, nm.Title, nm.Content, nm.Tier, b2i(nm.Pinned), nm.Confidence, nm.Rationale, details,
			nullStr(nm.ValidUntil), nullStr(nm.ReviewAfter), ContentHash(nm.Content), nm.ReviewState, now(), sc2)
		if err != nil {
			return mapTriggerErr(err)
		}
		if n != 1 {
			return fmt.Errorf("%w: memory changed concurrently", ErrConflict)
		}
		if p.Tags != nil {
			if _, err := w.exec(qMemTagsClear, m.ID, sc2); err != nil {
				return err
			}
			for _, t := range normTags(*p.Tags) {
				if _, err := w.exec(qMemTagInsert, m.ID, t, sc2); err != nil {
					return err
				}
			}
		}
		if err := w.addVersion(m.ID, scope, actorOf(ss, ""), "updated", p.Reason); err != nil {
			return err
		}
		out, err = w.getMemory(m.ID, scope)
		return err
	})
	if err != nil {
		return Memory{}, err
	}
	s.bumpScope(out.ScopeID)
	return s.decorate(ctx, ss, out)
}

// Confirm records that a duplicate observation was seen again.
func (s *Store) Confirm(ctx context.Context, ss auth.ScopeSet, id string, confidence float64, ev []EvidenceIn, agent string) (Memory, error) {
	var out Memory
	err := s.write(ctx, func(w wx) error {
		m, err := w.getMemory(id, ss.ReadIDs())
		if err != nil {
			return err
		}
		if !ss.CanPropose(m.ScopeID) {
			return denyWrite(ss, m.ScopeID)
		}
		scope := []string{m.ScopeID}
		sc := ids(scope)
		ts := now()
		if confidence > 1 {
			confidence = 1
		}
		n, err := w.execN(qMemConfirm, m.ID, ts, confidence, sc)
		if err != nil {
			return err
		}
		if n != 1 {
			return fmt.Errorf("%w: memory changed concurrently", ErrConflict)
		}
		for _, e := range ev {
			if !ValidEvidenceType(e.Type) {
				continue
			}
			if _, err := w.exec(qEvidenceInsert, newID(), m.ID, e.Type, strings.TrimSpace(e.Ref), strings.TrimSpace(e.Note), ss.Principal().ID, ts, sc); err != nil {
				return err
			}
		}
		if err := w.addVersion(m.ID, scope, actorOf(ss, agent), "confirmed", ""); err != nil {
			return err
		}
		out, err = w.getMemory(m.ID, scope)
		return err
	})
	if err != nil {
		return Memory{}, err
	}
	return s.decorate(ctx, ss, out)
}

// AddEvidence attaches evidence to a memory without changing its content.
func (s *Store) AddEvidence(ctx context.Context, ss auth.ScopeSet, id string, e EvidenceIn) (Evidence, error) {
	if !ValidEvidenceType(e.Type) || strings.TrimSpace(e.Ref) == "" && strings.TrimSpace(e.Note) == "" {
		return Evidence{}, fmt.Errorf("%w: evidence needs a known type and a ref or note", ErrInvalid)
	}
	ev := Evidence{ID: newID(), Type: e.Type, Ref: strings.TrimSpace(e.Ref), Note: strings.TrimSpace(e.Note), AddedBy: ss.Principal().ID, CreatedAt: now()}
	err := s.write(ctx, func(w wx) error {
		m, err := w.getMemory(id, ss.ReadIDs())
		if err != nil {
			return err
		}
		if !ss.CanPropose(m.ScopeID) {
			return denyWrite(ss, m.ScopeID)
		}
		n, err := w.execN(qEvidenceInsert, ev.ID, m.ID, ev.Type, ev.Ref, ev.Note, ev.AddedBy, ev.CreatedAt, ids([]string{m.ScopeID}))
		if err == nil && n != 1 {
			err = ErrNotFound
		}
		return err
	})
	return ev, err
}

// HardDelete removes a memory permanently, leaving a tombstone. Needs approve rights.
func (s *Store) HardDelete(ctx context.Context, ss auth.ScopeSet, id, reason string) error {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return fmt.Errorf("%w: reason required", ErrInvalid)
	}
	var scopeID string
	err := s.write(ctx, func(w wx) error {
		m, err := w.getMemory(id, ss.ReadIDs())
		if err != nil {
			return err
		}
		scopeID = m.ScopeID
		if !ss.CanApprove(m.ScopeID) {
			return fmt.Errorf("%w: hard delete needs approve rights", ErrForbidden)
		}
		sc := ids([]string{m.ScopeID})
		if _, err := w.exec(qTombstoneInsert, newID(), ss.Principal().ID, reason, now(), m.ID, sc); err != nil {
			return err
		}
		n, err := w.execN(qMemDelete, m.ID, sc)
		if err != nil {
			return mapTriggerErr(err)
		}
		if n != 1 {
			return ErrNotFound
		}
		return nil
	})
	if err == nil {
		s.bumpScope(scopeID)
	}
	return err
}

// Reads ----------------------------------------------------------------------

// GetMemory returns one memory with tags and evidence, or ErrNotFound when it
// is outside the caller's readable scopes.
func (s *Store) GetMemory(ctx context.Context, ss auth.ScopeSet, id string) (Memory, error) {
	m, err := scanMemory(s.reader(ctx).row(qMemGet, id, readable(ss)))
	if err != nil {
		return m, notFound(err)
	}
	s.access.touch(m.Seq)
	return s.decorate(ctx, ss, m)
}

// GetMemories loads several memories at once, silently skipping unreadable ids.
func (s *Store) GetMemories(ctx context.Context, ss auth.ScopeSet, list []string) ([]Memory, error) {
	if len(list) == 0 {
		return nil, nil
	}
	ms, err := collectRows(s.reader(ctx).q(qMemGetMany, ids(list), readable(ss)), scanMemory)
	if err != nil {
		return nil, err
	}
	return ms, s.attachTags(ctx, ss, ms)
}

func (s *Store) decorate(ctx context.Context, ss auth.ScopeSet, m Memory) (Memory, error) {
	list := []Memory{m}
	if err := s.attachTags(ctx, ss, list); err != nil {
		return m, err
	}
	m = list[0]
	ev, err := collectRows(s.reader(ctx).q(qEvidenceList, ids([]string{m.ID}), ids([]string{m.ScopeID})), scanEvidence)
	if err != nil {
		return m, err
	}
	m.Evidence = ev
	return m, nil
}

func scanEvidence(r rowScanner) (Evidence, error) {
	var e Evidence
	var mid string
	return e, r.Scan(&e.ID, &mid, &e.Type, &e.Ref, &e.Note, &e.AddedBy, &e.CreatedAt)
}

// attachTags fills Tags on the given memories (same order) in one query.
func (s *Store) attachTags(ctx context.Context, ss auth.ScopeSet, ms []Memory) error {
	if len(ms) == 0 {
		return nil
	}
	idList := make([]string, len(ms))
	for i, m := range ms {
		idList[i] = m.ID
	}
	rows, err := s.reader(ctx).query(qMemTags, ids(idList), readable(ss))
	if err != nil {
		return err
	}
	defer rows.Close()
	tags := map[string][]string{}
	for rows.Next() {
		var id, t string
		if err := rows.Scan(&id, &t); err != nil {
			return err
		}
		tags[id] = append(tags[id], t)
	}
	for i := range ms {
		if t := tags[ms[i].ID]; t != nil {
			ms[i].Tags = t
		}
	}
	return rows.Err()
}

// FindByHash returns an active or proposed memory with the same normalized body.
func (s *Store) FindByHash(ctx context.Context, ss auth.ScopeSet, scopeID, kind, content string) (Memory, bool, error) {
	if !ss.CanRead(scopeID) {
		return Memory{}, false, nil
	}
	m, err := scanMemory(s.reader(ctx).row(qMemByHash, ContentHash(content), kind, ids([]string{scopeID})))
	if errors.Is(notFound(err), ErrNotFound) {
		return Memory{}, false, nil
	}
	if err != nil {
		return Memory{}, false, err
	}
	return m, true, nil
}

// History is the full record of a memory: its versions, its lineage (including
// pending proposals targeting it), approvals and evidence.
type History struct {
	Memory    Memory     `json:"memory"`
	Versions  []Version  `json:"versions"`
	Lineage   []Memory   `json:"lineage"`
	Approvals []Approval `json:"approvals"`
}

func (s *Store) History(ctx context.Context, ss auth.ScopeSet, id string) (History, error) {
	var h History
	err := s.readTx(ctx, func(r rd) error {
		m, err := scanMemory(r.row(qMemGet, id, readable(ss)))
		if err != nil {
			return notFound(err)
		}
		sc := ids([]string{m.ScopeID})
		lineage, err := collectRows(r.q(qMemLineage, m.LineageID, sc), scanMemory)
		if err != nil {
			return err
		}
		// A pending proposal has its own lineage; include the chain it targets.
		if m.TargetLineageID != "" {
			more, err := collectRows(r.q(qMemLineage, m.TargetLineageID, sc), scanMemory)
			if err != nil {
				return err
			}
			lineage = mergeMemories(lineage, more)
		}
		idList := make([]string, 0, len(lineage)+1)
		idList = append(idList, m.ID)
		for _, x := range lineage {
			if x.ID != m.ID {
				idList = append(idList, x.ID)
			}
		}
		versions, err := collectRows(r.q(qVersionList, ids(idList), sc), func(rs rowScanner) (Version, error) {
			var v Version
			var snap string
			err := rs.Scan(&v.MemoryID, &v.Version, &snap, &v.ChangedBy, &v.ChangedAgent, &v.ChangeType, &v.ChangeReason, &v.CreatedAt)
			v.Snapshot = json.RawMessage(snap)
			return v, err
		})
		if err != nil {
			return err
		}
		approvals, err := collectRows(r.q(qApprovalList, ids(idList), sc), func(rs rowScanner) (Approval, error) {
			var a Approval
			return a, rs.Scan(&a.ID, &a.MemoryID, &a.FromStatus, &a.ToStatus, &a.ApproverClass, &a.ApproverID, &a.MarksUserStated, &a.Note, &a.CreatedAt)
		})
		if err != nil {
			return err
		}
		h = History{Memory: m, Versions: versions, Lineage: lineage, Approvals: approvals}
		return nil
	})
	if err != nil {
		return h, err
	}
	h.Memory, err = s.decorate(ctx, ss, h.Memory)
	if h.Versions == nil {
		h.Versions = []Version{}
	}
	if h.Approvals == nil {
		h.Approvals = []Approval{}
	}
	return h, err
}

func mergeMemories(a, b []Memory) []Memory {
	seen := map[string]bool{}
	for _, m := range a {
		seen[m.ID] = true
	}
	for _, m := range b {
		if !seen[m.ID] {
			a = append(a, m)
			seen[m.ID] = true
		}
	}
	return a
}

var (
	qMemAdopt = scoped("memory.adopt_lineage", `UPDATE memories SET lineage_id = ?2, supersedes_id = ?3, supersede_reason = ?4, version = version + 1, updated_at = ?5
		WHERE id = ?1 AND status = 'active' AND {{scope}}`)
	qMemBySeq = scoped("memory.by_seq", `SELECT `+memCols+` FROM memories m WHERE m.seq IN (SELECT value FROM json_each(?)) AND {{scope:m}}`)
)

// SupersedeWithExisting makes an existing active memory the replacement of
// another active memory in the same scope: the old one becomes superseded and
// the new one joins its lineage, in one transaction.
func (s *Store) SupersedeWithExisting(ctx context.Context, ss auth.ScopeSet, oldID, newID, reason string) (Memory, Memory, error) {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return Memory{}, Memory{}, fmt.Errorf("%w: reason required", ErrInvalid)
	}
	if oldID == newID {
		return Memory{}, Memory{}, fmt.Errorf("%w: a memory cannot supersede itself", ErrInvalid)
	}
	var nm, om Memory
	err := s.write(ctx, func(w wx) error {
		old, err := w.getMemory(oldID, ss.ReadIDs())
		if err != nil {
			return err
		}
		nw, err := w.getMemory(newID, ss.ReadIDs())
		if err != nil {
			return err
		}
		if old.ScopeID != nw.ScopeID {
			return fmt.Errorf("%w: both memories must be in the same scope", ErrInvalid)
		}
		if !ss.CanWrite(old.ScopeID) {
			return denyWrite(ss, old.ScopeID)
		}
		if old.Status != "active" || nw.Status != "active" {
			return fmt.Errorf("%w: both memories must be active (use a proposal for pending ones)", ErrConflict)
		}
		if old.LineageID == nw.LineageID {
			return fmt.Errorf("%w: memories already share a lineage", ErrConflict)
		}
		sc, err := w.scopeInfo(old.ScopeID)
		if err != nil {
			return err
		}
		if (Protected(sc.kind, sc.policy, old.Kind) || Protected(sc.kind, sc.policy, nw.Kind)) && !ss.CanApprove(old.ScopeID) {
			return fmt.Errorf("%w: replacing a protected memory needs approve rights", ErrForbidden)
		}
		scope := []string{old.ScopeID}
		sj := ids(scope)
		n, err := w.execN(qMemSupersede, old.ID, nw.ID, reason, now(), old.LineageID, old.ScopeID, sj)
		if err != nil {
			return mapTriggerErr(err)
		}
		if n != 1 {
			return fmt.Errorf("%w: memory %s is no longer active", ErrConflict, old.ID)
		}
		if n, err = w.execN(qMemAdopt, nw.ID, old.LineageID, old.ID, reason, now(), sj); err != nil {
			return mapTriggerErr(err)
		}
		if n != 1 {
			return fmt.Errorf("%w: replacement changed concurrently", ErrConflict)
		}
		a := actorOf(ss, "")
		if err := w.addVersion(old.ID, scope, a, "superseded", reason); err != nil {
			return err
		}
		if err := w.addVersion(nw.ID, scope, a, "adopted_lineage", "supersedes "+old.ID+": "+reason); err != nil {
			return err
		}
		if nm, err = w.getMemory(nw.ID, scope); err != nil {
			return err
		}
		om, err = w.getMemory(old.ID, scope)
		return err
	})
	if err != nil {
		return Memory{}, Memory{}, err
	}
	s.bumpScope(om.ScopeID)
	nm, err = s.decorate(ctx, ss, nm)
	return nm, om, err
}

// GetMemoriesBySeq loads memories by rowid (vector index hits), skipping unreadable ones.
func (s *Store) GetMemoriesBySeq(ctx context.Context, ss auth.ScopeSet, seqs []int64) ([]Memory, error) {
	if len(seqs) == 0 {
		return nil, nil
	}
	b, _ := json.Marshal(seqs)
	ms, err := collectRows(s.reader(ctx).q(qMemBySeq, string(b), readable(ss)), scanMemory)
	if err != nil {
		return nil, err
	}
	return ms, s.attachTags(ctx, ss, ms)
}

// Links ----------------------------------------------------------------------

func (s *Store) CreateLink(ctx context.Context, ss auth.ScopeSet, src, dst, label string) (Link, error) {
	label = strings.TrimSpace(label)
	if label == "" {
		label = "related"
	}
	if len(label) > 64 {
		return Link{}, fmt.Errorf("%w: label too long", ErrInvalid)
	}
	if src == dst {
		return Link{}, fmt.Errorf("%w: a memory cannot link to itself", ErrInvalid)
	}
	l := Link{SourceID: src, TargetID: dst, Label: label, CreatedBy: ss.Principal().ID, CreatedAt: now()}
	err := s.write(ctx, func(w wx) error {
		n, err := w.execN(qLinkInsert, src, dst, label, l.CreatedBy, l.CreatedAt, writable(ss), readable(ss))
		if err != nil {
			if isUniqueViolation(err) {
				return fmt.Errorf("%w: link already exists", ErrConflict)
			}
			return mapTriggerErr(err)
		}
		if n != 1 {
			return ErrNotFound
		}
		return nil
	})
	return l, err
}

func (s *Store) DeleteLink(ctx context.Context, ss auth.ScopeSet, src, dst string) error {
	var n int64
	err := s.write(ctx, func(w wx) (err error) {
		n, err = w.execN(qLinkDelete, src, dst, writable(ss))
		return err
	})
	if err == nil && n == 0 {
		return ErrNotFound
	}
	return err
}

// DeleteLinksByLabelPrefix removes auto-generated noisy links (e.g. topic:) in a scope.
func (s *Store) DeleteLinksByLabelPrefix(ctx context.Context, ss auth.ScopeSet, scopeID, prefix string) (int, error) {
	if prefix == "" || !ss.CanWrite(scopeID) {
		return 0, ErrForbidden
	}
	var n int64
	err := s.write(ctx, func(w wx) (err error) {
		n, err = w.execN(qLinkDeletePrefix, prefix, scopeID, writable(ss))
		return err
	})
	return int(n), err
}

// LinksFor returns links touching the given memories where both ends are readable.
func (s *Store) LinksFor(ctx context.Context, ss auth.ScopeSet, memoryIDs []string) ([]Link, error) {
	ls, err := collectRows(s.reader(ctx).q(qLinksFor, ids(memoryIDs), readable(ss), readable(ss)), func(r rowScanner) (Link, error) {
		var l Link
		return l, r.Scan(&l.SourceID, &l.TargetID, &l.Label, &l.CreatedBy, &l.CreatedAt)
	})
	if ls == nil {
		ls = []Link{}
	}
	return ls, err
}

// scope info used for policy decisions inside transactions
type scopeMeta struct {
	kind   string
	policy string
	slug   string
}

var qScopeMeta = system("scope.meta", "auth", `SELECT kind, review_policy, slug FROM scopes WHERE id = ? AND deleted_at IS NULL`)

func (w wx) scopeInfo(id string) (scopeMeta, error) {
	var m scopeMeta
	err := w.row(qScopeMeta, id).Scan(&m.kind, &m.policy, &m.slug)
	return m, notFound(err)
}

// ScopePolicy returns kind and review policy of a scope the caller holds a grant on.
func (s *Store) ScopePolicy(ctx context.Context, ss auth.ScopeSet, id string) (kind, policy string, err error) {
	if !ss.CanRead(id) && !ss.CanPropose(id) {
		return "", "", ErrNotFound
	}
	err = s.reader(ctx).row(qScopeMeta, id).Scan(&kind, &policy, new(string))
	return kind, policy, notFound(err)
}
