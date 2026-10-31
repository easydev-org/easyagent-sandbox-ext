package tools

import (
	"context"
	"strings"
	"testing"

	easybus "github.com/easydev-org/easybus-sdk"
	"github.com/easydev-org/easybus-sdk/extension"
)

// The writing tools must refuse a call the session is not authorised for.
//
// Worth asserting even though the mechanism is shared with gitea-ext: the TARGET here is a
// sandbox name resolved from the arguments, and a mistake in resolving it (or in passing it
// to Require) would leave the gate open while looking correct.

func TestExecRequiresGrant(t *testing.T) {
	f, url := newFakeServer(t)
	reg := newRegistry(t, url)

	_, err := reg["sandbox-job-start"](context.Background(),
		map[string]any{"command": "rm -rf /"}, "", "s", "ten")
	if err == nil {
		t.Fatal("running a command without a grant must be refused")
	}
	if !strings.Contains(err.Error(), "permission denied") {
		t.Errorf("err = %v, want a permission denial", err)
	}
	// The command must not have reached the worker: a gate that runs first and refuses after
	// is not a gate.
	if f.lastCommand != "" {
		t.Errorf("the command reached the sandbox anyway: %q", f.lastCommand)
	}
}

func TestExecGrantForAnotherSandboxIsRefused(t *testing.T) {
	f, url := newFakeServer(t)
	reg := newRegistry(t, url)

	// A grant scoped to a different sandbox must not authorise this one — that is the whole
	// point of putting the sandbox name in the params.
	ctx := extension.WithGrants(context.Background(), []*easybus.Grant{{
		Key:    PermExec,
		Params: easybus.Struct(map[string]any{"sandbox": "other-pod"}),
	}})
	_, err := reg["sandbox-job-start"](ctx,
		map[string]any{"command": "echo hi", "lang": "bash"}, "", "s", "ten")
	if err == nil {
		t.Fatal("a grant for a different sandbox must not authorise this one")
	}
	if !strings.Contains(err.Error(), "pod-a") || !strings.Contains(err.Error(), "other-pod") {
		t.Errorf("err = %v, want it to name both sandboxes", err)
	}
	if f.lastCommand != "" {
		t.Errorf("the command reached the sandbox anyway: %q", f.lastCommand)
	}
}

func TestFileWriteRequiresItsOwnGrant(t *testing.T) {
	_, url := newFakeServer(t)
	reg := newRegistry(t, url)

	// Holding `exec` says nothing about writing files: they are separate keys so a session
	// can be allowed to run commands without being allowed to replace the files it runs.
	ctx := withGrants(context.Background(), PermExec)
	_, err := reg["sandbox-file-write"](ctx,
		map[string]any{"path": "/w/x", "content": "y"}, "", "s", "ten")
	if err == nil {
		t.Fatal("file write with only an exec grant must be refused")
	}
	if !strings.Contains(err.Error(), "file.write") {
		t.Errorf("err = %v, want it to name file.write", err)
	}
}

func TestReadToolsNeedNoGrant(t *testing.T) {
	_, url := newFakeServer(t)
	reg := newRegistry(t, url)

	// Listing sandboxes is a READ. Gating it would make every write grant also a grant of
	// visibility, and a session that may not act should still be able to look.
	if _, err := reg["list-sandboxes"](context.Background(), map[string]any{}, "", "s", "ten"); err != nil {
		t.Fatalf("a read tool must not require a grant: %v", err)
	}
}

func TestWildcardGrantCoversAnySandbox(t *testing.T) {
	_, url := newFakeServer(t)
	reg := newRegistry(t, url)

	ctx := extension.WithGrants(context.Background(), []*easybus.Grant{{
		Key:    PermExec,
		Params: easybus.Struct(map[string]any{"sandbox": "*"}),
	}})
	// The fake server answers whatever is asked, so reaching it means the gate opened.
	if _, err := reg["sandbox-job-start"](ctx,
		map[string]any{"command": "echo hi", "lang": "bash"}, "", "s", "ten"); err != nil {
		t.Fatalf("a wildcard grant must cover any sandbox: %v", err)
	}
}
