package tools

import (
	"context"
	"log/slog"

	"github.com/easydev-org/easyagent-sandbox-ext/internal/config"
	"github.com/easydev-org/easyagent-sandbox-ext/internal/sandbox"
)

// Kind is this extension's word for the jobs it runs.
//
// IT PREFIXES THE ID the extension hands back, so a panel can tell which extension to ask about a job
// it was told about. There is no shared registry to filter any more — `sandbox-job-*` tools take the
// bare worker id — but the prefix stays on the id in messages, because a notification that names only
// `392f82df` gives the reader nothing to search for.
const Kind = "sandbox-job"

/**
 * trackJob follows a job to its end and wakes the conversation when it finishes.
 *
 * IT RETURNS IMMEDIATELY. The follow happens on its own goroutine, because the whole point of a
 * background job is that the call which started it is over.
 *
 * THERE IS NO REGISTRY ANY MORE, AND THAT REMOVED A WHOLE KIND OF WRONG ANSWER. The extension used to
 * write a copy of the job into a shared KV bucket with a heartbeat, and a sweeper marked the copy
 * `lost` when the heartbeat stopped. For a process on a worker that is exactly right — the extension
 * dying is the only way the heartbeat stops, and the process is gone with it. But the same mechanism
 * was shared with work this extension does not own (a GitHub run survives the extension restarting),
 * so it needed a `reconcile` hook to un-lie about it.
 *
 * NOW THE ONLY THING RECORDED IS "SOMEBODY IS WAITING". There is no state to disagree with the
 * machine: the panel lists jobs by asking the worker, `sandbox-job-wait` waits on the worker, and this
 * goroutine's whole purpose is the wake at the end.
 *
 * WHY A DEDICATED GOROUTINE PER JOB RATHER THAN A SWEEPING LOOP: `waitForJob` blocks on the worker's
 * own long-poll, so a loop would serialise jobs behind one another — ten builds would take ten
 * builds' worth of waiting in sequence. One follower per job costs a goroutine, which is the cheap
 * thing here.
 */
func (t *toolset) trackJob(
	ctx context.Context, cl sandbox.WorkerClient, s config.Sandbox,
	jobID, command, session, tenant, callID string,
) bool {
	// ONE LINE AT REGISTRATION, so "did the job start following" is answerable from the log without
	// inferring it from an absence of later messages.
	slog.InfoContext(ctx, "sandbox: following job", "svc", "sandbox-ext", "job", Kind+"."+jobID, "session", session)

	/**
	 * THE FOLLOW OUTLIVES THE CALL. `ctx` here belongs to the tool call, which ends as soon as this
	 * function returns — following on it would stop at exactly the moment the job became interesting.
	 * `WithoutCancel` keeps the values (tenant, tracing) and drops the deadline.
	 */
	bg := context.WithoutCancel(ctx)

	go func() {
		defer func() {
			// A PANIC IN ONE FOLLOWER MUST NOT TAKE THE EXTENSION DOWN: the job it was watching is
			// still running on the worker, and the other jobs are unaffected.
			if r := recover(); r != nil {
				slog.Error("sandbox: job follower panicked", "svc", "sandbox-ext", "job", jobID, "panic", r)
			}
		}()

		// ZERO BUDGET MEANS "NO BUDGET": keep asking until the worker says it is over.
		state, code, ok := waitForJob(bg, cl, jobID, 0)
		if !ok {
			/**
			 * THE WORKER STOPPED ANSWERING, SO NOTHING IS REPORTED.
			 *
			 * There is no entry to leave running and no sweeper to correct it — the record is gone
			 * with the registry. The conversation simply is not woken, which is the honest outcome:
			 * a wake saying "the job finished" would be a fabricated ending, and one saying "it was
			 * lost" would be a fabricated death. `sandbox-job-wait` can still ask the worker later.
			 */
			slog.WarnContext(bg, "sandbox: job follower stopped, outcome unknown",
				"svc", "sandbox-ext", "job", jobID)
			return
		}
		// The caller's context ended, not the job. Nothing to report yet.
		if state == "running" {
			return
		}

		var exit *int
		c := int(code)
		exit = &c

		/**
		 * THE WAKE IS THE WHOLE POINT OF THIS GOROUTINE NOW.
		 *
		 * The state travels as the WORKER'S OWN WORD — `done`, `killed` — rather than a shared enum,
		 * because there is no shared vocabulary left to translate into. A reader that wants the
		 * nuance reads the exit code and the output, which are the two things that carry it.
		 */
		if t.deps.NotifyJobDone != nil {
			if err := t.deps.NotifyJobDone(bg, tenant, session, Kind+"."+jobID, command, state, exit); err != nil {
				slog.WarnContext(bg, "sandbox: cannot wake the conversation", "svc", "sandbox-ext", "job", jobID, "err", err)
			}
		}
	}()
	return true
}
