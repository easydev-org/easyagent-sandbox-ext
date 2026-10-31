package tools

import (
	"context"
	"fmt"
	"strings"

	easybus "github.com/easydev-org/easybus-sdk"
	"github.com/easydev-org/easybus-sdk/extension"

	workerv1 "github.com/easydev-org/easyagent-sandbox-ext/gen/worker/v1"
	"github.com/easydev-org/easyagent-sandbox-ext/internal/patch"
)

/**
 * filePatch edits, creates and deletes files, from ONE patch document.
 *
 * IT REPLACES file-write, file-delete, file-move AND file-copy — four tools that did the same thing
 * four ways, so a caller had four chances to pick the wrong one. A patch says what the change IS:
 * "replace these lines" is reviewable in the tool call, where resending a 400-line file is not, and a
 * reviewer cannot see what changed.
 *
 * NOTHING IS WRITTEN UNTIL EVERY OPERATION HAS BEEN PROVEN. The whole patch is resolved against the
 * files as they are and only then applied, so a rejected patch — one bad hunk, one missing anchor —
 * leaves the workspace exactly as it was. A half-applied patch is worse than a rejected one, because
 * the caller believes the edit landed.
 *
 * THE GRAMMAR IS THE ONE gitdev-ext USES:
 *
 *	*** Begin Patch
 *	*** Add File: notes/todo.md
 *	+first line
 *	*** Update File: app.py
 *	@@ def main():
 *	     unchanged context
 *	-    old line
 *	+    new line
 *	*** Delete File: scratch.txt
 *	*** End Patch
 *
 * A MOVE IS A DELETE AND AN ADD IN THE SAME PATCH, and saying so is better than a `*** Move to:` that
 * only works when the destination is empty — the two operations are what actually happen, and spelling
 * them out makes the result visible before it runs.
 */
func (t *toolset) filePatch(ctx context.Context, args map[string]any, callID, session, tenant string) (extension.ToolResultData, error) {
	s, err := t.resolve(args, session, tenant)
	if err != nil {
		return extension.ToolResultData{}, err
	}
	if err := extension.Require(ctx, PermFileWrite, map[string]string{"sandbox": s.Name}); err != nil {
		return extension.ToolResultData{}, err
	}
	text := argStr(args, "patch")
	if strings.TrimSpace(text) == "" {
		return extension.ToolResultData{}, &extension.TypedError{
			Code: easybus.ToolResultErrorCodeInvalidArgument,
			Message: "patch is required: a *** Begin Patch / *** End Patch document. " +
				"`*** Add File:`, `*** Update File:` with `@@` hunks, and `*** Delete File:` are the operations.",
		}
	}
	ops, perr := patch.ParsePatch(text)
	if perr != nil {
		return extension.ToolResultData{}, &extension.TypedError{
			Code: easybus.ToolResultErrorCodeInvalidArgument, Message: perr.Error(),
		}
	}
	if len(ops) == 0 {
		return extension.ToolResultData{}, &extension.TypedError{
			Code: easybus.ToolResultErrorCodeInvalidArgument, Message: "the patch contains no file operations",
		}
	}

	cl := t.deps.Cache.Worker(ep(s))

	// THE PLAN IS BUILT FIRST: every hunk resolved against the file as it is now, nothing written yet.
	type planned struct {
		kind    string
		path    string
		content string // for add and update
	}
	plan := make([]planned, 0, len(ops))
	for _, op := range ops {
		path := strings.TrimSpace(op.Path)
		if path == "" {
			return extension.ToolResultData{}, &extension.TypedError{
				Code: easybus.ToolResultErrorCodeInvalidArgument, Message: "an operation names no path",
			}
		}
		switch op.Kind {
		case "add":
			// An Add over an existing file would silently discard it. Refusing is the only honest
			// answer: the caller either meant Update, or meant to delete first.
			if _, rerr := cl.FileRead(ctx, &workerv1.FileReadRequest{Path: path}); rerr == nil {
				return extension.ToolResultData{}, &extension.TypedError{
					Code: easybus.ToolResultErrorCodeBusiness,
					Message: fmt.Sprintf("%s already exists; use `*** Update File:` to change it", path),
				}
			}
			plan = append(plan, planned{kind: "add", path: path, content: op.Content})

		case "update":
			cur, rerr := cl.FileRead(ctx, &workerv1.FileReadRequest{Path: path})
			if rerr != nil {
				return extension.ToolResultData{}, &extension.TypedError{
					Code:    easybus.ToolResultErrorCodeNotFound,
					Message: fmt.Sprintf("%s cannot be read, so it cannot be updated: %v", path, rerr),
				}
			}
			old := string(cur.GetContent())
			next, herr := patch.ApplyHunks(old, op.Hunks)
			if herr != nil {
				return extension.ToolResultData{}, &extension.TypedError{
					Code:    easybus.ToolResultErrorCodeInvalidArgument,
					Message: fmt.Sprintf("%s: %v", path, herr),
				}
			}
			plan = append(plan, planned{kind: "update", path: path, content: next})

		case "delete":
			plan = append(plan, planned{kind: "delete", path: path})
		}
	}

	// AND ONLY NOW DOES ANYTHING CHANGE. A failure part way through is reported with what was already
	// done, because a caller needs to know the workspace is no longer what it was.
	applied := 0
	for _, p := range plan {
		var werr error
		switch p.kind {
		case "add", "update":
			_, werr = cl.FileWrite(ctx, &workerv1.FileWriteRequest{Path: p.path, Content: []byte(p.content)})
		case "delete":
			_, werr = cl.FileDelete(ctx, &workerv1.FileDeleteRequest{Path: p.path})
		}
		if werr != nil {
			return extension.ToolResultData{}, &extension.TypedError{
				Code: easybus.ToolResultErrorCodeRetryable,
				Message: fmt.Sprintf("%s failed after %d of %d operation(s) had been applied: %v",
					p.path, applied, len(plan), werr),
			}
		}
		applied++
	}

	var b strings.Builder
	for _, p := range plan {
		verb := map[string]string{"add": "created", "update": "updated", "delete": "deleted"}[p.kind]
		fmt.Fprintf(&b, "%-8s %s\n", verb, p.path)
	}
	fmt.Fprintf(&b, "\n%d file(s)", len(plan))

	rows := make([]map[string]any, 0, len(plan))
	for _, p := range plan {
		rows = append(rows, map[string]any{"kind": p.kind, "path": p.path})
	}
	return extension.ToolResultData{
		Content: b.String(),
		Data:    map[string]any{"changes": rows},
	}, nil
}
