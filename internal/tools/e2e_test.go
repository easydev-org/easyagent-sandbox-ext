package tools

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"

	workerv1 "github.com/easydev-org/easyagent-sandbox-ext/gen/worker/v1"
	"github.com/easydev-org/easyagent-sandbox-ext/gen/worker/v1/workerv1connect"
	"github.com/easydev-org/easyagent-sandbox-ext/internal/sandbox"
	easybus "github.com/easydev-org/easybus-sdk"
	"github.com/easydev-org/easybus-sdk/extension"
)

// fakeWorker is a minimal in-process WorkerService that records the last
// command and returns canned info/job results.
type fakeWorker struct {
	workerv1connect.UnimplementedWorkerServiceHandler
	lastCommand string
	jobID       string
}

func (f *fakeWorker) Info(context.Context, *workerv1.InfoRequest) (*workerv1.InfoResponse, error) {
	return &workerv1.InfoResponse{Os: "linux", Arch: "amd64", Shell: "builtin", Workspace: "/work", Home: "/home/u"}, nil
}
func (f *fakeWorker) Execute(_ context.Context, req *workerv1.ExecuteRequest) (*workerv1.ExecuteResponse, error) {
	f.lastCommand = req.GetCommand()
	return &workerv1.ExecuteResponse{JobId: "job-1"}, nil
}
func (f *fakeWorker) JobWait(context.Context, *workerv1.JobWaitRequest) (*workerv1.JobWaitResponse, error) {
	return &workerv1.JobWaitResponse{State: "done", ExitCode: 0}, nil
}
func (f *fakeWorker) JobOutput(context.Context, *workerv1.JobOutputRequest) (*workerv1.JobOutputResponse, error) {
	return &workerv1.JobOutputResponse{Lines: []string{"hello", "world"}, TotalLines: 2, Done: true}, nil
}

func newFakeServer(t *testing.T) (*fakeWorker, string) {
	t.Helper()
	f := &fakeWorker{}
	srv := connect.NewServer()
	workerv1connect.RegisterWorkerServiceHandler(srv, f)
	mux := http.NewServeMux()
	connecthttp.Mount(mux, srv, connecthttp.WithReadMaxBytes(0))
	ts := httptest.NewUnstartedServer(mux)
	proto := new(http.Protocols)
	proto.SetHTTP1(true)
	ts.Config.Protocols = proto
	ts.Start()
	t.Cleanup(ts.Close)
	return f, ts.URL
}

func newRegistry(t *testing.T, url string) map[string]Handler {
	t.Helper()
	return Registry(Deps{
		Cache:  sandbox.NewClientCache(4),
		Locale: func(string, string) string { return "en" },
		GetConfig: func(name, _, _ string) any {
			if name == "sandboxes" {
				return `[{"name":"pod-a","url":"` + url + `","token":"t"}]`
			}
			return ""
		},
	})
}

// withGrants puts the authorisations an agent would send onto the context.
//
// The write tools are permission-gated, so a test that calls them without grants is testing
// the refusal path, not the behaviour it means to exercise. Named explicitly rather than
// granting "*" so a test that forgets to state what it needs fails loudly.
func withGrants(ctx context.Context, keys ...string) context.Context {
	grants := make([]*easybus.Grant, 0, len(keys))
	for _, k := range keys {
		grants = append(grants, &easybus.Grant{
			Key: k,
			// pod-a is the sandbox the fixtures configure.
			Params: easybus.Struct(map[string]any{"sandbox": "pod-a"}),
		})
	}
	return extension.WithGrants(ctx, grants)
}

func TestExecRunsOnSandbox(t *testing.T) {
	f, url := newFakeServer(t)
	reg := newRegistry(t, url)
	// Executing a command needs `exec`: the tool refuses without it, which is the point.
	ctx := withGrants(context.Background(), PermExec)
	res, err := reg["sandbox-job-start"](ctx, map[string]any{"command": "echo hi", "lang": "bash"}, "", "s", "ten")
	if err != nil {
		t.Fatal(err)
	}
	if f.lastCommand != "echo hi" {
		t.Errorf("worker got %q", f.lastCommand)
	}
	if res.Content == "" {
		t.Error("empty content")
	}
}

func TestSandboxSelection(t *testing.T) {
	_, url := newFakeServer(t)
	reg := newRegistry(t, url)
	// An unknown sandbox name is rejected.
	if _, err := reg["sandbox-info"](context.Background(), map[string]any{"sandbox": "nope"}, "", "s", "ten"); err == nil {
		t.Error("unknown sandbox should error")
	}
	// The configured name works.
	if _, err := reg["sandbox-info"](context.Background(), map[string]any{"sandbox": "pod-a"}, "", "s", "ten"); err != nil {
		t.Fatal(err)
	}
}

func TestNotConfigured(t *testing.T) {
	reg := Registry(Deps{Cache: sandbox.NewClientCache(1), GetConfig: func(string, string, string) any { return "" }})
	if _, err := reg["sandbox-info"](context.Background(), map[string]any{}, "", "s", "ten"); err == nil {
		t.Error("unconfigured should error")
	}
}

func TestListSandboxes(t *testing.T) {
	_, url := newFakeServer(t)
	reg := newRegistry(t, url)
	res, err := reg["list-sandboxes"](context.Background(), map[string]any{}, "", "s", "ten")
	if err != nil {
		t.Fatal(err)
	}
	if res.Content == "" || res.Data == nil {
		t.Fatal("empty result")
	}
}
