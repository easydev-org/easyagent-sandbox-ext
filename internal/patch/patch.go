package patch

import (
	"fmt"
	"strings"
)

// Applying a multi-file patch to the staging area.
//
// ONE tool edits files, and it takes a patch rather than a whole file. Two reasons:
//
//   - a patch makes the intent legible. "replace line 12 of X" is reviewable in the tool call;
//     resending a 400-line file is not, and a reviewer cannot see what changed.
//   - it is one tool instead of three. A separate write/delete/move set gives a caller three
//     ways to do the same thing and three chances to pick the wrong one.
//
// THE GRAMMAR IS THE SAME ONE gitdev-ext USES, deliberately: the sandbox tools and the git tools are
// different products, and a model that has learned to write a patch for one should not have to learn a
// second dialect for the other.
//
//	*** Begin Patch
//	*** Add File: path/to/new.go
//	+package thing
//	*** Update File: path/to/existing.go
//	@@ func main() {
//	 	unchanged context
//	-old line
//	+new line
//	*** Delete File: path/to/gone.go
//	*** End Patch

// PatchOp is one file operation parsed out of a patch.
type PatchOp struct {
	// Kind is "add", "update" or "delete".
	Kind string
	// Path is the file the operation targets.
	Path string
	// Content is the whole new file, for "add".
	Content string
	// Hunks are the edits, for "update".
	Hunks []PatchHunk
}

// PatchHunk is one contiguous edit.
type PatchHunk struct {
	// Anchor is the text of the `@@` line, used to find where the hunk applies. Empty means
	// "search from the top".
	Anchor string
	// Lines carry a leading ' ', '-' or '+'.
	Lines []string
}

// ParsePatch reads a patch into operations.
//
// Rejects a patch it does not fully understand rather than applying the part it did: a
// half-applied patch is worse than a rejected one, because the caller believes the edit landed.
func ParsePatch(text string) ([]PatchOp, error) {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	var ops []PatchOp
	var cur *PatchOp
	var hunk *PatchHunk
	inPatch := false

	flush := func() {
		if cur == nil {
			return
		}
		if hunk != nil {
			cur.Hunks = append(cur.Hunks, *hunk)
			hunk = nil
		}
		ops = append(ops, *cur)
		cur = nil
	}

	for i, raw := range lines {
		line := raw
		switch {
		case strings.TrimSpace(line) == "*** Begin Patch":
			if inPatch {
				return nil, fmt.Errorf("line %d: nested *** Begin Patch", i+1)
			}
			inPatch = true
			continue
		case strings.TrimSpace(line) == "*** End Patch":
			if !inPatch {
				return nil, fmt.Errorf("line %d: *** End Patch without a begin", i+1)
			}
			flush()
			return ops, nil
		}
		if !inPatch {
			// Tolerate prose before the patch: a model often introduces it. Anything between
			// the markers is strict, so the leniency stops at the boundary.
			continue
		}

		switch {
		case strings.HasPrefix(line, "*** Add File: "):
			flush()
			cur = &PatchOp{Kind: "add", Path: strings.TrimSpace(line[len("*** Add File: "):])}
		case strings.HasPrefix(line, "*** Update File: "):
			flush()
			cur = &PatchOp{Kind: "update", Path: strings.TrimSpace(line[len("*** Update File: "):])}
		case strings.HasPrefix(line, "*** Delete File: "):
			flush()
			cur = &PatchOp{Kind: "delete", Path: strings.TrimSpace(line[len("*** Delete File: "):])}
		case strings.HasPrefix(line, "*** Move to: "):
			flush()
			return nil, fmt.Errorf("line %d: *** Move to is not supported; delete the old path and add the new one in the same patch", i+1)
		case strings.HasPrefix(line, "@@"):
			if cur == nil || cur.Kind != "update" {
				return nil, fmt.Errorf("line %d: @@ outside an Update File section", i+1)
			}
			if hunk != nil {
				cur.Hunks = append(cur.Hunks, *hunk)
			}
			hunk = &PatchHunk{Anchor: strings.TrimSpace(strings.TrimPrefix(line, "@@"))}
		default:
			if cur == nil {
				if strings.TrimSpace(line) == "" {
					continue
				}
				return nil, fmt.Errorf("line %d: content outside a file section", i+1)
			}
			switch cur.Kind {
			case "add":
				// A BLANK LINE IS NOT CONTENT, IT IS SPACING. Every patch a model writes tends to have
				// one before the next `***` marker, and rejecting the document for it made a
				// well-formed patch fail with "Add File lines must start with '+'". Blank CONTENT is
				// written as `+` on its own line, which still works.
				if line == "" {
					continue
				}
				if !strings.HasPrefix(line, "+") {
					return nil, fmt.Errorf("line %d: Add File lines must start with '+'", i+1)
				}
				cur.Content += strings.TrimPrefix(line, "+") + "\n"
			case "delete":
				// Body is ignored; the path is the whole instruction.
			case "update":
				if hunk == nil {
					if strings.TrimSpace(line) == "" {
						continue
					}
					// A HUNK WITH NO `@@` IS STILL A HUNK. `-old` / `+new` says exactly where the change
					// goes — the removal lines ARE the location — and demanding an anchor for it made a
					// perfectly reviewable one-line replacement fail with a message about punctuation.
					//
					// The anchor only narrows the search; ApplyHunks already refuses an ambiguous match,
					// so a hunk without one is not less safe, only less directed.
					if c := line[0]; c != ' ' && c != '-' && c != '+' {
						return nil, fmt.Errorf("line %d: hunk line must start with ' ', '-' or '+'", i+1)
					}
					hunk = &PatchHunk{}
				}
				if line == "" {
					// An empty line inside a hunk is a blank CONTEXT line in every patch
					// format; treating it as anything else silently drops it.
					hunk.Lines = append(hunk.Lines, " ")
					continue
				}
				if c := line[0]; c != ' ' && c != '-' && c != '+' {
					return nil, fmt.Errorf("line %d: hunk line must start with ' ', '-' or '+'", i+1)
				}
				hunk.Lines = append(hunk.Lines, line)
			}
		}
	}
	return nil, fmt.Errorf("patch has no *** End Patch")
}

// ApplyHunks applies one file's hunks to its current content.
//
// Every context and removal line must be found EXACTLY ONCE. A patch whose context matches
// twice is ambiguous, and applying it to the wrong place is the kind of silent corruption that
// a "best effort" implementation produces.
func ApplyHunks(old string, hunks []PatchHunk) (string, error) {
	hadTrailing := strings.HasSuffix(old, "\n")
	cur := strings.Split(strings.TrimSuffix(old, "\n"), "\n")
	if old == "" {
		cur = nil
	}

	for hi, h := range hunks {
		// Locate the hunk: the anchor narrows the search, then the leading context must match.
		start, err := findHunkStart(cur, h)
		if err != nil {
			return "", fmt.Errorf("hunk %d (@@ %s): %w", hi+1, h.Anchor, err)
		}
		// Build the replacement for the matched window.
		var out []string
		idx := start
		for _, l := range h.Lines {
			switch l[0] {
			case ' ':
				// Bounds-checked via a helper: a malformed hunk that runs past the end must be
				// reported, not panic the process hosting it.
				have, ok := lineAt(cur, idx)
				if !ok || have != l[1:] {
					return "", fmt.Errorf("hunk %d: context mismatch at line %d", hi+1, idx+1)
				}
				out = append(out, cur[idx])
				idx++
			case '-':
				have, ok := lineAt(cur, idx)
				if !ok || have != l[1:] {
					return "", fmt.Errorf("hunk %d: removal mismatch at line %d (want %q, have %q)",
						hi+1, idx+1, l[1:], have)
				}
				idx++
			case '+':
				out = append(out, l[1:])
			}
		}
		cur = append(append(append([]string{}, cur[:start]...), out...), cur[idx:]...)
	}

	res := strings.Join(cur, "\n")
	if hadTrailing && !strings.HasSuffix(res, "\n") {
		res += "\n"
	}
	return res, nil
}

// findHunkStart returns the index where a hunk's leading context begins.
// lineAt reads a line without panicking past the end.
func lineAt(lines []string, i int) (string, bool) {
	if i < 0 || i >= len(lines) {
		return "", false
	}
	return lines[i], true
}

func findHunkStart(lines []string, h PatchHunk) (int, error) {
	// Context BEFORE the first change tells us where to look.
	var lead []string
	for _, l := range h.Lines {
		if l[0] != ' ' {
			break
		}
		lead = append(lead, l[1:])
	}
	from := 0
	if h.Anchor != "" {
		// The anchor names a line the hunk sits BELOW (a function signature, a heading). Start
		// searching at the line AFTER it — starting AT it makes the anchor's own line eligible
		// to satisfy the leading context, which put the edit on the anchor instead of in the
		// body.
		found := false
		for i, l := range lines {
			if strings.Contains(l, h.Anchor) {
				from = i + 1
				found = true
				break
			}
		}
		if !found {
			return 0, fmt.Errorf("anchor %q not found (the file may have changed)", h.Anchor)
		}
	}
	if len(lead) == 0 {
		// NO LEADING CONTEXT STILL NEEDS A UNIQUENESS CHECK — this returned `from` unconditionally,
		// which meant a hunk of nothing but `-line`/`+line` was applied to the FIRST match without
		// looking for a second one. In a file with the same line twice, the edit landed at the top and
		// the caller was told it succeeded.
		//
		// So the whole hunk's CONTEXT AND REMOVAL lines are used as the pattern here. `+` lines are
		// excluded because they do not exist in the file yet.
		need := make([]string, 0, len(h.Lines))
		for _, l := range h.Lines {
			if l[0] == ' ' || l[0] == '-' {
				need = append(need, l[1:])
			}
		}
		if len(need) == 0 {
			// A hunk that only adds lines has nothing to locate; it means "insert here", and `from`
			// (the start, or just past the anchor) is the answer.
			return from, nil
		}
		var only = -1
		for i := from; i+len(need) <= len(lines); i++ {
			match := true
			for j, want := range need {
				if lines[i+j] != want {
					match = false
					break
				}
			}
			if !match {
				continue
			}
			if only >= 0 {
				return 0, fmt.Errorf("the change matches more than once; add surrounding context to disambiguate")
			}
			only = i
		}
		if only < 0 {
			return 0, fmt.Errorf("context not found (the file may have changed)")
		}
		return only, nil
	}
	var found = -1
	for i := from; i+len(lead) <= len(lines); i++ {
		match := true
		for j, want := range lead {
			if lines[i+j] != want {
				match = false
				break
			}
		}
		if !match {
			continue
		}
		if found >= 0 {
			return 0, fmt.Errorf("context matches more than once; add surrounding context to disambiguate")
		}
		found = i
	}
	if found < 0 {
		return 0, fmt.Errorf("context not found (the file may have changed)")
	}
	return found, nil
}
