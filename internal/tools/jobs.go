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
//
// Returns an ERROR when the worker could not be asked, because "the job produced no output" and
// "we could not read its output" are different facts and the caller cannot tell them apart from an
// empty string. This used to swallow the error and return "": a job-wait against an unreachable
// worker then reported a finished job with no output, which reads as a successful run of a silent
// command — the most expensive kind of wrong answer, because it looks like a result.
func renderOutput(ctx context.Context, cl sandbox.WorkerClient, jobID string, start, end int32, stream string) (string, int32, error) {
	res, err := cl.JobOutput(ctx, &workerv1.JobOutputRequest{JobId: jobID, Start: start, End: end, Stream: stream})
	if err != nil {
		return "", 0, err
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
	return text, res.GetTotalLines(), nil
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
	// THE LANGUAGES THIS SANDBOX CAN ACTUALLY RUN, with their ids and modes.
	//
	// THIS IS THE ANSWER TO "what do I pass as lang?". The engine probes each interpreter by RUNNING it,
	// so a binary that exists but cannot execute is never offered — the list is what will work, not what
	// is installed.
	langs := make([]map[string]any, 0, len(info.GetInterpreters()))
	ids := make([]string, 0, len(info.GetInterpreters()))
	for _, in := range info.GetInterpreters() {
		langs = append(langs, map[string]any{
			"id": in.GetId(), "name": in.GetName(), "path": in.GetPath(),
			"version": in.GetVersion(), "modes": in.GetModes(),
		})
		ids = append(ids, in.GetId())
	}
	shellHint := ""
	if len(ids) > 0 {
		shellHint = fmt.Sprintf(" | lang=%s", strings.Join(ids, "|"))
	}
	return extension.ToolResultData{
		Content: fmt.Sprintf("os=%s arch=%s workspace=%s home=%s%s",
			info.GetOs(), info.GetArch(), info.GetWorkspace(), info.GetHome(), shellHint),
		Data: map[string]any{
			"os": info.GetOs(), "arch": info.GetArch(), "shell": info.GetShell(),
			"workspace": info.GetWorkspace(), "home": info.GetHome(), "bootId": info.GetBootId(),
			// The ids `sandbox-job-start` accepts, so a caller need not guess.
			"interpreters": langs, "langs": ids,
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
	// Permission gate, before anything is executed.
	//
	// Checked HERE rather than trusted from the agent: this process holds the worker
	// credential, so it is the only place that can stop a command from running. The agent's
	// check is an earlier refusal with a friendlier message.
	if err := extension.Require(ctx, PermExec, map[string]string{"sandbox": s.Name}); err != nil {
		return extension.ToolResultData{}, err
	}
	command := argStr(args, "command")
	if command == "" {
		return extension.ToolResultData{}, &extension.TypedError{Code: easybus.ToolResultErrorCodeInvalidArgument, Message: "command is required"}
	}
	// THE LANGUAGE IS REQUIRED, and it is the engine that insists: `echo hi && ls` is bash, is a syntax
	// error in Windows PowerShell 5.1, and means something else again in PowerShell 7. A worker that
	// guessed would run the command as a different language from the one it was written in and report
	// success. So this is passed through, and the engine names the ids it has when it is missing.
	lang := argStr(args, "lang")
	if lang == "" {
		return extension.ToolResultData{}, &extension.TypedError{
			Code: easybus.ToolResultErrorCodeInvalidArgument,
			Message: "lang is required: the language the command is written in. " +
				"Read sandbox-info for the ids this sandbox has (bash, python, node, ...). " +
				"There is no default because guessing would run the command as the wrong language.",
		}
	}
	// Clamp, do not reset: asking for >60s must give 60s, not the default.
	timeoutS := clampInt(args, "timeout", defaultJobStartWaitS, maxJobStartWaitS)
	cl := t.deps.Cache.Worker(ep(s))
	started, err := cl.Execute(ctx, &workerv1.ExecuteRequest{
		Command: command, Workdir: argStr(args, "workdir"),
		// `env` IS GONE, and not by oversight. The engine refuses per-job environments: an "extra env"
		// is either an overlay (a second concept for every caller) or a replacement that silently drops
		// PATH and breaks child processes. A caller that needs a variable sets it inside its program.
		Lang: lang, Mode: argStr(args, "mode"),
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
		//
		// A failed read IS reported: the job state is still conveyed (the Data fields
		// below carry it), but the text says the output could not be fetched rather than
		// showing an empty body that reads as "this command printed nothing".
		text, _, rerr := renderOutput(ctx, cl, jobID, 0, 200, "all")
		if rerr != nil {
			text = outputUnavailableNote(t, session, tenant, rerr)
		}
		return extension.ToolResultData{
			// The status goes LAST, after the output.
			//
			// Output is what the caller asked for and what keeps growing as the job
			// runs; a status line pinned to the top would be pushed further away with
			// every new line, so the reader would have to scroll up past the answer to
			// find out whether it had finished. Trailing status reads like a shell
			// prompt after its output.
			Content: strings.TrimRight(text, "\n") + "\n" + jobStatusLine(t, session, tenant, jobID, state, code, timeoutS),
			Data: map[string]any{
				"jobId": jobID, "state": state, "timeoutSec": timeoutS,
				// The panel opens ON THIS JOB: the caller just waited for it, and making them find it
				// again would waste the wait they already did.
				"view": jobView(s.Name, jobID),
			},
		}, nil
	}
	text, _, rerr := renderOutput(ctx, cl, jobID, 0, 0, "all")
	if rerr != nil {
		text = outputUnavailableNote(t, session, tenant, rerr)
	}
	return extension.ToolResultData{
		Content: strings.TrimRight(text, "\n") + "\n" + jobStatusLine(t, session, tenant, jobID, state, code, timeoutS),
		Data: map[string]any{
			"jobId": jobID, "state": state, "exitCode": code,
			"view": jobView(s.Name, jobID),
		},
	}, nil
}

// outputUnavailableNote renders the text shown where a job's output would be.
//
// The tools still answer: the job's STATE (and exit code) is known and useful, so failing the whole
// call would throw away information the worker did give us. What must not happen is an empty body
// beside a "done" line, because that reads as "the command printed nothing" — a wrong answer that
// looks like a result.
func outputUnavailableNote(t *toolset, session, tenant string, err error) string {
	return i18n.T(t.locale(session, tenant), i18n.MsgJobOutputUnavailable,
		i18n.Params{"err": err.Error()})
}

// jobStatusLine renders the trailing status for a finished-or-still-running job.
//
// One place so the three outcomes read consistently: the output, then how it
// ended. Keeping it out of the head of the text means the same line always sits
// where a reader looks after finishing the output.
func jobStatusLine(t *toolset, session, tenant, jobID, state string, code int32, timeoutS int) string {
	if state == "running" {
		return i18n.T(t.locale(session, tenant), i18n.MsgJobStill,
			i18n.Params{"id": jobID, "timeout": fmt.Sprintf("%d", timeoutS)})
	}
	return i18n.T(t.locale(session, tenant), i18n.MsgJobDone,
		i18n.Params{"id": jobID, "state": state, "code": fmt.Sprintf("%d", code)})
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
	body := strings.Join(res.GetLines(), "\n")
	// THE INTERMEDIATE-STREAM GLIMPSE IS GONE WITH THE DATAFLOW GRAPH. It existed because the old
	// engine decomposed a shell script into flows, so a pipeline's upstream output could be read even
	// though only the last stage reached stdout. The engine now runs a program in one language and
	// streams what it writes; there is no decomposition to inspect, and the program's own output is
	// the whole of what it produced.
	inter := ""
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
	text, _, rerr := renderOutput(ctx, cl, jobID, -200, 0, "all")
	if rerr != nil {
		// The job's STATE is known and useful, so the tool still answers — but it must not
		// answer with an empty body next to "done", which is indistinguishable from a
		// command that printed nothing. Report the read failure in its place.
		return extension.ToolResultData{
			Content: i18n.T(t.locale(session, tenant), i18n.MsgJobDone,
				i18n.Params{"id": jobID, "state": state, "code": fmt.Sprintf("%d", code)}) +
				"\n" + outputUnavailableNote(t, session, tenant, rerr),
			Data: map[string]any{
				"jobId": jobID, "state": state, "exitCode": code, "outputError": rerr.Error(),
				"view": jobView(s.Name, jobID),
			},
		}, nil
	}
	// THE INTERMEDIATE STREAMS ARE GONE, for the reason given in jobOutput: the engine no longer
	// decomposes a script, so there is nothing between the program and its own output.
	head := i18n.T(t.locale(session, tenant), i18n.MsgJobDone, i18n.Params{"id": jobID, "state": state, "code": fmt.Sprintf("%d", code)})
	return extension.ToolResultData{
		Content: head + "\n" + text,
		Data: map[string]any{
			"jobId": jobID, "state": state, "exitCode": code,
			"view": jobView(s.Name, jobID),
		},
	}, nil
}

func (t *toolset) jobKill(ctx context.Context, args map[string]any, callID, session, tenant string) (extension.ToolResultData, error) {
	s, err := t.resolve(args, session, tenant)
	if err != nil {
		return extension.ToolResultData{}, err
	}
	if err := extension.Require(ctx, PermExec, map[string]string{"sandbox": s.Name}); err != nil {
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
	return extension.ToolResultData{
		Content: strings.Join(lines, "\n"),
		Data: map[string]any{
			"jobs": res.GetJobs(),
			// A listing points at the MACHINE, not at one job: the reader has just been shown a list,
			// and the panel's job rail is that same list, live.
			"view": sandboxView(s.Name),
		},
	}, nil
}
