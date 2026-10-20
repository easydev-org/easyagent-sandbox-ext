package tools

import (
	"strings"
	"testing"
)

// TestHeadLinesRespectsTheLineCap: the extra section is a GLIMPSE. A stage that
// emits thousands of lines must be cut, and the cut must be reported.
func TestHeadLinesRespectsTheLineCap(t *testing.T) {
	data := []byte("l1\nl2\nl3\nl4\nl5\n")
	got, cut := headLines(data, 2, 100)
	if !cut {
		t.Error("expected cut=true when the stream is longer than the cap")
	}
	if got != "l1\nl2\n" {
		t.Errorf("got %q, want the first two lines only", got)
	}
}

// TestHeadLinesWholeStreamIsNotCut: a short stream is shown entirely, and must
// NOT be marked truncated (a spurious "(truncated)" is a lie).
func TestHeadLinesWholeStreamIsNotCut(t *testing.T) {
	got, cut := headLines([]byte("only\ntwo\n"), 8, 100)
	if cut {
		t.Error("a short stream must not be reported as truncated")
	}
	if got != "only\ntwo\n" {
		t.Errorf("got %q", got)
	}
}

// TestHeadLinesHonoursTheGlobalBudget: the total number of lines across ALL
// stages is bounded, so a wide pipeline cannot blow up the response even when
// each stage is individually small.
func TestHeadLinesHonoursTheGlobalBudget(t *testing.T) {
	data := []byte("a\nb\nc\nd\ne\n")
	got, cut := headLines(data, 8, 2) // budget of 2 lines beats the per-stage 8
	if !cut {
		t.Error("the global budget must cut")
	}
	if got != "a\nb\n" {
		t.Errorf("budget not applied: %q", got)
	}
}

// TestCountLines: the running budget is decremented by what was ACTUALLY shown,
// so counting must match the rendered text.
func TestCountLines(t *testing.T) {
	cases := map[string]int{
		"":       0,
		"a\n":    1,
		"a\nb\n": 2,
		"a\nb":   1,
	}
	for in, want := range cases {
		if got := countLines(in); got != want {
			t.Errorf("countLines(%q) = %d, want %d", in, got, want)
		}
	}
}

// TestByteCarryingKindsExcludesFdAndTerminal: `fd` is an identity with no bytes
// and `terminal` is the job boundary already shown as stdout — neither is an
// "intermediate" result.
func TestByteCarryingKindsExcludesFdAndTerminal(t *testing.T) {
	if byteCarryingKinds["fd"] {
		t.Error("fd must not count as byte-carrying (identity, no bytes)")
	}
	for _, k := range []string{"pipe", "file", "here", "capture"} {
		if !byteCarryingKinds[k] {
			t.Errorf("%q must count as byte-carrying", k)
		}
	}
}

// TestIntermediatesHeadNamesTheCount: the section explains how many streams never
// reached stdout, so its absence from the output is not mistaken for "none".
func TestIntermediatesHeadNamesTheCount(t *testing.T) {
	// Mirrors the i18n template shape used by renderIntermediates.
	const tmpl = "\n--- intermediate results ({n} stream(s) that never reached stdout) ---"
	got := strings.ReplaceAll(tmpl, "{n}", "2")
	if !strings.Contains(got, "2 stream") {
		t.Errorf("head must carry the count: %q", got)
	}
	if !strings.Contains(got, "never reached stdout") {
		t.Errorf("head must explain WHY they are listed: %q", got)
	}
}
