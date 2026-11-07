package store

import (
	"context"
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"impamind/internal/auth"
)

var (
	qSearchFTS = scoped("search.fts", `SELECT `+memCols+`, bm25(memories_fts, 4.0, 1.0) FROM memories_fts JOIN memories m ON m.seq = memories_fts.rowid
		WHERE memories_fts MATCH ? AND {{scope:m}} AND m.status IN (SELECT value FROM json_each(?)) AND (? = '' OR m.kind = ?)
		ORDER BY bm25(memories_fts, 4.0, 1.0) LIMIT ?`)
	qMemList = scoped("memory.list", `SELECT `+memCols+` FROM memories m WHERE {{scope:m}}
		AND (? = '[]' OR m.status IN (SELECT value FROM json_each(?)))
		AND (? = '' OR m.kind = ?) AND (? = 0 OR m.tier = ?) AND (? = '' OR m.session_id = ?)
		AND (? = '' OR EXISTS (SELECT 1 FROM memory_tags t WHERE t.memory_id = m.id AND t.tag = ?))
		ORDER BY m.pinned DESC, m.tier, m.updated_at DESC LIMIT ? OFFSET ?`)
	qMemCount = scoped("memory.count", `SELECT count(*) FROM memories m WHERE {{scope:m}}
		AND (? = '[]' OR m.status IN (SELECT value FROM json_each(?)))
		AND (? = '' OR m.kind = ?) AND (? = 0 OR m.tier = ?) AND (? = '' OR m.session_id = ?)
		AND (? = '' OR EXISTS (SELECT 1 FROM memory_tags t WHERE t.memory_id = m.id AND t.tag = ?))`)

	qGraphNodes = scoped("graph.nodes", `SELECT m.id, m.scope_id, m.title, m.kind, m.tier, m.status, m.pinned, COALESCE(m.supersedes_id,''), m.access_count
		FROM memories m WHERE {{scope:m}} AND m.status IN (SELECT value FROM json_each(?)) AND m.kind NOT IN ('checkpoint','session_summary')
		ORDER BY m.pinned DESC, m.tier, m.updated_at DESC LIMIT ?`)
	qGraphCount = scoped("graph.count", `SELECT count(*) FROM memories m WHERE {{scope:m}} AND m.status IN (SELECT value FROM json_each(?)) AND m.kind NOT IN ('checkpoint','session_summary')`)

	qRecallGlobal = scoped("recall.global", `SELECT `+memCols+` FROM memories m WHERE {{scope:m}} AND m.status = 'active' AND (m.tier = 1 OR m.pinned = 1)
		AND m.kind NOT IN ('checkpoint','session_summary') AND (m.valid_until IS NULL OR m.valid_until > ?)
		ORDER BY m.pinned DESC, m.tier, m.confidence DESC, m.updated_at DESC LIMIT ?`)
	qRecallDecisions = scoped("recall.decisions", `SELECT `+memCols+` FROM memories m WHERE {{scope:m}} AND m.status = 'active'
		AND m.kind IN ('decision','architecture','business_rule','requirement','preference','pattern')
		AND (m.valid_until IS NULL OR m.valid_until > ?)
		ORDER BY m.pinned DESC, m.tier, m.updated_at DESC LIMIT ?`)
	qRecallTodos = scoped("recall.todos", `SELECT `+memCols+` FROM memories m WHERE {{scope:m}} AND m.status = 'active' AND m.kind = 'todo'
		ORDER BY m.pinned DESC, m.tier, m.updated_at DESC LIMIT ?`)
	qRecallCheckpoint = scoped("recall.checkpoint", `SELECT `+memCols+` FROM memories m WHERE {{scope:m}} AND m.status = 'active' AND m.kind = 'checkpoint'
		ORDER BY m.updated_at DESC LIMIT 1`)
	qRecallProposed = scoped("recall.proposed", `SELECT `+memCols+` FROM memories m WHERE {{scope:m}} AND m.status = 'proposed' ORDER BY m.created_at DESC LIMIT ?`)
	qRecallCounts   = scoped("recall.counts", `SELECT
		(SELECT count(*) FROM memories m WHERE {{scope:m}} AND m.status = 'proposed'),
		(SELECT count(*) FROM memories m WHERE {{scope:m}} AND m.status = 'active' AND m.review_after IS NOT NULL AND m.review_after <= ?),
		(SELECT count(*) FROM review_items r WHERE {{scope:r}} AND r.status = 'open' AND r.type = 'conflict')`)
	qRecallBrief = scoped("recall.brief", `SELECT m.id, m.title, m.supersede_reason FROM memories m WHERE m.id IN (SELECT value FROM json_each(?)) AND {{scope:m}}`)
)

// FTSQuery turns free text into a safe FTS5 expression of quoted terms.
// mode "all" joins with AND; anything else uses OR.
func FTSQuery(text, mode string) string {
	var terms []string
	seen := map[string]bool{}
	var cur strings.Builder
	push := func() {
		t := cur.String()
		cur.Reset()
		if len([]rune(t)) < 2 || seen[t] || stopword(t) {
			return
		}
		seen[t] = true
		terms = append(terms, t)
	}
	for _, r := range strings.ToLower(text) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' {
			cur.WriteRune(r)
			continue
		}
		push()
	}
	push()
	if len(terms) > 24 {
		terms = terms[:24]
	}
	if len(terms) == 0 {
		return ""
	}
	parts := make([]string, len(terms))
	for i, t := range terms {
		parts[i] = `"` + t + `"`
	}
	parts[len(parts)-1] += "*"
	sep := " OR "
	if mode == "all" {
		sep = " AND "
	}
	return strings.Join(parts, sep)
}

var stopwords = map[string]bool{
	"the": true, "and": true, "for": true, "with": true, "that": true, "this": true, "from": true, "into": true, "are": true, "was": true,
	"de": true, "da": true, "do": true, "das": true, "dos": true, "em": true, "no": true, "na": true, "um": true, "uma": true,
	"para": true, "por": true, "com": true, "que": true, "os": true, "as": true, "se": true, "ao": true, "the_": true,
}

func stopword(t string) bool { return stopwords[t] }

// Hit is a search result with its raw FTS rank (lower is better).
type Hit struct {
	Memory Memory  `json:"memory"`
	Rank   float64 `json:"rank"`
}

type SearchFilter struct {
	ScopeIDs []string
	Statuses []string
	Kind     string
	Mode     string
	Limit    int
}

func intersect(ss auth.ScopeSet, want []string) []string {
	if len(want) == 0 {
		return ss.ReadIDs()
	}
	var out []string
	for _, id := range want {
		if ss.CanRead(id) {
			out = append(out, id)
		}
	}
	return out
}

// SearchFTS runs a full-text query over the caller's readable scopes.
func (s *Store) SearchFTS(ctx context.Context, ss auth.ScopeSet, text string, f SearchFilter) ([]Hit, error) {
	return s.searchFTS(s.reader(ctx), ss, text, f)
}

func (s *Store) searchFTS(r rd, ss auth.ScopeSet, text string, f SearchFilter) ([]Hit, error) {
	q := FTSQuery(text, f.Mode)
	if q == "" {
		return []Hit{}, nil
	}
	if f.Limit <= 0 || f.Limit > 200 {
		f.Limit = 20
	}
	if len(f.Statuses) == 0 {
		f.Statuses = []string{"active"}
	}
	hits, err := collectRows(r.q(qSearchFTS, q, ids(intersect(ss, f.ScopeIDs)), ids(f.Statuses), f.Kind, f.Kind, f.Limit), func(rs rowScanner) (Hit, error) {
		var h Hit
		var m Memory
		var details string
		err := rs.Scan(&m.Seq, &m.ID, &m.ScopeID, &m.SessionID, &m.Kind, &m.Title, &m.Content, &m.Tier, &m.Pinned, &m.Status, &m.ReviewState,
			&m.Confidence, &m.SourceType, &m.CreatedByPrincipalID, &m.CreatedByClient, &m.CreatedByAgent, &m.CreatedByModel,
			&m.CreatedSessionID, &m.UserConfirmedClaim, &m.ApprovedBy, &m.ApprovedVia, &m.LineageID,
			&m.SupersedesID, &m.TargetLineageID, &m.SupersededByID, &m.SupersedeReason,
			&m.DerivedFromID, &m.Rationale, &details, &m.ValidFrom, &m.ValidUntil,
			&m.ReviewAfter, &m.ContentHash, &m.Version, &m.Confirmations, &m.LastConfirmedAt, &m.AccessCount,
			&m.LastAccessedAt, &m.CreatedAt, &m.UpdatedAt, &h.Rank)
		if details != "" && details != "{}" {
			m.Details = json.RawMessage(details)
		}
		m.Tags = []string{}
		h.Memory = m
		return h, err
	})
	if hits == nil {
		hits = []Hit{}
	}
	return hits, err
}

type MemoryFilter struct {
	ScopeIDs  []string
	Statuses  []string
	Kind      string
	Tier      int
	Tag       string
	SessionID string
	Limit     int
	Offset    int
}

// ListMemories pages through memories in readable scopes.
func (s *Store) ListMemories(ctx context.Context, ss auth.ScopeSet, f MemoryFilter) ([]Memory, int, error) {
	if f.Limit <= 0 || f.Limit > 500 {
		f.Limit = 50
	}
	if f.Offset < 0 {
		f.Offset = 0
	}
	st := ids(f.Statuses)
	scope := ids(intersect(ss, f.ScopeIDs))
	args := []any{scope, st, st, f.Kind, f.Kind, f.Tier, f.Tier, f.SessionID, f.SessionID, strings.ToLower(f.Tag), strings.ToLower(f.Tag)}
	var total int
	r := s.reader(ctx)
	if err := r.row(qMemCount, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	list, err := collectRows(r.q(qMemList, append(args, f.Limit, f.Offset)...), scanMemory)
	if err != nil {
		return nil, 0, err
	}
	if list == nil {
		list = []Memory{}
	}
	return list, total, s.attachTags(ctx, ss, list)
}

// Graph -------------------------------------------------------------------------

// Link type constants for GraphEdge.Type.
const (
	EdgeTypeLink       = "link"
	EdgeTypeSupersedes = "supersedes"
	EdgeTypeSimilar    = "similar"
)

// SimilarLinkLabel is the links.label value used for auto-suggested edges.
const SimilarLinkLabel = "similar"

type GraphNode struct {
	ID          string `json:"id"`
	ScopeID     string `json:"scope_id"`
	Title       string `json:"title"`
	Kind        string `json:"kind"`
	Tier        int    `json:"tier"`
	Status      string `json:"status"`
	Pinned      bool   `json:"pinned"`
	AccessCount int    `json:"access_count"`
	Degree      int    `json:"degree"`
	LinkCount   int    `json:"link_count"`
}

type GraphEdge struct {
	Source string  `json:"source"`
	Target string  `json:"target"`
	Label  string  `json:"label"`
	Type   string  `json:"type"`
	Weight float64 `json:"weight"`
}

type Graph struct {
	Nodes     []GraphNode `json:"nodes"`
	Edges     []GraphEdge `json:"edges"`
	Total     int         `json:"total"`
	Truncated bool        `json:"truncated"`
	Limit     int         `json:"limit"`
}

func classifyEdge(label string) (typ string, weight float64) {
	switch {
	case label == EdgeTypeSupersedes:
		return EdgeTypeSupersedes, 1
	case label == SimilarLinkLabel || strings.HasPrefix(label, SimilarLinkLabel+":"):
		w := 0.55
		if i := strings.IndexByte(label, ':'); i >= 0 {
			if parsed, err := strconv.ParseFloat(label[i+1:], 64); err == nil && parsed > 0 && parsed <= 1 {
				w = parsed
			}
		}
		return EdgeTypeSimilar, w
	default:
		return EdgeTypeLink, 0.8
	}
}

func (s *Store) Graph(ctx context.Context, ss auth.ScopeSet, scopeIDs []string, withSuperseded bool, limit int) (Graph, error) {
	if limit <= 0 || limit > 2000 {
		limit = 500
	}
	statuses := []string{"active", "proposed"}
	if withSuperseded {
		statuses = append(statuses, "superseded")
	}
	scope := ids(intersect(ss, scopeIDs))
	st := ids(statuses)
	g := Graph{Limit: limit, Nodes: []GraphNode{}, Edges: []GraphEdge{}}
	type sup struct{ from, to string }
	var sups []sup
	// Fetch up to 2000 candidates so truncation can prefer high-degree hubs.
	fetch := 2000
	err := s.readTx(ctx, func(r rd) error {
		if err := r.row(qGraphCount, scope, st).Scan(&g.Total); err != nil {
			return err
		}
		rows, err := r.query(qGraphNodes, scope, st, fetch)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var n GraphNode
			var supersedes string
			if err := rows.Scan(&n.ID, &n.ScopeID, &n.Title, &n.Kind, &n.Tier, &n.Status, &n.Pinned, &supersedes, &n.AccessCount); err != nil {
				return err
			}
			g.Nodes = append(g.Nodes, n)
			if supersedes != "" {
				sups = append(sups, sup{n.ID, supersedes})
			}
		}
		return rows.Err()
	})
	if err != nil {
		return g, err
	}
	in := map[string]bool{}
	idList := make([]string, 0, len(g.Nodes))
	for _, n := range g.Nodes {
		in[n.ID] = true
		idList = append(idList, n.ID)
	}
	var edges []GraphEdge
	for _, x := range sups {
		if in[x.to] {
			edges = append(edges, GraphEdge{Source: x.from, Target: x.to, Label: "supersedes", Type: EdgeTypeSupersedes, Weight: 1})
		}
	}
	links, err := s.LinksFor(ctx, ss, idList)
	if err != nil {
		return g, err
	}
	for _, l := range links {
		if !in[l.SourceID] || !in[l.TargetID] {
			continue
		}
		typ, w := classifyEdge(l.Label)
		edges = append(edges, GraphEdge{Source: l.SourceID, Target: l.TargetID, Label: l.Label, Type: typ, Weight: w})
	}
	deg := map[string]int{}
	linkN := map[string]int{}
	for _, e := range edges {
		deg[e.Source]++
		deg[e.Target]++
		if e.Type != EdgeTypeSupersedes {
			linkN[e.Source]++
			linkN[e.Target]++
		}
	}
	for i := range g.Nodes {
		g.Nodes[i].Degree = deg[g.Nodes[i].ID]
		g.Nodes[i].LinkCount = linkN[g.Nodes[i].ID]
	}
	sort.SliceStable(g.Nodes, func(i, j int) bool {
		a, b := g.Nodes[i], g.Nodes[j]
		if a.Degree != b.Degree {
			return a.Degree > b.Degree
		}
		if a.AccessCount != b.AccessCount {
			return a.AccessCount > b.AccessCount
		}
		if a.Pinned != b.Pinned {
			return a.Pinned
		}
		return a.Title < b.Title
	})
	g.Truncated = g.Total > limit || len(g.Nodes) > limit
	if len(g.Nodes) > limit {
		g.Nodes = g.Nodes[:limit]
	}
	keep := map[string]bool{}
	for _, n := range g.Nodes {
		keep[n.ID] = true
	}
	g.Edges = g.Edges[:0]
	for _, e := range edges {
		if keep[e.Source] && keep[e.Target] {
			g.Edges = append(g.Edges, e)
		}
	}
	if g.Edges == nil {
		g.Edges = []GraphEdge{}
	}
	return g, nil
}

// Recall ------------------------------------------------------------------------

type RecallQuery struct {
	GlobalID      string
	ProjectID     string
	SessionID     string
	Text          string
	GlobalLimit   int
	DecisionLimit int
	TodoLimit     int
	HitLimit      int
	ProposedLimit int
}

type Brief struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Reason string `json:"reason,omitempty"`
}

type RecallData struct {
	Global        []Memory
	Decisions     []Memory
	Todos         []Memory
	Checkpoint    *Memory
	Hits          []Hit
	Proposed      []Memory
	ProposedCount int
	StaleCount    int
	ConflictCount int
	Superseded    map[string]Brief
}

// RecallSnapshot reads everything the recall pack needs in one read transaction.
// Scopes outside the caller's readable set are skipped.
func (s *Store) RecallSnapshot(ctx context.Context, ss auth.ScopeSet, q RecallQuery) (RecallData, error) {
	var d RecallData
	ts := now()
	var gl, pr []string
	if q.GlobalID != "" && ss.CanRead(q.GlobalID) {
		gl = []string{q.GlobalID}
	}
	if q.ProjectID != "" && ss.CanRead(q.ProjectID) {
		pr = []string{q.ProjectID}
	}
	both := append(append([]string{}, gl...), pr...)
	glj, prj, bothj := ids(gl), ids(pr), ids(both)
	err := s.readTx(ctx, func(r rd) error {
		var err error
		if d.Global, err = collectRows(r.q(qRecallGlobal, glj, ts, q.GlobalLimit), scanMemory); err != nil {
			return err
		}
		if d.Decisions, err = collectRows(r.q(qRecallDecisions, prj, ts, q.DecisionLimit), scanMemory); err != nil {
			return err
		}
		if d.Todos, err = collectRows(r.q(qRecallTodos, prj, q.TodoLimit), scanMemory); err != nil {
			return err
		}
		var cp []Memory
		if q.SessionID != "" {
			cp, err = collectRows(r.q(qSessionCheckpoint, q.SessionID, prj), scanMemory)
			if err != nil {
				return err
			}
		}
		if len(cp) == 0 {
			if cp, err = collectRows(r.q(qRecallCheckpoint, prj), scanMemory); err != nil {
				return err
			}
		}
		if len(cp) > 0 {
			d.Checkpoint = &cp[0]
		}
		if q.Text != "" && q.HitLimit > 0 {
			if d.Hits, err = s.searchFTS(r, ss, q.Text, SearchFilter{ScopeIDs: both, Limit: q.HitLimit + 10}); err != nil {
				return err
			}
		}
		if d.Proposed, err = collectRows(r.q(qRecallProposed, bothj, q.ProposedLimit), scanMemory); err != nil {
			return err
		}
		if err := r.row(qRecallCounts, bothj, bothj, ts, bothj).Scan(&d.ProposedCount, &d.StaleCount, &d.ConflictCount); err != nil {
			return err
		}
		var sup []string
		for _, m := range d.Decisions {
			if m.SupersedesID != "" {
				sup = append(sup, m.SupersedesID)
			}
		}
		for _, m := range d.Global {
			if m.SupersedesID != "" {
				sup = append(sup, m.SupersedesID)
			}
		}
		for _, m := range d.Proposed {
			if m.SupersedesID != "" {
				sup = append(sup, m.SupersedesID)
			}
		}
		d.Superseded = map[string]Brief{}
		if len(sup) > 0 {
			rows, err := r.query(qRecallBrief, ids(sup), bothj)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var b Brief
				if err := rows.Scan(&b.ID, &b.Title, &b.Reason); err != nil {
					return err
				}
				d.Superseded[b.ID] = b
			}
			return rows.Err()
		}
		return nil
	})
	if err != nil {
		return d, err
	}
	var seqs []int64
	for _, list := range [][]Memory{d.Global, d.Decisions, d.Todos} {
		for _, m := range list {
			seqs = append(seqs, m.Seq)
		}
	}
	s.access.touch(seqs...)
	return d, nil
}

// TouchAccess records reads of memories returned outside GetMemory.
func (s *Store) TouchAccess(ms []Memory) {
	seqs := make([]int64, len(ms))
	for i, m := range ms {
		seqs[i] = m.Seq
	}
	s.access.touch(seqs...)
}
