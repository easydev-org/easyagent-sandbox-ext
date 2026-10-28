package tools

import (
	"net/url"
	"strings"
)

// The extension's own UI id. Matches the Service (`sandbox-ext`), the mount (`/ext/sandbox`) and the
// permission namespace; one name, so a mismatch shows up as a 404 rather than as wrong data.
const uiExtID = "sandbox"

/**
 * sandboxView points at this extension's panel, on one machine.
 *
 * WHY THE TOOL AND NOT THE MANIFEST: the sandbox NAME is resolved by the tool — a call may name it,
 * or fall back to the only configured one — so a static template would have to guess. The tool knows
 * which machine it just used.
 *
 * Returns nil when there is no machine to point at, so a call that failed before resolving one emits
 * no button rather than a link to a panel that would open on nothing.
 */
func sandboxView(name string) map[string]any {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil
	}
	return map[string]any{
		"ext":   uiExtID,
		"route": "/sb/" + url.PathEscape(name),
		"title": name,
	}
}

/**
 * jobView points at one machine's job output.
 *
 * A job is the thing the caller just waited for, so opening the panel anywhere else would make them
 * find it again. Both values are needed: a job id is only unique within a machine.
 */
func jobView(name, jobID string) map[string]any {
	name = strings.TrimSpace(name)
	jobID = strings.TrimSpace(jobID)
	if name == "" || jobID == "" {
		return nil
	}
	return map[string]any{
		"ext":   uiExtID,
		"route": "/sb/" + url.PathEscape(name) + "/job/" + url.PathEscape(jobID),
		"title": name + " · " + jobID,
	}
}
