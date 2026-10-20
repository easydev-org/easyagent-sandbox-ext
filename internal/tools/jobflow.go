package tools

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	workerv1 "github.com/easydev-org/easyagent-sandbox-ext/gen/worker/v1"
	"github.com/easydev-org/easyagent-sandbox-ext/internal/i18n"
	easybus "github.com/easydev-org/easybus-sdk"
	"github.com/easydev-org/easybus-sdk/extension"
)

// maxInlineFlowBytes caps how much of one intermediate stream is inlined.
//
// A pipeline stage can emit megabytes; a flow read exists to let a caller SEE the
// intermediate result (why did `wc -w` print 4 instead of 5?), not to transfer a
// binary artefact. Anything larger is reported by size, and the caller can still
// page it with offset/limit.
const maxInlineFlowBytes = 64 << 10

// jobGraph renders a job's dataflow: every stream with its producer, consumer
// and size, plus the edges between them.
//
// WHY this tool exists: the shell underneath (easysh) makes every stream an
// observable object, but a job's final stdout hides the pipeline that produced
// it. With this, "which command wrote this, into what, and how big" is answerable
// instead of guessed.
func (t *toolset) jobGraph(ctx context.Context, args map[string]any, session, tenant string) (extension.ToolResultData, error) {
	s, err := t.resolve(args, session, tenant)
	if err != nil {
		return extension.ToolResultData{}, err
	}
	jobID := argStr(args, "job-id")
	if jobID == "" {
		return extension.ToolResultData{}, &extension.TypedError{
			Code: easybus.ToolResultErrorCodeInvalidArgument, Message: "job-id is required",
		}
	}
	cl := t.deps.Cache.Worker(ep(s))
	res, err := cl.JobGraph(ctx, &workerv1.JobGraphRequest{JobId: jobID})
	if err != nil {
		return extension.ToolResultData{}, &extension.TypedError{
			Code:    easybus.ToolResultErrorCodeBusiness,
			Message: fmt.Sprintf("job graph for %s: %v", jobID, err),
		}
	}
	flows := res.GetFlows()
	edges := res.GetEdges()
	showAll := argBool(args, "all", false)

	locale := t.locale(session, tenant)
	var b strings.Builder
	if len(flows) == 0 {
		fmt.Fprintf(&b, "%s\n", i18n.T(locale, i18n.MsgJobGraphHead, i18n.Params{
			"id": jobID, "n": "0", "e": "0",
		}))
		b.WriteString(i18n.T(locale, i18n.MsgJobGraphEmpty, nil))
		return extension.ToolResultData{Content: b.String(), Data: map[string]any{"jobId": jobID, "flows": 0}}, nil
	}

	// An `fd` flow is a stage's stdin/stdout/stderr ENDPOINT: it names where that
	// fd points and carries no bytes of its own (see easysh flow.FdEndpoint —
	// "identity, no bytes"). Every stage contributes three, so a four-command
	// pipeline yields 12 of them and drowns the two flows that actually moved
	// data. They are folded by default; the EDGES still list every fid, so the
	// wiring stays complete and `all: true` brings the rows back.
	visible := make([]*workerv1.Flow, 0, len(flows))
	for _, f := range flows {
		if !showAll && f.GetKind() == "fd" && f.GetBytes() == 0 {
			continue
		}
		visible = append(visible, f)
	}
	folded := len(flows) - len(visible)

	fmt.Fprintf(&b, "%s\n", i18n.T(locale, i18n.MsgJobGraphHead, i18n.Params{
		"id": jobID, "n": fmt.Sprintf("%d", len(visible)), "e": fmt.Sprintf("%d", len(edges)),
	}))

	// Fixed-width columns so a graph is scannable. fid is right-aligned: it is the
	// key the caller passes to sandbox-job-flow.
	b.WriteString("  fid   kind     producer                 consumer                 bytes  lines\n")
	for _, f := range visible {
		fmt.Fprintf(&b, "  %-5d %-8s %-24s %-24s %6d %6d\n",
			f.GetFid(),
			truncCell(f.GetKind(), 8),
			truncCell(orDash(f.GetProducer()), 24),
			truncCell(orDash(f.GetConsumer()), 24),
			f.GetBytes(), f.GetLines())
	}
	if folded > 0 {
		fmt.Fprintf(&b, "\n  %s\n", i18n.T(locale, i18n.MsgJobGraphFolded, i18n.Params{
			"n": fmt.Sprintf("%d", folded),
		}))
	}
	// Not every edge carries data. Most are INHERITED wiring that every stage has:
	// its stdin pointing at the job's stdin, and its stderr pointing at the job's
	// stderr. In a 3-stage pipeline that is 3 identical stderr edges, which read
	// as noise and say nothing about what the pipeline DID.
	//
	// An edge is a DATA PATH when either end is a byte-carrying flow (pipe, file,
	// here, capture) — that is the edge that explains how output reached input.
	if len(edges) > 0 {
		dataEdges, inherited := splitEdges(edges, flows)
		b.WriteString("\n")
		if len(dataEdges) > 0 {
			fmt.Fprintf(&b, "  %s\n", i18n.T(locale, i18n.MsgJobGraphDataEdges, nil))
			for _, e := range dataEdges {
				fmt.Fprintf(&b, "    %s\n", renderEdge(e, flows))
			}
		}
		if inherited > 0 {
			fmt.Fprintf(&b, "\n  %s\n", i18n.T(locale, i18n.MsgJobGraphInheritedEdges, i18n.Params{
				"n": fmt.Sprintf("%d", inherited),
			}))
		}
		if len(dataEdges) == 0 {
			// A job with no pipeline still deserves a truthful line rather than an
			// empty section that looks like a bug.
			fmt.Fprintf(&b, "  %s\n", i18n.T(locale, i18n.MsgJobGraphNoDataEdges, nil))
		}
	}
	if len(flows) > 0 {
		fmt.Fprintf(&b, "\n  %s\n", i18n.T(locale, i18n.MsgJobGraphHint, nil))
	}
	return extension.ToolResultData{
		Content: strings.TrimRight(b.String(), "\n"),
		Data: map[string]any{
			"jobId":    jobID,
			"flows":    flowsToData(visible),
			"edges":    edgesToData(dataEdgeList(edges, flows)),
			"allEdges": edgesToData(edges),
			"folded":   folded,
			// The full set stays available so a caller never has to re-run with a
			// flag just to see what was folded.
			"allFlows": flowsToData(flows),
		},
	}, nil
}

// jobFlow returns one intermediate stream's content, by fid.
//
// Paged by BYTE offset/limit: a flow's size is known from the graph, and bytes
// are what the caller wants to inspect (a partial line is still evidence). Text
// is split into lines for display only.
func (t *toolset) jobFlow(ctx context.Context, args map[string]any, session, tenant string) (extension.ToolResultData, error) {
	s, err := t.resolve(args, session, tenant)
	if err != nil {
		return extension.ToolResultData{}, err
	}
	jobID := argStr(args, "job-id")
	if jobID == "" {
		return extension.ToolResultData{}, &extension.TypedError{
			Code: easybus.ToolResultErrorCodeInvalidArgument, Message: "job-id is required",
		}
	}
	// fid has no sensible default: 0 is a real FID, so "absent" must be distinct.
	// Guard on presence in the map, then read with the shared helper.
	if _, present := args["fid"]; !present {
		return extension.ToolResultData{}, &extension.TypedError{
			Code:    easybus.ToolResultErrorCodeInvalidArgument,
			Message: "fid is required (see sandbox-job-graph)",
		}
	}
	fid := int64(argInt(args, "fid", 0))
	cl := t.deps.Cache.Worker(ep(s))
	res, err := cl.JobFlow(ctx, &workerv1.JobFlowRequest{JobId: jobID, Fid: fid})
	if err != nil {
		return extension.ToolResultData{}, &extension.TypedError{
			Code:    easybus.ToolResultErrorCodeBusiness,
			Message: fmt.Sprintf("flow %d of %s: %v", fid, jobID, err),
		}
	}
	raw := res.GetData()
	locale := t.locale(session, tenant)

	off := argInt(args, "offset", 0)
	limit := argInt(args, "limit", 0)
	if off < 0 {
		off = 0
	}
	total := len(raw)
	if off > total {
		off = total
	}
	end := total
	if limit > 0 && off+limit < end {
		end = off + limit
	}
	// Keep the transport bounded even when the caller asks for everything.
	clampedTo := end
	if clampedTo-off > maxInlineFlowBytes {
		clampedTo = off + maxInlineFlowBytes
	}
	chunk := raw[off:clampedTo]

	var b strings.Builder
	fmt.Fprintf(&b, "%s\n", i18n.T(locale, i18n.MsgJobFlowHead, i18n.Params{
		"fid": fmt.Sprintf("%d", fid), "total": fmt.Sprintf("%d", total),
	}))
	if total == 0 {
		b.WriteString(i18n.T(locale, i18n.MsgJobFlowEmpty, nil))
		return extension.ToolResultData{Content: b.String(), Data: map[string]any{
			"jobId": jobID, "fid": fid, "bytes": 0,
		}}, nil
	}
	b.WriteString(renderBytes(chunk))

	truncated := clampedTo < total
	if truncated {
		fmt.Fprintf(&b, "\n\n%s\n", i18n.T(locale, i18n.MsgJobFlowMore, i18n.Params{
			"next":  fmt.Sprintf("%d", clampedTo),
			"total": fmt.Sprintf("%d", total),
		}))
	}
	return extension.ToolResultData{
		Content: strings.TrimRight(b.String(), "\n"),
		Data: map[string]any{
			"jobId": jobID, "fid": fid,
			"bytes": total, "offset": off, "returned": len(chunk), "truncated": truncated,
			"text": string(chunk),
		},
	}, nil
}

// jobStdin writes to a running job's stdin, optionally closing it.
//
// The worker has had JobStdin all along; without a tool, an interactive command
// (`read x`, a REPL, a prompt) could only ever be started and then timed out.
func (t *toolset) jobStdin(ctx context.Context, args map[string]any, session, tenant string) (extension.ToolResultData, error) {
	s, err := t.resolve(args, session, tenant)
	if err != nil {
		return extension.ToolResultData{}, err
	}
	jobID := argStr(args, "job-id")
	if jobID == "" {
		return extension.ToolResultData{}, &extension.TypedError{
			Code: easybus.ToolResultErrorCodeInvalidArgument, Message: "job-id is required",
		}
	}
	data := argStr(args, "data")
	closeStdin := argBool(args, "close", false)
	if data == "" && !closeStdin {
		return extension.ToolResultData{}, &extension.TypedError{
			Code:    easybus.ToolResultErrorCodeInvalidArgument,
			Message: "nothing to do: pass `data`, or `close: true` to signal EOF",
		}
	}
	cl := t.deps.Cache.Worker(ep(s))
	if _, err := cl.JobStdin(ctx, &workerv1.JobStdinRequest{
		JobId: jobID, Data: []byte(data), Close: closeStdin,
	}); err != nil {
		return extension.ToolResultData{}, &extension.TypedError{
			Code:    easybus.ToolResultErrorCodeBusiness,
			Message: fmt.Sprintf("stdin to %s: %v", jobID, err),
		}
	}
	locale := t.locale(session, tenant)
	msg := i18n.T(locale, i18n.MsgJobStdinOK, i18n.Params{
		"id": jobID, "n": fmt.Sprintf("%d", len(data)),
		"closed": fmt.Sprintf("%v", closeStdin),
	})
	return extension.ToolResultData{
		Content: msg,
		Data:    map[string]any{"jobId": jobID, "bytes": len(data), "closed": closeStdin},
	}, nil
}

// ---- small render helpers -------------------------------------------------

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// truncCell trims a cell to n bytes-safe characters, marking the cut.
func truncCell(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n <= 1 {
		return string(r[:n])
	}
	return string(r[:n-1]) + "~"
}

// renderBytes shows a chunk readably when it is text, and as a size when it is
// not: dumping binary into a tool response corrupts it and helps nobody.
func renderBytes(b []byte) string {
	if !looksText(b) {
		return fmt.Sprintf("(binary, %d bytes not shown)", len(b))
	}
	lines := strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
	var out strings.Builder
	for i, l := range lines {
		if i > 0 {
			out.WriteString("\n")
		}
		fmt.Fprintf(&out, "%5d| %s", i+1, l)
	}
	return out.String()
}

// looksText reports whether b is safe to embed (no NUL, valid UTF-8).
func looksText(b []byte) bool {
	if len(b) == 0 {
		return true
	}
	for _, c := range b {
		if c == 0 {
			return false
		}
	}
	return utf8.Valid(b)
}

func flowsToData(flows []*workerv1.Flow) []map[string]any {
	out := make([]map[string]any, 0, len(flows))
	for _, f := range flows {
		out = append(out, map[string]any{
			"fid": f.GetFid(), "kind": f.GetKind(), "fd": f.GetFd(),
			"producer": f.GetProducer(), "consumer": f.GetConsumer(),
			"path": f.GetPath(), "bytes": f.GetBytes(), "lines": f.GetLines(),
			"iter": f.GetIter(),
		})
	}
	// Stable order by fid: a caller comparing two runs should see the same shape.
	sort.Slice(out, func(i, j int) bool {
		return out[i]["fid"].(int64) < out[j]["fid"].(int64)
	})
	return out
}

func edgesToData(edges []*workerv1.FlowEdge) []map[string]any {
	out := make([]map[string]any, 0, len(edges))
	for _, e := range edges {
		out = append(out, map[string]any{"from": e.GetFromFid(), "to": e.GetToFid()})
	}
	return out
}

// byteCarryingKinds are the flow kinds that actually move bytes.
//
// `fd` is deliberately absent: it is a stage's stdin/stdout/stderr IDENTITY, and
// `dup`/`null` only alias or discard. An edge incident to one of these is wiring,
// not data.
var byteCarryingKinds = map[string]bool{
	"pipe": true, "file": true, "here": true, "capture": true, "terminal": true,
}

// splitEdges separates DATA PATH edges from inherited wiring.
//
// An edge is a data path when either end is a byte-carrying flow: that is the
// pair that explains "output of A became input of B". Everything else is the
// default plumbing every stage gets (stdin from the job, stderr to the job),
// which repeats once per stage and says nothing about what the job did.
func splitEdges(edges []*workerv1.FlowEdge, flows []*workerv1.Flow) (data []*workerv1.FlowEdge, inherited int) {
	kind := make(map[int64]string, len(flows))
	for _, f := range flows {
		kind[f.GetFid()] = f.GetKind()
	}
	for _, e := range edges {
		if byteCarryingKinds[kind[e.GetFromFid()]] || byteCarryingKinds[kind[e.GetToFid()]] {
			data = append(data, e)
			continue
		}
		inherited++
	}
	return data, inherited
}

func dataEdgeList(edges []*workerv1.FlowEdge, flows []*workerv1.Flow) []*workerv1.FlowEdge {
	d, _ := splitEdges(edges, flows)
	return d
}

// renderEdge explains one edge in words: "f10->f4  (echo stdout -> pipe)".
//
// A bare "f10->f4" forces the reader to cross-reference two table rows. The
// arrow form is the whole point of the graph, so it is spelled out.
func renderEdge(e *workerv1.FlowEdge, flows []*workerv1.Flow) string {
	byFid := make(map[int64]*workerv1.Flow, len(flows))
	for _, f := range flows {
		byFid[f.GetFid()] = f
	}
	from, to := byFid[e.GetFromFid()], byFid[e.GetToFid()]
	return fmt.Sprintf("f%d -> f%d   %s => %s",
		e.GetFromFid(), e.GetToFid(), describeEnd(from, "out"), describeEnd(to, "in"))
}

// describeEnd renders one side of an edge: the producer for a source, the
// consumer for a sink, falling back to the flow's kind.
func describeEnd(f *workerv1.Flow, side string) string {
	if f == nil {
		return "?"
	}
	var who string
	switch side {
	case "out":
		who = f.GetProducer()
		if who == "" {
			who = f.GetConsumer()
		}
	default:
		who = f.GetConsumer()
		if who == "" {
			who = f.GetProducer()
		}
	}
	if who == "" {
		who = "(job)"
	}
	k := f.GetKind()
	if k == "fd" {
		// An fd endpoint is more usefully named by which fd it is.
		if fd := f.GetFd(); fd != "" {
			return who + " " + fd
		}
	}
	return who + " " + k
}
