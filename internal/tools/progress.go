package tools

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"

	easybus "github.com/easydev-org/easybus-sdk"
)

// ProgressFunc reports an in-flight update for the current tool call.
type ProgressFunc func(ctx context.Context, p *easybus.ToolProgress)

type progressKey struct{}

// WithProgress attaches a progress sink to ctx.
//
// Carried on the context rather than added to Deps because the tenant and call id
// are only known inside the per-call closure in main: the sink has to be created
// after those are in hand, and every handler already receives the ctx.
func WithProgress(ctx context.Context, fn ProgressFunc) context.Context {
	return context.WithValue(ctx, progressKey{}, fn)
}

// EmitProgress reports one update for the call this ctx belongs to. A nil sink (no
// UI attached, or a caller that did not install one) makes this a no-op.
func EmitProgress(ctx context.Context, p *easybus.ToolProgress) {
	if fn, ok := ctx.Value(progressKey{}).(ProgressFunc); ok && fn != nil {
		fn(ctx, p)
		return
	}
	// A frame with nowhere to go was dropped silently, which is indistinguishable
	// from "the extension never reported anything" — the worst kind of silence when
	// a user is waiting for output. Say it once.
	slog.Warn("tool progress dropped: no sink on context")
}

// ProgressReporter batches output lines into throttled progress events.
//
// WHY throttle: a command can emit a line per millisecond. Relaying every line as
// its own message floods the bus and the browser, and the UI cannot render
// thousands of updates a second anyway. Batching keeps the stream useful.
//
// WHY a ticker rather than a timer per line: a per-line timer resets constantly
// under load and can starve, so nothing would ever be sent during a burst.
type ProgressReporter struct {
	mu   sync.Mutex
	buf  []string
	last time.Time

	// minInterval bounds how often a batch is flushed.
	minInterval time.Duration
	// maxLinesPerEvent bounds how much a single event carries, so one burst cannot
	// produce a multi-megabyte message.
	maxLinesPerEvent int
}

// NewProgressReporter returns a reporter with defaults tuned for a terminal-like
// view: ~5 updates/second, 50 lines each.
func NewProgressReporter() *ProgressReporter {
	return &ProgressReporter{minInterval: 200 * time.Millisecond, maxLinesPerEvent: 50}
}

// Add queues lines and flushes when it is time. Call Flush at the end so the tail
// is not lost.
func (r *ProgressReporter) Add(ctx context.Context, lines ...string) {
	r.mu.Lock()
	for _, l := range lines {
		l = strings.TrimRight(l, "\r\n")
		if l == "" {
			continue
		}
		r.buf = append(r.buf, l)
	}
	due := time.Since(r.last) >= r.minInterval
	var batch []string
	if due && len(r.buf) > 0 {
		batch, r.buf = r.buf, nil
		r.last = time.Now()
	}
	r.mu.Unlock()

	if len(batch) > 0 {
		r.emit(ctx, batch)
	}
}

// Flush sends whatever is pending.
func (r *ProgressReporter) Flush(ctx context.Context) {
	r.mu.Lock()
	batch := r.buf
	r.buf = nil
	r.last = time.Now()
	r.mu.Unlock()
	if len(batch) > 0 {
		r.emit(ctx, batch)
	}
}

func (r *ProgressReporter) emit(ctx context.Context, lines []string) {
	// SPLIT rather than truncate.
	//
	// `maxLinesPerEvent` used to CAP a batch and append "… N more line(s)", which was
	// fine when every frame carried the whole output: the cut only affected one frame's
	// preview. Live frames are DELTAS now — the agent appends them — so a dropped line
	// is gone for good, and a job that printed faster than the reporter polls lost its
	// middle while the console showed a promise ("N more lines") it could never deliver.
	//
	// The per-event bound still exists, because a single enormous event is worth
	// avoiding; it is enforced by sending several events instead of discarding the
	// excess. Sizing the result is the agent's job, and it already trims to a fixed
	// number of lines.
	for start := 0; start < len(lines); start += r.maxLinesPerEvent {
		end := start + r.maxLinesPerEvent
		if end > len(lines) {
			end = len(lines)
		}
		EmitProgress(ctx, &easybus.ToolProgress{
			Phase: easybusString("running"),
			Text:  easybusString(strings.Join(lines[start:end], "\n")),
		})
	}
}

func easybusString(s string) *string { return &s }

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
