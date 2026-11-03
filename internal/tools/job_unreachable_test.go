package tools

import (
	"context"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect/v2"
	workerv1 "github.com/easydev-org/easyagent-sandbox-ext/gen/worker/v1"
	"github.com/easydev-org/easyagent-sandbox-ext/gen/worker/v1/workerv1connect"
	"github.com/easydev-org/easyagent-sandbox-ext/internal/sandbox"
)

/**
 * WHAT HAPPENS WHEN THE WORKER STOPS ANSWERING.
 *
 * The job may be running perfectly well on a machine this extension can no longer reach. Reporting a
 * job state for that invents a result — and the old code did exactly that: it returned the literal
 * `"failed"`, which is not one of the four states, so the tool result said "failed" while the
 * follower reading the same value mapped it to `done`. Two readers, two answers, neither of them true.
 */

// vanishAfterStart accepts the execute and then never answers again.
type vanishAfterStart struct {
	workerv1connect.UnimplementedWorkerServiceHandler
	killed []string
}

func (v *vanishAfterStart) Execute(context.Context, *workerv1.ExecuteRequest) (*workerv1.ExecuteResponse, error) {
	return &workerv1.ExecuteResponse{JobId: "job-x"}, nil
}

func (v *vanishAfterStart) JobWait(context.Context, *workerv1.JobWaitRequest) (*workerv1.JobWaitResponse, error) {
	return nil, connect.NewError(connect.CodeUnavailable, "worker went away")
}

func (v *vanishAfterStart) JobOutput(context.Context, *workerv1.JobOutputRequest) (*workerv1.JobOutputResponse, error) {
	return nil, connect.NewError(connect.CodeUnavailable, "worker went away")
}

func (v *vanishAfterStart) JobKill(_ context.Context, req *workerv1.JobKillRequest) (*workerv1.JobKillResponse, error) {
	v.killed = append(v.killed, req.GetJobId())
	return &workerv1.JobKillResponse{}, nil
}

// A JOB WHOSE OUTCOME IS UNKNOWN IS AN ERROR, NOT A RESULT.
//
// The distinction matters to the model: "failed" is a fact it can act on (the command ran and failed),
// while "we lost contact" means the command may still be going. Telling it the former when the latter
// is true is how a running build gets abandoned, or a second copy started.
func TestUnreachableWorkerIsNotReportedAsAFailedJob(t *testing.T) {
	rec := &vanishAfterStart{}
	_, url := newFakeServerWith(t, rec)
	reg := newRegistry(t, url)
	ctx := withGrants(context.Background(), PermExec)

	got, err := reg["sandbox-job-start"](ctx,
		map[string]any{"sandbox": "pod-a", "command": "make", "lang": "bash", "timeout": 1},
		"c1", "s", "ten")

	if err == nil {
		t.Fatalf("an unreachable worker was reported as a job result: content=%q data=%#v",
			got.Content, got.Data)
	}
	if strings.Contains(got.Content, "failed") {
		t.Fatalf("the content claims a state the system does not know: %q", got.Content)
	}
	// THE ERROR NAMES THE JOB AND SAYS WHAT IS ACTUALLY KNOWN. An error that only says "unavailable"
	// leaves the caller unable to look the job up later.
	msg := err.Error()
	for _, want := range []string{"job-x", "unknown"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the error does not mention %q: %s", want, msg)
		}
	}
}

// And the follower records nothing when it cannot ask.
func TestFollowerWaitsForTheSweeperWhenTheWorkerVanishes(t *testing.T) {
	wc := sandbox.Dial(sandbox.Endpoint{URL: mustDeadSettle(t), Token: "t"})
	state, code, ok := waitForJob(context.Background(), wc, "job-y", 2*time.Second)
	if ok {
		t.Fatalf("waitForJob claimed to know the outcome: state=%q code=%d", state, code)
	}
	if state != "" || code != 0 {
		t.Fatalf("waitForJob returned invented values alongside ok=false: %q %d", state, code)
	}
}

// mustDeadSettle serves JobWait that always fails.
func mustDeadSettle(t *testing.T) string {
	t.Helper()
	_, url := newFakeServerWith(t, &vanishAfterStart{})
	return url
}

type killFails struct {
	workerv1connect.UnimplementedWorkerServiceHandler
}

func (k *killFails) JobKill(context.Context, *workerv1.JobKillRequest) (*workerv1.JobKillResponse, error) {
	return nil, connect.NewError(connect.CodeUnavailable, "cannot reach the worker")
}
