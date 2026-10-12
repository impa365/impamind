package store

import (
	"context"
	"sync"
	"time"
)

var qAccessBump = system("memory.access_bump", "job", `UPDATE memories SET access_count = access_count + ?, last_accessed_at = ? WHERE seq = ?`)

// accessBatcher aggregates read counters in memory and writes them in batches,
// so reads never cost a write. Access counts are telemetry: they do not create
// versions and never change tier or status.
type accessBatcher struct {
	s    *Store
	mu   sync.Mutex
	hits map[int64]int
	last map[int64]string
	quit chan struct{}
	done chan struct{}
}

func newAccessBatcher(s *Store) *accessBatcher {
	a := &accessBatcher{s: s, hits: map[int64]int{}, last: map[int64]string{}, quit: make(chan struct{}), done: make(chan struct{})}
	go a.loop()
	return a
}

func (a *accessBatcher) touch(seqs ...int64) {
	if a == nil || len(seqs) == 0 {
		return
	}
	ts := now()
	a.mu.Lock()
	for _, s := range seqs {
		a.hits[s]++
		a.last[s] = ts
	}
	a.mu.Unlock()
}

func (a *accessBatcher) loop() {
	defer close(a.done)
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			a.flush()
		case <-a.quit:
			a.flush()
			return
		}
	}
}

func (a *accessBatcher) flush() {
	a.mu.Lock()
	if len(a.hits) == 0 {
		a.mu.Unlock()
		return
	}
	hits, last := a.hits, a.last
	a.hits, a.last = map[int64]int{}, map[int64]string{}
	a.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = a.s.write(ctx, func(w wx) error {
		for seq, n := range hits {
			if _, err := w.exec(qAccessBump, n, last[seq], seq); err != nil {
				return err
			}
		}
		return nil
	})
}

// FlushAccess writes pending access counters now (tests and shutdown).
func (s *Store) FlushAccess() { s.access.flush() }

func (a *accessBatcher) stop() {
	select {
	case <-a.quit:
	default:
		close(a.quit)
	}
	<-a.done
}
