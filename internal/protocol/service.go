// Package protocol implements the memory operations shared by MCP and REST:
// recall, observe, decide, checkpoint, handoff, supersede and review.
package protocol

import (
	"context"
	"runtime"
	"time"

	"impamind/internal/auth"
	"impamind/internal/config"
	"impamind/internal/store"
)

var Version = "dev"

// Indexer receives memory changes for the optional vector index.
type Indexer interface {
	Enqueue(m store.Memory)
	Remove(scopeID string, seq int64)
	Search(ctx context.Context, scopeIDs []string, text string, k int) ([]VectorHit, error)
	Similar(ctx context.Context, scopeID, text string, k int) ([]VectorHit, error)
	Stats() map[string]any
}

type VectorHit struct {
	Seq   int64
	Score float64
}

type Service struct {
	St      *store.Store
	Cfg     config.Config
	Log     *store.Logger
	Index   Indexer
	started time.Time
	recall  *recallCache
}

func New(st *store.Store, cfg config.Config, log *store.Logger) *Service {
	return &Service{St: st, Cfg: cfg, Log: log, started: time.Now(), recall: newRecallCache()}
}

type Status struct {
	Name       string            `json:"name"`
	Version    string            `json:"version"`
	Go         string            `json:"go"`
	Uptime     string            `json:"uptime"`
	Principal  string            `json:"principal"`
	Class      string            `json:"class"`
	Project    string            `json:"project,omitempty"`
	Scopes     int               `json:"readable_scopes"`
	SQLite     store.ProbeResult `json:"sqlite"`
	RAG        bool              `json:"rag"`
	MCP        []string          `json:"mcp_versions"`
	Goroutines int               `json:"goroutines"`
}

func (s *Service) Status(ctx context.Context, ss auth.ScopeSet) (Status, error) {
	p, err := s.St.Probe(ctx)
	if err != nil {
		return Status{}, err
	}
	st := Status{
		Name: "impamind", Version: Version, Go: runtime.Version(),
		Uptime:    time.Since(s.started).Round(time.Second).String(),
		Principal: ss.Principal().Label, Class: string(ss.Principal().Class),
		Project: ss.Project().Slug, Scopes: len(ss.ReadIDs()), SQLite: p,
		RAG: s.Index != nil, MCP: s.Cfg.MCPVersions, Goroutines: runtime.NumGoroutine(),
	}
	return st, nil
}
