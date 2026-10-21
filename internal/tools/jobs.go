package tools

import (
	"context"
	"fmt"
	"strings"
	"time"

	workerv1 "github.com/easydev-org/easyagent-sandbox-ext/gen/worker/v1"
	"github.com/easydev-org/easyagent-sandbox-ext/internal/i18n"
	"github.com/easydev-org/easyagent-sandbox-ext/internal/sandbox"
	easybus "github.com/easydev-org/easybus-sdk"
	"github.com/easydev-org/easybus-sdk/extension"
)

// maxOutputLines caps a rendered job-output window.
const maxOutputLines = 1000

// waitForJob polls JobWait in <=600s slices until the job leaves "running" or
// the budget expires (the worker blocks server-side up to its own cap).
func waitForJob(ctx context.Context, cl sandbox.WorkerClient, jobID string, budget time.Duration) (state string, code int32) {
	deadline := time.Now().Add(budget)
	for {
		remaining := time.Until(deadline)
		slice := remaining
		if slice > 600*time.Second {
			slice = 600 * time.Second
		}
		if slice < time.Millisecond {
			slice = time.Millisecond
		}
		res, err := cl.JobWait(ctx, &workerv1.JobWaitRequest{JobId: jobID, TimeoutMs: int32(slice.Milliseconds())})
		if err != nil {
			return "failed", -1
		}
		if res.GetState() != "running" {
			return res.GetState(), res.GetExitCode()
		}
		if time.Now().After(deadline) {
			return res.GetState(), res.GetExitCode()
		}
	}
}

// renderOutput fetches a line window and caps it.
func renderOutput(ctx context.Context, cl sandbox.WorkerClient, jobID string, start, end int32, stream string) (string, int32) {
	res, err := cl.JobOutput(ctx, &workerv1.JobOutputRequest{JobId: jobID, Start: start, End: end, Stream: stream})
	if err != nil {
		return "", 0
	}
	lines := res.GetLines()
	truncated := false
	if len(lines) > maxOutputLines {
		lines = lines[:maxOutputLines]
		truncated = true
	}
	text := strings.Join(lines, "\n")
	if truncated {
		text += fmt.Sprintf("\n... (truncated at %d of %d lines)", maxOutputLines, res.GetTotalLines())
	}
	return text, res.GetTotalLines()
}

// ---- handlers --------------------------------------------------------------

func (t *toolset) info(ctx context.Context, args map[string]any, callID, session, tenant string) (extension.ToolResultData, error) {
	s, err := t.resolve(args, session, tenant)
	if err != nil {
		return extension.ToolResultData{}, err
	}
	info, err := t.deps.Cache.Worker(ep(s)).Info(ctx, &workerv1.InfoRequest{})
	if err != nil {
		return extension.ToolResultData{}, &extension.TypedError{Code: easybus.ToolResultErrorCodeRetryable, Message: fmt.Sprintf("info: %v", err)}
	}
	return extension.ToolResultData{
		Content: fmt.Sprintf("os=%s arch=%s workspace=%s home=%s", info.GetOs(), info.GetArch(), info.GetWorkspace(), info.GetHome()),
		Data: map[string]any{
			"os": info.GetOs(), "arch": info.GetArch(), "shell": info.GetShell(),
			"workspace": info.GetWorkspace(), "home": info.GetHome(), "bootId": info.GetBootId(),
		},
	}, nil
}

// jobStart runs a command and WAITS for it, briefly.
//
// This is the single "run a command" tool: it absorbed sandbox-exec. Waiting a
// little is what callers actually want (nearly every command finishes in under a
// second, and a tool that always answers "started, id=k3j9" makes the model spend
// another turn asking for the output). Waiting forever is what callers must never
// get, because the turn is blocked for that whole time.
//
// So: wait up to `timeout` (default 10s, clamped to 60s), and if the command is
// still running at the deadline, return the job id plus what it has printed so
// far. The job keeps running, and its output keeps arriving as tool-progress, so
// the caller can watch it live instead of blocking.
func (t *toolset) jobStart(ctx context.Context, args map[string]any, callID, session, tenant string) (extension.ToolResultData, error) {
	s, err := t.resolve(args, session, tenant)
	if err != nil {
		return extension.ToolResultData{}, err
	}
	command := argStr(args, "command")
	if command == "" {
		return extension.ToolResultData{}, &extension.TypedError{Code: easybus.ToolResultErrorCodeInvalidArgument, Message: "command is required"}
	}
	// Clamp, do not reset: asking for >60s must give 60s, not the default.
	timeoutS := clampInt(args, "timeout", defaultJobStartWaitS, maxJobStartWaitS)
	cl := t.deps.Cache.Worker(ep(s))
	started, err := cl.Execute(ctx, &workerv1.ExecuteRequest{
		Command: command, Workdir: argStr(args, "workdir"), Env: argEnv(args),
	})
	if err != nil {
		return extension.ToolResultData{}, &extension.TypedError{Code: easybus.ToolResultErrorCodeRetryable, Message: fmt.Sprintf("execute: %v", err)}
	}
	jobID := started.GetJobId()
	// Relay the job's output to the UI as it arrives.
	//
	// Started BEFORE the wait so the caller sees output during the wait, not only
	// after it. Best-effort: if the worker cannot be watched the job still runs and
	// the result is unaffected.
	//
	// follow=false opts out for a job whose output nobody reads live.
	if followEnabled(args) {
		watcher.Watch(ctx, cl, jobID)
	}
	state, code := waitForJob(ctx, cl, jobID, time.Duration(timeoutS)*time.Second)
	if state == "running" {
		// Still going: hand back the id and whatever it has produced. This is NOT a
		// failure — the command is running and its output keeps streaming.
		text, _ := renderOutput(ctx, cl, jobID, 0, 200, "all")
		return extension.ToolResultData{
			Content: i18n.T(t.locale(session, tenant), i18n.MsgJobStill, i18n.Params{"id": jobID, "timeout": fmt.Sprintf("%d", timeoutS)}) + "\n" + text,
			Data:    map[string]any{"jobId": jobID, "state": state, "timeoutSec": timeoutS},
		}, nil
	}
	text, _ := renderOutput(ctx, cl, jobID, 0, 0, "all")
	head := i18n.T(t.locale(session, tenant), i18n.MsgJobDone, i18n.Params{"id": jobID, "state": state, "code": fmt.Sprintf("%d", code)})
	return extension.ToolResultData{
		Content: head + "\n" + text,
		Data:    map[string]any{"jobId": jobID, "state": state, "exitCode": code},
	}, nil
}

const (
	// defaultJobStartWaitS is how long a command is given before the tool hands
	// back a job id instead. Most commands finish well inside it, so the common
	// case returns the output directly and costs no extra turn.
	defaultJobStartWaitS = 10
	// maxJobStartWaitS caps the wait so a caller cannot block a turn for minutes.
	maxJobStartWaitS = 60
)

// followEnabled reads the optional `follow` argument (default true).
//
// Default true because the common case is "I started something and want to see
// it"; a caller that deliberately wants silence can say so.
func followEnabled(args map[string]any) bool {
	v, ok := args["follow"]
	if !ok {
		return true
	}
	b, ok := v.(bool)
	return !ok || b
}

func (t *toolset) jobOutput(ctx context.Context, args map[string]any, callID, session, tenant string) (extension.ToolResultData, error) {
	s, err := t.resolve(args, session, tenant)
	if err != nil {
		return extension.ToolResultData{}, err
	}
	jobID := argStr(args, "job-id")
	if jobID == "" {
		return extension.ToolResultData{}, &extension.TypedError{Code: easybus.ToolResultErrorCodeInvalidArgument, Message: "job-id is required"}
	}
	start := argInt(args, "offset", 0)
	limit := clampInt(args, "limit", 200, maxOutputLines)
	stream := argStr(args, "stream")
	if stream == "" {
		stream = "all"
	}
	res, err := t.deps.Cache.Worker(ep(s)).JobOutput(ctx, &workerv1.JobOutputRequest{JobId: jobID, Start: int32(start), End: int32(start + limit), Stream: stream})
	if err != nil {
		return extension.ToolResultData{}, &extension.TypedError{Code: easybus.ToolResultErrorCodeNotFound, Message: fmt.Sprintf("job-output: %v", err)}
	}
	cl := t.deps.Cache.Worker(ep(s))
	body := strings.Join(res.GetLines(), "\n")
	// Append a bounded glimpse of the streams that never reached stdout: in a
	// pipeline the upstream stages' output goes into a pipe, so stdout alone shows
	// only the last stage. Opt-out via intermediate: false.
	inter := ""
	if argBool(args, "intermediate", true) {
		inter = renderIntermediates(ctx, cl, jobID,
			argInt(args, "intermediate_flows", defaultIntermediateFlows),
			argInt(args, "intermediate_lines", defaultIntermediateLines))
	}
	if inter != "" {
		body = strings.TrimRight(body, "\n") + "\n" + inter
	}
	return extension.ToolResultData{
		Content: body,
		Data: map[string]any{
			"lines": res.GetLines(), "totalLines": res.GetTotalLines(), "done": res.GetDone(),
			"intermediates": inter,
		},
	}, nil
}

func (t *toolset) jobWait(ctx context.Context, args map[string]any, callID, session, tenant string) (extension.ToolResultData, error) {
	s, err := t.resolve(args, session, tenant)
	if err != nil {
		return extension.ToolResultData{}, err
	}
	jobID := argStr(args, "job-id")
	if jobID == "" {
		return extension.ToolResultData{}, &extension.TypedError{Code: easybus.ToolResultErrorCodeInvalidArgument, Message: "job-id is required"}
	}
	// Default 60s, capped at 90s (clamped, not reset).
	timeoutS := clampInt(args, "timeout", 60, 90)
	cl := t.deps.Cache.Worker(ep(s))
	state, code := waitForJob(ctx, cl, jobID, time.Duration(timeoutS)*time.Second)
	text, _ := renderOutput(ctx, cl, jobID, -200, 0, "all")
	// Same reasoning as jobOutput: stdout/stderr is only the tail of a pipeline.
	if argBool(args, "intermediate", true) {
		if inter := renderIntermediates(ctx, cl, jobID,
			argInt(args, "intermediate_flows", defaultIntermediateFlows),
			argInt(args, "intermediate_lines", defaultIntermediateLines)); inter != "" {
			text = strings.TrimRight(text, "\n") + "\n" + inter
		}
	}
	head := i18n.T(t.locale(session, tenant), i18n.MsgJobDone, i18n.Params{"id": jobID, "state": state, "code": fmt.Sprintf("%d", code)})
	return extension.ToolResultData{Content: head + "\n" + text, Data: map[string]any{"jobId": jobID, "state": state, "exitCode": code}}, nil
}

func (t *toolset) jobKill(ctx context.Context, args map[string]any, callID, session, tenant string) (extension.ToolResultData, error) {
	s, err := t.resolve(args, session, tenant)
	if err != nil {
		return extension.ToolResultData{}, err
	}
	jobID := argStr(args, "job-id")
	if jobID == "" {
		return extension.ToolResultData{}, &extension.TypedError{Code: easybus.ToolResultErrorCodeInvalidArgument, Message: "job-id is required"}
	}
	if _, err := t.deps.Cache.Worker(ep(s)).JobKill(ctx, &workerv1.JobKillRequest{JobId: jobID}); err != nil {
		return extension.ToolResultData{}, &extension.TypedError{Code: easybus.ToolResultErrorCodeRetryable, Message: fmt.Sprintf("job-kill: %v", err)}
	}
	return extension.ToolResultData{Content: fmt.Sprintf("killed %s", jobID)}, nil
}

func (t *toolset) jobList(ctx context.Context, args map[string]any, callID, session, tenant string) (extension.ToolResultData, error) {
	s, err := t.resolve(args, session, tenant)
	if err != nil {
		return extension.ToolResultData{}, err
	}
	res, err := t.deps.Cache.Worker(ep(s)).ListJobs(ctx, &workerv1.ListJobsRequest{Limit: int32(argInt(args, "limit", 50))})
	if err != nil {
		return extension.ToolResultData{}, &extension.TypedError{Code: easybus.ToolResultErrorCodeRetryable, Message: fmt.Sprintf("list-jobs: %v", err)}
	}
	lines := make([]string, 0, len(res.GetJobs()))
	for _, j := range res.GetJobs() {
		lines = append(lines, fmt.Sprintf("%s %s exit=%d %s", j.GetId(), j.GetState(), j.GetExitCode(), j.GetCommand()))
	}
	return extension.ToolResultData{Content: strings.Join(lines, "\n"), Data: map[string]any{"jobs": res.GetJobs()}}, nil
}
