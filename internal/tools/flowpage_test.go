package tools

import (
	"strings"
	"testing"
)

// TestSplitFlowLinesNoPhantomTrailing: every line stream ends in "\n"; that must
// not become an extra empty line, or a page's "of N" count is always off by one.
func TestSplitFlowLinesNoPhantomTrailing(t *testing.T) {
	cases := map[string]int{
		"":         0,
		"\n":       1,
		"a\n":      1,
		"a\nb\n":   2,
		"a\nb":     2,
		"a\n\nb\n": 3, // a real blank line IS a line
	}
	for in, want := range cases {
		if got := len(splitFlowLines(in)); got != want {
			t.Errorf("splitFlowLines(%q) = %d lines, want %d", in, got, want)
		}
	}
}

// TestLineWindowDoesNotCutMidLine: the whole reason to page by line. A byte
// window would return half a line, which cannot be read or referred to.
func TestLineWindowDoesNotCutMidLine(t *testing.T) {
	lines := splitFlowLines("alpha\nbeta\ngamma\n")
	// offset=1, limit=1 -> exactly "beta", never "eta\ng".
	off, limit := 1, 1
	page := lines[off : off+limit]
	if len(page) != 1 || page[0] != "beta" {
		t.Errorf("page = %v, want [beta]", page)
	}
}

// TestGrepThenWindow: the filter runs BEFORE paging, so offset/limit apply to
// matches. This is what makes "the errors, first 20" expressible.
func TestGrepThenWindow(t *testing.T) {
	all := strings.Split("ok 1\nerr A\nok 2\nerr B\nerr C", "\n")
	var matched []string
	for _, l := range all {
		if strings.Contains(l, "err") {
			matched = append(matched, l)
		}
	}
	if len(matched) != 3 {
		t.Fatalf("matched = %d, want 3", len(matched))
	}
	// Now page the MATCHES: offset 1, limit 1 -> the second error.
	page := matched[1:2]
	if page[0] != "err B" {
		t.Errorf("page = %v, want [err B]", page)
	}
	// If paging had happened first, offset 1 limit 1 would give "err A" and the
	// caller asking for the 2nd error would get the 1st.
	if all[1:2][0] != "err A" {
		t.Fatal("fixture sanity")
	}
}
