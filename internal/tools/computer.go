package tools

import (
	"context"
	"fmt"

	workerv1 "github.com/easydev-org/easyagent-sandbox-ext/gen/worker/v1"
	easybus "github.com/easydev-org/easybus-sdk"
	"github.com/easydev-org/easybus-sdk/extension"
)

func joinLines(lines []string) string {
	out := ""
	for i, l := range lines {
		if i > 0 {
			out += "\n"
		}
		out += l
	}
	return out
}

func (t *toolset) computerInfo(ctx context.Context, args map[string]any, callID, session, tenant string) (extension.ToolResultData, error) {
	s, err := t.resolve(args, session, tenant)
	if err != nil {
		return extension.ToolResultData{}, err
	}
	info, err := t.deps.Cache.Computer(ep(s)).Info(ctx, &workerv1.ComputerInfoRequest{})
	if err != nil {
		return extension.ToolResultData{}, &extension.TypedError{Code: easybus.ToolResultErrorCodeRetryable, Message: fmt.Sprintf("computer-info: %v", err)}
	}
	return extension.ToolResultData{
		Content: fmt.Sprintf("platform=%s display=%s tools=%v size=%dx%d", info.GetPlatform(), info.GetDisplay(), info.GetTools(), info.GetWidth(), info.GetHeight()),
		Data:    map[string]any{"platform": info.GetPlatform(), "display": info.GetDisplay(), "tools": info.GetTools(), "width": info.GetWidth(), "height": info.GetHeight()},
	}, nil
}

func (t *toolset) computerApps(ctx context.Context, args map[string]any, callID, session, tenant string) (extension.ToolResultData, error) {
	s, err := t.resolve(args, session, tenant)
	if err != nil {
		return extension.ToolResultData{}, err
	}
	res, err := t.deps.Cache.Computer(ep(s)).Apps(ctx, &workerv1.ComputerAppsRequest{})
	if err != nil {
		return extension.ToolResultData{}, &extension.TypedError{Code: easybus.ToolResultErrorCodeRetryable, Message: fmt.Sprintf("computer-apps: %v", err)}
	}
	lines := make([]string, 0, len(res.GetApps()))
	for _, a := range res.GetApps() {
		mark := " "
		if a.GetFocused() {
			mark = "*"
		}
		lines = append(lines, fmt.Sprintf("%s %s\t%s", mark, a.GetName(), a.GetPid()))
	}
	return extension.ToolResultData{Content: joinLines(lines), Data: map[string]any{"apps": res.GetApps()}}, nil
}

func (t *toolset) computerSnapshot(ctx context.Context, args map[string]any, callID, session, tenant string) (extension.ToolResultData, error) {
	s, err := t.resolve(args, session, tenant)
	if err != nil {
		return extension.ToolResultData{}, err
	}
	res, err := t.deps.Cache.Computer(ep(s)).Snapshot(ctx, &workerv1.ComputerSnapshotRequest{App: argStr(args, "app"), Depth: int32(argInt(args, "depth", 0))})
	if err != nil {
		return extension.ToolResultData{}, &extension.TypedError{Code: easybus.ToolResultErrorCodeRetryable, Message: fmt.Sprintf("computer-snapshot: %v", err)}
	}
	return extension.ToolResultData{Content: res.GetText()}, nil
}

func (t *toolset) computerFind(ctx context.Context, args map[string]any, callID, session, tenant string) (extension.ToolResultData, error) {
	s, err := t.resolve(args, session, tenant)
	if err != nil {
		return extension.ToolResultData{}, err
	}
	sel := argStr(args, "selector")
	if sel == "" {
		return extension.ToolResultData{}, &extension.TypedError{Code: easybus.ToolResultErrorCodeInvalidArgument, Message: "selector is required"}
	}
	out := argStr(args, "output")
	if out == "" {
		out = "pretty"
	}
	res, err := t.deps.Cache.Computer(ep(s)).Find(ctx, &workerv1.ComputerFindRequest{Selector: sel, App: argStr(args, "app"), Output: out})
	if err != nil {
		return extension.ToolResultData{}, &extension.TypedError{Code: easybus.ToolResultErrorCodeRetryable, Message: fmt.Sprintf("computer-find: %v", err)}
	}
	return extension.ToolResultData{Content: res.GetText()}, nil
}

func (t *toolset) computerAction(ctx context.Context, args map[string]any, callID, session, tenant string) (extension.ToolResultData, error) {
	s, err := t.resolve(args, session, tenant)
	if err != nil {
		return extension.ToolResultData{}, err
	}
	action := argStr(args, "action")
	target := argStr(args, "target")
	if action == "" || target == "" {
		return extension.ToolResultData{}, &extension.TypedError{Code: easybus.ToolResultErrorCodeInvalidArgument, Message: "action and target are required"}
	}
	if _, err := t.deps.Cache.Computer(ep(s)).Action(ctx, &workerv1.ComputerActionRequest{Action: action, Target: target, Value: argStr(args, "value"), App: argStr(args, "app")}); err != nil {
		return extension.ToolResultData{}, &extension.TypedError{Code: easybus.ToolResultErrorCodeBusiness, Message: fmt.Sprintf("computer-action: %v", err)}
	}
	return extension.ToolResultData{Content: fmt.Sprintf("%s %s", action, target)}, nil
}

func (t *toolset) computerClick(ctx context.Context, args map[string]any, callID, session, tenant string) (extension.ToolResultData, error) {
	s, err := t.resolve(args, session, tenant)
	if err != nil {
		return extension.ToolResultData{}, err
	}
	button := argStr(args, "button")
	if button == "" {
		button = "left"
	}
	if _, err := t.deps.Cache.Computer(ep(s)).Click(ctx, &workerv1.ComputerClickRequest{X: int32(argInt(args, "x", 0)), Y: int32(argInt(args, "y", 0)), Button: button, Count: int32(argInt(args, "count", 1))}); err != nil {
		return extension.ToolResultData{}, &extension.TypedError{Code: easybus.ToolResultErrorCodeBusiness, Message: fmt.Sprintf("computer-click: %v", err)}
	}
	return extension.ToolResultData{Content: "clicked"}, nil
}

func (t *toolset) computerType(ctx context.Context, args map[string]any, callID, session, tenant string) (extension.ToolResultData, error) {
	s, err := t.resolve(args, session, tenant)
	if err != nil {
		return extension.ToolResultData{}, err
	}
	text := argStr(args, "text")
	if text == "" {
		return extension.ToolResultData{}, &extension.TypedError{Code: easybus.ToolResultErrorCodeInvalidArgument, Message: "text is required"}
	}
	if _, err := t.deps.Cache.Computer(ep(s)).Type(ctx, &workerv1.ComputerTypeRequest{Text: text, Target: argStr(args, "target")}); err != nil {
		return extension.ToolResultData{}, &extension.TypedError{Code: easybus.ToolResultErrorCodeBusiness, Message: fmt.Sprintf("computer-type: %v", err)}
	}
	return extension.ToolResultData{Content: "typed"}, nil
}

func (t *toolset) computerKey(ctx context.Context, args map[string]any, callID, session, tenant string) (extension.ToolResultData, error) {
	s, err := t.resolve(args, session, tenant)
	if err != nil {
		return extension.ToolResultData{}, err
	}
	key := argStr(args, "key")
	if key == "" {
		return extension.ToolResultData{}, &extension.TypedError{Code: easybus.ToolResultErrorCodeInvalidArgument, Message: "key is required"}
	}
	if _, err := t.deps.Cache.Computer(ep(s)).Key(ctx, &workerv1.ComputerKeyRequest{Key: key, Held: argStrList(args, "held")}); err != nil {
		return extension.ToolResultData{}, &extension.TypedError{Code: easybus.ToolResultErrorCodeBusiness, Message: fmt.Sprintf("computer-key: %v", err)}
	}
	return extension.ToolResultData{Content: "pressed " + key}, nil
}

func (t *toolset) computerScroll(ctx context.Context, args map[string]any, callID, session, tenant string) (extension.ToolResultData, error) {
	s, err := t.resolve(args, session, tenant)
	if err != nil {
		return extension.ToolResultData{}, err
	}
	if _, err := t.deps.Cache.Computer(ep(s)).Scroll(ctx, &workerv1.ComputerScrollRequest{X: int32(argInt(args, "x", 0)), Y: int32(argInt(args, "y", 0)), Dx: int32(argInt(args, "dx", 0)), Dy: int32(argInt(args, "dy", 0))}); err != nil {
		return extension.ToolResultData{}, &extension.TypedError{Code: easybus.ToolResultErrorCodeBusiness, Message: fmt.Sprintf("computer-scroll: %v", err)}
	}
	return extension.ToolResultData{Content: "scrolled"}, nil
}

func (t *toolset) computerDrag(ctx context.Context, args map[string]any, callID, session, tenant string) (extension.ToolResultData, error) {
	s, err := t.resolve(args, session, tenant)
	if err != nil {
		return extension.ToolResultData{}, err
	}
	if _, err := t.deps.Cache.Computer(ep(s)).Drag(ctx, &workerv1.ComputerDragRequest{
		FromX: int32(argInt(args, "from_x", 0)), FromY: int32(argInt(args, "from_y", 0)),
		ToX: int32(argInt(args, "to_x", 0)), ToY: int32(argInt(args, "to_y", 0)),
		DurationMs: int32(argInt(args, "duration_ms", 0)),
	}); err != nil {
		return extension.ToolResultData{}, &extension.TypedError{Code: easybus.ToolResultErrorCodeBusiness, Message: fmt.Sprintf("computer-drag: %v", err)}
	}
	return extension.ToolResultData{Content: "dragged"}, nil
}

func (t *toolset) computerScreenshot(ctx context.Context, args map[string]any, callID, session, tenant string) (extension.ToolResultData, error) {
	s, err := t.resolve(args, session, tenant)
	if err != nil {
		return extension.ToolResultData{}, err
	}
	res, err := t.deps.Cache.Computer(ep(s)).Screenshot(ctx, &workerv1.ComputerScreenshotRequest{Region: argStr(args, "region")})
	if err != nil {
		return extension.ToolResultData{}, &extension.TypedError{Code: easybus.ToolResultErrorCodeRetryable, Message: fmt.Sprintf("computer-screenshot: %v", err)}
	}
	return extension.ToolResultData{
		Content: fmt.Sprintf("screenshot %dx%d (%d bytes)", res.GetWidth(), res.GetHeight(), len(res.GetPng())),
		Data:    map[string]any{"width": res.GetWidth(), "height": res.GetHeight(), "bytes": len(res.GetPng())},
	}, nil
}
