// Command probeprogress drives sandbox-job-start DIRECTLY over the bus and
// watches the tool-progress channel at the same time.
//
// It exists to test the extension half of the live-output chain without an LLM:
// the agent needs a model to start a turn, and this dev stack has none, so the
// only way to see whether the extension really reports progress is to call it the
// way the agent would.
//
// What this proves:  the extension publishes progress frames while a job runs.
// What it does NOT prove: that the agent relays them onto the session stream, or
// that the console renders them. Those need a turn.
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	abcagent "github.com/easydev-org/easybus-sdk/agent"
	sdkbus "github.com/easydev-org/easybus-sdk/bus"
	"github.com/easydev-org/easybus-sdk/transport/nats"
)

func main() {
	natsURL := os.Getenv("NATS_URL")
	if natsURL == "" {
		natsURL = "nats://nats.dev.svc.cluster.local:4222"
	}
	tenant := "alice"
	session := "probe-session"

	b, err := nats.Connect(natsURL)
	if err != nil {
		fmt.Fprintln(os.Stderr, "nats:", err)
		os.Exit(1)
	}
	defer b.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	// Watch progress FIRST: core NATS drops what nobody is subscribed for, and the
	// job can print immediately.
	sub, err := b.Subscribe(ctx, "easybus."+tenant+".tool.progress.>", sdkbus.SubscribeOpts{})
	if err != nil {
		fmt.Fprintln(os.Stderr, "subscribe:", err)
		os.Exit(1)
	}
	got := 0
	go func() {
		for {
			env, ok := sub.Next(ctx)
			if !ok {
				return
			}
			got++
			fmt.Printf("[PROGRESS #%d] %v\n", got, env.Payload)
		}
	}()

	a := abcagent.New(b)
	if _, err := a.Discover(ctx, 3000); err != nil {
		fmt.Fprintln(os.Stderr, "discover:", err)
		os.Exit(1)
	}

	fmt.Println("calling sandbox-job-start ...")
	res, err := a.CallTool(ctx, tenant, session, "sandbox", "sandbox-job-start", "probe-1", map[string]any{
		"command": `for i in 1 2 3 4 5 6 7 8; do echo "tick $i"; sleep 1; done`,
		"timeout": 4, // smaller than the job, so it returns a job id mid-run
		"follow":  true,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "call:", err)
		os.Exit(1)
	}
	fmt.Printf("RESULT: %+v\n", res)

	// Keep watching so progress emitted after the tool returned is still captured —
	// that is the whole point of the relay outliving the call.
	time.Sleep(12 * time.Second)
	fmt.Printf("\nTOTAL progress frames: %d\n", got)
}
