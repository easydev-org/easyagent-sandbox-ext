package tools

import (
	"context"
	"strings"
	"testing"

	workerv1 "github.com/easydev-org/easyagent-sandbox-ext/gen/worker/v1"
	"github.com/easydev-org/easyagent-sandbox-ext/gen/worker/v1/workerv1connect"
)

/**
 * CANCELLATION IS SYNCHRONOUS, AND THAT IS THE POINT.
 *
 * The worker owns the process and answers JobKill with a result or an error, so a caller learns
 * whether the kill landed. Routing this through the job list as an intent — the shape you reach for
 * when the callee has no direct channel — would turn a reliable call into a request nobody confirms.
 */

// killRecorder records what the tool asked the worker to do.
type killRecorder struct {
	workerv1connect.UnimplementedWorkerServiceHandler
	killed []string
	err    error
}

func (k *killRecorder) Info(context.Context, *workerv1.InfoRequest) (*workerv1.InfoResponse, error) {
	return &workerv1.InfoResponse{Os: "linux", Arch: "amd64", Shell: "builtin", Workspace: "/w", Home: "/h"}, nil
}

func (k *killRecorder) JobKill(_ context.Context, req *workerv1.JobKillRequest) (*workerv1.JobKillResponse, error) {
	if k.err != nil {
		return nil, k.err
	}
	k.killed = append(k.killed, req.GetJobId())
	return &workerv1.JobKillResponse{}, nil
}

func TestJobCancelAsksTheWorkerToKill(t *testing.T) {
	rec := &killRecorder{}
	_, url := newFakeServerWith(t, rec)
	reg := newRegistry(t, url)
	ctx := withGrants(context.Background(), PermExec)

	out, err := reg["sandbox-job-cancel"](ctx,
		map[string]any{"sandbox": "pod-a", "job-id": "job-1"}, "c1", "s", "ten")
	if err != nil {
		t.Fatalf("cancel failed: %v", err)
	}
	// THE WORKER WAS ACTUALLY ASKED. A tool that reported success without reaching the process would
	// make a reader believe a runaway command had been stopped.
	if len(rec.killed) != 1 || rec.killed[0] != "job-1" {
		t.Fatalf("the worker was asked to kill %v, want [job-1]", rec.killed)
	}
	if !strings.Contains(out.Content, "job-1") {
		t.Fatalf("the reply does not name the job: %q", out.Content)
	}
}

/**
 * A FAILED CANCEL IS AN ERROR, NOT A QUIET SUCCESS.
 *
 * The process is still running; reporting that it was cancelled would leave the shared job list
 * disagreeing with the machine, and the reader with no reason to try again.
 */
func TestJobCancelReportsAFailedKill(t *testing.T) {
	rec := &killRecorder{err: context.DeadlineExceeded}
	_, url := newFakeServerWith(t, rec)
	reg := newRegistry(t, url)
	ctx := withGrants(context.Background(), PermExec)

	_, err := reg["sandbox-job-cancel"](ctx,
		map[string]any{"sandbox": "pod-a", "job-id": "job-1"}, "c1", "s", "ten")
	if err == nil {
		t.Fatal("a kill the worker refused must be reported, not swallowed")
	}
}

func TestJobCancelRequiresAnId(t *testing.T) {
	rec := &killRecorder{}
	_, url := newFakeServerWith(t, rec)
	reg := newRegistry(t, url)
	ctx := withGrants(context.Background(), PermExec)

	_, err := reg["sandbox-job-cancel"](ctx, map[string]any{"sandbox": "pod-a"}, "c1", "s", "ten")
	if err == nil {
		t.Fatal("an id is required")
	}
	if len(rec.killed) != 0 {
		t.Fatalf("an id-less call still reached the worker: %v", rec.killed)
	}
}

// THE OLD NAME IS GONE, so a caller that memorised it gets a clear miss instead of a tool that
// stopped existing between one prompt and the next.
func TestTheKillToolIsNamedCancel(t *testing.T) {
	rec := &killRecorder{}
	_, url := newFakeServerWith(t, rec)
	reg := newRegistry(t, url)
	if _, ok := reg["sandbox-job-cancel"]; !ok {
		t.Fatal("sandbox-job-cancel is missing")
	}
	if _, ok := reg["sandbox-job-kill"]; ok {
		t.Fatal("sandbox-job-kill still exists; the rename was incomplete")
	}
}
