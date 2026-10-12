package store

import (
	"context"
	"sync"
	"time"
)

var (
	qLogInsert = system("log.insert", "log", `INSERT INTO logs(ts, source, principal_id, scope_id, operation, level, status, detail, duration_us) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	qLogList   = system("log.list", "log", `SELECT id, ts, source, principal_id, scope_id, operation, level, status, detail, duration_us FROM logs
		WHERE (? = '' OR operation LIKE ? || '%') AND (? = '' OR level = ?) AND (? = '' OR source = ?) AND (? = 0 OR id < ?)
		ORDER BY id DESC LIMIT ?`)
	qLogPurge = system("log.purge", "job", `DELETE FROM logs WHERE ts < ?`)
)

// Logger buffers audit entries and writes them in batches. Entries never carry
// memory content, only the operation, status and short details.
type Logger struct {
	s    *Store
	ch   chan LogEntry
	quit chan struct{}
	done chan struct{}
	once sync.Once
}

func (s *Store) NewLogger() *Logger {
	l := &Logger{s: s, ch: make(chan LogEntry, 4096), quit: make(chan struct{}), done: make(chan struct{})}
	go l.loop()
	return l
}

// Log enqueues an entry; when the buffer is full the entry is dropped rather
// than blocking a request.
func (l *Logger) Log(e LogEntry) {
	if l == nil {
		return
	}
	if e.TS == "" {
		e.TS = now()
	}
	if e.Level == "" {
		e.Level = "info"
	}
	e.Detail = clip(e.Detail, 500)
	select {
	case l.ch <- e:
	default:
	}
}

func (l *Logger) loop() {
	defer close(l.done)
	t := time.NewTicker(time.Second)
	defer t.Stop()
	var buf []LogEntry
	flush := func() {
		if len(buf) == 0 {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		_ = l.s.write(ctx, func(w wx) error {
			for _, e := range buf {
				if _, err := w.exec(qLogInsert, e.TS, e.Source, e.PrincipalID, e.ScopeID, e.Operation, e.Level, e.Status, e.Detail, e.DurationUS); err != nil {
					return err
				}
			}
			return nil
		})
		cancel()
		buf = buf[:0]
	}
	for {
		select {
		case e := <-l.ch:
			buf = append(buf, e)
			if len(buf) >= 200 {
				flush()
			}
		case <-t.C:
			flush()
		case <-l.quit:
			for {
				select {
				case e := <-l.ch:
					buf = append(buf, e)
				default:
					flush()
					return
				}
			}
		}
	}
}

func (l *Logger) Close() {
	if l == nil {
		return
	}
	l.once.Do(func() { close(l.quit) })
	<-l.done
}

type LogFilter struct {
	Operation string
	Level     string
	Source    string
	BeforeID  int64
	Limit     int
}

// ListLogs is for the admin UI only; callers check the principal.
func (s *Store) ListLogs(ctx context.Context, f LogFilter) ([]LogEntry, error) {
	if f.Limit <= 0 || f.Limit > 1000 {
		f.Limit = 200
	}
	list, err := collectRows(s.reader(ctx).q(qLogList, f.Operation, f.Operation, f.Level, f.Level, f.Source, f.Source, f.BeforeID, f.BeforeID, f.Limit), func(r rowScanner) (LogEntry, error) {
		var e LogEntry
		return e, r.Scan(&e.ID, &e.TS, &e.Source, &e.PrincipalID, &e.ScopeID, &e.Operation, &e.Level, &e.Status, &e.Detail, &e.DurationUS)
	})
	if list == nil {
		list = []LogEntry{}
	}
	return list, err
}

func (s *Store) PurgeLogs(ctx context.Context, before string) (int64, error) {
	var n int64
	err := s.write(ctx, func(w wx) (err error) {
		n, err = w.execN(qLogPurge, before)
		return err
	})
	return n, err
}
