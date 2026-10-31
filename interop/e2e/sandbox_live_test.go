package e2e

import (
	"strings"
	"testing"
)

// What this file asserts, and why each one would otherwise be invisible:
//
//   discovery   — an extension that fails to register serves nothing, and every prompt mentioning
//                 its tools silently stops working.
//   manifest    — the tool list is the contract an agent renders; a lost tool is a dead prompt.
//   gating      — every tool declares `required_config: [sandboxes]`. Without it the call must be
//                 REFUSED, not attempted against a guessed URL.
//   real file   — write then read, through a real easyworker, is the only end-to-end proof that
//                 the extension reached the sandbox rather than answering from its own process.

func TestRegistersOverDiscovery(t *testing.T) {
	a, _ := harness(t, env("E2E_SANDBOX_URL", ""))
	ctx, cancel := ctxWithTimeout(t, 30)
	defer cancel()

	found, err := a.Discover(ctx, 5000)
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	var ids []string
	for _, m := range found {
		ids = append(ids, m.Id)
	}
	if !contains(ids, extID) {
		t.Fatalf("extension not discovered; got %v", ids)
	}
}

func TestDeclaresItsTools(t *testing.T) {
	a, _ := harness(t, env("E2E_SANDBOX_URL", ""))
	ctx, cancel := ctxWithTimeout(t, 30)
	defer cancel()

	found, err := a.Discover(ctx, 5000)
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	// `Tools` is a SLICE of specs, not a map keyed by name — the same shape that once made a
	// name check pass on "0","1","2"…
	var declared []string
	for _, m := range found {
		if m.Id != extID {
			continue
		}
		for _, tl := range m.Tools {
			declared = append(declared, tl.Name)
		}
	}
	if len(declared) == 0 {
		t.Fatalf("%s declared no tools", extID)
	}
	for _, want := range []string{
		"list-sandboxes", "sandbox-info",
		"sandbox-file-patch", "sandbox-file-read",
		"sandbox-job-start",
	} {
		if !contains(declared, want) {
			t.Fatalf("tool %q missing; declared %v", want, declared)
		}
	}
}

// With no `sandboxes` configured, a tool that NEEDS one must refuse and name the missing config.
//
// NOT `list-sandboxes`: listing an empty list is a perfectly good answer to "what is configured",
// and the extension returns a message saying so. Asserting a refusal there would have encoded my
// assumption instead of the contract — the first version of this test did exactly that.
//
// The tool below actually needs a sandbox, so without config it must say WHY rather than failing
// with a transport error that names no field.
func TestNeedsSandboxRefusesWithoutConfig(t *testing.T) {
	a, _ := harness(t, env("E2E_SANDBOX_URL", ""))
	ctx, cancel := ctxWithTimeout(t, 60)
	defer cancel()

	tr, err := a.CallToolWithGrants(ctx, tenant, session, extID, "sandbox-file-read",
		"c-noconf", map[string]any{"path": "/etc/hostname"}, sandboxGrants("pod-a"))
	if err != nil {
		// A transport-level failure is acceptable ONLY if it names the config; a bare timeout would
		// mean the extension waited on a URL it should not have chosen.
		if !strings.Contains(strings.ToLower(err.Error()), "config") &&
			!strings.Contains(strings.ToLower(err.Error()), "sandbox") {
			t.Fatalf("call without config failed without naming it: %v", err)
		}
		return
	}
	if tr.Error == nil {
		t.Fatal("a tool that needs a sandbox succeeded with none configured; it must refuse")
	}
	t.Logf("refused as expected: %s", tr.Error.Message)
}

// The end-to-end proof: the bytes must come back from the SANDBOX, not from the extension's memory.
//
// Written and read are compared, and the content carries a per-run nonce: a constant would pass
// even if the read returned something cached or hard-coded.
func TestWritesAndReadsAFileInTheSandbox(t *testing.T) {
	sandboxURL := env("E2E_SANDBOX_URL", "")
	a, _ := harness(t, sandboxURL)
	configure(t, a, sandboxURL)
	body := "e2e-" + nowNonce()
	path := "/root/workspace/e2e-probe.txt"

	// Let the config watch apply before the first tool call; racing it would look like "no config".
	if err := waitConfigured(t, a); err != nil {
		t.Fatal(err)
	}

	// file.write for the write, and nothing for the read: the read tool declares no `requires`, and
	// passing a grant it does not need would hide a manifest that stopped gating the write.
	//
	// THE WRITE IS THE PATCH TOOL NOW. One tool creates, edits and deletes, so a caller cannot pick
	// `write` where it meant `update` — and the hunk form makes the change reviewable in the call.
	callToolGrants(t, a, "sandbox-file-patch",
		map[string]any{"patch": "*** Begin Patch\n*** Add File: " + path + "\n+" +
			strings.ReplaceAll(body, "\n", "\n+") + "\n*** End Patch\n"}, sandboxGrants("pod-a"))

	got := callTool(t, a, "sandbox-file-read", map[string]any{"path": path})
	if !strings.Contains(got, body) {
		t.Fatalf("read back %q, want it to contain %q — the write did not reach the sandbox, or the "+
			"read did not come from it", got, body)
	}
}

// sandbox-info must describe the REAL worker, so its answer is evidence the extension connected.
func TestInfoDescribesTheSandbox(t *testing.T) {
	sandboxURL := env("E2E_SANDBOX_URL", "")
	a, _ := harness(t, sandboxURL)
	configure(t, a, sandboxURL)
	if err := waitConfigured(t, a); err != nil {
		t.Fatal(err)
	}
	got := callTool(t, a, "sandbox-info", map[string]any{})
	if strings.TrimSpace(got) == "" {
		t.Fatal("sandbox-info returned nothing")
	}
	t.Logf("sandbox-info: %s", got)
}
