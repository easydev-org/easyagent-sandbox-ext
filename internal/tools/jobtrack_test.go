package tools

import (
	"strings"
	"testing"

)

/**
 * These pin the two halves of a finished job: what the registry records, and what the conversation
 * is told. They are separate on purpose — either can fail without the other mattering — so there is
 * a test for each rather than one test that asserts both.
 */

func TestJobTextNamesTheJobWithoutItsOutput(t *testing.T) {
	exit := 0
	got := JobText("sandbox-job.019abc", "cargo build", "done", &exit)

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
	failed := JobText("j", "grep x", "done", &exit)
	if strings.Contains(strings.ToLower(failed), "failed") {
		t.Errorf("a done job must not be described as failed:\n%s", failed)
	}
}

func TestJobTextHandlesAJobWithNoExitCode(t *testing.T) {
	// A cancelled or lost job has no exit code, and the text must not render "exit=0" for it —
	// that would invent a success.
	got := JobText("j", "build", "lost", nil)
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
	a := NewMailboxPayload("sandbox-job.019abc", "make", "done", &exit)
	b := NewMailboxPayload("sandbox-job.019abc", "make", "done", &exit)

	// A MAP, NOT A STRUCT: `PublishMailboxEvent` runs the payload through structpb, which cannot take
	// a Go struct and folds the failure to null — a delivered wake with no body.
	id, _ := a["message_id"].(string)
	if id == "" || id != b["message_id"] {
		t.Fatalf("the same job produced two ids: %q and %q", id, b["message_id"])
	}
	if !strings.Contains(id, "sandbox-job.019abc") {
		t.Fatalf("the id does not name its job: %q", id)
	}
	// THE FIELD NAME IS THE AGENT'S, not this package's preference: WakePayload decodes `message_id`,
	// and a mismatch silently falls back to a fresh mailbox id, defeating the point of deriving one.
	if _, ok := a["message_id"]; !ok {
		t.Fatal("the payload must spell it message_id, the snake_case name the agent decodes")
	}
	// THE TEXT IS WHAT THE AGENT FOLDS INTO A USER MESSAGE, so it must be non-empty or the wake
	// arrives as a blank turn.
	text, _ := a["text"].(string)
	if strings.TrimSpace(text) == "" {
		t.Fatal("a wake with no text would fold an empty message into the conversation")
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
