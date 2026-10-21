// Package tools implements the sandbox-* and computer-* tool handlers over the
// worker.v1 Connect contract — the Go twin of the worker-extension's TS tools.
package tools

import (
	"context"
	"fmt"
	"strings"

	workerv1 "github.com/easydev-org/easyagent-sandbox-ext/gen/worker/v1"
	"github.com/easydev-org/easyagent-sandbox-ext/internal/config"
	"github.com/easydev-org/easyagent-sandbox-ext/internal/i18n"
	"github.com/easydev-org/easyagent-sandbox-ext/internal/sandbox"
	easybus "github.com/easydev-org/easybus-sdk"
	"github.com/easydev-org/easybus-sdk/bus"
	"github.com/easydev-org/easybus-sdk/extension"
)

// Deps is what the tools need from the extension host.
type Deps struct {
	// GetConfig reads an effective config value for the calling scope.
	GetConfig func(name, session, tenant string) any
	// Locale resolves the session locale for result text.
	Locale func(session, tenant string) string
	// Cache is the worker-client cache.
	Cache *sandbox.ClientCache
	// Bus is the extension's bus connection, used for the agent file RPCs
	// (download/upload persist bytes through the agent — no local store).
	Bus bus.Bus
	// Progress reports an in-flight update for the tool call.
	//
	// This is the UI side-channel: it reaches the agent over
	// easybus.tool.progress.<callID> and is never fed into the model's context, so
	// streaming a command's output here costs no tokens and cannot confuse the
	// conversation. nil means "nobody is listening" and the reporter is skipped.
	Progress func(ctx context.Context, callID string, p *easybus.ToolProgress)
}

// Handler is one tool implementation.
//
// callID is the identity of THIS invocation. It is needed to address progress
// updates (easybus.tool.progress.<callID>) back to the caller that is watching
// this specific tool call — without it a long-running command has no way to say
// anything until it finishes.
type Handler func(ctx context.Context, args map[string]any, callID, session, tenant string) (extension.ToolResultData, error)

// Registry builds the sandbox-* + computer-* handler map.
func Registry(d Deps) map[string]Handler {
	t := &toolset{deps: d}
	return map[string]Handler{
		"list-sandboxes":     t.listSandboxes,
		"sandbox-info":       t.info,
		"sandbox-job-start":  t.jobStart,
		"sandbox-job-output": t.jobOutput,
		"sandbox-job-wait":   t.jobWait,
		"sandbox-job-kill":   t.jobKill,
		"sandbox-job-list":   t.jobList,
		// The dataflow surface: easysh makes every stream an observable Flow, so a
		// job's pipeline is inspectable instead of hidden behind its final stdout.
		"sandbox-job-graph":   t.jobGraph,
		"sandbox-job-flow":    t.jobFlow,
		"sandbox-job-stdin":   t.jobStdin,
		"sandbox-file-read":   t.fileRead,
		"sandbox-file-write":  t.fileWrite,
		"sandbox-file-list":   t.fileList,
		"sandbox-file-delete": t.fileDelete,
		"sandbox-file-move":   t.fileMove,
		"sandbox-file-copy":   t.fileCopy,
		"sandbox-download":    t.download,
		"sandbox-upload":      t.upload,
		"computer-info":       t.computerInfo,
		"computer-apps":       t.computerApps,
		"computer-snapshot":   t.computerSnapshot,
		"computer-find":       t.computerFind,
		"computer-action":     t.computerAction,
		"computer-click":      t.computerClick,
		"computer-type":       t.computerType,
		"computer-key":        t.computerKey,
		"computer-scroll":     t.computerScroll,
		"computer-drag":       t.computerDrag,
		"computer-screenshot": t.computerScreenshot,
	}
}

type toolset struct{ deps Deps }

func (t *toolset) locale(session, tenant string) string {
	if t.deps.Locale != nil {
		return t.deps.Locale(session, tenant)
	}
	return ""
}

// resolve parses the sandbox list and selects the one named by args["sandbox"]
// (empty = the first configured).
func (t *toolset) resolve(args map[string]any, session, tenant string) (config.Sandbox, error) {
	raw := ""
	if t.deps.GetConfig != nil {
		if s, ok := t.deps.GetConfig(config.KnobSandboxes, session, tenant).(string); ok {
			raw = s
		}
	}
	list := config.Parse(raw)
	if len(list) == 0 {
		return config.Sandbox{}, &extension.TypedError{
			Code:    easybus.ToolResultErrorCodeInvalidArgument,
			Message: i18n.T(t.locale(session, tenant), i18n.MsgNotConfigured, i18n.Params{"name": config.KnobSandboxes}),
		}
	}
	wanted := argStr(args, "sandbox")
	sel, err := config.Select(list, wanted)
	if err != nil {
		return config.Sandbox{}, &extension.TypedError{
			Code:    easybus.ToolResultErrorCodeInvalidArgument,
			Message: i18n.T(t.locale(session, tenant), i18n.MsgUnknownTarget, i18n.Params{"target": wanted, "known": strings.Join(config.Names(list), ", ")}),
		}
	}
	return sel, nil
}

func ep(s config.Sandbox) sandbox.Endpoint { return sandbox.Endpoint{URL: s.URL, Token: s.Token} }

// ---- discovery -------------------------------------------------------------

func (t *toolset) listSandboxes(ctx context.Context, args map[string]any, callID, session, tenant string) (extension.ToolResultData, error) {
	raw := ""
	if t.deps.GetConfig != nil {
		if s, ok := t.deps.GetConfig(config.KnobSandboxes, session, tenant).(string); ok {
			raw = s
		}
	}
	list := config.Parse(raw)
	if len(list) == 0 {
		return extension.ToolResultData{Content: i18n.T(t.locale(session, tenant), i18n.MsgNoSandboxes, nil)}, nil
	}
	type row struct {
		Name     string `json:"name"`
		OS       string `json:"os"`
		Platform string `json:"platform"`
		A11y     bool   `json:"a11y"`
	}
	rows := make([]row, 0, len(list))
	var lines []string
	for _, s := range list {
		r := row{Name: s.Name}
		if info, err := t.deps.Cache.Worker(ep(s)).Info(ctx, &workerv1.InfoRequest{}); err == nil {
			r.OS = info.GetOs()
			r.Platform = info.GetCapabilities().GetPlatform()
			r.A11y = info.GetCapabilities().GetXa11Y()
		}
		rows = append(rows, r)
		a11y := "no"
		if r.A11y {
			a11y = "yes"
		}
		lines = append(lines, fmt.Sprintf("%s\t%s\ta11y=%s", r.Name, r.OS, a11y))
	}
	return extension.ToolResultData{Content: strings.Join(lines, "\n"), Data: map[string]any{"sandboxes": rows}}, nil
}
