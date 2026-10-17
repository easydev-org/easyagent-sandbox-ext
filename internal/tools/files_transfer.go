package tools

import (
	"context"
	"fmt"
	"path"

	workerv1 "github.com/easydev-org/easyagent-sandbox-ext/gen/worker/v1"
	"github.com/easydev-org/easyagent-sandbox-ext/internal/i18n"
	easybus "github.com/easydev-org/easybus-sdk"
	"github.com/easydev-org/easybus-sdk/extension"
)

// download pulls a sandbox file's bytes and persists them through the AGENT
// (the extension has no store of its own), returning a `file:<code>`.
func (t *toolset) download(ctx context.Context, args map[string]any, session, tenant string) (extension.ToolResultData, error) {
	s, err := t.resolve(args, session, tenant)
	if err != nil {
		return extension.ToolResultData{}, err
	}
	p := argStr(args, "path")
	if p == "" {
		return extension.ToolResultData{}, &extension.TypedError{Code: easybus.ToolResultErrorCodeInvalidArgument, Message: "path is required"}
	}
	if t.deps.Bus == nil {
		return extension.ToolResultData{}, &extension.TypedError{Code: easybus.ToolResultErrorCodeInternal, Message: "file RPC unavailable (no bus wired)"}
	}
	res, err := t.deps.Cache.Worker(ep(s)).FileRead(ctx, &workerv1.FileReadRequest{Path: p})
	if err != nil {
		return extension.ToolResultData{}, &extension.TypedError{Code: easybus.ToolResultErrorCodeNotFound, Message: fmt.Sprintf("file-read: %v", err)}
	}
	code, mime, err := extension.IngestFileViaAgent(ctx, t.deps.Bus, tenant, path.Base(p), res.GetContent(), session)
	if err != nil {
		return extension.ToolResultData{}, &extension.TypedError{Code: easybus.ToolResultErrorCodeRetryable, Message: fmt.Sprintf("ingest: %v", err)}
	}
	return extension.ToolResultData{
		Content: i18n.T(t.locale(session, tenant), i18n.MsgUploaded, i18n.Params{"n": "1", "code": code}),
		Data:    map[string]any{"code": code, "mime": mime, "path": p, "size": len(res.GetContent())},
	}, nil
}

// upload fetches a `file:<code>` from the agent and writes its bytes into a
// sandbox path.
func (t *toolset) upload(ctx context.Context, args map[string]any, session, tenant string) (extension.ToolResultData, error) {
	s, err := t.resolve(args, session, tenant)
	if err != nil {
		return extension.ToolResultData{}, err
	}
	code := argStr(args, "code")
	p := argStr(args, "path")
	if code == "" || p == "" {
		return extension.ToolResultData{}, &extension.TypedError{Code: easybus.ToolResultErrorCodeInvalidArgument, Message: "code and path are required"}
	}
	if t.deps.Bus == nil {
		return extension.ToolResultData{}, &extension.TypedError{Code: easybus.ToolResultErrorCodeInternal, Message: "file RPC unavailable (no bus wired)"}
	}
	meta, data, err := extension.GetFileViaAgent(ctx, t.deps.Bus, tenant, code)
	if err != nil {
		return extension.ToolResultData{}, &extension.TypedError{Code: easybus.ToolResultErrorCodeNotFound, Message: fmt.Sprintf("get-file: %v", err)}
	}
	if _, err := t.deps.Cache.Worker(ep(s)).FileWrite(ctx, &workerv1.FileWriteRequest{Path: p, Content: data}); err != nil {
		return extension.ToolResultData{}, &extension.TypedError{Code: easybus.ToolResultErrorCodeRetryable, Message: fmt.Sprintf("file-write: %v", err)}
	}
	name := p
	if meta != nil && meta.Name != "" {
		name = meta.Name
	}
	return extension.ToolResultData{
		Content: i18n.T(t.locale(session, tenant), i18n.MsgDownloaded, i18n.Params{"code": code, "path": p}),
		Data:    map[string]any{"path": p, "code": code, "name": name, "size": len(data)},
	}, nil
}
