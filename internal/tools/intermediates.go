package tools

import (
	"context"
	"fmt"
	"sort"
	"strings"

	workerv1 "github.com/easydev-org/easyagent-sandbox-ext/gen/worker/v1"
	"github.com/easydev-org/easyagent-sandbox-ext/internal/i18n"
	"github.com/easydev-org/easyagent-sandbox-ext/internal/sandbox"
)

// Bounds on the extra section. The point is a GLIMPSE of the pipeline, not a
// second full output: `in | grep | tr` can produce megabytes per stage, and a
// tool response has to stay small enough to be read.
const (
	defaultIntermediateFlows  = 3
	defaultIntermediateLines  = 8
	maxIntermediateFlows      = 10
	maxIntermediateLinesTotal = 60
)

// renderIntermediates summarises the flows that CARRIED DATA but never reached
// the job's stdout.
//
// WHY: `JobOutput` shows a job's stdout/stderr, and in a pipeline the upstream
// stages' stdout goes into a pipe instead — so a three-stage command reports only
// the last one. Measured: `printf 'alpha\nbeta\n' | grep beta | tr a-z A-Z`
// yields stdout ["BETA"], while the two pipes hold "alpha\nbeta\n" and "beta\n".
// Without them a reader cannot tell whether grep or tr produced the result, or
// whether printf emitted what was expected.
//
// It is deliberately bounded and best-effort: if the graph or one flow cannot be
// read, the section says so and the stdout output (the primary answer) is
// untouched.
func renderIntermediates(ctx context.Context, cl sandbox.WorkerClient, jobID string, maxFlows, maxLines int) string {
	if maxFlows <= 0 {
		return ""
	}
	if maxFlows > maxIntermediateFlows {
		maxFlows = maxIntermediateFlows
	}
	if maxLines <= 0 {
		maxLines = defaultIntermediateLines
	}

	g, err := cl.JobGraph(ctx, &workerv1.JobGraphRequest{JobId: jobID})
	if err != nil {
		// Best-effort, and SILENTLY so — unlike the stdout read, which reports its failure.
		//
		// The difference is what the reader would conclude. An empty stdout looks like "the command
		// printed nothing", which is a wrong answer; a missing dataflow section looks like "this
		// job had no pipes", which is at worst incomplete. The primary answer is unaffected, so
		// spending the caller's attention on it would be worse than omitting it.
		return ""
	}

	// Only flows that moved bytes and are NOT the job's own stdout/stderr/fds.
	// `fd` is a stage's fd identity (no bytes) and `terminal` is the job boundary
	// — both are already represented by the output above.
	var picked []*workerv1.Flow
	for _, f := range g.GetFlows() {
		if f.GetBytes() <= 0 {
			continue
		}
		if !byteCarryingKinds[f.GetKind()] || f.GetKind() == "terminal" {
			continue
		}
		picked = append(picked, f)
	}
	if len(picked) == 0 {
		return ""
	}
	sort.Slice(picked, func(i, j int) bool { return picked[i].GetFid() < picked[j].GetFid() })
	total := len(picked)
	more := 0
	if len(picked) > maxFlows {
		picked = picked[:maxFlows]
		more = total - len(picked)
	}

	var b strings.Builder
	b.WriteString(i18n.T("en", i18n.MsgIntermediatesHead, i18n.Params{
		"n": fmt.Sprintf("%d", total),
	}))
	b.WriteString("\n")

	linesLeft := maxIntermediateLinesTotal
	for _, f := range picked {
		if linesLeft <= 0 {
			break
		}
		fr, ferr := cl.JobFlow(ctx, &workerv1.JobFlowRequest{JobId: jobID, Fid: f.GetFid()})
		if ferr != nil {
			fmt.Fprintf(&b, "\n  f%d %s: (unreadable: %v)\n", f.GetFid(), f.GetKind(), ferr)
			continue
		}
		data := fr.GetData()
		shown, cut := headLines(data, maxLines, linesLeft)
		linesLeft -= countLines(shown)

		fmt.Fprintf(&b, "\n  f%d %s  %s => %s  (%d B)\n",
			f.GetFid(), f.GetKind(),
			orDash(f.GetProducer()), orDash(f.GetConsumer()),
			f.GetBytes())
		if !looksText(data) {
			fmt.Fprintf(&b, "    (binary, %d bytes not shown)\n", len(data))
			continue
		}
		for _, l := range strings.Split(strings.TrimSuffix(shown, "\n"), "\n") {
			if l == "" && len(shown) == 0 {
				continue
			}
			fmt.Fprintf(&b, "    | %s\n", l)
		}
		if cut {
			fmt.Fprintf(&b, "    %s\n", i18n.T("en", i18n.MsgIntermediatesCut, i18n.Params{
				"bytes": fmt.Sprintf("%d", len(data)),
			}))
		}
	}
	if more > 0 {
		fmt.Fprintf(&b, "\n  %s\n", i18n.T("en", i18n.MsgIntermediatesMore, i18n.Params{
			"n": fmt.Sprintf("%d", more),
		}))
	}
	fmt.Fprintf(&b, "\n  %s\n", i18n.T("en", i18n.MsgIntermediatesHint, nil))
	return strings.TrimRight(b.String(), "\n")
}

// headLines returns at most maxLines lines (and no more than budget lines) from
// data, plus whether anything was left behind.
func headLines(data []byte, maxLines, budget int) (string, bool) {
	if budget < maxLines {
		maxLines = budget
	}
	if maxLines <= 0 {
		return "", len(data) > 0
	}
	s := string(data)
	trailing := strings.HasSuffix(s, "\n")
	lines := strings.Split(strings.TrimSuffix(s, "\n"), "\n")
	if len(lines) <= maxLines {
		return s, false
	}
	out := strings.Join(lines[:maxLines], "\n")
	if trailing || len(lines) > maxLines {
		out += "\n"
	}
	return out, true
}

func countLines(s string) int {
	if s == "" {
		return 0
	}
	return strings.Count(s, "\n")
}
