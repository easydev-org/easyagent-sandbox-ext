package tools

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"

	workerv1 "github.com/easydev-org/easyagent-sandbox-ext/gen/worker/v1"
	"github.com/easydev-org/easyagent-sandbox-ext/gen/worker/v1/workerv1connect"
)

// dataOf reads the result's Data as a map.
//
// `ToolResultData.Data` is `any` — the wire type is deliberately open — so a test has to say what it
// expects instead of indexing an interface.
func dataOf(t *testing.T, d any) map[string]any {
	t.Helper()
	m, ok := d.(map[string]any)
	if !ok {
		t.Fatalf("result Data is %T, want map[string]any", d)
	}
	return m
}

// newFakeServerWith serves an arbitrary worker handler.
//
// The existing newFakeServer hard-codes the healthy fake; these tests need one whose OUTPUT read
// fails, which is the whole point — a worker that cannot answer must not be reported as a job that
// printed nothing.
func newFakeServerWith(t *testing.T, h workerv1connect.WorkerServiceHandler) (*httptest.Server, string) {
	t.Helper()
	srv := connect.NewServer()
	workerv1connect.RegisterWorkerServiceHandler(srv, h)
	mux := http.NewServeMux()
	connecthttp.Mount(mux, srv, connecthttp.WithReadMaxBytes(0))
	ts := httptest.NewUnstartedServer(mux)
	proto := new(http.Protocols)
	proto.SetHTTP1(true)
	ts.Config.Protocols = proto
	ts.Start()
	t.Cleanup(ts.Close)
	return ts, ts.URL
}

// A failed OUTPUT read must be visible, not rendered as an empty body.
//
// The distinction is what a reader concludes. `job-wait` on a finished job prints its output and a
// state line; when the output could not be fetched the old code printed the state line and NOTHING
// else, which reads as "this command printed nothing". A wrong answer that looks like a result is
// the most expensive kind, so the failure has to appear where the output would have been.
//
/**
 * reading output when the reader cannot is a FAILURE, and that is different from waiting.
 *
 * `sandbox-job-wait` used to answer with an empty body and a note, because it had a second thing to
 * report — the state and exit code it had already been given. `sandbox-job-output` has no second
 * thing: its whole purpose is the bytes, so failing to fetch them is the answer, and an empty body
 * here would be read as "the command printed nothing".
 *
 * WAITING ITSELF IS NO LONGER THIS EXTENSION'S TOOL. It is `job-wait` in bundled, which reads the
 * shared job list and returns no output at all — so the rule it used to carry has no home here.
 */
func TestJobOutputFailsWhenTheWorkerCannotAnswer(t *testing.T) {
	_, url := newFakeServerWith(t, &outputFailingWorker{})
	reg := newRegistry(t, url)
	ctx := withGrants(context.Background(), PermExec)

	_, err := reg["sandbox-job-output"](ctx,
		map[string]any{"sandbox": "pod-a", "job-id": "job-1"}, "c1", "s", "ten")
	if err == nil {
		t.Fatal("a failed output read must be an error, not an empty body that reads as silence")
	}
	if !strings.Contains(err.Error(), "job-1") && !strings.Contains(err.Error(), "output") {
		t.Fatalf("the error does not say what failed: %v", err)
	}
}

// The mirror case: a job that genuinely printed nothing must NOT look like a read failure.
//
// Without this the new signal would be worthless — every quiet command would report a problem.
func TestJobOutputSilentJobIsNotAnError(t *testing.T) {
	_, url := newFakeServerWith(t, &silentWorker{})
	reg := newRegistry(t, url)
	ctx := withGrants(context.Background(), PermExec)

	got, err := reg["sandbox-job-output"](ctx,
		map[string]any{"sandbox": "pod-a", "job-id": "job-2"}, "c2", "s", "ten")
	if err != nil {
		t.Fatalf("job-output failed: %v", err)
	}
	if strings.Contains(strings.ToLower(got.Content), "output unavailable") {
		t.Fatalf("a job that printed nothing was reported as a read failure: %q", got.Content)
	}
}

// outputFailingWorker finishes the job but refuses to return its output.
type outputFailingWorker struct {
	workerv1connect.UnimplementedWorkerServiceHandler
}

func (f *outputFailingWorker) Info(context.Context, *workerv1.InfoRequest) (*workerv1.InfoResponse, error) {
	return &workerv1.InfoResponse{Os: "linux", Arch: "amd64", Shell: "builtin", Workspace: "/w", Home: "/h"}, nil
}
func (f *outputFailingWorker) JobWait(context.Context, *workerv1.JobWaitRequest) (*workerv1.JobWaitResponse, error) {
	return &workerv1.JobWaitResponse{State: "done", ExitCode: 0}, nil
}
func (f *outputFailingWorker) JobOutput(context.Context, *workerv1.JobOutputRequest) (*workerv1.JobOutputResponse, error) {
	// v2 takes a STRING message, not an error (see transfer_test.go): `connect.NewError(code, string)`.
	return nil, connect.NewError(connect.CodeUnavailable, "worker went away")
}

// silentWorker finishes successfully with no output at all.
type silentWorker struct {
	workerv1connect.UnimplementedWorkerServiceHandler
}

func (f *silentWorker) Info(context.Context, *workerv1.InfoRequest) (*workerv1.InfoResponse, error) {
	return &workerv1.InfoResponse{Os: "linux", Arch: "amd64", Shell: "builtin", Workspace: "/w", Home: "/h"}, nil
}
func (f *silentWorker) JobWait(context.Context, *workerv1.JobWaitRequest) (*workerv1.JobWaitResponse, error) {
	return &workerv1.JobWaitResponse{State: "done", ExitCode: 0}, nil
}
func (f *silentWorker) JobOutput(context.Context, *workerv1.JobOutputRequest) (*workerv1.JobOutputResponse, error) {
	return &workerv1.JobOutputResponse{Lines: nil, TotalLines: 0, Done: true}, nil
}
