package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"impamind/internal/auth"
)

const sessionCols = `id, scope_id, created_by_principal_id, client, agent, model, title, task, status, files, recalled, observations, writes,
	checkpoints, violations, duplicates_avoided, secrets_blocked, handed_off, started_at, last_activity_at, COALESCE(closed_at,'')`

var (
	qSessionInsert = scoped("session.insert", `INSERT INTO work_sessions(id, scope_id, created_by_principal_id, client, agent, model, title, task, status, files, recalled, started_at, last_activity_at)
		SELECT ?1, ?2, ?3, ?4, ?5, ?6, ?7, ?8, 'active', ?9, ?10, ?11, ?11 WHERE {{in:?2}}`)
	qSessionGet  = scoped("session.get", `SELECT `+sessionCols+` FROM work_sessions WHERE id = ? AND {{scope}}`)
	qSessionList = scoped("session.list", `SELECT `+sessionCols+` FROM work_sessions WHERE {{scope}} AND (? = '' OR status = ?) ORDER BY last_activity_at DESC LIMIT ?`)
	qSessionBump = scoped("session.bump", `UPDATE work_sessions SET observations = observations + ?2, writes = writes + ?3, checkpoints = checkpoints + ?4,
		violations = violations + ?5, duplicates_avoided = duplicates_avoided + ?6, secrets_blocked = secrets_blocked + ?7,
		recalled = max(recalled, ?8), last_activity_at = ?9 WHERE id = ?1 AND status <> 'closed' AND {{scope}}`)
	qSessionTask = scoped("session.task", `UPDATE work_sessions SET task = CASE WHEN ?2 = '' THEN task ELSE ?2 END, files = ?3, status = ?4, last_activity_at = ?5
		WHERE id = ?1 AND status <> 'closed' AND {{scope}}`)
	qSessionClose = scoped("session.close", `UPDATE work_sessions SET status = 'closed', closed_at = ?2, handed_off = ?3, last_activity_at = ?2
		WHERE id = ?1 AND status <> 'closed' AND {{scope}}`)
	qSessionHealth = scoped("session.health", `SELECT client, agent, count(*), sum(recalled), sum(CASE WHEN recalled = 1 THEN 0 ELSE 1 END), sum(writes), sum(observations),
		sum(checkpoints), sum(violations), sum(duplicates_avoided), sum(secrets_blocked),
		sum(CASE WHEN status = 'closed' AND handed_off = 0 THEN 1 ELSE 0 END), sum(handed_off)
		FROM work_sessions WHERE {{scope}} AND started_at >= ? GROUP BY client, agent ORDER BY count(*) DESC`)
	qSessionCheckpoint = scoped("session.checkpoint_memory", `SELECT `+memCols+` FROM memories m WHERE m.session_id = ? AND m.kind = 'checkpoint' AND m.status = 'active' AND {{scope:m}} LIMIT 1`)
)

func scanSession(r rowScanner) (WorkSession, error) {
	var w WorkSession
	var files string
	err := r.Scan(&w.ID, &w.ScopeID, &w.CreatedBy, &w.Client, &w.Agent, &w.Model, &w.Title, &w.Task, &w.Status, &files, &w.Recalled,
		&w.Observations, &w.Writes, &w.Checkpoints, &w.Violations, &w.DuplicatesAvoided, &w.SecretsBlocked, &w.HandedOff,
		&w.StartedAt, &w.LastActivityAt, &w.ClosedAt)
	_ = json.Unmarshal([]byte(files), &w.Files)
	if w.Files == nil {
		w.Files = []string{}
	}
	return w, err
}

type NewSession struct {
	Client   string
	Agent    string
	Model    string
	Title    string
	Task     string
	Files    []string
	Recalled bool
}

func filesJSON(files []string) string {
	clean := make([]string, 0, len(files))
	seen := map[string]bool{}
	for _, f := range files {
		f = strings.TrimSpace(f)
		if f == "" || len(f) > 500 || seen[f] {
			continue
		}
		seen[f] = true
		clean = append(clean, f)
		if len(clean) == 100 {
			break
		}
	}
	b, _ := json.Marshal(clean)
	return string(b)
}

func clip(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	for n > 0 && n < len(s) && (s[n]&0xC0) == 0x80 {
		n--
	}
	return s[:n]
}

// StartSession opens a work session in the caller's current project.
func (s *Store) StartSession(ctx context.Context, ss auth.ScopeSet, in NewSession) (WorkSession, error) {
	if !ss.HasProject() {
		return WorkSession{}, fmt.Errorf("%w: a work session needs a project", ErrInvalid)
	}
	scopeID := ss.Project().ID
	if !ss.CanPropose(scopeID) && !ss.CanRead(scopeID) {
		return WorkSession{}, ErrNotFound
	}
	id := newID()
	ts := now()
	err := s.write(ctx, func(w wx) error {
		n, err := w.execN(qSessionInsert, id, scopeID, ss.Principal().ID, clip(in.Client, 80), clip(in.Agent, 80), clip(in.Model, 120),
			clip(in.Title, 200), clip(in.Task, 2000), filesJSON(in.Files), b2i(in.Recalled), ts, readable(ss))
		if err == nil && n != 1 {
			err = ErrNotFound
		}
		return err
	})
	if err != nil {
		return WorkSession{}, err
	}
	return s.GetSession(ctx, ss, id)
}

// GetSession reads a session in a readable scope. Reading does not imply the
// right to write into it.
func (s *Store) GetSession(ctx context.Context, ss auth.ScopeSet, id string) (WorkSession, error) {
	w, err := scanSession(s.reader(ctx).row(qSessionGet, id, readable(ss)))
	return w, notFound(err)
}

// SessionForWrite checks, in order: the session exists, belongs to the current
// project, was created by this principal, and is not closed.
func (s *Store) SessionForWrite(ctx context.Context, ss auth.ScopeSet, id string) (WorkSession, error) {
	if !ss.HasProject() {
		return WorkSession{}, ErrNotFound
	}
	w, err := scanSession(s.reader(ctx).row(qSessionGet, id, ids([]string{ss.Project().ID})))
	if err != nil {
		return w, notFound(err)
	}
	if w.ScopeID != ss.Project().ID || w.CreatedBy != ss.Principal().ID {
		return WorkSession{}, ErrNotFound
	}
	if w.Status == "closed" {
		return w, ErrClosed
	}
	return w, nil
}

// SessionDelta increments session counters.
type SessionDelta struct {
	Observations, Writes, Checkpoints, Violations, Duplicates, Secrets int
	Recalled                                                           bool
}

func (s *Store) BumpSession(ctx context.Context, ss auth.ScopeSet, id string, d SessionDelta) error {
	w, err := s.SessionForWrite(ctx, ss, id)
	if err != nil {
		return err
	}
	return s.write(ctx, func(x wx) error {
		n, err := x.execN(qSessionBump, w.ID, d.Observations, d.Writes, d.Checkpoints, d.Violations, d.Duplicates, d.Secrets, b2i(d.Recalled), now(), ids([]string{w.ScopeID}))
		if err == nil && n != 1 {
			err = ErrClosed
		}
		return err
	})
}

// UpdateSessionTask stores the current task, files and status (active/paused).
func (s *Store) UpdateSessionTask(ctx context.Context, ss auth.ScopeSet, id, task string, files []string, status string) error {
	if status != "active" && status != "paused" {
		return fmt.Errorf("%w: status must be active or paused", ErrInvalid)
	}
	w, err := s.SessionForWrite(ctx, ss, id)
	if err != nil {
		return err
	}
	merged := append(append([]string{}, w.Files...), files...)
	return s.write(ctx, func(x wx) error {
		n, err := x.execN(qSessionTask, w.ID, clip(task, 2000), filesJSON(merged), status, now(), ids([]string{w.ScopeID}))
		if err == nil && n != 1 {
			err = ErrClosed
		}
		return err
	})
}

// CloseSession closes a session for good.
func (s *Store) CloseSession(ctx context.Context, ss auth.ScopeSet, id string, handedOff bool) error {
	w, err := s.SessionForWrite(ctx, ss, id)
	if err != nil {
		return err
	}
	return s.write(ctx, func(x wx) error {
		n, err := x.execN(qSessionClose, w.ID, now(), b2i(handedOff), ids([]string{w.ScopeID}))
		if err == nil && n != 1 {
			err = ErrClosed
		}
		return err
	})
}

func (s *Store) ListSessions(ctx context.Context, ss auth.ScopeSet, status string, limit int) ([]WorkSession, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	list, err := collectRows(s.reader(ctx).q(qSessionList, readable(ss), status, status, limit), scanSession)
	if list == nil {
		list = []WorkSession{}
	}
	return list, err
}

// SessionCheckpoint returns the active checkpoint memory of a session, if any.
func (s *Store) SessionCheckpoint(ctx context.Context, ss auth.ScopeSet, sessionID string) (Memory, bool, error) {
	m, err := scanMemory(s.reader(ctx).row(qSessionCheckpoint, sessionID, readable(ss)))
	if err != nil {
		if notFound(err) == ErrNotFound {
			return Memory{}, false, nil
		}
		return Memory{}, false, err
	}
	m, err = s.decorate(ctx, ss, m)
	return m, err == nil, err
}

// ProtocolHealth aggregates session metrics per client and agent.
type ProtocolHealth struct {
	Client            string  `json:"client"`
	Agent             string  `json:"agent"`
	Sessions          int     `json:"sessions"`
	Recalled          int     `json:"recalled"`
	RecallMissing     int     `json:"recall_missing"`
	Writes            int     `json:"writes"`
	Observations      int     `json:"observations"`
	Checkpoints       int     `json:"checkpoints"`
	Violations        int     `json:"violations"`
	DuplicatesAvoided int     `json:"duplicates_avoided"`
	SecretsBlocked    int     `json:"secrets_blocked"`
	ClosedNoHandoff   int     `json:"closed_without_handoff"`
	HandedOff         int     `json:"handed_off"`
	RecallRate        float64 `json:"recall_rate"`
}

func (s *Store) ProtocolHealth(ctx context.Context, ss auth.ScopeSet, since string) ([]ProtocolHealth, error) {
	list, err := collectRows(s.reader(ctx).q(qSessionHealth, readable(ss), since), func(r rowScanner) (ProtocolHealth, error) {
		var h ProtocolHealth
		err := r.Scan(&h.Client, &h.Agent, &h.Sessions, &h.Recalled, &h.RecallMissing, &h.Writes, &h.Observations, &h.Checkpoints,
			&h.Violations, &h.DuplicatesAvoided, &h.SecretsBlocked, &h.ClosedNoHandoff, &h.HandedOff)
		if h.Sessions > 0 {
			h.RecallRate = float64(h.Recalled) / float64(h.Sessions)
		}
		return h, err
	})
	if list == nil {
		list = []ProtocolHealth{}
	}
	return list, err
}
