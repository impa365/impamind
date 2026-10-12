package protocol

import (
	"time"

	"impamind/internal/auth"
	"impamind/internal/store"
)

// Profile names which surface the call came through.
const (
	ProfileAgent = "agent" // /mcp
	ProfileAdmin = "admin" // /mcp/admin
	ProfileREST  = "rest"
	ProfileUI    = "ui"
)

// Call carries the authorization and client identity of one operation.
type Call struct {
	SS      auth.ScopeSet
	Profile string
	Client  string
	Agent   string
	Model   string
	// Rescope re-resolves the ScopeSet for a project slug discovered from an
	// alias. It goes through auth.Resolve, so it cannot widen the grant.
	Rescope func(slug string) (auth.ScopeSet, error)
}

func (c Call) human() bool { return c.SS.Principal().Class.IsHuman() }

// fromUI is true only for content typed in the web UI by the admin session.
func (c Call) fromUI() bool { return c.Profile == ProfileUI && c.SS.IsAdmin() }

// Item is the compact projection used in recall and search results.
type Item struct {
	ID          string       `json:"id"`
	Scope       string       `json:"scope"`
	Kind        string       `json:"kind"`
	Title       string       `json:"title"`
	Content     string       `json:"content,omitempty"`
	Tier        int          `json:"tier"`
	Status      string       `json:"status"`
	Confidence  float64      `json:"confidence"`
	Source      string       `json:"source"`
	Agent       string       `json:"created_by_agent,omitempty"`
	ChangedAt   string       `json:"changed_at"`
	Stale       bool         `json:"stale,omitempty"`
	Expired     bool         `json:"expired,omitempty"`
	Pinned      bool         `json:"pinned,omitempty"`
	Tags        []string     `json:"tags,omitempty"`
	Supersedes  *store.Brief `json:"supersedes,omitempty"`
	Truncated   bool         `json:"truncated,omitempty"`
	Score       float64      `json:"score,omitempty"`
	ReviewState string       `json:"review_state,omitempty"`
}

func clipText(s string, n int) (string, bool) {
	if n <= 0 || len(s) <= n {
		return s, false
	}
	for n > 0 && (s[n]&0xC0) == 0x80 {
		n--
	}
	return s[:n] + "…", true
}

func isStale(m store.Memory, now time.Time) bool {
	t, ok := store.ParseTime(m.ReviewAfter)
	return ok && m.Status == "active" && !t.After(now)
}

func isExpired(m store.Memory, now time.Time) bool {
	if m.Status == "expired" {
		return true
	}
	t, ok := store.ParseTime(m.ValidUntil)
	return ok && !t.After(now)
}

// slugs maps scope ids to display names for items.
type slugs map[string]string

func (s slugs) of(id string) string {
	if v, ok := s[id]; ok {
		return v
	}
	return id
}

func project(m store.Memory, sl slugs, maxContent int, now time.Time) Item {
	c, cut := clipText(m.Content, maxContent)
	if maxContent < 0 {
		c, cut = "", false
	}
	return Item{
		ID: m.ID, Scope: sl.of(m.ScopeID), Kind: m.Kind, Title: m.Title, Content: c, Tier: m.Tier, Status: m.Status,
		Confidence: m.Confidence, Source: m.SourceType, Agent: m.CreatedByAgent, ChangedAt: m.UpdatedAt,
		Stale: isStale(m, now), Expired: isExpired(m, now), Pinned: m.Pinned, Tags: m.Tags, Truncated: cut,
		ReviewState: m.ReviewState,
	}
}

// ProjectRef identifies the project of a call.
type ProjectRef struct {
	ID   string `json:"id"`
	Slug string `json:"slug"`
	Name string `json:"name,omitempty"`
}

// Inputs and outputs ------------------------------------------------------------

type RecallIn struct {
	Project      string   `json:"project,omitempty" jsonschema:"project slug; optional when the connection is bound to a project"`
	ProjectHint  string   `json:"project_hint,omitempty" jsonschema:"git remote URL, repository path or folder name used to find the project"`
	Task         string   `json:"task" jsonschema:"what you are about to do, in one or two sentences"`
	Files        []string `json:"files,omitempty" jsonschema:"files you expect to touch"`
	Query        string   `json:"query,omitempty" jsonschema:"extra search terms"`
	BudgetTokens int      `json:"budget_tokens,omitempty" jsonschema:"approximate size limit of the pack in tokens (default 1500)"`
	SessionID    string   `json:"session_id,omitempty" jsonschema:"resume this work session instead of starting a new one"`
	Agent        string   `json:"agent,omitempty" jsonschema:"agent name, e.g. cursor-agent"`
	Model        string   `json:"model,omitempty" jsonschema:"model name"`
}

type Unresolved struct {
	Hint       string       `json:"hint,omitempty"`
	Candidates []ProjectRef `json:"candidates"`
	Message    string       `json:"message"`
}

type CheckpointView struct {
	ID        string   `json:"id"`
	SessionID string   `json:"session_id,omitempty"`
	Task      string   `json:"task"`
	Status    string   `json:"status,omitempty"`
	Files     []string `json:"files,omitempty"`
	Errors    []string `json:"errors,omitempty"`
	Tests     []string `json:"tests,omitempty"`
	NextSteps []string `json:"next_steps,omitempty"`
	Notes     string   `json:"notes,omitempty"`
	Agent     string   `json:"created_by_agent,omitempty"`
	ChangedAt string   `json:"changed_at"`
	Version   int      `json:"version"`
}

type Alerts struct {
	ProposedPending int `json:"proposed_pending"`
	Stale           int `json:"stale"`
	Conflicts       int `json:"conflicts"`
}

type BudgetInfo struct {
	Tokens    int  `json:"tokens"`
	Used      int  `json:"used_estimate"`
	Truncated bool `json:"truncated"`
}

type RecallOut struct {
	SessionID         string          `json:"session_id,omitempty"`
	Project           *ProjectRef     `json:"project,omitempty"`
	ProjectUnresolved *Unresolved     `json:"project_unresolved,omitempty"`
	Global            []Item          `json:"global"`
	Decisions         []Item          `json:"decisions"`
	Todos             []Item          `json:"todos"`
	Checkpoint        *CheckpointView `json:"checkpoint,omitempty"`
	Relevant          []Item          `json:"relevant"`
	Pending           []Item          `json:"pending_proposals"`
	Alerts            Alerts          `json:"alerts"`
	Budget            BudgetInfo      `json:"budget"`
	Warnings          []string        `json:"warnings,omitempty"`
	Protocol          string          `json:"protocol"`
}

type ObserveIn struct {
	Kind          string             `json:"kind" jsonschema:"preference, pattern, knowledge, observation, note, todo, requirement, business_rule or architecture"`
	Title         string             `json:"title" jsonschema:"short, specific title"`
	Content       string             `json:"content" jsonschema:"the learning itself, self-contained"`
	Evidence      []store.EvidenceIn `json:"evidence,omitempty" jsonschema:"where this came from: type file, commit, url, test, log, issue or conversation, with ref"`
	Confidence    float64            `json:"confidence,omitempty" jsonschema:"0..1, your confidence"`
	Tags          []string           `json:"tags,omitempty"`
	Scope         string             `json:"scope,omitempty" jsonschema:"project (default) or global; global writes are always proposals"`
	Project       string             `json:"project,omitempty"`
	SessionID     string             `json:"session_id,omitempty" jsonschema:"session_id returned by memory_recall"`
	DistinctFrom  []string           `json:"distinct_from,omitempty" jsonschema:"ids of similar memories you confirm are different"`
	UserConfirmed bool               `json:"user_confirmed,omitempty" jsonschema:"the user said this in the chat; stored as an annotation, it does not approve anything"`
	ValidUntil    string             `json:"valid_until,omitempty" jsonschema:"RFC 3339 date after which this stops being true"`
}

type DecideIn struct {
	Kind          string             `json:"kind,omitempty" jsonschema:"decision (default), architecture, business_rule or requirement"`
	Title         string             `json:"title"`
	Decision      string             `json:"decision" jsonschema:"what was decided"`
	Rationale     string             `json:"rationale" jsonschema:"why; required"`
	Alternatives  []string           `json:"alternatives,omitempty"`
	Consequences  []string           `json:"consequences,omitempty"`
	Evidence      []store.EvidenceIn `json:"evidence,omitempty"`
	Supersedes    string             `json:"supersedes,omitempty" jsonschema:"id of the active decision this one replaces"`
	Reason        string             `json:"reason,omitempty" jsonschema:"why the old decision is replaced (with supersedes)"`
	Coexists      []string           `json:"coexists,omitempty" jsonschema:"ids of similar decisions you confirm remain valid alongside this one"`
	Tier          int                `json:"tier,omitempty"`
	Tags          []string           `json:"tags,omitempty"`
	Confidence    float64            `json:"confidence,omitempty"`
	Project       string             `json:"project,omitempty"`
	SessionID     string             `json:"session_id,omitempty"`
	UserConfirmed bool               `json:"user_confirmed,omitempty" jsonschema:"annotation only; does not approve"`
}

type WriteOut struct {
	Result              string   `json:"result" jsonschema:"created, proposed, duplicate, duplicate_candidate, needs_resolution, superseded or updated"`
	Memory              *Item    `json:"memory,omitempty"`
	Replaced            *Item    `json:"replaced,omitempty"`
	DuplicateOf         string   `json:"duplicate_of,omitempty"`
	DuplicateCandidates []Item   `json:"duplicate_candidates,omitempty"`
	PossibleConflicts   []Item   `json:"possible_conflicts,omitempty"`
	ReviewItemID        string   `json:"review_item_id,omitempty"`
	SessionID           string   `json:"session_id,omitempty"`
	Warnings            []string `json:"warnings,omitempty"`
	Message             string   `json:"message"`
}

type NewContent struct {
	Kind      string             `json:"kind,omitempty"`
	Title     string             `json:"title"`
	Content   string             `json:"content"`
	Rationale string             `json:"rationale,omitempty"`
	Tags      []string           `json:"tags,omitempty"`
	Evidence  []store.EvidenceIn `json:"evidence,omitempty"`
	Tier      int                `json:"tier,omitempty"`
}

type SupersedeIn struct {
	OldID     string      `json:"old_id" jsonschema:"id of the active memory being replaced"`
	New       *NewContent `json:"new,omitempty" jsonschema:"content of the replacement"`
	NewID     string      `json:"new_id,omitempty" jsonschema:"or: id of an existing active memory that replaces it"`
	Reason    string      `json:"reason" jsonschema:"why it is replaced"`
	Project   string      `json:"project,omitempty"`
	SessionID string      `json:"session_id,omitempty"`
}

type CheckpointIn struct {
	SessionID string   `json:"session_id,omitempty" jsonschema:"session_id from memory_recall"`
	Task      string   `json:"task" jsonschema:"current task"`
	Files     []string `json:"files,omitempty"`
	Errors    []string `json:"errors,omitempty"`
	Tests     []string `json:"tests,omitempty"`
	NextSteps []string `json:"next_steps,omitempty"`
	Notes     string   `json:"notes,omitempty"`
	Status    string   `json:"status,omitempty" jsonschema:"in_progress (default), paused or done; done closes the session"`
	Project   string   `json:"project,omitempty"`
}

type CheckpointOut struct {
	SessionID    string   `json:"session_id"`
	CheckpointID string   `json:"checkpoint_id"`
	Version      int      `json:"version"`
	Closed       bool     `json:"closed"`
	Warnings     []string `json:"warnings,omitempty"`
	Message      string   `json:"message"`
}

type HandoffIn struct {
	SessionID           string   `json:"session_id,omitempty"`
	Summary             string   `json:"summary" jsonschema:"what was done, what is left, what the next agent must know"`
	NextSteps           []string `json:"next_steps,omitempty"`
	LearningsCandidates []string `json:"learnings_candidates,omitempty" jsonschema:"learnings worth keeping; confirm each with memory_observe or memory_decide"`
	Project             string   `json:"project,omitempty"`
}

type HandoffOut struct {
	SessionID  string   `json:"session_id"`
	SummaryID  string   `json:"summary_id"`
	Candidates []string `json:"learnings_candidates"`
	Message    string   `json:"message"`
	Warnings   []string `json:"warnings,omitempty"`
}

type ReviewIn struct {
	Mode    string `json:"mode,omitempty" jsonschema:"proposed, conflict, duplicate, stale, expired or tier_suggestion; empty lists all"`
	Scope   string `json:"scope,omitempty" jsonschema:"project, global or both (default)"`
	Limit   int    `json:"limit,omitempty"`
	Project string `json:"project,omitempty"`
}

type ReviewOut struct {
	Items []ReviewView `json:"items"`
}

type ReviewView struct {
	ID        string `json:"id"`
	Type      string `json:"type"`
	Scope     string `json:"scope"`
	Memories  []Item `json:"memories"`
	Payload   any    `json:"payload,omitempty"`
	CreatedAt string `json:"created_at"`
	Protected bool   `json:"protected"`
}

type ResolveIn struct {
	ItemID   string `json:"item_id"`
	Action   string `json:"action" jsonschema:"approve, reject, merge, archive, supersede, reconfirm or dismiss"`
	Note     string `json:"note,omitempty"`
	TargetID string `json:"target_id,omitempty" jsonschema:"memory to keep (merge) or the replacement (supersede)"`
	Project  string `json:"project,omitempty"`
}

type ResolveOut struct {
	ItemID  string `json:"item_id"`
	Action  string `json:"action"`
	Result  string `json:"result"`
	Message string `json:"message"`
}

type ReadIn struct {
	ID             string `json:"id"`
	IncludeHistory bool   `json:"include_history,omitempty" jsonschema:"include versions, lineage and approvals"`
	Project        string `json:"project,omitempty"`
}

type ReadOut struct {
	Memory  store.Memory   `json:"memory"`
	Scope   string         `json:"scope"`
	Stale   bool           `json:"stale"`
	Expired bool           `json:"expired"`
	Links   []store.Link   `json:"links"`
	History *store.History `json:"history,omitempty"`
}

type SearchIn struct {
	Query             string `json:"query"`
	Kind              string `json:"kind,omitempty"`
	Scope             string `json:"scope,omitempty" jsonschema:"project, global or both (default)"`
	IncludeSuperseded bool   `json:"include_superseded,omitempty"`
	IncludeProposed   bool   `json:"include_proposed,omitempty"`
	Limit             int    `json:"limit,omitempty"`
	Project           string `json:"project,omitempty"`
	AllProjects       bool   `json:"-"`
}

type SearchOut struct {
	Items []Item `json:"items"`
	Mode  string `json:"mode" jsonschema:"fts or hybrid"`
}
