package store

import "encoding/json"

// Kinds -----------------------------------------------------------------------

var Kinds = []string{"preference", "pattern", "knowledge", "decision", "requirement", "business_rule", "architecture", "todo", "observation", "checkpoint", "session_summary", "note"}

func ValidKind(k string) bool {
	for _, v := range Kinds {
		if v == k {
			return true
		}
	}
	return false
}

var Statuses = []string{"proposed", "active", "rejected", "superseded", "deprecated", "expired", "archived"}

var EvidenceTypes = []string{"file", "commit", "url", "test", "conversation", "issue", "log", "manifest"}

func ValidEvidenceType(t string) bool {
	for _, v := range EvidenceTypes {
		if v == t {
			return true
		}
	}
	return false
}

// ObservedEvidence are the evidence types that justify source_type=tool_observed.
func ObservedEvidence(t string) bool {
	switch t {
	case "file", "commit", "url", "test", "log":
		return true
	}
	return false
}

// Protected mirrors the memories_protected_* triggers: memories that need an
// approval row before they become active.
func Protected(scopeKind, policy, kind string) bool {
	if scopeKind == "global" {
		return true
	}
	switch policy {
	case "balanced":
		return kind == "architecture" || kind == "business_rule"
	case "strict":
		switch kind {
		case "decision", "business_rule", "architecture", "requirement", "preference":
			return true
		}
	}
	return false
}

// Entities --------------------------------------------------------------------

type Scope struct {
	ID           string   `json:"id"`
	Kind         string   `json:"kind"`
	Slug         string   `json:"slug"`
	Name         string   `json:"name"`
	Description  string   `json:"description"`
	Hidden       bool     `json:"hidden"`
	ReviewPolicy string   `json:"review_policy"`
	Tags         []string `json:"tags"`
	CreatedAt    string   `json:"created_at"`
	UpdatedAt    string   `json:"updated_at"`
	DeletedAt    string   `json:"deleted_at,omitempty"`
	Counts       *Counts  `json:"counts,omitempty"`
}

type Counts struct {
	Active   int `json:"active"`
	Proposed int `json:"proposed"`
	Total    int `json:"total"`
}

type Alias struct {
	Alias     string `json:"alias"`
	ScopeID   string `json:"scope_id"`
	Kind      string `json:"kind"`
	CreatedAt string `json:"created_at"`
}

type Evidence struct {
	ID        string `json:"id"`
	Type      string `json:"type"`
	Ref       string `json:"ref"`
	Note      string `json:"note,omitempty"`
	AddedBy   string `json:"added_by"`
	CreatedAt string `json:"created_at"`
}

type EvidenceIn struct {
	Type string `json:"type"`
	Ref  string `json:"ref"`
	Note string `json:"note,omitempty"`
}

type Memory struct {
	Seq                  int64           `json:"-"`
	ID                   string          `json:"id"`
	ScopeID              string          `json:"scope_id"`
	SessionID            string          `json:"session_id,omitempty"`
	Kind                 string          `json:"kind"`
	Title                string          `json:"title"`
	Content              string          `json:"content"`
	Tier                 int             `json:"tier"`
	Pinned               bool            `json:"pinned"`
	Status               string          `json:"status"`
	ReviewState          string          `json:"review_state"`
	Confidence           float64         `json:"confidence"`
	SourceType           string          `json:"source_type"`
	CreatedByPrincipalID string          `json:"created_by_principal_id"`
	CreatedByClient      string          `json:"created_by_client,omitempty"`
	CreatedByAgent       string          `json:"created_by_agent,omitempty"`
	CreatedByModel       string          `json:"created_by_model,omitempty"`
	CreatedSessionID     string          `json:"created_session_id,omitempty"`
	UserConfirmedClaim   bool            `json:"user_confirmed_claim"`
	ApprovedBy           string          `json:"approved_by,omitempty"`
	ApprovedVia          string          `json:"approved_via,omitempty"`
	LineageID            string          `json:"lineage_id"`
	SupersedesID         string          `json:"supersedes_id,omitempty"`
	TargetLineageID      string          `json:"-"`
	SupersededByID       string          `json:"superseded_by_id,omitempty"`
	SupersedeReason      string          `json:"supersede_reason,omitempty"`
	DerivedFromID        string          `json:"derived_from_id,omitempty"`
	Rationale            string          `json:"rationale,omitempty"`
	Details              json.RawMessage `json:"details,omitempty"`
	ValidFrom            string          `json:"valid_from,omitempty"`
	ValidUntil           string          `json:"valid_until,omitempty"`
	ReviewAfter          string          `json:"review_after,omitempty"`
	ContentHash          string          `json:"content_hash"`
	Version              int             `json:"version"`
	Confirmations        int             `json:"confirmations"`
	LastConfirmedAt      string          `json:"last_confirmed_at,omitempty"`
	AccessCount          int             `json:"access_count"`
	LastAccessedAt       string          `json:"last_accessed_at,omitempty"`
	CreatedAt            string          `json:"created_at"`
	UpdatedAt            string          `json:"updated_at"`
	Tags                 []string        `json:"tags"`
	Evidence             []Evidence      `json:"evidence,omitempty"`
	ScopeSlug            string          `json:"scope,omitempty"`
}

type Version struct {
	MemoryID     string          `json:"memory_id"`
	Version      int             `json:"version"`
	Snapshot     json.RawMessage `json:"snapshot"`
	ChangedBy    string          `json:"changed_by"`
	ChangedAgent string          `json:"changed_by_agent,omitempty"`
	ChangeType   string          `json:"change_type"`
	ChangeReason string          `json:"change_reason,omitempty"`
	CreatedAt    string          `json:"created_at"`
}

type Approval struct {
	ID              string `json:"id"`
	MemoryID        string `json:"memory_id"`
	FromStatus      string `json:"from_status"`
	ToStatus        string `json:"to_status"`
	ApproverClass   string `json:"approver_class"`
	ApproverID      string `json:"approver_id"`
	MarksUserStated bool   `json:"marks_user_stated"`
	Note            string `json:"note,omitempty"`
	CreatedAt       string `json:"created_at"`
}

type Link struct {
	SourceID  string `json:"source_id"`
	TargetID  string `json:"target_id"`
	Label     string `json:"label"`
	CreatedBy string `json:"created_by"`
	CreatedAt string `json:"created_at"`
}

type WorkSession struct {
	ID                string   `json:"id"`
	ScopeID           string   `json:"scope_id"`
	CreatedBy         string   `json:"created_by_principal_id"`
	Client            string   `json:"client,omitempty"`
	Agent             string   `json:"agent,omitempty"`
	Model             string   `json:"model,omitempty"`
	Title             string   `json:"title,omitempty"`
	Task              string   `json:"task,omitempty"`
	Status            string   `json:"status"`
	Files             []string `json:"files"`
	Recalled          bool     `json:"recalled"`
	Observations      int      `json:"observations"`
	Writes            int      `json:"writes"`
	Checkpoints       int      `json:"checkpoints"`
	Violations        int      `json:"violations"`
	DuplicatesAvoided int      `json:"duplicates_avoided"`
	SecretsBlocked    int      `json:"secrets_blocked"`
	HandedOff         bool     `json:"handed_off"`
	StartedAt         string   `json:"started_at"`
	LastActivityAt    string   `json:"last_activity_at"`
	ClosedAt          string   `json:"closed_at,omitempty"`
}

type ReviewItem struct {
	ID         string          `json:"id"`
	ScopeID    string          `json:"scope_id"`
	Type       string          `json:"type"`
	DedupeKey  string          `json:"-"`
	MemoryIDs  []string        `json:"memory_ids"`
	Payload    json.RawMessage `json:"payload"`
	Status     string          `json:"status"`
	CreatedAt  string          `json:"created_at"`
	ResolvedBy string          `json:"resolved_by,omitempty"`
	Resolution string          `json:"resolution,omitempty"`
	ResolvedAt string          `json:"resolved_at,omitempty"`
	Memories   []Memory        `json:"memories,omitempty"`
}

type Token struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	Class       string  `json:"class"`
	ClientType  string  `json:"client_type"`
	PrincipalID string  `json:"principal_id"`
	CreatedAt   string  `json:"created_at"`
	LastUsedAt  string  `json:"last_used_at,omitempty"`
	RevokedAt   string  `json:"revoked_at,omitempty"`
	Grants      []Grant `json:"grants"`
}

// Grant is the stored form; ScopeSlug is filled for display.
type Grant struct {
	ScopeID      string `json:"scope_id"`
	ScopeSlug    string `json:"scope,omitempty"`
	CanRead      bool   `json:"can_read"`
	CanWrite     bool   `json:"can_write"`
	CanPropose   bool   `json:"can_propose"`
	CanApprove   bool   `json:"can_approve"`
	CrossProject bool   `json:"cross_project"`
}

type LogEntry struct {
	ID          int64  `json:"id"`
	TS          string `json:"ts"`
	Source      string `json:"source"`
	PrincipalID string `json:"principal_id,omitempty"`
	ScopeID     string `json:"scope_id,omitempty"`
	Operation   string `json:"operation"`
	Level       string `json:"level"`
	Status      int    `json:"status"`
	Detail      string `json:"detail,omitempty"`
	DurationUS  int64  `json:"duration_us"`
}
