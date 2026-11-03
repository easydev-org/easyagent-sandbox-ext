package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect/v2"
	workerv1 "github.com/easydev-org/easyagent-sandbox-ext/gen/worker/v1"
	"github.com/easydev-org/easyagent-sandbox-ext/gen/worker/v1/workerv1connect"
	"github.com/easydev-org/easyagent-sandbox-ext/internal/sandbox"
	"github.com/easydev-org/easybus-sdk/bus"
	"github.com/easydev-org/easybus-sdk/job"
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

/**
 * CANCELLATION WRITES THE SHARED LIST, not just the worker.
 *
 * The panel lists jobs from the registry, so a cancel that only reaches the worker leaves the job
 * showing "running" until the follower notices — and if the follower has already returned (the worker
 * was unreachable), never. The old tests never assembled a registry, so this half had no coverage at
 * all and the state was silently reverted by the heartbeat.
 */
func TestJobCancelRecordsTheCancellationInTheJobList(t *testing.T) {
	rec := &vanishAfterStart{}
	_, url := newFakeServerWith(t, rec)

	kv := &recordingKV{val: `{"id":"sandbox-job.job-1","kind":"sandbox-job","state":"running",` +
		`"tenant":"ten","session":"s","extId":"sandbox","cancelMode":"kill"}`}
	jobs := job.NewRegistry(kv)

	reg := Registry(Deps{
		Cache:  sandbox.NewClientCache(4),
		Jobs:   jobs,
		Locale: func(string, string) string { return "en" },
		GetConfig: func(name, _, _ string) any {
			if name == "sandboxes" {
				return `[{"name":"pod-a","url":"` + url + `","token":"t"}]`
			}
			return ""
		},
	})
	ctx := withGrants(context.Background(), PermExec)

	if _, err := reg["sandbox-job-cancel"](ctx,
		map[string]any{"sandbox": "pod-a", "job-id": "job-1"}, "c1", "s", "ten"); err != nil {
		t.Fatalf("cancel failed: %v", err)
	}

	var got job.Entry
	if err := json.Unmarshal([]byte(kv.val), &got); err != nil {
		t.Fatalf("the entry is not readable: %v (%q)", err, kv.val)
	}
	if got.State != job.StateCancelled {
		t.Fatalf("the list still says %q; a panel would show it running", got.State)
	}
	if got.FinishedAt == 0 {
		t.Error("a cancelled job has no finish time")
	}
	// THE ID IN THE LIST CARRIES THE EXTENSION'S PREFIX. A caller passes the bare worker id, so the
	// lookup has to rebuild it; if it did not, this write would land on a key nobody reads.
	if got.ID != "sandbox-job.job-1" {
		t.Errorf("the entry id lost its prefix: %q", got.ID)
	}
}

// A FAILED KILL MUST NOT TOUCH THE LIST. The process is still running, and recording "cancelled"
// would make the list disagree with the machine.
func TestJobCancelLeavesTheListAloneWhenTheKillFails(t *testing.T) {
	kv := &recordingKV{val: `{"id":"sandbox-job.job-1","kind":"sandbox-job","state":"running",` +
		`"tenant":"ten","session":"s","extId":"sandbox","cancelMode":"kill"}`}
	jobs := job.NewRegistry(kv)

	_, url := newFakeServerWith(t, &killFails{})
	reg := Registry(Deps{
		Cache:  sandbox.NewClientCache(4),
		Jobs:   jobs,
		Locale: func(string, string) string { return "en" },
		GetConfig: func(name, _, _ string) any {
			if name == "sandboxes" {
				return `[{"name":"pod-a","url":"` + url + `","token":"t"}]`
			}
			return ""
		},
	})
	ctx := withGrants(context.Background(), PermExec)

	before := kv.val
	if _, err := reg["sandbox-job-cancel"](ctx,
		map[string]any{"sandbox": "pod-a", "job-id": "job-1"}, "c1", "s", "ten"); err == nil {
		t.Fatal("a refused kill was reported as success")
	}
	if kv.val != before {
		t.Fatalf("the list changed although the process is still running:\n before %s\n after  %s",
			before, kv.val)
	}
}

type killFails struct {
	workerv1connect.UnimplementedWorkerServiceHandler
}

func (k *killFails) JobKill(context.Context, *workerv1.JobKillRequest) (*workerv1.JobKillResponse, error) {
	return nil, connect.NewError(connect.CodeUnavailable, "cannot reach the worker")
}

// recordingKV remembers one value, so a test can read what the registry wrote.
type recordingKV struct {
	bus.Bus
	val string
}

func (r *recordingKV) KVPut(_ context.Context, _, _, value string, _ int64) error {
	r.val = value
	return nil
}

func (r *recordingKV) KVGet(context.Context, string, string) (string, error) {
	return r.val, nil
}
