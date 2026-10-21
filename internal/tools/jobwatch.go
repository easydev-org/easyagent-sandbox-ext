package tools

import (
	"context"
	"errors"
	"io"
	"sync"

	workerv1 "github.com/easydev-org/easyagent-sandbox-ext/gen/worker/v1"
	"github.com/easydev-org/easyagent-sandbox-ext/internal/sandbox"
	easybus "github.com/easydev-org/easybus-sdk"
)

// The watcher is process-wide, not per-toolset: sandbox-job-start runs on a
// different toolset instance than the one that later calls sandbox-job-kill, and
// a per-call map would lose track of relays that are still running.
var watcher = newJobWatcher()

// CloseWatchers stops every live relay. The extension calls it on shutdown.
func CloseWatchers() { watcher.CloseAll() }

// jobWatcher relays a running job's output to the UI while the job runs.
//
// WHY it outlives the tool call: sandbox-job-start returns as soon as the job is
// launched — that is the whole point of it being a separate tool from
// sandbox-exec. The output, however, keeps coming for as long as the command
// runs, which may be minutes. Tying the relay to the tool call would mean the
// stream stops exactly when it becomes useful.
//
// So each watch gets its OWN lifetime (background context), and the set of live
// watches is tracked so the extension can cancel them all on shutdown — otherwise
// a stuck worker would leak a goroutine per job.
type jobWatcher struct {
	mu   sync.Mutex
	live map[*watchHandle]struct{}
}

type watchHandle struct {
	cancel context.CancelFunc
}

func newJobWatcher() *jobWatcher {
	return &jobWatcher{live: make(map[*watchHandle]struct{})}
}

// Watch starts relaying jobID's output to the progress sink carried by parentCtx.
//
// parentCtx is used ONLY to reach the sink (tenant + call id); it is deliberately
// not the lifetime of the relay. Everything else runs on a background context.
func (w *jobWatcher) Watch(parentCtx context.Context, wc sandbox.WorkerClient, jobID string) {
	ctx, cancel := context.WithCancel(context.Background())
	h := &watchHandle{cancel: cancel}
	w.mu.Lock()
	w.live[h] = struct{}{}
	w.mu.Unlock()

	go func() {
		defer func() {
			w.mu.Lock()
			delete(w.live, h)
			w.mu.Unlock()
			cancel()
		}()
		w.run(ctx, parentCtx, wc, jobID)
	}()
}

// CloseAll cancels every live relay. Called on extension shutdown so no goroutine
// outlives the process's usefulness.
func (w *jobWatcher) CloseAll() {
	w.mu.Lock()
	handles := make([]*watchHandle, 0, len(w.live))
	for h := range w.live {
		handles = append(handles, h)
	}
	w.live = make(map[*watchHandle]struct{})
	w.mu.Unlock()
	for _, h := range handles {
		h.cancel()
	}
}

func (w *jobWatcher) run(ctx context.Context, parentCtx context.Context, wc sandbox.WorkerClient, jobID string) {
	rep := NewProgressReporter()
	defer rep.Flush(parentCtx)

	stream, err := wc.WatchJob(ctx, &workerv1.WatchJobRequest{JobId: jobID})
	if err != nil {
		// Watching is best-effort telemetry: failing to attach must not turn a
		// successfully started job into an error the model sees.
		return
	}
	for {
		msg, err := stream.Receive()
		if errors.Is(err, io.EOF) {
			return
		}
		if err != nil {
			// The job ended, the worker restarted, or ctx was cancelled — all of
			// them mean "stop relaying", not "fail the tool".
			return
		}
		switch ev := msg.Event.(type) {
		case *workerv1.WatchJobResponse_Output:
			rep.Add(parentCtx, ev.Output)
		case *workerv1.WatchJobResponse_Done_:
			// The terminal event: flush the tail so the UI's last lines match what
			// the tool result will report.
			rep.Flush(parentCtx)
			EmitProgress(parentCtx, &easybus.ToolProgress{
				Phase: easybusString("exited"),
				// The exit code rides in metadata: it is not output text, and
				// putting it in `text` would make the UI render it as a log line.
				Metadata: easybus.Value(map[string]any{"exitCode": ev.Done.ExitCode}),
			})
			return
		}
	}
}
