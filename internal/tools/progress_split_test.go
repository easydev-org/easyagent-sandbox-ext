package tools

import (
	"context"
	"strings"
	"sync"
	"testing"

	easybus "github.com/easydev-org/easybus-sdk"
)

// TestLargeBatchIsSplitNotDropped pins the promise live output makes.
//
// The reporter used to CAP a batch at maxLinesPerEvent and append "… N more line(s)".
// That was acceptable while each frame carried the whole output — the cut only spoiled
// one frame's preview. Live frames are DELTAS now, which the agent APPENDS: a dropped
// line never comes back, so the console showed a note promising lines it would never
// receive and the stored result lost its middle.
//
// The per-event bound still holds — a single enormous event is worth avoiding — but it
// is enforced by sending several events rather than discarding the excess.
func TestLargeBatchIsSplitNotDropped(t *testing.T) {
	var (
		mu  sync.Mutex
		got []string
	)
	ctx := WithProgress(context.Background(), func(_ context.Context, p *easybus.ToolProgress) {
		mu.Lock()
		defer mu.Unlock()
		got = append(got, p.GetText())
	})

	r := NewProgressReporter()
	bound := r.maxLinesPerEvent
	total := bound*3 + 7
	lines := make([]string, total)
	for i := range lines {
		lines[i] = "line" + itoa(i)
	}

	r.emit(ctx, lines)

	mu.Lock()
	defer mu.Unlock()

	if len(got) < 2 {
		t.Fatalf("emitted %d event(s); a batch of %d lines must be SPLIT across several",
			len(got), total)
	}
	for i, text := range got {
		n := strings.Count(text, "\n") + 1
		if n > bound {
			t.Errorf("event %d carries %d lines, over the per-event bound %d", i, n, bound)
		}
		if strings.Contains(text, "more line(s)") {
			t.Errorf("event %d truncates with a note instead of splitting: %q", i, text)
		}
	}

	// Every line must survive, exactly once, in order.
	seen := strings.Split(strings.Join(got, "\n"), "\n")
	if len(seen) != total {
		t.Fatalf("delivered %d lines, want %d — lines were dropped", len(seen), total)
	}
	for i, l := range seen {
		if want := "line" + itoa(i); l != want {
			t.Fatalf("line %d = %q, want %q (order or content corrupted)", i, l, want)
		}
	}
}
