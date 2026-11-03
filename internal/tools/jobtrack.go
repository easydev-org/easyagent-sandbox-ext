package tools

import (
	"context"
	"log/slog"

	"github.com/easydev-org/easyagent-sandbox-ext/internal/config"
	"github.com/easydev-org/easyagent-sandbox-ext/internal/sandbox"
	"github.com/easydev-org/easybus-sdk/job"
)

// Kind is this extension's word for the jobs it runs.
//
// IT IS THE FILTER A CLIENT USES. A panel listing "sandbox-job" gets this extension's work and
// nothing else, which is why the value is a constant here rather than a string chosen at each call
// site: two spellings of it would be two categories.
const Kind = "sandbox-job"

/**
 * trackJob registers a running job and follows it to its end.
 *
 * IT RETURNS IMMEDIATELY. The follow happens on its own goroutine, because the whole point of a
 * background job is that the call which started it is over.
 *
 * IT REPORTS WHETHER THE JOB IS LISTED. When it is not, the caller must not hand out the id: a job
 * nobody can look up is worse than a call that simply waited — the reader is told "it is still going,
 * watch it here" and there is no here. Returning the answer rather than logging it is what lets
 * `sandbox-job-start` act on that.
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
	if t.deps.Jobs == nil {
		// NO REGISTRY, NO BACKGROUND JOB. Reporting one would hand the caller an id that no other
		// tool can resolve, which is worse than a call that simply waited.
		slog.WarnContext(ctx, "sandbox: no job registry; the job will not be listed", "svc", "sandbox-ext", "job", jobID)
		return false
	}

	/**
	 * THE JOB'S IDENTITY CARRIES ITS CATEGORY, so a client can filter by kind without consulting a
	 * second list: "sandbox-job.<worker id>". The prefix also makes the id unambiguous across
	 * extensions, which the key layout relies on.
	 */
	id := Kind + "." + jobID
	handle, err := t.deps.Jobs.Start(ctx, job.Entry{
		ID:    id,
		Kind:  Kind,
		Title: command,
		ExtID: "sandbox",
		// THE TENANT IS THE FIRST KEY SEGMENT, and an entry without it cannot be keyed correctly:
		// a job written under the wrong tenant registers, renews and finishes, and the list that
		// asks for this conversation never sees it.
		Tenant:    tenant,
		Session:   session,
		ToolUseID: callID,
		// A SANDBOX JOB IS CANCELABLE: the worker owns the process and answers JobKill. `Finish`
		// clears this on the way out, so a reader never has to check the state to know whether a
		// cancel button would do anything.
		CancelMode: job.CancelKill,
	})
	if err != nil {
		slog.WarnContext(ctx, "sandbox: cannot register job", "svc", "sandbox-ext", "job", jobID, "err", err)
		return false
	}
	// ONE LINE AT REGISTRATION, so "did the job reach the list" is answerable without inferring it
	// from an absence of later messages.
	slog.InfoContext(ctx, "sandbox: job registered", "svc", "sandbox-ext", "job", id, "session", session)

	/**
	 * THE HEARTBEAT OUTLIVES THE CALL. `ctx` here belongs to the tool call, which ends as soon as
	 * this function returns — renewing on it would stop the heartbeat at exactly the moment the job
	 * became interesting. `WithoutCancel` keeps the values (tenant, tracing) and drops the deadline.
	 */
	bg := context.WithoutCancel(ctx)
	go handle.KeepAlive(bg)

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
			 * THE WORKER STOPPED ANSWERING, and the job is left as it was.
			 *
			 * Recording an ending here would be a lie about work that may still be happening — the
			 * process is on a machine, and this extension losing contact with it does not stop it.
			 * So the entry stays `running` with a stale `RenewedAt`, the heartbeat below is allowed
			 * to stop, and the SWEEPER records it as lost after the staleness threshold.
			 *
			 * THAT ROUTE EXISTS PRECISELY FOR THIS. Marking it lost here would pre-empt the sweeper
			 * while pretending this process knows more than it does.
			 */
			return
		}

		/**
		 * THE WORKER'S VERDICT IS MAPPED, NOT COPIED, AND THE EXIT CODE IS NOT CONSULTED.
		 *
		 * A non-zero exit is still `done`: the job ended, and the code says how it ended. Deciding
		 * "failed" here would put this process's opinion of the exit code into a field every reader
		 * then trusts — and the opinion is unknowable from here. `grep` exits 1 for a clean "no
		 * match"; a caller wanting a match reads that as failure, a caller running a search does not.
		 */
		var final job.State
		switch state {
		case "running":
			// The budget was unlimited, so "running" here means the CALLER's context ended, not the
			// job. Recording an ending would be a lie about work that is still happening.
			return
		case "killed":
			final = job.StateCancelled
		default:
			final = job.StateDone
		}

		var exit *int
		if state != "running" {
			c := int(code)
			exit = &c
		}
		if err := handle.Finish(bg, final, exit, ""); err != nil {
			slog.WarnContext(bg, "sandbox: cannot record job end", "svc", "sandbox-ext", "job", jobID, "err", err)
		}
		/**
		 * THE WAKE IS SEPARATE FROM THE RECORD, and either can fail without the other mattering.
		 *
		 * The KV write is what a panel reads; the mailbox message is what makes the conversation
		 * look. A job whose state was recorded but whose wake was lost still shows as finished;
		 * one whose wake arrived but whose write failed still gets read. Insisting both succeed
		 * would mean retrying a notification the reader may already have seen.
		 *
		 * IT FIRES WHETHER OR NOT ANYBODY IS WAITING. A `job-wait` call in flight watches the
		 * registry and will already have returned its own answer, so this is a second copy — and
		 * that is the accepted cost of the common case, which is a job nobody waited for.
		 */
		if t.deps.NotifyJobDone != nil {
			if err := t.deps.NotifyJobDone(bg, tenant, session, id, command, final, exit); err != nil {
				slog.WarnContext(bg, "sandbox: cannot wake the conversation", "svc", "sandbox-ext", "job", jobID, "err", err)
			}
		}
	}()
	return true
}
