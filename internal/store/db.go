// Package store is the only package that talks to SQLite. Every statement is a
// registered *Query; queries over scoped tables carry a scope marker that is
// expanded into a predicate bound to the caller's auth.ScopeSet.
package store

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	_ "modernc.org/sqlite"
)

//go:embed schema.sql
var schemaSQL string

const SchemaVersion = "3"

var (
	ErrNotFound  = errors.New("not found")
	ErrConflict  = errors.New("conflict")
	ErrForbidden = errors.New("forbidden")
	ErrInvalid   = errors.New("invalid")
	ErrClosed    = errors.New("work session is closed")
	errFailpoint = errors.New("failpoint")
)

type Store struct {
	path   string
	w      *sql.DB
	r      *sql.DB
	wcache *stmtCache
	rcache *stmtCache
	wmu    sync.Mutex
	fail   func(stage string) error
	access *accessBatcher
	refs   scopeRefCache

	// authVersion changes whenever tokens, grants, principals or scopes change,
	// so request-level caches can drop stale entries.
	authVersion atomic.Int64
	// scopeVersion changes on every memory write in a scope.
	scopeVersion sync.Map // scope id -> *atomic.Int64
}

func dsn(path string, readOnly bool) string {
	q := url.Values{}
	for _, p := range []string{
		"journal_mode(WAL)",
		"busy_timeout(10000)",
		"foreign_keys(1)",
		"synchronous(NORMAL)",
		"temp_store(MEMORY)",
		"cache_size(-32000)",
		"mmap_size(268435456)",
	} {
		q.Add("_pragma", p)
	}
	if readOnly {
		q.Add("_pragma", "query_only(1)")
	} else {
		q.Set("_txlock", "immediate")
	}
	return "file:" + filepath.ToSlash(path) + "?" + q.Encode()
}

// Open opens (and if needed creates) the database at path.
func Open(ctx context.Context, path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	w, err := sql.Open("sqlite", dsn(path, false))
	if err != nil {
		return nil, err
	}
	w.SetMaxOpenConns(1)
	w.SetMaxIdleConns(1)
	w.SetConnMaxLifetime(0)
	s := &Store{path: path, w: w, wcache: newStmtCache(w)}
	if err := s.migrate(ctx); err != nil {
		w.Close()
		return nil, err
	}
	r, err := sql.Open("sqlite", dsn(path, false))
	if err != nil {
		w.Close()
		return nil, err
	}
	n := runtime.GOMAXPROCS(0)
	if n < 2 {
		n = 2
	}
	r.SetMaxOpenConns(n)
	r.SetMaxIdleConns(n)
	r.SetConnMaxLifetime(0)
	s.r = r
	s.rcache = newStmtCache(r)
	s.access = newAccessBatcher(s)
	return s, nil
}

func (s *Store) Path() string { return s.path }

func (s *Store) Close() error {
	if s.access != nil {
		s.access.stop()
	}
	if s.rcache != nil {
		s.rcache.close()
	}
	s.wcache.close()
	var errs []error
	if s.r != nil {
		errs = append(errs, s.r.Close())
	}
	_, _ = s.w.Exec(qWalCheckpoint.SQL)
	errs = append(errs, s.w.Close())
	return errors.Join(errs...)
}

// SetFailpoint installs a hook called at named stages of write transactions.
// Returning an error aborts the transaction. Used by rollback tests.
func (s *Store) SetFailpoint(f func(stage string) error) { s.fail = f }

func (s *Store) failpoint(stage string) error {
	if s.fail == nil {
		return nil
	}
	if err := s.fail(stage); err != nil {
		return fmt.Errorf("%w at %s: %v", errFailpoint, stage, err)
	}
	return nil
}

func (s *Store) AuthVersion() int64 { return s.authVersion.Load() }
func (s *Store) bumpAuth()          { s.authVersion.Add(1) }

// ScopeVersion is a per-scope write counter used to invalidate recall caches.
func (s *Store) ScopeVersion(scopeID string) int64 {
	v, ok := s.scopeVersion.Load(scopeID)
	if !ok {
		return 0
	}
	return v.(*atomic.Int64).Load()
}

func (s *Store) bumpScope(scopeID string) {
	v, _ := s.scopeVersion.LoadOrStore(scopeID, new(atomic.Int64))
	v.(*atomic.Int64).Add(1)
}

var (
	qSchemaExists  = system("schema.exists", "schema", `SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = 'meta'`)
	qMetaGet       = system("meta.get", "schema", `SELECT value FROM meta WHERE key = ?`)
	qMetaSet       = system("meta.set", "schema", `INSERT INTO meta(key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`)
	qWalCheckpoint = system("wal.checkpoint", "backup", `PRAGMA wal_checkpoint(TRUNCATE)`)
	qProbeVersion  = system("probe.version", "probe", `SELECT sqlite_version()`)
	qProbeFTS5     = system("probe.fts5", "probe", `SELECT sqlite_compileoption_used('ENABLE_FTS5')`)
	qProbeJournal  = system("probe.journal", "probe", `PRAGMA journal_mode`)
	qProbeTempFTS  = system("probe.fts_create", "probe", `CREATE VIRTUAL TABLE temp.impamind_probe_fts USING fts5(body, tokenize = 'porter unicode61 remove_diacritics 2')`)
	qProbeTempIns  = system("probe.fts_insert", "probe", `INSERT INTO temp.impamind_probe_fts(body) VALUES ('Configurações persistentes funcionando')`)
	qProbeTempHit  = system("probe.fts_match", "probe", `SELECT count(*) FROM temp.impamind_probe_fts WHERE impamind_probe_fts MATCH 'configuracoes AND funciona*'`)
	qProbeTempDrop = system("probe.fts_drop", "probe", `DROP TABLE temp.impamind_probe_fts`)
	qProbeInsert   = system("probe.insert", "probe", `INSERT INTO probe(note, at) VALUES (?, ?)`)
	qProbeCount    = system("probe.count", "probe", `SELECT count(*) FROM probe`)
	qVacuumInto    = system("backup.vacuum_into", "backup", `VACUUM INTO ?`)
	qFTSIntegrity  = system("backup.fts_integrity", "backup", `INSERT INTO memories_fts(memories_fts, rank) VALUES ('integrity-check', 1)`)
	qIntegrity     = system("backup.integrity", "backup", `PRAGMA integrity_check`)
)

func (s *Store) migrate(ctx context.Context) error {
	var n int
	if err := s.w.QueryRowContext(ctx, qSchemaExists.SQL).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		var v string
		if err := s.w.QueryRowContext(ctx, qMetaGet.SQL, "schema_version").Scan(&v); err != nil {
			return fmt.Errorf("read schema version: %w", err)
		}
		if v == SchemaVersion {
			return nil
		}
		switch v {
		case "1":
			if _, err := s.w.ExecContext(ctx, `ALTER TABLE oauth_codes ADD COLUMN principal_id TEXT NOT NULL DEFAULT ''`); err != nil && !strings.Contains(err.Error(), "duplicate column") {
				return err
			}
			if _, err := s.w.ExecContext(ctx, `ALTER TABLE oauth_tokens ADD COLUMN principal_id TEXT NOT NULL DEFAULT ''`); err != nil && !strings.Contains(err.Error(), "duplicate column") {
				return err
			}
			fallthrough
		case "2":
			if _, err := s.w.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS vault_entries (
  id TEXT PRIMARY KEY,
  scope_id TEXT NOT NULL REFERENCES scopes(id),
  key TEXT NOT NULL,
  label TEXT NOT NULL DEFAULT '',
  hint TEXT NOT NULL DEFAULT '',
  ciphertext BLOB NOT NULL,
  created_by_principal_id TEXT NOT NULL,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  last_accessed_at TEXT,
  UNIQUE (scope_id, key),
  CHECK (length(key) >= 1 AND length(key) <= 128)
)`); err != nil {
				return err
			}
			if _, err := s.w.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS vault_entries_scope ON vault_entries(scope_id)`); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unsupported schema version %q", v)
		}
		if _, err := s.w.ExecContext(ctx, qMetaSet.SQL, "schema_version", SchemaVersion); err != nil {
			return err
		}
		return nil
	}
	tx, err := s.w.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, schemaSQL); err != nil {
		return fmt.Errorf("apply schema: %w", err)
	}
	if _, err := tx.ExecContext(ctx, qMetaSet.SQL, "schema_version", SchemaVersion); err != nil {
		return err
	}
	ts := now()
	if _, err := tx.ExecContext(ctx, qScopeInsertGlobal.SQL, newID(), ts, ts); err != nil {
		return err
	}
	adminID := newID()
	if _, err := tx.ExecContext(ctx, qPrincipalInsertAdmin.SQL, adminID, ts); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, qGrantInsertAdmin.SQL, adminID); err != nil {
		return err
	}
	return tx.Commit()
}

// ProbeResult reports the SQLite build facts the deployment depends on.
type ProbeResult struct {
	SQLiteVersion string `json:"sqlite_version"`
	FTS5          bool   `json:"fts5"`
	FTS5Porter    bool   `json:"fts5_porter"`
	JournalMode   string `json:"journal_mode"`
}

func (s *Store) Probe(ctx context.Context) (ProbeResult, error) {
	var p ProbeResult
	if err := s.r.QueryRowContext(ctx, qProbeVersion.SQL).Scan(&p.SQLiteVersion); err != nil {
		return p, err
	}
	var used int
	if err := s.r.QueryRowContext(ctx, qProbeFTS5.SQL).Scan(&used); err != nil {
		return p, err
	}
	p.FTS5 = used == 1
	if err := s.r.QueryRowContext(ctx, qProbeJournal.SQL).Scan(&p.JournalMode); err != nil {
		return p, err
	}
	s.wmu.Lock()
	defer s.wmu.Unlock()
	conn, err := s.w.Conn(ctx)
	if err != nil {
		return p, err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, qProbeTempFTS.SQL); err != nil {
		return p, nil
	}
	defer conn.ExecContext(ctx, qProbeTempDrop.SQL)
	if _, err := conn.ExecContext(ctx, qProbeTempIns.SQL); err != nil {
		return p, nil
	}
	var hits int
	if err := conn.QueryRowContext(ctx, qProbeTempHit.SQL).Scan(&hits); err == nil {
		p.FTS5Porter = hits == 1
	}
	return p, nil
}

// ProbeWrite appends a row to the probe table and returns the total count. It
// proves that data survives container restarts.
func (s *Store) ProbeWrite(ctx context.Context, note string) (int, error) {
	err := s.write(ctx, func(w wx) error {
		_, err := w.exec(qProbeInsert, note, now())
		return err
	})
	if err != nil {
		return 0, err
	}
	var n int
	err = s.w.QueryRowContext(ctx, qProbeCount.SQL).Scan(&n)
	return n, err
}

func (s *Store) ProbeCount(ctx context.Context) (int, error) {
	var n int
	err := s.r.QueryRowContext(ctx, qProbeCount.SQL).Scan(&n)
	return n, err
}

// Backup writes a consistent copy of the database to dst with VACUUM INTO.
// It uses its own connection so writers are not blocked.
func (s *Store) Backup(ctx context.Context, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	if _, err := os.Stat(dst); err == nil {
		return fmt.Errorf("%w: backup target exists", ErrConflict)
	}
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(s.path)+"?_pragma=busy_timeout(10000)")
	if err != nil {
		return err
	}
	defer db.Close()
	_, err = db.ExecContext(ctx, qVacuumInto.SQL, dst)
	return err
}

// CheckIntegrity runs the SQLite and FTS5 integrity checks.
func (s *Store) CheckIntegrity(ctx context.Context) error {
	var res string
	if err := s.r.QueryRowContext(ctx, qIntegrity.SQL).Scan(&res); err != nil {
		return err
	}
	if res != "ok" {
		return fmt.Errorf("integrity_check: %s", res)
	}
	return s.write(ctx, func(w wx) error {
		_, err := w.exec(qFTSIntegrity)
		return err
	})
}

// tx runs fn inside a BEGIN IMMEDIATE transaction while holding the process
// writer mutex.
func (s *Store) tx(ctx context.Context, fn func(*sql.Tx) error) error {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	tx, err := s.w.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// TimeFormat is fixed-width so timestamps sort lexically.
const TimeFormat = "2006-01-02T15:04:05.000000Z"

func now() string { return time.Now().UTC().Format(TimeFormat) }

func FormatTime(t time.Time) string { return t.UTC().Format(TimeFormat) }

func ParseTime(s string) (time.Time, bool) {
	if s == "" {
		return time.Time{}, false
	}
	for _, f := range []string{TimeFormat, time.RFC3339Nano, time.RFC3339, "2006-01-02"} {
		if t, err := time.Parse(f, s); err == nil {
			return t.UTC(), true
		}
	}
	return time.Time{}, false
}

// NormalizeTime converts accepted inputs to TimeFormat; empty stays empty.
func NormalizeTime(s string) (string, error) {
	if strings.TrimSpace(s) == "" {
		return "", nil
	}
	t, ok := ParseTime(strings.TrimSpace(s))
	if !ok {
		return "", fmt.Errorf("%w: bad time %q", ErrInvalid, s)
	}
	return FormatTime(t), nil
}

func newID() string { return uuid.Must(uuid.NewV7()).String() }

// NewID returns a time-ordered unique identifier.
func NewID() string { return newID() }

func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}

func isAbort(err error, msg string) bool {
	return err != nil && strings.Contains(err.Error(), msg)
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}
