package tools

import (
	"context"
	"strings"
	"testing"
	"time"

	workerv1 "github.com/easydev-org/easyagent-sandbox-ext/gen/worker/v1"
	"github.com/easydev-org/easyagent-sandbox-ext/gen/worker/v1/workerv1connect"
)

/**
 * sandbox-job-wait: the three behaviours it is defined by.
 *
 * IT IS THE EXTENSION'S OWN WAIT, WHICH IS WHY THE GENERIC `job-wait` COULD GO. That one answered from
 * a KV copy the extension had written; this one asks the worker, so "still running" means the process
 * is still running rather than that a record was recently renewed.
 *
 *   1. the job ends while we wait  → return at that moment, with the whole output
 *   2. the budget expires          → return the LAST 200 lines and say it is still running
 *   3. the worker cannot be asked  → an error, not a state
 */

// waitScript answers JobWait from a list, so a test can decide when the job "ends".
type waitScript struct {
	workerv1connect.UnimplementedWorkerServiceHandler
	// states are returned in order; the last one repeats.
	states []string
	calls  int
	// outputLines is what JobOutput returns, numbered.
	outputLines []string
	// lastOutputStart records the `start` the tool asked for, which is how "the last 200 lines"
	// is asserted without guessing at the tool's internals.
	lastOutputStart int32
	lastOutputEnd   int32
}

func (w *waitScript) Info(context.Context, *workerv1.InfoRequest) (*workerv1.InfoResponse, error) {
	return &workerv1.InfoResponse{Os: "linux", Arch: "amd64", Shell: "builtin", Workspace: "/w", Home: "/h"}, nil
}

func (w *waitScript) JobWait(_ context.Context, req *workerv1.JobWaitRequest) (*workerv1.JobWaitResponse, error) {
	// A SHORT ARTIFICIAL SLEEP so an unbounded loop cannot spin: the real worker blocks for up to
	// timeout_ms, and a fake that returns instantly would make "it waited" indistinguishable from
	// "it gave up".
	time.Sleep(10 * time.Millisecond)
	i := w.calls
	if i >= len(w.states) {
		i = len(w.states) - 1
	}
	w.calls++
	state := w.states[i]
	code := int32(0)
	if state == "failed" || state == "killed" {
		code = 1
	}
	return &workerv1.JobWaitResponse{State: state, ExitCode: code}, nil
}

func (w *waitScript) JobOutput(_ context.Context, req *workerv1.JobOutputRequest) (*workerv1.JobOutputResponse, error) {
	w.lastOutputStart = req.GetStart()
	w.lastOutputEnd = req.GetEnd()
	// The worker's convention: a negative start counts back from the end.
	lines := w.outputLines
	if req.GetStart() < 0 && len(lines) > 0 {
		from := len(lines) + int(req.GetStart())
		if from < 0 {
			from = 0
		}
		lines = lines[from:]
	}
	return &workerv1.JobOutputResponse{Lines: lines, TotalLines: int32(len(w.outputLines))}, nil
}

func newWaitRegistry(t *testing.T, h workerv1connect.WorkerServiceHandler) map[string]Handler {
	t.Helper()
	_, url := newFakeServerWith(t, h)
	return newRegistry(t, url)
}

// 1. The job ends during the wait: return immediately, with everything it printed.
func TestJobWaitReturnsWhenTheJobEnds(t *testing.T) {
	h := &waitScript{
		states:      []string{"running", "running", "done"},
		outputLines: []string{"a", "b", "c"},
	}
	reg := newWaitRegistry(t, h)
	ctx := withGrants(context.Background(), PermExec)

	got, err := reg["sandbox-job-wait"](ctx,
		map[string]any{"sandbox": "pod-a", "job-id": "job-1", "timeout": 10},
		"c1", "s", "ten")
	if err != nil {
		t.Fatalf("job-wait failed: %v", err)
	}
	if !strings.Contains(got.Content, "done") {
		t.Errorf("the reply does not report the ending: %q", got.Content)
	}
	if !strings.Contains(got.Content, "a\nb\nc") && !strings.Contains(got.Content, "a") {
		t.Errorf("the reply is missing the output: %q", got.Content)
	}
	// A FINISHED JOB IS READ FROM THE START — the caller wants the result, not the tail.
	if h.lastOutputStart != 0 {
		t.Errorf("a finished job was read from offset %d, want the whole buffer", h.lastOutputStart)
	}
	if h.calls < 2 {
		t.Errorf("it stopped after %d poll(s); the job only ended on the third", h.calls)
	}
}

// 2. The budget expires: return the LAST 200 lines, and say it is still running.
func TestJobWaitReturnsTheTailWhenTheBudgetExpires(t *testing.T) {
	lines := make([]string, 500)
	for i := range lines {
		lines[i] = "line-" + itoa(i)
	}
	h := &waitScript{states: []string{"running"}, outputLines: lines}
	reg := newWaitRegistry(t, h)
	ctx := withGrants(context.Background(), PermExec)

	got, err := reg["sandbox-job-wait"](ctx,
		map[string]any{"sandbox": "pod-a", "job-id": "job-2", "timeout": 1},
		"c1", "s", "ten")
	if err != nil {
		t.Fatalf("a timeout must be an answer, not an error: %v", err)
	}
	if !strings.Contains(got.Content, "still running") {
		t.Errorf("the reply does not say it is still running: %q", got.Content)
	}
	// THE WINDOW IS THE POINT: asking for 0..200 would return the FIRST 200 lines of a job that has
	// printed 500 — the least useful part of a running build.
	if h.lastOutputStart != -200 {
		t.Fatalf("the tail read started at %d, want -200 (count back from the end)", h.lastOutputStart)
	}
	if strings.Contains(got.Content, "line-0\n") {
		t.Error("the reply contains the FIRST line, so it read the head rather than the tail")
	}
	if !strings.Contains(got.Content, "line-499") {
		t.Error("the reply is missing the most recent line")
	}
}

// 3. An unreachable worker is an error, not a fabricated state.
func TestJobWaitFailsWhenTheWorkerCannotAnswer(t *testing.T) {
	_, url := newFakeServerWith(t, &vanishAfterStart{})
	reg := newRegistry(t, url)
	ctx := withGrants(context.Background(), PermExec)

	got, err := reg["sandbox-job-wait"](ctx,
		map[string]any{"sandbox": "pod-a", "job-id": "job-3", "timeout": 1},
		"c1", "s", "ten")
	if err == nil {
		t.Fatalf("an unreachable worker was reported as a job result: %q", got.Content)
	}
	if strings.Contains(got.Content, "failed") {
		t.Errorf("the content claims a state the system does not know: %q", got.Content)
	}
}

// The tool is registered, because a caller that memorised its name must not get "no such tool".
func TestJobWaitIsRegistered(t *testing.T) {
	reg := newWaitRegistry(t, &waitScript{states: []string{"done"}})
	if _, ok := reg["sandbox-job-wait"]; !ok {
		t.Fatal("sandbox-job-wait is missing from the registry")
	}
}
