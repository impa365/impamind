// Package jobs runs the periodic maintenance of a single impamind process:
// proposal expiry, validity, staleness, tier suggestions, session archival and backups.
package jobs

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"impamind/internal/auth"
	"impamind/internal/config"
	"impamind/internal/store"
)

// LinkSuggester is implemented by protocol.Service for periodic similar-link scans.
type LinkSuggester interface {
	SuggestSimilarBatch(ctx context.Context, ss auth.ScopeSet, maxMemories int) (int, error)
}

type Jobs struct {
	st      *store.Store
	suggest LinkSuggester
	cfg     config.Config
	lg      *slog.Logger
}

func New(st *store.Store, suggest any, cfg config.Config, lg *slog.Logger) *Jobs {
	if lg == nil {
		lg = slog.Default()
	}
	j := &Jobs{st: st, cfg: cfg, lg: lg}
	if s, ok := suggest.(LinkSuggester); ok {
		j.suggest = s
	}
	return j
}

// Run sweeps once, then on every interval, until the context ends.
func (j *Jobs) Run(ctx context.Context) {
	j.Once(ctx)
	interval := j.cfg.JobsInterval
	if interval <= 0 {
		interval = 10 * time.Minute
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			j.Once(ctx)
		}
	}
}

// Once runs every sweep a single time.
func (j *Jobs) Once(ctx context.Context) {
	admin, err := j.st.AdminScope(ctx)
	if err != nil {
		j.lg.Error("jobs admin", "err", err)
		return
	}
	now := time.Now().UTC()
	stamp := store.FormatTime(now)
	if n, err := j.expireProposals(ctx, admin, now); err != nil {
		j.lg.Error("expire proposals", "err", err)
	} else if n > 0 {
		j.lg.Info("proposals expired", "n", n)
	}
	if n, err := j.st.ExpireDue(ctx, admin.Principal().ID, stamp); err != nil {
		j.lg.Error("expire memories", "err", err)
	} else if n > 0 {
		j.lg.Info("memories expired", "n", n)
	}
	if n, err := j.st.FlagStale(ctx, stamp); err != nil {
		j.lg.Error("flag stale", "err", err)
	} else if n > 0 {
		j.lg.Info("stale flagged", "n", n)
	}
	if n, err := j.st.SuggestTiers(ctx); err != nil {
		j.lg.Error("tier suggestions", "err", err)
	} else if n > 0 {
		j.lg.Info("tier suggestions", "n", n)
	}
	if j.cfg.SessionArchive > 0 {
		cutoff := store.FormatTime(now.Add(-j.cfg.SessionArchive))
		if n, err := j.st.ArchiveClosedSessions(ctx, admin.Principal().ID, cutoff); err != nil {
			j.lg.Error("archive sessions", "err", err)
		} else if n > 0 {
			j.lg.Info("session memories archived", "n", n)
		}
	}
	if j.cfg.BackupInterval > 0 && j.cfg.BackupDir != "" && j.backupDue() {
		if dst, err := j.st.RotateBackups(ctx, j.cfg.BackupDir, j.cfg.BackupKeep); err != nil {
			j.lg.Error("backup", "err", err)
		} else {
			j.lg.Info("backup", "file", dst)
		}
	}
	if j.cfg.LogRetention > 0 {
		if _, err := j.st.PurgeLogs(ctx, store.FormatTime(now.Add(-j.cfg.LogRetention))); err != nil {
			j.lg.Error("purge logs", "err", err)
		}
	}
	if j.suggest != nil {
		if n, err := j.suggest.SuggestSimilarBatch(ctx, admin, 40); err != nil {
			j.lg.Error("suggest similar links", "err", err)
		} else if n > 0 {
			j.lg.Info("similar links created", "n", n)
		}
	}
}

func (j *Jobs) backupDue() bool {
	entries, err := os.ReadDir(j.cfg.BackupDir)
	if err != nil {
		return true
	}
	var newest time.Time
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".db" {
			continue
		}
		info, err := e.Info()
		if err == nil && info.ModTime().After(newest) {
			newest = info.ModTime()
		}
	}
	return newest.IsZero() || time.Since(newest) >= j.cfg.BackupInterval
}

func (j *Jobs) expireProposals(ctx context.Context, ss auth.ScopeSet, now time.Time) (int, error) {
	if j.cfg.ProposalTTL <= 0 {
		return 0, nil
	}
	list, err := j.st.DueProposals(ctx, store.FormatTime(now.Add(-j.cfg.ProposalTTL)))
	if err != nil {
		return 0, err
	}
	n := 0
	for _, m := range list {
		if _, err := j.st.Reject(ctx, ss, m.ID, "expired_unreviewed"); err != nil {
			j.lg.Warn("reject proposal", "id", m.ID, "err", err)
			continue
		}
		n++
	}
	return n, nil
}
