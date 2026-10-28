package uiapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"connectrpc.com/connect/v2"

	workerv1 "github.com/easydev-org/easyagent-sandbox-ext/gen/worker/v1"
	"github.com/easydev-org/easyagent-sandbox-ext/internal/config"
	"github.com/easydev-org/easyagent-sandbox-ext/internal/sandbox"
)

// SandboxRow is one machine as the panel shows it.
type SandboxRow struct {
	Name string `json:"name"`
	OS   string `json:"os"`
	// Platform is the GUI platform ("wayland", "x11", "macos", "windows", "android"), or "" for a
	// headless machine.
	Platform string `json:"platform"`
	// A11y is true when the machine can drive a GUI — the panel offers a live screen only for those,
	// because a headless worker has none to show.
	A11y bool `json:"a11y"`
	// Live is false when the worker did not answer. Reported rather than omitted: a sandbox that is
	// down is information, and hiding it makes the panel look empty instead of broken.
	Live  bool   `json:"live"`
	Error string `json:"error,omitempty"`
}

// sandboxes lists the caller's configured machines with their live status.
func (h *handler) sandboxes(w http.ResponseWriter, r *http.Request) {
	session, tenant, ok := h.scope(w, r)
	if !ok {
		return
	}
	list := h.d.Sandboxes(session, tenant)
	rows := make([]SandboxRow, 0, len(list))
	for _, s := range list {
		row := SandboxRow{Name: s.Name}
		// One probe per sandbox, in sequence: a tenant has a handful, and a parallel fan-out would
		// make one slow worker hold the whole list while complicating cancellation for no gain.
		info, err := h.d.Cache.Worker(sandbox.Endpoint{URL: s.URL, Token: s.Token}).
			Info(r.Context(), &workerv1.InfoRequest{})
		if err != nil {
			row.Error = err.Error()
		} else {
			row.Live = true
			row.OS = info.GetOs()
			row.Platform = info.GetCapabilities().GetPlatform()
			row.A11y = info.GetCapabilities().GetXa11Y()
		}
		rows = append(rows, row)
	}
	writeJSON(w, http.StatusOK, map[string]any{"sandboxes": rows, "count": len(rows)})
}

// sandboxSub dispatches /sandboxes/{name}/screen.
func (h *handler) sandboxSub(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/sandboxes/")
	parts := strings.Split(rest, "/")
	if len(parts) < 2 || parts[0] == "" {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	if parts[1] == "screen" {
		h.screen(w, r, parts[0])
		return
	}
	writeErr(w, http.StatusNotFound, "not found")
}

// jobs lists what one sandbox's daemon knows about.
//
// Asked of the WORKER, not of this process: a job belongs to the machine that ran it, and this
// extension keeps no record — a job started before this process booted, or by a tool call that has
// long since returned, must still appear.
func (h *handler) jobs(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("sb")
	if name == "" {
		writeErr(w, http.StatusBadRequest, "sb (sandbox name) is required")
		return
	}
	sb, ok := h.findSandbox(w, r, name)
	if !ok {
		return
	}
	limit := int32(100)
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 1000 {
			limit = int32(n)
		}
	}
	ctx, cancel := contextWithCancel(r)
	defer cancel()
	res, err := h.d.Cache.Worker(sandbox.Endpoint{URL: sb.URL, Token: sb.Token}).
		ListJobs(ctx, &workerv1.ListJobsRequest{Limit: limit})
	if err != nil {
		writeWorkerErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"jobs": res.GetJobs(), "count": len(res.GetJobs())})
}

// jobSub dispatches /jobs/{sb}/{id}/watch and /jobs/{sb}/{id}/output.
func (h *handler) jobSub(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/jobs/")
	parts := strings.Split(rest, "/")
	if len(parts) < 3 || parts[0] == "" || parts[1] == "" {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	sb, ok := h.findSandbox(w, r, parts[0])
	if !ok {
		return
	}
	switch parts[2] {
	case "watch":
		h.watch(w, r, sb, parts[1])
	case "output":
		h.output(w, r, sb, parts[1])
	default:
		writeErr(w, http.StatusNotFound, "not found")
	}
}

// output returns the job's whole output once, for a reader that does not need it live.
func (h *handler) output(w http.ResponseWriter, r *http.Request, sb config.Sandbox, id string) {
	start := int32(0)
	if v := r.URL.Query().Get("start"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			start = int32(n)
		}
	}
	ctx, cancel := contextWithCancel(r)
	defer cancel()
	res, err := h.d.Cache.Worker(sandbox.Endpoint{URL: sb.URL, Token: sb.Token}).
		JobOutput(ctx, &workerv1.JobOutputRequest{JobId: id, Start: start})
	if err != nil {
		writeWorkerErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"lines": res.GetLines(),
		// The window the server actually returned, so a reader can tell "this is the whole output"
		// from "this is the part that fit".
		"totalLines": res.GetTotalLines(),
		"startLine":  res.GetStartLine(),
		"endLine":    res.GetEndLine(),
		"done":       res.GetDone(),
	})
}

// watch streams a job's output as Server-Sent Events until it finishes.
//
// WHY SSE AND NOT THE RPC: the browser cannot speak Connect. It CAN consume `text/event-stream` with
// EventSource, and this layer's whole job is to put a browser-shaped door on the worker's own stream.
//
// The upstream is `WorkerService.WatchJob`, a SERVER-STREAMING RPC — so this is a genuine push from
// the daemon, not a poll dressed up as one.
func (h *handler) watch(w http.ResponseWriter, r *http.Request, sb config.Sandbox, id string) {
	flusher, canFlush := w.(http.Flusher)
	if !canFlush {
		writeErr(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	// Nginx and most proxies buffer a response unless told not to, which defeats SSE entirely.
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	ctx, cancel := contextWithCancel(r)
	defer cancel()

	stream, err := h.d.Cache.Worker(sandbox.Endpoint{URL: sb.URL, Token: sb.Token}).
		WatchJob(ctx, &workerv1.WatchJobRequest{JobId: id})
	if err != nil {
		sse(w, "error", err.Error())
		flusher.Flush()
		return
	}
	defer func() { _ = stream.Close() }()

	for {
		msg, err := stream.Receive()
		if err != nil {
			// io.EOF is the normal end of a server stream; anything else is worth telling the reader
			// about, unless they are the ones who hung up.
			if !errors.Is(err, io.EOF) && ctx.Err() == nil {
				sse(w, "error", err.Error())
				flusher.Flush()
			}
			return
		}
		switch ev := msg.GetEvent().(type) {
		case *workerv1.WatchJobResponse_Output:
			sse(w, "output", ev.Output)
		case *workerv1.WatchJobResponse_Done_:
			// The terminal event, always last. Sent as JSON so the reader can show the exit code
			// rather than guessing from the last line.
			b, _ := json.Marshal(map[string]any{
				"exitCode": ev.Done.GetExitCode(),
				"stdout":   ev.Done.GetStdout(),
				"stderr":   ev.Done.GetStderr(),
			})
			sse(w, "done", string(b))
		}
		flusher.Flush()
	}
}

// sse writes one event, splitting on newlines as the format requires.
func sse(w http.ResponseWriter, event, data string) {
	for _, line := range strings.Split(data, "\n") {
		_, _ = w.Write([]byte("event: " + event + "\ndata: " + line + "\n\n"))
	}
	if data == "" {
		// A single empty data line, so an empty chunk is still a complete event.
		_, _ = w.Write([]byte("event: " + event + "\ndata: \n\n"))
	}
}

// writeWorkerErr maps a Connect error to something a browser can act on.
//
// A worker that is merely unreachable answers 503, not 500: it is a transient state of a live
// resource, and a panel retries it rather than showing a permanent failure.
func writeWorkerErr(w http.ResponseWriter, err error) {
	code := connect.CodeOf(err)
	switch code {
	case connect.CodeNotFound:
		writeErr(w, http.StatusNotFound, err.Error())
	case connect.CodeInvalidArgument:
		writeErr(w, http.StatusBadRequest, err.Error())
	case connect.CodeUnauthenticated, connect.CodePermissionDenied:
		writeErr(w, http.StatusForbidden, err.Error())
	case connect.CodeUnavailable:
		writeErr(w, http.StatusServiceUnavailable, err.Error())
	default:
		writeErr(w, http.StatusBadGateway, err.Error())
	}
}
