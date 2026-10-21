package tools

import (
	"context"
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
	}
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
	dropped := 0
	if len(lines) > r.maxLinesPerEvent {
		dropped = len(lines) - r.maxLinesPerEvent
		lines = lines[:r.maxLinesPerEvent]
	}
	text := strings.Join(lines, "\n")
	if dropped > 0 {
		// Say so instead of silently truncating: a UI that shows a gap without
		// explanation looks like the command hung.
		text += "\n… " + itoa(dropped) + " more line(s) in this batch"
	}
	EmitProgress(ctx, &easybus.ToolProgress{
		Phase: easybusString("running"),
		Text:  easybusString(text),
	})
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
