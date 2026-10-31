// Call the sandbox extension the way an agent does, so the deployed thing is verified end to end
// rather than by reading its configuration back.
//
// A TOOL CALL, NOT A CURL: the extension answers over the bus with a tenant-scoped subject, and the
// question here — "does sandbox-job-start reach the NEW labwc worker and does it require lang?" — can
// only be answered by going through the same path a caller takes, including the permission gate.
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	bus "github.com/easydev-org/easybus-sdk"
	"github.com/easydev-org/easybus-sdk/agent"
	"github.com/easydev-org/easybus-sdk/transport/nats"
)

func main() {
	url := os.Getenv("NATS_URL")
	if url == "" {
		url = "nats://nats.dev.svc.cluster.local:4222"
	}
	tenant := os.Getenv("TENANT")
	if tenant == "" {
		tenant = "alice"
	}

	nc, err := nats.Connect(url)
	if err != nil {
		fail("connect", err)
	}
	defer nc.Close()

	a := agent.New(nc)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	call := func(tool string, args map[string]any) {
		fmt.Printf("\n=== %s %v\n", tool, args)
		res, err := a.CallToolWithGrants(ctx, tenant, "smoke-session", "sandbox", tool,
			fmt.Sprintf("smoke-%s-%d", tool, time.Now().UnixNano()), args,
			[]*bus.Grant{
				{Key: "exec", Params: bus.Struct(map[string]any{"sandbox": "labwc"})},
			})
		if err != nil {
			fmt.Printf("  transport: %v\n", err)
			return
		}
		if res.Error != nil {
			fmt.Printf("  tool error: %s\n", res.Error.Message)
			return
		}
		out := res.Content
		if len(out) > 600 {
			out = out[:600] + "…"
		}
		fmt.Printf("  %s\n", out)
	}

	call("sandbox-info", map[string]any{"sandbox": "labwc"})
	// No lang: the new worker must REFUSE this. If it runs, the deployed worker is the old one.
	call("sandbox-job-start", map[string]any{"sandbox": "labwc", "command": "echo should-not-run"})
	call("sandbox-job-start", map[string]any{"sandbox": "labwc", "command": "echo NEW-WORKER-OK", "lang": "bash"})
}

func fail(what string, err error) {
	fmt.Fprintf(os.Stderr, "%s: %v\n", what, err)
	os.Exit(1)
}
