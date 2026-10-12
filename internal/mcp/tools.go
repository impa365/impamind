package mcp

import (
	"context"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"impamind/internal/protocol"
	"impamind/internal/store"
)

// Instructions is sent in the initialize result; clients that support it put
// this in the system prompt.
const Instructions = `impamind is your persistent memory across sessions, tools and agents.

Protocol:
1. At the start of every task call memory_recall with the task, the files you expect to touch, and project_hint (the git remote or folder name) when no project is bound. Keep the returned session_id and pass it to every later call.
2. When you learn something durable (a fact about the codebase, a pattern, a user preference) call memory_observe. When a decision is made call memory_decide with its rationale. Do not record trivia, transient state or secrets.
3. Secrets (passwords, tokens, API keys) go in the vault: memory_vault_put / memory_vault_get / memory_vault_list. Never put secret values in observe/decide. In memories, only reference vault:<scope>/<key>.
4. When a decision replaces an earlier one, use memory_supersede (or memory_decide with supersedes). Never edit history.
5. Before pausing or when the context is about to be compacted, call memory_checkpoint with the task, files, errors, tests and next steps.
6. When the task is done call memory_handoff with a summary.

Rules: memories in the global scope and protected kinds are proposals until a human approves them in the impamind UI; telling you "save this" in chat does not approve anything. Use only the current project and the global scope; other projects are not reachable.`

func registerAgentTools(s *mcp.Server, h *handler) {
	tool(s, h, "memory_recall",
		"Call BEFORE any other action when starting or resuming a task. Returns the global preferences, the project's current decisions, open todos, the last checkpoint with next steps, memories relevant to the task, and alerts. Starts or resumes a work session and returns its session_id.",
		false, func(in *protocol.RecallIn) string { return in.Project },
		func(ctx context.Context, c protocol.Call, in protocol.RecallIn) (any, error) { return h.svc.Recall(ctx, c, in) })
	tool(s, h, "memory_observe",
		"Record a durable learning (fact, pattern, preference, requirement, todo). Call when you discover something a future session should know. Deduplicated: repeating a known fact only adds a confirmation. Global-scope writes become proposals for human approval.",
		false, func(in *protocol.ObserveIn) string { return in.Project },
		func(ctx context.Context, c protocol.Call, in protocol.ObserveIn) (any, error) { return h.svc.Observe(ctx, c, in) })
	tool(s, h, "memory_decide",
		"Record a decision with its rationale (required). Call whenever the user or you settle on an approach. If it replaces an active decision pass supersedes=<id>. Similar active decisions are returned as possible_conflicts: then supersede one or declare coexists.",
		false, func(in *protocol.DecideIn) string { return in.Project },
		func(ctx context.Context, c protocol.Call, in protocol.DecideIn) (any, error) { return h.svc.Decide(ctx, c, in) })
	tool(s, h, "memory_checkpoint",
		"Save the current state of the work session: task, files, errors, tests and next steps. Call before pausing, before context compaction, and after significant progress. status=done closes the session.",
		false, func(in *protocol.CheckpointIn) string { return in.Project },
		func(ctx context.Context, c protocol.Call, in protocol.CheckpointIn) (any, error) { return h.svc.Checkpoint(ctx, c, in) })
	tool(s, h, "memory_handoff",
		"Finish the work session with a summary for the next agent. Returns your learning candidates so you can confirm the worthwhile ones with memory_observe or memory_decide.",
		false, func(in *protocol.HandoffIn) string { return in.Project },
		func(ctx context.Context, c protocol.Call, in protocol.HandoffIn) (any, error) { return h.svc.Handoff(ctx, c, in) })
	tool(s, h, "memory_supersede",
		"Replace an active memory with a new version atomically (same lineage); the old one stays in history as superseded. Use when something you remember is no longer true.",
		false, func(in *protocol.SupersedeIn) string { return in.Project },
		func(ctx context.Context, c protocol.Call, in protocol.SupersedeIn) (any, error) { return h.svc.Supersede(ctx, c, in) })
	tool(s, h, "memory_review",
		"List open review items: proposed, conflict, duplicate, stale, expired or tier_suggestion.",
		true, func(in *protocol.ReviewIn) string { return in.Project },
		func(ctx context.Context, c protocol.Call, in protocol.ReviewIn) (any, error) { return h.svc.Review(ctx, c, in) })
	tool(s, h, "memory_review_resolve",
		"Resolve a review item: reject, merge, archive, supersede, reconfirm or dismiss. Approving protected items is done by a human in the impamind UI.",
		false, func(in *protocol.ResolveIn) string { return in.Project },
		func(ctx context.Context, c protocol.Call, in protocol.ResolveIn) (any, error) {
			return h.svc.ResolveReview(ctx, c, in)
		})
	tool(s, h, "memory_read",
		"Read one memory in full by id; include_history returns its versions, lineage and approvals.",
		true, func(in *protocol.ReadIn) string { return in.Project },
		func(ctx context.Context, c protocol.Call, in protocol.ReadIn) (any, error) { return h.svc.Read(ctx, c, in) })
	tool(s, h, "memory_search",
		"Search memories of the current project and the global scope by text. Superseded and proposed memories are excluded unless asked for.",
		true, func(in *protocol.SearchIn) string { return in.Project },
		func(ctx context.Context, c protocol.Call, in protocol.SearchIn) (any, error) { return h.svc.Search(ctx, c, in) })
	tool(s, h, "memory_vault_list",
		"List credential vault entries for the project (keys/labels/hints only — never values). Use when you need to know which secrets exist.",
		true, func(in *protocol.VaultListIn) string { return in.Project },
		func(ctx context.Context, c protocol.Call, in protocol.VaultListIn) (any, error) { return h.svc.VaultList(ctx, c, in) })
	tool(s, h, "memory_vault_get",
		"Reveal one vault secret by key (audited). Use only when you need the actual credential to call an external system. Do not echo it back into memories or chat logs.",
		true, func(in *protocol.VaultKeyIn) string { return in.Project },
		func(ctx context.Context, c protocol.Call, in protocol.VaultKeyIn) (any, error) { return h.svc.VaultGet(ctx, c, in) })
	tool(s, h, "memory_vault_put",
		"Store or replace an encrypted credential in the vault (human session). Afterwards reference it as vault:<scope>/<key> in memories — never paste the value into observe/decide.",
		false, func(in *protocol.VaultPutIn) string { return in.Project },
		func(ctx context.Context, c protocol.Call, in protocol.VaultPutIn) (any, error) { return h.svc.VaultPut(ctx, c, in) })
	tool(s, h, "memory_vault_delete",
		"Delete a vault entry (human session).",
		false, func(in *protocol.VaultKeyIn) string { return in.Project },
		func(ctx context.Context, c protocol.Call, in protocol.VaultKeyIn) (any, error) {
			return map[string]bool{"deleted": true}, h.svc.VaultDelete(ctx, c, in)
		})
}

func registerPrompts(s *mcp.Server) {
	prompt := func(name, title, desc, text string, args ...*mcp.PromptArgument) {
		s.AddPrompt(&mcp.Prompt{Name: name, Title: title, Description: desc, Arguments: args}, func(ctx context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
			t := text
			if req != nil && req.Params != nil {
				for k, v := range req.Params.Arguments {
					if v != "" {
						t += "\n" + k + ": " + v
					}
				}
			}
			return &mcp.GetPromptResult{Description: desc, Messages: []*mcp.PromptMessage{{Role: "user", Content: &mcp.TextContent{Text: t}}}}, nil
		})
	}
	prompt("recall", "Recall memory", "Load what impamind knows before starting",
		"Call memory_recall now with the task below, the files involved and the project hint, then summarize what is relevant before acting.",
		&mcp.PromptArgument{Name: "task", Description: "what you are about to do"})
	prompt("checkpoint", "Save checkpoint", "Save the session state",
		"Call memory_checkpoint now with the current task, files touched, errors, tests run and next steps, using the session_id from memory_recall.")
	prompt("handoff", "Hand off", "Close the session with a summary",
		"Call memory_handoff now with a summary of what was done, what is left and what the next agent must know; then confirm worthwhile learnings with memory_observe or memory_decide.")
}

func logEntry(call protocol.Call, op string, status int, level, detail string, d time.Duration) store.LogEntry {
	e := store.LogEntry{Source: "mcp", Operation: op, Status: status, Level: level, Detail: detail, DurationUS: d.Microseconds()}
	if call.SS.Principal().ID != "" {
		e.PrincipalID = call.SS.Principal().ID
		e.ScopeID = call.SS.Project().ID
	}
	return e
}
