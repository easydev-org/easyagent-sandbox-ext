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

/**
 * waitForJob polls JobWait until the job leaves "running" or the budget expires.
 *
 * A BUDGET OF ZERO MEANS "UNTIL IT FINISHES", and that has to be its own case rather than a
 * deadline in the past. `time.Now().Add(0)` is a deadline that has already passed, so the loop
 * returned on its first pass with the state still "running" — a follower waiting for a background
 * job would have stopped the moment it started looking.
 *
 * THE THIRD RESULT IS "WE COULD NOT ASK", and it is separate from the state on purpose. A worker that
 * cannot be reached has told us NOTHING about the job, and folding that into a state invents a
 * death: the caller then reports a failed job for work that may still be running on the machine. It
 * used to return the literal "failed" here, which is not even one of the state's words — so the tool
 * result said "failed" while the follower, reading the same answer, mapped it to `done`.
 *
 * THE CONTEXT ENDING IS NOT THE JOB ENDING either, and it is not an error: a cancelled wait says
 * nothing about the work, so the last known state comes back with ok=true.
 */
func waitForJob(ctx context.Context, cl sandbox.WorkerClient, jobID string, budget time.Duration) (state string, code int32, ok bool) {
	unbounded := budget <= 0
	deadline := time.Now().Add(budget)
	for {
		remaining := 600 * time.Second
		if !unbounded {
			remaining = time.Until(deadline)
		}
		slice := remaining
		if slice > 600*time.Second {
			slice = 600 * time.Second
		}
		if slice < time.Millisecond {
			slice = time.Millisecond
		}
		res, err := cl.JobWait(ctx, &workerv1.JobWaitRequest{JobId: jobID, TimeoutMs: int32(slice.Milliseconds())})
		if err != nil {
			if ctx.Err() != nil {
				return "running", 0, true
			}
			return "", 0, false
		}
		if res.GetState() != "running" {
			return res.GetState(), res.GetExitCode(), true
		}
		if !unbounded && time.Now().After(deadline) {
			return res.GetState(), res.GetExitCode(), true
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
// far. The job keeps running, and its output keeps arriving on the progress
// subject for anything watching this call — a panel, not the model, which has
// already received its result.
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
	// Relay the job's output while it runs.
	//
	// Started BEFORE the wait so a UI watching this call sees output DURING the wait rather than only
	// after it. Best-effort: if the worker cannot be watched the job still runs and the result is
	// unaffected.
	//
	// THE RELAY DOES NOT REACH THE MODEL, and never did for a backgrounded job: the turn's own relay
	// stops when this call returns, so a command that outlives it is watched by the panel (via this
	// subject, or its own `WatchJob` stream) and by `sandbox-job-output` afterwards. The tool result
	// is the model's whole view.
	//
	// follow=false opts out for a job whose output nobody reads live.
	if followEnabled(args) {
		watcher.Watch(ctx, cl, jobID)
	}
	state, code, ok := waitForJob(ctx, cl, jobID, time.Duration(timeoutS)*time.Second)
	if !ok {
		/**
		 * THE WORKER STOPPED ANSWERING. The job may be running perfectly well on a machine we can no
		 * longer reach, so this is NOT a job result — it is a failed call, and saying anything about
		 * the job's state would invent one.
		 *
		 * IT STILL REGISTERS THE JOB, because the process is out there: if the worker comes back, the
		 * follower can record how it ended, and a panel can find it by id. The alternative — dropping
		 * it — leaves a command running that nothing in the system knows about.
		 */
		t.trackJob(ctx, cl, s, jobID, command, session, tenant, callID)
		return extension.ToolResultData{}, &extension.TypedError{
			Code: easybus.ToolResultErrorCodeRetryable,
			Message: fmt.Sprintf("job %s: the worker stopped answering, so its outcome is unknown "+
				"(it may still be running; use job-wait or job-output to check)", jobID),
		}
	}
	if state == "running" {
		// STILL GOING: hand back the id and let a separate call wait for it.
		//
		// THIS TOOL NO LONGER WAITS AS LONG AS IT COULD. It used to sit here for ten seconds and
		// then report the same thing, which meant every background command cost ten seconds of a
		// turn before the caller learned it was slow. Starting is what this tool knows how to do;
		// waiting is a question about a job that already exists, and `job-wait` answers it.
		//
		// IT REGISTERS THE JOB FIRST, and that is not optional: the id in the reply is only useful
		// if another tool can look it up, so a registry that is missing means the reader would get
		// an id nobody can resolve.
		//
		// IF IT CANNOT REGISTER, THE CALL REPORTS THAT INSTEAD OF THE ID. Before this, the id was
		// returned either way — with a warning in the log that the caller never sees — so the model
		// was told "it is still going, watch it with job-wait" and every later call answered "no such
		// job".
		if !t.trackJob(ctx, cl, s, jobID, command, session, tenant, callID) {
			return extension.ToolResultData{}, &extension.TypedError{
				Code: easybus.ToolResultErrorCodeRetryable,
				Message: fmt.Sprintf("job %s is still running, but it could not be added to the job "+
					"list, so it cannot be waited on or read later; raise its `timeout` to let it finish "+
					"in one call", jobID),
			}
		}
		text, _, rerr := renderOutput(ctx, cl, jobID, 0, 200, "all")
		if rerr != nil {
			text = outputUnavailableNote(t, session, tenant, rerr)
		}
		return extension.ToolResultData{
			// The status goes LAST, after the output — a status line pinned to the top would be
			// pushed further away with every new line, so a reader would scroll past the answer to
			// find out whether it had finished. Trailing status reads like a shell prompt.
			Content: strings.TrimRight(text, "\n") + "\n" + jobStatusLine(t, session, tenant, jobID, state, code, timeoutS),
			Data: map[string]any{
				"jobId": jobID, "state": state, "timeoutSec": timeoutS,
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
	// NO INTERMEDIATE STREAMS. The old engine decomposed a shell script into flows, so a pipeline's
	// upstream output could be read even though only the last stage reached stdout; it ran a program
	// in one language instead, whose own output is the whole of what it produced. The field that
	// carried them was written as a constant `""` and is gone with the concept — a key that can only
	// ever be empty reads as "supported" to anyone shaping a consumer around it.
	body := strings.Join(res.GetLines(), "\n")
	return extension.ToolResultData{
		Content: body,
		Data: map[string]any{
			"lines": res.GetLines(), "totalLines": res.GetTotalLines(), "done": res.GetDone(),
		},
	}, nil
}

/**
 * jobCancel stops a running command and says so to the shared job list.
 *
 * IT IS SYNCHRONOUS ON PURPOSE. The worker owns the process and answers JobKill with a result or an
 * error, so a caller learns whether the kill landed — which is the question it asked. Routing this
 * through the job list as an "intent" would turn a reliable call into a request nobody confirms.
 *
 * IT RECORDS `cancelled` ITSELF RATHER THAN WAITING FOR THE FOLLOWER. The follower is watching the
 * worker's own state, and the worker reports "killed" asynchronously; writing the state here means a
 * panel sees the cancellation at the moment it was requested, not one heartbeat later. The follower
 * will write the same state again when the process actually dies, which is idempotent.
 */
func (t *toolset) jobCancel(ctx context.Context, args map[string]any, callID, session, tenant string) (extension.ToolResultData, error) {
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
		// A REFUSED KILL IS REPORTED. The process is still running, and saying otherwise would leave
		// the reader with no reason to try again.
		return extension.ToolResultData{}, &extension.TypedError{Code: easybus.ToolResultErrorCodeRetryable, Message: fmt.Sprintf("job-cancel: %v", err)}
	}
	// NOTHING ELSE TO RECORD. There is no shared entry to mark: the worker owns the process and this
	// call has its answer, so a panel that asks the worker sees `killed` without anybody writing it
	// down. The follower goroutine will also see it and wake the conversation, which is the one thing
	// that still needs telling.
	return extension.ToolResultData{Content: fmt.Sprintf("cancelled %s", jobID)}, nil
}
