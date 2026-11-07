package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"

	"impamind/internal/auth"
)

type queryKind uint8

const (
	kindScoped queryKind = iota + 1
	kindSystem
)

// SystemReasons are the only accepted justifications for a query that does not
// carry a scope predicate.
var SystemReasons = map[string]bool{
	"schema":   true, // migrations and meta
	"probe":    true, // build and persistence probes
	"auth":     true, // token, principal, grant and web session lookup
	"approval": true, // append-only approval rows written inside Approve
	"job":      true, // background sweeps that run without a caller
	"backup":   true, // VACUUM INTO and integrity checks
	"log":      true, // audit log writes and admin reads
	"oauth":    true, // OAuth client, code and token tables
	"index":    true, // vector index load at startup
}

// Query is one registered SQL statement.
type Query struct {
	Name   string
	SQL    string
	Raw    string
	Kind   queryKind
	Reason string
	params int
}

func (q *Query) Scoped() bool { return q.Kind == kindScoped }

var (
	registryMu sync.Mutex
	registry   []*Query
	markerRe   = regexp.MustCompile(`\{\{(scope|in)(?::([A-Za-z0-9_.?]+))?\}\}`)
)

// Registry returns every registered query, sorted by name.
func Registry() []*Query {
	registryMu.Lock()
	defer registryMu.Unlock()
	out := make([]*Query, len(registry))
	copy(out, registry)
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func register(q *Query) *Query {
	registryMu.Lock()
	defer registryMu.Unlock()
	for _, e := range registry {
		if e.Name == q.Name {
			panic("store: duplicate query name " + q.Name)
		}
	}
	registry = append(registry, q)
	return q
}

// scoped registers a query that must contain at least one scope marker:
//
//	{{scope}}     scope_id IN (allowed)
//	{{scope:m}}   m.scope_id IN (allowed)
//	{{in:expr}}   expr IN (allowed)
//
// Each marker consumes one argument, a JSON array produced by ids().
func scoped(name, raw string) *Query {
	n := 0
	expanded := markerRe.ReplaceAllStringFunc(raw, func(m string) string {
		n++
		sub := markerRe.FindStringSubmatch(m)
		col := "scope_id"
		switch {
		case sub[1] == "scope" && sub[2] != "":
			col = sub[2] + ".scope_id"
		case sub[1] == "in":
			if sub[2] == "" {
				panic("store: {{in}} marker needs an expression in " + name)
			}
			col = sub[2]
		}
		return col + " IN (SELECT value FROM json_each(?))"
	})
	if n == 0 {
		panic("store: scoped query without scope predicate: " + name)
	}
	return register(&Query{Name: name, SQL: expanded, Raw: raw, Kind: kindScoped, params: n})
}

// system registers a query without scope predicate. reason must be one of
// SystemReasons; the process refuses to start otherwise.
func system(name, reason, raw string) *Query {
	if !SystemReasons[reason] {
		panic(fmt.Sprintf("store: query %s has unknown system reason %q", name, reason))
	}
	if markerRe.MatchString(raw) {
		panic("store: system query contains a scope marker: " + name)
	}
	return register(&Query{Name: name, SQL: raw, Raw: raw, Kind: kindSystem, Reason: reason})
}

// ids encodes a scope ID list as the JSON array consumed by json_each.
func ids(list []string) string {
	if len(list) == 0 {
		return "[]"
	}
	b, _ := json.Marshal(list)
	return string(b)
}

func readable(ss auth.ScopeSet) string { return ids(ss.ReadIDs()) }
func writable(ss auth.ScopeSet) string { return ids(ss.WritableIDs()) }

// Statement cache ---------------------------------------------------------------

type stmtCache struct {
	db *sql.DB
	mu sync.RWMutex
	m  map[*Query]*sql.Stmt
}

func newStmtCache(db *sql.DB) *stmtCache {
	return &stmtCache{db: db, m: map[*Query]*sql.Stmt{}}
}

func (c *stmtCache) get(ctx context.Context, q *Query) (*sql.Stmt, error) {
	c.mu.RLock()
	st := c.m[q]
	c.mu.RUnlock()
	if st != nil {
		return st, nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if st = c.m[q]; st != nil {
		return st, nil
	}
	st, err := c.db.PrepareContext(ctx, q.SQL)
	if err != nil {
		return nil, fmt.Errorf("prepare %s: %w", q.Name, err)
	}
	c.m[q] = st
	return st, nil
}

func (c *stmtCache) close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, st := range c.m {
		st.Close()
	}
	c.m = map[*Query]*sql.Stmt{}
}

// Executors -------------------------------------------------------------------
// These are the only places that hand SQL to database/sql.

type rowScanner interface{ Scan(dest ...any) error }

// rd runs reads on the reader pool, or inside a read transaction when tx is set.
type rd struct {
	s   *Store
	ctx context.Context
	tx  *sql.Tx
}

func (s *Store) reader(ctx context.Context) rd { return rd{s: s, ctx: ctx} }

// readTx runs fn inside one read transaction so multi-query reads see a single
// snapshot.
func (s *Store) readTx(ctx context.Context, fn func(rd) error) error {
	tx, err := s.r.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	return fn(rd{s: s, ctx: ctx, tx: tx})
}

func (r rd) stmt(q *Query) (*sql.Stmt, error) {
	st, err := r.s.rcache.get(r.ctx, q)
	if err != nil {
		return nil, err
	}
	if r.tx != nil {
		return r.tx.StmtContext(r.ctx, st), nil
	}
	return st, nil
}

func (r rd) query(q *Query, args ...any) (*sql.Rows, error) {
	st, err := r.stmt(q)
	if err != nil {
		return nil, err
	}
	rows, err := st.QueryContext(r.ctx, args...)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", q.Name, err)
	}
	return rows, nil
}

func (r rd) row(q *Query, args ...any) rowScanner {
	st, err := r.stmt(q)
	if err != nil {
		return errRow{err}
	}
	return st.QueryRowContext(r.ctx, args...)
}

// wx runs statements inside the current write transaction.
type wx struct {
	s   *Store
	ctx context.Context
	tx  *sql.Tx
}

func (w wx) stmt(q *Query) (*sql.Stmt, error) {
	st, err := w.s.wcache.get(w.ctx, q)
	if err != nil {
		return nil, err
	}
	return w.tx.StmtContext(w.ctx, st), nil
}

func (w wx) exec(q *Query, args ...any) (sql.Result, error) {
	// Statements are prepared on the transaction directly. Reusing a
	// connection-level *sql.Stmt through Tx.Stmt deadlocks on this driver when
	// the database lives on a file share.
	st, err := w.tx.PrepareContext(w.ctx, q.SQL)
	if err != nil {
		return nil, fmt.Errorf("prepare %s: %w", q.Name, err)
	}
	defer st.Close()
	res, err := st.ExecContext(w.ctx, args...)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", q.Name, err)
	}
	return res, nil
}

// execN runs exec and returns the number of affected rows.
func (w wx) execN(q *Query, args ...any) (int64, error) {
	res, err := w.exec(q, args...)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (w wx) query(q *Query, args ...any) (*sql.Rows, error) {
	st, err := w.tx.PrepareContext(w.ctx, q.SQL)
	if err != nil {
		return nil, fmt.Errorf("prepare %s: %w", q.Name, err)
	}
	defer st.Close()
	rows, err := st.QueryContext(w.ctx, args...)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", q.Name, err)
	}
	return rows, nil
}

func (w wx) row(q *Query, args ...any) rowScanner {
	st, err := w.tx.PrepareContext(w.ctx, q.SQL)
	if err != nil {
		return errRow{fmt.Errorf("prepare %s: %w", q.Name, err)}
	}
	// Closed with the transaction; closing it here would invalidate the row.
	return st.QueryRowContext(w.ctx, args...)
}

// write runs fn in a BEGIN IMMEDIATE transaction under the writer mutex.
func (s *Store) write(ctx context.Context, fn func(wx) error) error {
	return s.tx(ctx, func(tx *sql.Tx) error { return fn(wx{s: s, ctx: ctx, tx: tx}) })
}

type errRow struct{ err error }

func (e errRow) Scan(...any) error { return e.err }

func notFound(err error) error {
	if err == sql.ErrNoRows {
		return ErrNotFound
	}
	return err
}

// queryResult carries a query's rows and error so callers can pass
// r.query(...) straight through without splitting the tuple.
type queryResult struct {
	rows *sql.Rows
	err  error
}

func (r rd) q(query *Query, args ...any) queryResult {
	rows, err := r.query(query, args...)
	return queryResult{rows, err}
}

func (w wx) q(query *Query, args ...any) queryResult {
	rows, err := w.query(query, args...)
	return queryResult{rows, err}
}

func collectRows[T any](res queryResult, scan func(rowScanner) (T, error)) ([]T, error) {
	rows, err := res.rows, res.err
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []T
	for rows.Next() {
		v, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func placeholders(raw string) int { return strings.Count(raw, "?") }
