package activity

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/robertjndw/oakestra-mcp/internal/auth"
	"github.com/robertjndw/oakestra-mcp/internal/mcpserver"
)

const queueSize = 1024

// Recorder implements mcpserver.Recorder. Writes go through a bounded queue
// to one goroutine so a slow disk never delays a tool call; when the queue is
// full the entry is dropped and counted instead.
type Recorder struct {
	store   *Store
	ch      chan queued
	done    chan struct{}
	dropped atomic.Int64

	mu     sync.RWMutex // guards closed against sends racing with close(ch)
	closed bool
}

var _ mcpserver.Recorder = (*Recorder)(nil)

// queued holds unredacted args. Redaction happens in the writer goroutine
// to keep it off the tool call path; tool inputs aren't mutated afterwards.
type queued struct {
	entry Entry
	args  any
}

// NewRecorder starts the writer goroutine.
func NewRecorder(s *Store) *Recorder {
	r := &Recorder{store: s, ch: make(chan queued, queueSize), done: make(chan struct{})}
	go r.loop()
	return r
}

// Record queues the call. It never blocks.
func (r *Recorder) Record(ctx context.Context, c mcpserver.Call) {
	p, _ := auth.FromContext(ctx)
	e := Entry{
		TS:           time.Now(),
		User:         p.User,
		Organization: p.Organization,
		ClientName:   c.ClientName,
		Tool:         c.Tool,
		Kind:         string(c.Kind),
		Outcome:      string(c.Outcome),
		Error:        c.Err,
		DurationMS:   c.Duration.Milliseconds(),
		ActionID:     c.ActionID,
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.closed {
		return
	}
	select {
	case r.ch <- queued{entry: e, args: c.Args}:
	default:
		n := r.dropped.Add(1)
		slog.Warn("activity buffer full, dropping record", "tool", c.Tool, "dropped_total", n)
	}
}

func (r *Recorder) loop() {
	defer close(r.done)
	for q := range r.ch {
		e := q.entry
		e.ArgsJSON = RedactArgs(q.args)
		// A fresh context: the write must outlive whichever request produced it.
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		if _, err := r.store.Insert(ctx, e); err != nil {
			slog.Error("activity write failed", "tool", e.Tool, "err", err)
		}
		cancel()
	}
}

// Close stops accepting records and waits for queued ones to be written.
// It does not close the Store.
func (r *Recorder) Close() {
	r.mu.Lock()
	if !r.closed {
		r.closed = true
		close(r.ch)
	}
	r.mu.Unlock()
	<-r.done
}

// Run prunes entries older than retention hourly until ctx is done. A
// non-positive retention keeps calls forever. Expired pending actions are
// always pruned; they are useless once their token has expired.
func (s *Store) Run(ctx context.Context, retention time.Duration) {
	prune := func() {
		if retention > 0 {
			n, err := s.Prune(ctx, time.Now().Add(-retention))
			if err != nil && ctx.Err() == nil {
				slog.Error("activity prune failed", "err", err)
			} else if n > 0 {
				slog.Info("activity pruned", "deleted", n)
			}
		}
		// A grace period keeps an action that expired a moment ago around,
		// so confirm can still say "expired" instead of "unknown".
		if _, err := s.PrunePending(ctx, time.Now().Add(-time.Hour)); err != nil && ctx.Err() == nil {
			slog.Error("pending action prune failed", "err", err)
		}
	}
	prune()
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			prune()
		}
	}
}
