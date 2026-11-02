package tools

import (
	"strings"
	"testing"
	"time"

	"github.com/easydev-org/easybus-sdk/job"
)

/**
 * These pin the two halves of a finished job: what the registry records, and what the conversation
 * is told. They are separate on purpose — either can fail without the other mattering — so there is
 * a test for each rather than one test that asserts both.
 */

func TestJobTextNamesTheJobWithoutItsOutput(t *testing.T) {
	exit := 0
	got := JobText("sandbox-job.019abc", "cargo build", job.StateDone, &exit)

	// THE OUTPUT IS DELIBERATELY ABSENT. It is on the tool-output stream, and a `job-output` call
	// reads it. Repeating it here would send the same bytes twice and would need a second
	// truncation policy beside the agent's.
	for _, want := range []string{
		`sandbox-job.019abc`,
		`state="done"`,
		`exit="0"`,
		`title="cargo build"`,
		"job-output",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("job text is missing %q:\n%s", want, got)
		}
	}

	// IT MUST NOT CALL A NON-ZERO EXIT A FAILURE. The exit code carries the verdict; the job
	// deliberately does not make one — `grep` exiting 1 is a clean "no match".
	failed := JobText("j", "grep x", job.StateDone, &exit)
	if strings.Contains(strings.ToLower(failed), "failed") {
		t.Errorf("a done job must not be described as failed:\n%s", failed)
	}
}

func TestJobTextHandlesAJobWithNoExitCode(t *testing.T) {
	// A cancelled or lost job has no exit code, and the text must not render "exit=0" for it —
	// that would invent a success.
	got := JobText("j", "build", job.StateLost, nil)
	if strings.Contains(got, "exit=") {
		t.Errorf("a job with no exit code rendered one:\n%s", got)
	}
	if !strings.Contains(got, "stopped reporting") {
		t.Errorf("a lost job must say so:\n%s", got)
	}
}

/**
 * THE MESSAGE ID IS DERIVED FROM THE JOB.
 *
 * A job has exactly one ending, so a wake about it should fold at most once however many times the
 * durable mailbox delivers it. A generated id would make every redelivery a second message.
 */
func TestMailboxPayloadIdIsDerivedFromTheJob(t *testing.T) {
	exit := 1
	a := NewMailboxPayload("sandbox-job.019abc", "make", job.StateDone, &exit)
	b := NewMailboxPayload("sandbox-job.019abc", "make", job.StateDone, &exit)

	if a.MessageID != b.MessageID {
		t.Fatalf("the same job produced two ids: %q and %q", a.MessageID, b.MessageID)
	}
	if !strings.Contains(a.MessageID, "sandbox-job.019abc") {
		t.Fatalf("the id does not name its job: %q", a.MessageID)
	}
	// THE TEXT IS WHAT THE AGENT FOLDS INTO A USER MESSAGE, so it must be non-empty or the wake
	// arrives as a blank turn.
	if strings.TrimSpace(a.Text) == "" {
		t.Fatal("a wake with no text would fold an empty message into the conversation")
	}
}

/**
 * THE TIMING THAT MAKES A LOST JOB RECORDABLE.
 *
 * The sweeper judges an entry that is still present but stale. If the running TTL were shorter than
 * the staleness threshold, a stalled job would expire before anything could look at it and would
 * disappear rather than being recorded as lost — the one outcome this design exists to prevent.
 *
 * DUPLICATED FROM THE SDK'S OWN TEST ON PURPOSE: this extension configures its registry, and a
 * change here that broke the relationship would only show up in production otherwise.
 */
func TestRegistryDefaultsKeepLostJobsRecordable(t *testing.T) {
	r := job.NewRegistry(nil)
	if r.RunningTTL <= r.StaleAfter {
		t.Fatalf("running TTL %v must exceed stale-after %v, or a stalled job vanishes instead of "+
			"being recorded as lost", r.RunningTTL, r.StaleAfter)
	}
	if r.StaleAfter < 2*r.RenewEvery {
		t.Fatalf("stale-after %v is under two renewal intervals (%v); one delayed call reads as death",
			r.StaleAfter, 2*r.RenewEvery)
	}
	if r.DoneTTL != 24*time.Hour {
		t.Fatalf("finished jobs are kept %v; the decision is 24h", r.DoneTTL)
	}
}

func TestKindIsTheFilterAClientUses(t *testing.T) {
	// THE VALUE IS A CONTRACT: a panel filtering on it must match what the extension writes.
	if Kind != "sandbox-job" {
		t.Fatalf("kind is %q; clients filter on \"sandbox-job\"", Kind)
	}
	// AND THE ID CARRIES IT, so an id alone says which extension to ask.
	if !strings.HasPrefix(Kind+".019abc", "sandbox-job.") {
		t.Fatal("the job id must start with its kind")
	}
}
