package tools

import "fmt"

/**
 * JobText renders the message a finished job leaves in the conversation.
 *
 * IT NAMES THE JOB, ITS STATE AND ITS EXIT CODE, AND NOTHING ELSE. The output is deliberately
 * absent: it is already on the tool-output stream, and a `job-output` call reads it on demand.
 * Copying it here would put the same bytes in front of the model twice and would mean writing a
 * second truncation policy beside the one the agent applies to tool results — two policies that
 * would drift.
 *
 * THE SHAPE IS THE ONE THE MODEL ALREADY KNOWS. `<job id=... state=...>` is what a tool result for
 * a finished job looks like, so a wake and a `job-wait` return are read the same way.
 */
func JobText(jobID, title string, state string, exit *int) string {
	exitAttr := ""
	if exit != nil {
		exitAttr = fmt.Sprintf(" exit=%q", fmt.Sprintf("%d", *exit))
	}
	how := ""
	switch state {
	case "done":
		// THE EXIT CODE CARRIES THE VERDICT, so this sentence does not. Saying "it failed" here
		// would repeat a judgement the job deliberately does not make — see State's documentation.
		how = "The job finished."
	case "killed":
		how = "The job was stopped."
	case "lost":
		// NOT A THING THIS EXTENSION PRODUCES — the sweeper writes it after this process is gone.
		// Rendered anyway, because an extension that comes back to a job of its own must be able
		// to describe it rather than printing an empty sentence.
		how = "The job stopped reporting; its result is unknown."
	default:
		how = "The job reported a state this reader does not recognise."
	}
	return fmt.Sprintf(
		"<job id=%q state=%q%s title=%q>\n%s\nUse job-output with this id to read what it produced.\n</job>",
		jobID, string(state), exitAttr, title, how,
	)
}

// NewMailboxPayload builds the wake for one finished job.
//
// A MAP, NOT A STRUCT, AND THAT IS NOT A STYLE CHOICE. `PublishMailboxEvent` puts the payload through
// `structpb.NewValue`, which accepts objects, arrays and scalars — NOT Go structs. A struct fails that
// conversion and the SDK folds the error to NullValue, so the message arrives with `payload: null`:
// delivered, folded as an empty user message, and indistinguishable from a notification the model
// ignored.
//
// TWO FIELDS, AND THE SPLIT IS WHAT THE CONSUMER NEEDS. The agent folds this into a user message:
// `text` becomes its body and `message_id` decides whether that has happened before. THE NAME IS
// SNAKE_CASE because that is what the agent's WakePayload decodes; a mismatch silently falls back to
// the mailbox row's own id, which is fresh on every delivery and so defeats the idempotence a derived
// id exists for.
func NewMailboxPayload(jobID, title string, state string, exit *int) map[string]any {
	return map[string]any{
		// DERIVED FROM THE JOB, not generated: a job has exactly one ending, so a wake about it
		// should fold at most once no matter how many times it is delivered.
		"message_id": "job-done-" + jobID,
		"text":       JobText(jobID, title, state, exit),
	}
}
