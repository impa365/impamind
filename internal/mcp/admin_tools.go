package mcp

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"impamind/internal/protocol"
	"impamind/internal/store"
)

type projectArg struct {
	Project string `json:"project,omitempty"`
}

type projectCreateIn struct {
	store.ProjectInput
}

type projectUpdateIn struct {
	Slug         string   `json:"slug"`
	Name         string   `json:"name,omitempty"`
	Description  string   `json:"description,omitempty"`
	Hidden       bool     `json:"hidden,omitempty"`
	ReviewPolicy string   `json:"review_policy,omitempty" jsonschema:"strict, balanced or open"`
	Tags         []string `json:"tags,omitempty"`
}

type listIn struct {
	Project string `json:"project,omitempty"`
	Scope   string `json:"scope,omitempty" jsonschema:"project, global or both"`
	Status  string `json:"status,omitempty"`
	Kind    string `json:"kind,omitempty"`
	Tier    int    `json:"tier,omitempty"`
	Tag     string `json:"tag,omitempty"`
	Limit   int    `json:"limit,omitempty"`
	Offset  int    `json:"offset,omitempty"`
}

type updateIn struct {
	ID          string    `json:"id"`
	Project     string    `json:"project,omitempty"`
	Title       *string   `json:"title,omitempty"`
	Content     *string   `json:"content,omitempty"`
	Tier        *int      `json:"tier,omitempty"`
	Pinned      *bool     `json:"pinned,omitempty"`
	Confidence  *float64  `json:"confidence,omitempty"`
	Rationale   *string   `json:"rationale,omitempty"`
	ValidUntil  *string   `json:"valid_until,omitempty"`
	ReviewAfter *string   `json:"review_after,omitempty"`
	Tags        *[]string `json:"tags,omitempty"`
	Reason      string    `json:"reason,omitempty"`
}

func (u updateIn) patch() store.MemoryPatch {
	return store.MemoryPatch{Title: u.Title, Content: u.Content, Tier: u.Tier, Pinned: u.Pinned, Confidence: u.Confidence,
		Rationale: u.Rationale, ValidUntil: u.ValidUntil, ReviewAfter: u.ReviewAfter, Tags: u.Tags, Reason: u.Reason}
}

type approveIn struct {
	ID              string `json:"id"`
	Project         string `json:"project,omitempty"`
	Note            string `json:"note,omitempty"`
	MarksUserStated bool   `json:"marks_user_stated,omitempty" jsonschema:"the text is the user's own words"`
}

type idNoteIn struct {
	ID      string `json:"id"`
	Project string `json:"project,omitempty"`
	Note    string `json:"note,omitempty"`
}

type transitionIn struct {
	ID      string `json:"id"`
	Project string `json:"project,omitempty"`
	To      string `json:"to" jsonschema:"deprecated or archived"`
	Reason  string `json:"reason,omitempty"`
}

type linkIn struct {
	SourceID string `json:"source_id"`
	TargetID string `json:"target_id"`
	Label    string `json:"label,omitempty"`
	Project  string `json:"project,omitempty"`
}

type sessionListIn struct {
	Project string `json:"project,omitempty"`
	Status  string `json:"status,omitempty"`
	Limit   int    `json:"limit,omitempty"`
}

type promoteIn struct {
	ID      string `json:"id"`
	Project string `json:"project,omitempty"`
	Approve bool   `json:"approve,omitempty"`
}

func registerAdminTools(s *mcp.Server, h *handler) {
	tool(s, h, "impamind_status", "Server status, SQLite build facts and the caller's identity.", true,
		func(in *projectArg) string { return in.Project },
		func(ctx context.Context, c protocol.Call, _ projectArg) (any, error) { return h.svc.Status(ctx, c.SS) })
	tool(s, h, "project_list", "List the global scope and the projects this token can see.", true,
		func(in *projectArg) string { return "" },
		func(ctx context.Context, c protocol.Call, _ projectArg) (any, error) { return h.svc.ListScopes(ctx, c) })
	tool(s, h, "project_create", "Create a project scope.", false,
		func(in *projectCreateIn) string { return "" },
		func(ctx context.Context, c protocol.Call, in projectCreateIn) (any, error) {
			return h.svc.CreateProject(ctx, c, in.ProjectInput)
		})
	tool(s, h, "project_update", "Update a project's name, description, tags, visibility or review policy.", false,
		func(in *projectUpdateIn) string { return in.Slug },
		func(ctx context.Context, c protocol.Call, in projectUpdateIn) (any, error) {
			sc, err := h.svc.ScopeBySlug(ctx, c, in.Slug)
			if err != nil {
				return nil, err
			}
			return h.svc.UpdateProject(ctx, c, sc.ID, store.ProjectInput{Name: in.Name, Description: in.Description, Hidden: in.Hidden, ReviewPolicy: in.ReviewPolicy, Tags: in.Tags})
		})
	tool(s, h, "memory_list", "List memories with filters.", true,
		func(in *listIn) string { return in.Project },
		func(ctx context.Context, c protocol.Call, in listIn) (any, error) {
			return h.svc.List(ctx, c, protocol.ListIn{Scope: in.Scope, Status: in.Status, Kind: in.Kind, Tier: in.Tier, Tag: in.Tag, Limit: in.Limit, Offset: in.Offset})
		})
	tool(s, h, "memory_add", "Create a memory directly. approve=true approves it immediately when the scope requires approval.", false,
		func(in *protocol.AddIn) string { return in.Project },
		func(ctx context.Context, c protocol.Call, in protocol.AddIn) (any, error) { return h.svc.AddMemory(ctx, c, in) })
	tool(s, h, "memory_update", "Edit a memory in place (creates a version). Tier and pin are human-only.", false,
		func(in *updateIn) string { return in.Project },
		func(ctx context.Context, c protocol.Call, in updateIn) (any, error) { return h.svc.UpdateMemory(ctx, c, in.ID, in.patch()) })
	tool(s, h, "memory_approve", "Approve a proposed memory (human token with approve grant).", false,
		func(in *approveIn) string { return in.Project },
		func(ctx context.Context, c protocol.Call, in approveIn) (any, error) {
			return h.svc.Approve(ctx, c, in.ID, store.ApproveInput{Note: in.Note, MarksUserStated: in.MarksUserStated})
		})
	tool(s, h, "memory_reject", "Reject a proposed memory.", false,
		func(in *idNoteIn) string { return in.Project },
		func(ctx context.Context, c protocol.Call, in idNoteIn) (any, error) { return h.svc.Reject(ctx, c, in.ID, in.Note) })
	tool(s, h, "memory_transition", "Move an active memory to deprecated or archived.", false,
		func(in *transitionIn) string { return in.Project },
		func(ctx context.Context, c protocol.Call, in transitionIn) (any, error) {
			return h.svc.Transition(ctx, c, in.ID, in.To, in.Reason)
		})
	tool(s, h, "memory_delete", "Permanently delete a memory, leaving a tombstone (needs approve grant).", false,
		func(in *idNoteIn) string { return in.Project },
		func(ctx context.Context, c protocol.Call, in idNoteIn) (any, error) {
			return map[string]string{"deleted": in.ID}, h.svc.DeleteMemory(ctx, c, in.ID, in.Note)
		})
	tool(s, h, "memory_promote", "Copy an active project memory to the global scope as a new memory (derived_from_id).", false,
		func(in *promoteIn) string { return in.Project },
		func(ctx context.Context, c protocol.Call, in promoteIn) (any, error) { return h.svc.Promote(ctx, c, in.ID, in.Approve) })
	tool(s, h, "link_create", "Link two memories (same project, or project to global).", false,
		func(in *linkIn) string { return in.Project },
		func(ctx context.Context, c protocol.Call, in linkIn) (any, error) {
			return h.svc.St.CreateLink(ctx, c.SS, in.SourceID, in.TargetID, in.Label)
		})
	tool(s, h, "link_delete", "Remove a link.", false,
		func(in *linkIn) string { return in.Project },
		func(ctx context.Context, c protocol.Call, in linkIn) (any, error) {
			return map[string]bool{"deleted": true}, h.svc.St.DeleteLink(ctx, c.SS, in.SourceID, in.TargetID)
		})
	tool(s, h, "session_list", "List work sessions of the project.", true,
		func(in *sessionListIn) string { return in.Project },
		func(ctx context.Context, c protocol.Call, in sessionListIn) (any, error) {
			return h.svc.St.ListSessions(ctx, c.SS, in.Status, in.Limit)
		})
	tool(s, h, "checkpoint_save", "Alias of memory_checkpoint.", false,
		func(in *protocol.CheckpointIn) string { return in.Project },
		func(ctx context.Context, c protocol.Call, in protocol.CheckpointIn) (any, error) { return h.svc.Checkpoint(ctx, c, in) })
	tool(s, h, "checkpoint_done", "Alias of memory_checkpoint with status=done.", false,
		func(in *protocol.CheckpointIn) string { return in.Project },
		func(ctx context.Context, c protocol.Call, in protocol.CheckpointIn) (any, error) {
			in.Status = "done"
			return h.svc.Checkpoint(ctx, c, in)
		})
	tool(s, h, "checkpoint_load", "Load the latest checkpoint of the project (via memory_recall).", true,
		func(in *protocol.RecallIn) string { return in.Project },
		func(ctx context.Context, c protocol.Call, in protocol.RecallIn) (any, error) {
			out, err := h.svc.Recall(ctx, c, in)
			if err != nil {
				return nil, err
			}
			return map[string]any{"session_id": out.SessionID, "checkpoint": out.Checkpoint}, nil
		})
	tool(s, h, "checkpoint_query", "List checkpoints of the project.", true,
		func(in *listIn) string { return in.Project },
		func(ctx context.Context, c protocol.Call, in listIn) (any, error) {
			return h.svc.List(ctx, c, protocol.ListIn{Scope: "project", Kind: "checkpoint", Status: in.Status, Limit: in.Limit, Offset: in.Offset})
		})
}
