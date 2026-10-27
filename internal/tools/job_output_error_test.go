package tools

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"connectrpc.com/connect/v2/connecthttp"

	workerv1 "github.com/easydev-org/easyagent-sandbox-ext/gen/worker/v1"
	"github.com/easydev-org/easyagent-sandbox-ext/gen/worker/v1/workerv1connect"
)

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
// The tool still ANSWERS: the job's state and exit code are real information the worker did give
// us, and failing the call would trade one missing fact for another.
func TestJobWaitReportsOutputReadFailure(t *testing.T) {
	_, url := newFakeServerWith(t, &outputFailingWorker{})
	reg := newRegistry(t, url)
	ctx := withGrants(context.Background(), PermExec)

	got, err := reg["sandbox-job-wait"](ctx,
		map[string]any{"sandbox": "pod-a", "job-id": "job-1", "timeout": float64(5)}, "c1", "s", "ten")
	if err != nil {
		t.Fatalf("job-wait returned an error instead of a result: %v", err)
	}
	if !strings.Contains(got.Content, "job-1") {
		t.Fatalf("result does not name the job: %q", got.Content)
	}
	if got.Data["state"] == nil {
		t.Fatalf("result carries no state: %+v", got.Data)
	}
	if !strings.Contains(strings.ToLower(got.Content), "output unavailable") {
		t.Fatalf("result does not report that the output could not be read: %q\n"+
			"an empty body beside a 'done' line reads as 'the command printed nothing'", got.Content)
	}
	if got.Data["outputError"] == nil {
		t.Fatalf("result carries no outputError for a caller to branch on: %+v", got.Data)
	}
}

// The mirror case: a job that genuinely printed nothing must NOT look like a read failure.
//
// Without this the new signal would be worthless — every quiet command would report a problem.
func TestJobWaitSilentJobIsNotAnError(t *testing.T) {
	_, url := newFakeServerWith(t, &silentWorker{})
	reg := newRegistry(t, url)
	ctx := withGrants(context.Background(), PermExec)

	got, err := reg["sandbox-job-wait"](ctx,
		map[string]any{"sandbox": "pod-a", "job-id": "job-2", "timeout": float64(5)}, "c2", "s", "ten")
	if err != nil {
		t.Fatalf("job-wait failed: %v", err)
	}
	if strings.Contains(strings.ToLower(got.Content), "output unavailable") {
		t.Fatalf("a job that printed nothing was reported as a read failure: %q", got.Content)
	}
	if got.Data["outputError"] != nil {
		t.Fatalf("a silent job carries an outputError: %+v", got.Data)
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
	return nil, connect.NewError(connect.CodeUnavailable, errors.New("worker went away"))
}
func (f *outputFailingWorker) JobGraph(context.Context, *workerv1.JobGraphRequest) (*workerv1.JobGraphResponse, error) {
	return &workerv1.JobGraphResponse{}, nil
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
func (f *silentWorker) JobGraph(context.Context, *workerv1.JobGraphRequest) (*workerv1.JobGraphResponse, error) {
	return &workerv1.JobGraphResponse{}, nil
}
