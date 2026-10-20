package tools

import (
	"strings"
	"testing"

	workerv1 "github.com/easydev-org/easyagent-sandbox-ext/gen/worker/v1"
)

// TestRenderBytesNumbersText: a text stream is shown with line numbers so a
// caller can point at a line; that is the point of reading an intermediate flow.
func TestRenderBytesNumbersText(t *testing.T) {
	out := renderBytes([]byte("alpha\nbeta\n"))
	if !strings.Contains(out, "1| alpha") || !strings.Contains(out, "2| beta") {
		t.Errorf("lines must be numbered:\n%s", out)
	}
	// The trailing newline must not produce a phantom empty numbered line.
	if strings.Contains(out, "3|") {
		t.Errorf("trailing newline produced an extra line:\n%s", out)
	}
}

// TestRenderBytesRefusesBinary: dumping binary bytes corrupts the response and
// helps nobody, so it is reported by size instead.
func TestRenderBytesRefusesBinary(t *testing.T) {
	out := renderBytes([]byte{'a', 0, 'b', 0})
	if !strings.Contains(out, "binary") || !strings.Contains(out, "4") {
		t.Errorf("binary must be reported by size, got %q", out)
	}
	if strings.Contains(out, "0|") {
		t.Errorf("binary must not be numbered as text: %q", out)
	}
}

// TestTruncCellMarksTheCut: a fixed-width table must not silently lose the tail
// of a long command name — a `~` says "there is more".
func TestTruncCellMarksTheCut(t *testing.T) {
	if got := truncCell("short", 24); got != "short" {
		t.Errorf("short cell changed: %q", got)
	}
	long := "a-very-long-producer-command-name"
	got := truncCell(long, 24)
	if len([]rune(got)) != 24 {
		t.Errorf("cell width = %d, want 24 (%q)", len([]rune(got)), got)
	}
	if !strings.HasSuffix(got, "~") {
		t.Errorf("a cut cell must be marked: %q", got)
	}
}

// TestFlowsToDataIsSortedByFID: two runs of the same pipeline must produce the
// same ordering, or a diff between them is noise.
func TestFlowsToDataIsSortedByFID(t *testing.T) {
	in := []*workerv1.Flow{
		{Fid: 7, Kind: "file"},
		{Fid: 2, Kind: "pipe"},
		{Fid: 5, Kind: "capture"},
	}
	out := flowsToData(in)
	for i := 1; i < len(out); i++ {
		if out[i-1]["fid"].(int64) > out[i]["fid"].(int64) {
			t.Fatalf("not sorted by fid: %v", out)
		}
	}
	if out[0]["fid"].(int64) != 2 || out[2]["fid"].(int64) != 7 {
		t.Errorf("order = %v", out)
	}
}

// TestOrDash: an empty producer/consumer is rendered as "-", not as a blank cell
// that looks like a formatting bug.
func TestOrDash(t *testing.T) {
	if orDash("") != "-" {
		t.Error("empty must render as dash")
	}
	if orDash("echo") != "echo" {
		t.Error("non-empty must pass through")
	}
}

// TestSplitEdgesKeepsOnlyDataPaths: a 3-stage pipeline produces one stderr edge
// per stage pointing at the job's own stderr — identical wiring that says nothing
// about what the job did. Only edges touching a byte-carrying flow are data.
func TestSplitEdgesKeepsOnlyDataPaths(t *testing.T) {
	flows := []*workerv1.Flow{
		{Fid: 1, Kind: "fd", Fd: "stdin"},  // job stdin
		{Fid: 2, Kind: "fd", Fd: "stdout"}, // job stdout
		{Fid: 3, Kind: "fd", Fd: "stderr"}, // job stderr
		{Fid: 4, Kind: "pipe"},             // echo | wc
		{Fid: 5, Kind: "fd", Fd: "stdin"},  // wc stdin
		{Fid: 6, Kind: "fd", Fd: "stdout"}, // wc stdout
		{Fid: 8, Kind: "file"},             // > count.txt
	}
	edges := []*workerv1.FlowEdge{
		{FromFid: 6, ToFid: 8}, // data: wc stdout -> file
		{FromFid: 4, ToFid: 5}, // data: pipe -> wc stdin
		{FromFid: 1, ToFid: 5}, // inherited: stdin from job
		{FromFid: 3, ToFid: 3}, // inherited-ish: stderr wiring
	}
	data, inherited := splitEdges(edges, flows)
	if len(data) != 2 {
		t.Errorf("data edges = %d, want 2", len(data))
	}
	if inherited != 2 {
		t.Errorf("inherited = %d, want 2", inherited)
	}
	// The data edges must be exactly the two that touch pipe/file.
	for _, e := range data {
		from, to := e.GetFromFid(), e.GetToFid()
		if !((from == 6 && to == 8) || (from == 4 && to == 5)) {
			t.Errorf("unexpected data edge f%d->f%d", from, to)
		}
	}
}

// TestSplitEdgesNoData: a job with no pipe or redirect has no data paths, and
// the caller must be told that rather than shown an empty section.
func TestSplitEdgesNoData(t *testing.T) {
	flows := []*workerv1.Flow{
		{Fid: 1, Kind: "fd", Fd: "stdin"},
		{Fid: 2, Kind: "fd", Fd: "stdout"},
	}
	edges := []*workerv1.FlowEdge{{FromFid: 1, ToFid: 2}}
	data, inherited := splitEdges(edges, flows)
	if len(data) != 0 {
		t.Errorf("data edges = %d, want 0", len(data))
	}
	if inherited != 1 {
		t.Errorf("inherited = %d, want 1", inherited)
	}
}

// TestRenderEdgeNamesBothEnds: a bare "f6->f8" forces the reader to cross-check
// two table rows; the arrow must say who wrote and what read it.
func TestRenderEdgeNamesBothEnds(t *testing.T) {
	flows := []*workerv1.Flow{
		{Fid: 6, Kind: "fd", Fd: "stdout", Producer: "wc -w"},
		{Fid: 8, Kind: "file", Producer: "wc -w", Path: "/tmp/count.txt"},
	}
	out := renderEdge(&workerv1.FlowEdge{FromFid: 6, ToFid: 8}, flows)
	if !strings.Contains(out, "f6 -> f8") {
		t.Errorf("must name both fids: %q", out)
	}
	if !strings.Contains(out, "wc -w") {
		t.Errorf("must name the command: %q", out)
	}
	if !strings.Contains(out, "=>") {
		t.Errorf("must show the arrow: %q", out)
	}
}
