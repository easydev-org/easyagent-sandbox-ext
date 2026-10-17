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

func (t *toolset) info(ctx context.Context, args map[string]any, session, tenant string) (extension.ToolResultData, error) {
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

func (t *toolset) exec(ctx context.Context, args map[string]any, session, tenant string) (extension.ToolResultData, error) {
	s, err := t.resolve(args, session, tenant)
	if err != nil {
		return extension.ToolResultData{}, err
	}
	command := argStr(args, "command")
	if command == "" {
		return extension.ToolResultData{}, &extension.TypedError{Code: easybus.ToolResultErrorCodeInvalidArgument, Message: "command is required"}
	}
	timeoutS := argInt(args, "timeout", 5)
	if timeoutS <= 0 || timeoutS > 60 {
		timeoutS = 5
	}
	cl := t.deps.Cache.Worker(ep(s))
	started, err := cl.Execute(ctx, &workerv1.ExecuteRequest{
		Command: command, Workdir: argStr(args, "workdir"), Env: argEnv(args),
	})
	if err != nil {
		return extension.ToolResultData{}, &extension.TypedError{Code: easybus.ToolResultErrorCodeRetryable, Message: fmt.Sprintf("execute: %v", err)}
	}
	jobID := started.GetJobId()
	state, code := waitForJob(ctx, cl, jobID, time.Duration(timeoutS)*time.Second)
	if state == "running" {
		text, _ := renderOutput(ctx, cl, jobID, 0, 200, "all")
		return extension.ToolResultData{
			Content: i18n.T(t.locale(session, tenant), i18n.MsgJobStill, i18n.Params{"id": jobID, "timeout": fmt.Sprintf("%d", timeoutS)}) + "\n" + text,
			Data:    map[string]any{"jobId": jobID, "state": state},
		}, nil
	}
	text, _ := renderOutput(ctx, cl, jobID, 0, 0, "all")
	head := i18n.T(t.locale(session, tenant), i18n.MsgJobDone, i18n.Params{"id": jobID, "state": state, "code": fmt.Sprintf("%d", code)})
	return extension.ToolResultData{Content: head + "\n" + text, Data: map[string]any{"jobId": jobID, "state": state, "exitCode": code}}, nil
}

func (t *toolset) jobStart(ctx context.Context, args map[string]any, session, tenant string) (extension.ToolResultData, error) {
	s, err := t.resolve(args, session, tenant)
	if err != nil {
		return extension.ToolResultData{}, err
	}
	command := argStr(args, "command")
	if command == "" {
		return extension.ToolResultData{}, &extension.TypedError{Code: easybus.ToolResultErrorCodeInvalidArgument, Message: "command is required"}
	}
	started, err := t.deps.Cache.Worker(ep(s)).Execute(ctx, &workerv1.ExecuteRequest{
		Command: command, Workdir: argStr(args, "workdir"), Env: argEnv(args),
	})
	if err != nil {
		return extension.ToolResultData{}, &extension.TypedError{Code: easybus.ToolResultErrorCodeRetryable, Message: fmt.Sprintf("execute: %v", err)}
	}
	return extension.ToolResultData{
		Content: i18n.T(t.locale(session, tenant), i18n.MsgJobStarted, i18n.Params{"id": started.GetJobId()}),
		Data:    map[string]any{"jobId": started.GetJobId()},
	}, nil
}

func (t *toolset) jobOutput(ctx context.Context, args map[string]any, session, tenant string) (extension.ToolResultData, error) {
	s, err := t.resolve(args, session, tenant)
	if err != nil {
		return extension.ToolResultData{}, err
	}
	jobID := argStr(args, "job-id")
	if jobID == "" {
		return extension.ToolResultData{}, &extension.TypedError{Code: easybus.ToolResultErrorCodeInvalidArgument, Message: "job-id is required"}
	}
	start := argInt(args, "offset", 0)
	limit := argInt(args, "limit", 200)
	if limit <= 0 || limit > maxOutputLines {
		limit = 200
	}
	stream := argStr(args, "stream")
	if stream == "" {
		stream = "all"
	}
	res, err := t.deps.Cache.Worker(ep(s)).JobOutput(ctx, &workerv1.JobOutputRequest{JobId: jobID, Start: int32(start), End: int32(start + limit), Stream: stream})
	if err != nil {
		return extension.ToolResultData{}, &extension.TypedError{Code: easybus.ToolResultErrorCodeNotFound, Message: fmt.Sprintf("job-output: %v", err)}
	}
	return extension.ToolResultData{
		Content: strings.Join(res.GetLines(), "\n"),
		Data:    map[string]any{"lines": res.GetLines(), "totalLines": res.GetTotalLines(), "done": res.GetDone()},
	}, nil
}

func (t *toolset) jobWait(ctx context.Context, args map[string]any, session, tenant string) (extension.ToolResultData, error) {
	s, err := t.resolve(args, session, tenant)
	if err != nil {
		return extension.ToolResultData{}, err
	}
	jobID := argStr(args, "job-id")
	if jobID == "" {
		return extension.ToolResultData{}, &extension.TypedError{Code: easybus.ToolResultErrorCodeInvalidArgument, Message: "job-id is required"}
	}
	timeoutS := argInt(args, "timeout", 60)
	if timeoutS <= 0 || timeoutS > 300 {
		timeoutS = 60
	}
	cl := t.deps.Cache.Worker(ep(s))
	state, code := waitForJob(ctx, cl, jobID, time.Duration(timeoutS)*time.Second)
	text, _ := renderOutput(ctx, cl, jobID, -200, 0, "all")
	head := i18n.T(t.locale(session, tenant), i18n.MsgJobDone, i18n.Params{"id": jobID, "state": state, "code": fmt.Sprintf("%d", code)})
	return extension.ToolResultData{Content: head + "\n" + text, Data: map[string]any{"jobId": jobID, "state": state, "exitCode": code}}, nil
}

func (t *toolset) jobKill(ctx context.Context, args map[string]any, session, tenant string) (extension.ToolResultData, error) {
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

func (t *toolset) jobList(ctx context.Context, args map[string]any, session, tenant string) (extension.ToolResultData, error) {
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
