// Package e2e drives this extension over a REAL broker against a REAL easyworker sandbox.
//
// The AGENT IS NOT PART OF THIS. A tool call is a request on
// `easybus.<tenant>.tool.call.<extId>.<tool>`, so this test plays the agent's part with the SDK's
// own client. That is not a shortcut: `callTool` uses `Request`, which sets ReplyTo, and the
// extension SILENTLY IGNORES a call that arrives without one — a hand-rolled publish would look
// like a timeout naming nothing.
//
// Gated on E2E_NATS_URL: the unit suite has no services, and a test that fails for want of a broker
// teaches people to ignore red.
package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/easydev-org/easybus-sdk/agent"
	"github.com/easydev-org/easybus-sdk/extension"
	"github.com/easydev-org/easybus-sdk/manifest"
	"github.com/easydev-org/easybus-sdk/transport/nats"

	"github.com/easydev-org/easyagent-sandbox-ext/internal/sandbox"
	"github.com/easydev-org/easyagent-sandbox-ext/internal/tools"
)

const (
	extID   = "sandbox"
	tenant  = "e2e"
	session = "s-e2e"
)

func env(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

// harness wires the extension to a real broker and returns an agent-side client.
func harness(t *testing.T, sandboxURL string) (*agent.Agent, *extension.Extension) {
	t.Helper()
	natsURL := os.Getenv("E2E_NATS_URL")
	if natsURL == "" {
		t.Skip("E2E_NATS_URL not set")
	}
	if sandboxURL == "" {
		t.Skip("E2E_SANDBOX_URL not set")
	}

	raw, err := os.ReadFile("../../manifest.yaml")
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	m, err := manifest.ParseManifest(raw)
	if err != nil {
		t.Fatalf("parse manifest: %v", err)
	}

	extBus, err := nats.Connect(natsURL)
	if err != nil {
		t.Fatalf("connect ext: %v", err)
	}
	t.Cleanup(func() { _ = extBus.Close() })

	// The same wiring main.go performs, minus process concerns.
	cache := sandbox.NewClientCache(16)
	var ext *extension.Extension
	handlers := tools.Registry(tools.Deps{
		Cache: cache,
		Bus:   extBus,
		GetConfig: func(name, sess, tn string) any {
			if ext == nil {
				return nil
			}
			return ext.GetConfig(tn, name, sess)
		},
		Locale: func(sess, tn string) string { return "" },
	})
	specs := make(map[string]extension.ToolSpec, len(handlers))
	for name, h := range handlers {
		h := h
		specs[name] = extension.ToolSpec{
			Execute: func(ctx context.Context, args map[string]any, callID, sess, tn string) (extension.ToolResultData, error) {
				return h(ctx, args, callID, sess, tn)
			},
		}
	}
	cfg := m.BuildConfig(manifest.Bindings{Handlers: specs})
	ext = extension.New(extBus, cfg)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = ext.Serve(ctx) }()
	t.Cleanup(func() { _ = ext.Close() })

	agBus, err := nats.Connect(natsURL)
	if err != nil {
		t.Fatalf("connect agent: %v", err)
	}
	t.Cleanup(func() { _ = agBus.Close() })
	return agent.New(agBus), ext
}

// configure writes the `sandboxes` list the way the console does: a JSON value in the cfg KV under
// the extension's namespace. Every tool declares `required_config: [sandboxes]`, so without it each
// call is refused before the sandbox is contacted.
//
// Takes the AGENT-side bus, so the extension under test sees a write from "somebody else" — the
// same relationship a real console has to it.
func configure(t *testing.T, a *agent.Agent, sandboxURL string) {
	t.Helper()
	raw, err := json.Marshal([]map[string]string{
		{"name": "pod-a", "url": sandboxURL, "token": ""},
	})
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	b := a.Bus()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := b.KVPut(ctx, "cfg", fmt.Sprintf("t.%s.%s.sandboxes", tenant, extID), string(raw), 0); err != nil {
		t.Fatalf("put config: %v", err)
	}
}

// callTool calls a tool and returns content, failing on a tool error.
func callTool(t *testing.T, a *agent.Agent, tool string, args map[string]any) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	tr, err := a.CallTool(ctx, tenant, session, extID, tool, fmt.Sprintf("c-%s-%d", tool, time.Now().UnixNano()), args)
	if err != nil {
		t.Fatalf("%s: %v", tool, err)
	}
	if tr.Error != nil {
		t.Fatalf("%s: %s", tool, tr.Error.Message)
	}
	return tr.Content
}

func ctxWithTimeout(t *testing.T, seconds int) (context.Context, context.CancelFunc) {
	t.Helper()
	return context.WithTimeout(context.Background(), time.Duration(seconds)*time.Second)
}

func contains(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}

// nowNonce makes each run's content distinct, so a read answering from a cache or a hard-coded value
// cannot pass.
func nowNonce() string {
	return fmt.Sprintf("%d", time.Now().UnixNano())
}

// waitConfigured waits for the config write to reach the extension.
//
// The watch is asynchronous, so a tool call issued right after the KV put races it. That race looks
// exactly like "the config never arrived", which is the failure this suite exists to catch — so it
// must not be the reason the suite fails.
func waitConfigured(t *testing.T, a *agent.Agent) error {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		ctx, cancel := ctxWithTimeout(t, 10)
		tr, err := a.CallTool(ctx, tenant, session, extID, "list-sandboxes", fmt.Sprintf("c-probe-%d", time.Now().UnixNano()), map[string]any{})
		cancel()
		if err == nil && tr.Error == nil {
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("config never took effect: list-sandboxes still refused after 20s")
}
