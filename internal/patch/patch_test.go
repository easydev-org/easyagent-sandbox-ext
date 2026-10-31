package patch

import (
	"strings"
	"testing"
)

// A patch that does all three things parses into all three operations.
func TestParseAllThreeKinds(t *testing.T) {
	ops, err := ParsePatch(`*** Begin Patch
*** Add File: a.txt
+one
+two
*** Update File: b.txt
@@ keep
 same
-old
+new
*** Delete File: c.txt
*** End Patch
`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(ops) != 3 {
		t.Fatalf("got %d ops, want 3", len(ops))
	}
	if ops[0].Kind != "add" || ops[0].Path != "a.txt" || ops[0].Content != "one\ntwo\n" {
		t.Errorf("add = %+v", ops[0])
	}
	if ops[1].Kind != "update" || len(ops[1].Hunks) != 1 || ops[1].Hunks[0].Anchor != "keep" {
		t.Errorf("update = %+v", ops[1])
	}
	if ops[2].Kind != "delete" || ops[2].Path != "c.txt" {
		t.Errorf("delete = %+v", ops[2])
	}
}

// PROSE BEFORE THE MARKERS IS TOLERATED, and prose after them is not: a model often introduces a
// patch, and nothing after the end marker can be an instruction.
func TestParseToleratesLeadingProse(t *testing.T) {
	ops, err := ParsePatch("Here is the change you asked for:\n\n*** Begin Patch\n*** Add File: x\n+y\n*** End Patch\n")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(ops) != 1 || ops[0].Path != "x" {
		t.Fatalf("ops = %+v", ops)
	}
}

// A PATCH WITHOUT ITS END MARKER IS REJECTED, not truncated: half a patch applied is worse than none.
func TestParseRejectsUnterminated(t *testing.T) {
	// WELL-FORMED INSIDE, MISSING ITS END. The point is what happens at the BOUNDARY: the parser had a
	// complete operation and must still refuse the document rather than hand it back.
	if _, err := ParsePatch("*** Begin Patch\n*** Add File: x\n+y\n"); err == nil ||
		!strings.Contains(err.Error(), "End Patch") {
		t.Fatalf("err = %v, want a complaint about the missing End Patch", err)
	}
}

// A MOVE IS REFUSED WITH ADVICE, because a Move silently overwrites and cannot be reviewed.
func TestParseRefusesMove(t *testing.T) {
	_, err := ParsePatch("*** Begin Patch\n*** Update File: a\n*** Move to: b\n*** End Patch\n")
	if err == nil || !strings.Contains(err.Error(), "Move to is not supported") {
		t.Fatalf("err = %v, want a refusal naming the alternative", err)
	}
}

func TestApplyHunksReplaces(t *testing.T) {
	old := "a\nb\nc\n"
	got, err := ApplyHunks(old, []PatchHunk{{Lines: []string{" b", "-c", "+C"}}})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if got != "a\nb\nC\n" {
		t.Errorf("got %q", got)
	}
}

// AN ANCHOR NAMES A LINE THE HUNK SITS BELOW. Searching from the anchor itself let the anchor's own
// line satisfy the context, which put the edit in the wrong place.
func TestApplyHunksAnchor(t *testing.T) {
	old := "def f():\n    a = 1\n\ndef g():\n    a = 1\n"
	got, err := ApplyHunks(old, []PatchHunk{
		{Anchor: "def g():", Lines: []string{"-    a = 1", "+    a = 2"}},
	})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if !strings.Contains(got, "def g():\n    a = 2") || !strings.Contains(got, "def f():\n    a = 1") {
		t.Errorf("the edit landed on the wrong function:\n%s", got)
	}
}

// AMBIGUOUS CONTEXT IS REFUSED. Two matches and one hunk means the patch could mean either, and
// choosing one is silent corruption.
func TestApplyHunksRefusesAmbiguous(t *testing.T) {
	old := "x = 1\nx = 1\n"
	_, err := ApplyHunks(old, []PatchHunk{{Lines: []string{"-x = 1", "+x = 2"}}})
	if err == nil || !strings.Contains(err.Error(), "more than once") {
		t.Fatalf("err = %v, want a refusal about ambiguity", err)
	}
}

// A CONTEXT LINE THAT IS NOT THERE IS AN ANSWER, not something to work around.
func TestApplyHunksRefusesMissingContext(t *testing.T) {
	_, err := ApplyHunks("a\nb\n", []PatchHunk{{Lines: []string{" zz", "-a", "+c"}}})
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("err = %v, want 'not found'", err)
	}
}

// A FILE WITHOUT A TRAILING NEWLINE KEEPS THAT PROPERTY, or the patch would add a newline the
// author did not write.
func TestApplyHunksKeepsMissingTrailingNewline(t *testing.T) {
	got, err := ApplyHunks("a\nb", []PatchHunk{{Lines: []string{"-b", "+B"}}})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if got != "a\nB" {
		t.Errorf("got %q, want no trailing newline", got)
	}
}

// A HUNK WITHOUT `@@` IS A HUNK. The removal lines locate the change; an anchor only narrows the
// search. Demanding one made a one-line replacement fail with a message about punctuation.
func TestUpdateWithoutAnchor(t *testing.T) {
	ops, err := ParsePatch("*** Begin Patch\n*** Update File: a.txt\n-old\n+new\n*** End Patch\n")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(ops) != 1 || len(ops[0].Hunks) != 1 {
		t.Fatalf("ops = %+v, want one update with one hunk", ops)
	}
	got, err := ApplyHunks("keep\nold\n", ops[0].Hunks)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if got != "keep\nnew\n" {
		t.Errorf("got %q", got)
	}
}

// AND IT IS STILL REFUSED WHEN THE REMOVAL MATCHES TWICE — dropping the anchor must not drop the
// uniqueness check.
func TestUpdateWithoutAnchorStillRefusesAmbiguity(t *testing.T) {
	ops, err := ParsePatch("*** Begin Patch\n*** Update File: a.txt\n-old\n+new\n*** End Patch\n")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if _, err := ApplyHunks("old\nold\n", ops[0].Hunks); err == nil ||
		!strings.Contains(err.Error(), "more than once") {
		t.Fatalf("err = %v, want a refusal about ambiguity", err)
	}
}
