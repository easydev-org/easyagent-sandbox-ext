package tools

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	"github.com/easydev-org/easybus-sdk/bus"
	"github.com/easydev-org/easybus-sdk/natsrun"
	"github.com/easydev-org/easybus-sdk/protocol"
	"github.com/easydev-org/easybus-sdk/transport/nats"

	workerv1 "github.com/easydev-org/easyagent-sandbox-ext/gen/worker/v1"
	"github.com/easydev-org/easyagent-sandbox-ext/gen/worker/v1/workerv1connect"
	"github.com/easydev-org/easyagent-sandbox-ext/internal/sandbox"
)

// fileWorker is a fake worker that stores one file in memory, so download/upload
// can round-trip through the agent file RPC.
type fileWorker struct {
	workerv1connect.UnimplementedWorkerServiceHandler
	files map[string][]byte
}

func (f *fileWorker) FileRead(_ context.Context, req *workerv1.FileReadRequest) (*workerv1.FileReadResponse, error) {
	b, ok := f.files[req.GetPath()]
	if !ok {
		return nil, connect.NewError(connect.CodeNotFound, "no file")
	}
	return &workerv1.FileReadResponse{Content: b, TotalLines: 1}, nil
}
func (f *fileWorker) FileWrite(_ context.Context, req *workerv1.FileWriteRequest) (*workerv1.FileWriteResponse, error) {
	f.files[req.GetPath()] = req.GetContent()
	return &workerv1.FileWriteResponse{Ok: true}, nil
}

// fakeAgent answers the file.ingest / file.get RPCs by persisting to the object
// store (a stand-in for the real agent's ServeFileRPC).
func fakeAgent(t *testing.T, url string) *nats.Bus {
	t.Helper()
	b, err := nats.Connect(url)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	ing, _ := b.Subscribe(ctx, protocol.FileIngestWildcard, bus.SubscribeOpts{})
	go func() {
		for {
			env, ok := ing.Next(ctx)
			if !ok {
				return
			}
			obj, _ := b.ObjectGet(ctx, protocol.TenantObjectName(env.Tenant, "obj"))
			_ = obj
			var req protocol.FileIngestRequest
			_ = protocol.Coerce(env.Payload, &req)
			data, _ := b.ObjectGet(ctx, protocol.TenantObjectName(env.Tenant, req.Object))
			code := "code-" + req.Name
			_ = b.ObjectPut(ctx, protocol.TenantObjectName(env.Tenant, code), data)
			if env.ReplyTo != nil {
				_ = b.Publish(ctx, *env.ReplyTo, map[string]any{"ok": true, "code": code, "mime": "text/plain"}, bus.PublishOpts{Tenant: env.Tenant})
			}
		}
	}()
	get, _ := b.Subscribe(ctx, protocol.FileGetWildcard, bus.SubscribeOpts{})
	go func() {
		for {
			env, ok := get.Next(ctx)
			if !ok {
				return
			}
			var req protocol.FileGetRequest
			_ = protocol.Coerce(env.Payload, &req)
			data, _ := b.ObjectGet(ctx, protocol.TenantObjectName(env.Tenant, req.Code))
			obj := "get-" + req.Code
			_ = b.ObjectPut(ctx, protocol.TenantObjectName(env.Tenant, obj), data)
			if env.ReplyTo != nil {
				_ = b.Publish(ctx, *env.ReplyTo, map[string]any{"ok": true, "meta": map[string]any{"code": req.Code, "name": "x.txt"}, "object": obj}, bus.PublishOpts{Tenant: env.Tenant})
			}
		}
	}()
	return b
}

func TestDownloadUploadRoundTrip(t *testing.T) {
	srv, err := natsrun.Start(natsrun.Config{Port: 0})
	if err != nil {
		t.Skipf("nats-server unavailable: %v", err)
	}
	t.Cleanup(func() { _ = srv.Stop() })

	// A fake worker holding one file.
	fw := &fileWorker{files: map[string][]byte{"/w/hello.txt": []byte("hello-bytes")}}
	csrv := connect.NewServer()
	workerv1connect.RegisterWorkerServiceHandler(csrv, fw)
	mux := http.NewServeMux()
	connecthttp.Mount(mux, csrv, connecthttp.WithReadMaxBytes(0))
	ts := httptest.NewUnstartedServer(mux)
	p := new(http.Protocols)
	p.SetHTTP1(true)
	ts.Config.Protocols = p
	ts.Start()
	t.Cleanup(ts.Close)

	agent := fakeAgent(t, srv.URL())
	t.Cleanup(func() { _ = agent.Close() })

	// The extension's own bus connection.
	eb, err := nats.Connect(srv.URL())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = eb.Close() })

	r := Registry(Deps{
		Cache:  sandbox.NewClientCache(4),
		Bus:    eb,
		Locale: func(string, string) string { return "en" },
		GetConfig: func(name, _, _ string) any {
			if name == "sandboxes" {
				return `[{"name":"pod-a","url":"` + ts.URL + `","token":""}]`
			}
			return ""
		},
	})

	ctx := context.Background()
	// download: sandbox file -> agent file:<code>
	res, err := r["sandbox-download"](ctx, map[string]any{"sandbox": "pod-a", "path": "/w/hello.txt"}, "call-1", "s", "ten")
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	code, _ := res.Data.(map[string]any)["code"].(string)
	if code == "" {
		t.Fatalf("no code: %+v", res.Data)
	}
	// upload: agent file:<code> -> a new sandbox path
	if _, err := r["sandbox-upload"](ctx, map[string]any{"sandbox": "pod-a", "code": code, "path": "/w/copy.txt"}, "call-2", "s", "ten"); err != nil {
		t.Fatalf("upload: %v", err)
	}
	if string(fw.files["/w/copy.txt"]) != "hello-bytes" {
		t.Errorf("uploaded bytes = %q", fw.files["/w/copy.txt"])
	}
}
