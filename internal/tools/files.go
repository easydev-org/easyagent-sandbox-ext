package tools

import (
	"context"
	"fmt"
	"strings"

	workerv1 "github.com/easydev-org/easyagent-sandbox-ext/gen/worker/v1"
	easybus "github.com/easydev-org/easybus-sdk"
	"github.com/easydev-org/easybus-sdk/extension"
)

// maxReadLines caps a line-windowed read.
const maxReadLines = 1000

// readLines returns the file's text, windowed + line-numbered, rejecting binary.
func readLines(content []byte, start, end int32) (string, bool) {
	if isBinary(content) {
		return "", false
	}
	lines := strings.Split(string(content), "\n")
	total := int32(len(lines))
	s, e := start, end
	if s < 0 {
		s = total + s
	}
	if s < 0 {
		s = 0
	}
	if e <= 0 || e > total {
		e = total
	}
	if s > e {
		s = e
	}
	var b strings.Builder
	for i := s; i < e; i++ {
		fmt.Fprintf(&b, "%d\t%s\n", i+1, lines[i])
	}
	return b.String(), true
}

// isBinary reports whether content looks binary (a NUL in the first 8 KiB).
func isBinary(content []byte) bool {
	n := len(content)
	if n > 8192 {
		n = 8192
	}
	for i := 0; i < n; i++ {
		if content[i] == 0 {
			return true
		}
	}
	return false
}

func (t *toolset) fileRead(ctx context.Context, args map[string]any, callID, session, tenant string) (extension.ToolResultData, error) {
	s, err := t.resolve(args, session, tenant)
	if err != nil {
		return extension.ToolResultData{}, err
	}
	path := argStr(args, "path")
	if path == "" {
		return extension.ToolResultData{}, &extension.TypedError{Code: easybus.ToolResultErrorCodeInvalidArgument, Message: "path is required"}
	}
	res, err := t.deps.Cache.Worker(ep(s)).FileRead(ctx, &workerv1.FileReadRequest{
		Path: path, StartLine: int32(argInt(args, "offset", 0)), EndLine: int32(argInt(args, "limit", 0)),
	})
	if err != nil {
		return extension.ToolResultData{}, &extension.TypedError{Code: easybus.ToolResultErrorCodeNotFound, Message: fmt.Sprintf("file-read: %v", err)}
	}
	text, ok := readLines(res.GetContent(), 0, 0)
	if !ok {
		return extension.ToolResultData{}, &extension.TypedError{Code: easybus.ToolResultErrorCodeInvalidArgument, Message: "binary file (use sandbox-download)"}
	}
	return extension.ToolResultData{Content: text, Data: map[string]any{"totalLines": res.GetTotalLines()}}, nil
}

func (t *toolset) fileWrite(ctx context.Context, args map[string]any, callID, session, tenant string) (extension.ToolResultData, error) {
	s, err := t.resolve(args, session, tenant)
	if err != nil {
		return extension.ToolResultData{}, err
	}
	if err := extension.Require(ctx, PermFileWrite, map[string]string{"sandbox": s.Name}); err != nil {
		return extension.ToolResultData{}, err
	}
	path := argStr(args, "path")
	content := argStr(args, "content")
	if path == "" {
		return extension.ToolResultData{}, &extension.TypedError{Code: easybus.ToolResultErrorCodeInvalidArgument, Message: "path is required"}
	}
	if _, err := t.deps.Cache.Worker(ep(s)).FileWrite(ctx, &workerv1.FileWriteRequest{Path: path, Content: []byte(content)}); err != nil {
		return extension.ToolResultData{}, &extension.TypedError{Code: easybus.ToolResultErrorCodeRetryable, Message: fmt.Sprintf("file-write: %v", err)}
	}
	// Return the numbered whole file (mirrors worker-extension).
	res, _ := t.deps.Cache.Worker(ep(s)).FileRead(ctx, &workerv1.FileReadRequest{Path: path})
	text, _ := readLines(res.GetContent(), 0, 0)
	return extension.ToolResultData{Content: text}, nil
}

func (t *toolset) fileList(ctx context.Context, args map[string]any, callID, session, tenant string) (extension.ToolResultData, error) {
	s, err := t.resolve(args, session, tenant)
	if err != nil {
		return extension.ToolResultData{}, err
	}
	depth := argInt(args, "depth", 3)
	limit := argInt(args, "limit", 200)
	res, err := t.deps.Cache.Worker(ep(s)).FileList(ctx, &workerv1.FileListRequest{Path: argStr(args, "path"), Depth: int32(depth), Limit: int32(limit)})
	if err != nil {
		return extension.ToolResultData{}, &extension.TypedError{Code: easybus.ToolResultErrorCodeNotFound, Message: fmt.Sprintf("file-list: %v", err)}
	}
	lines := make([]string, 0, len(res.GetFiles()))
	for _, f := range res.GetFiles() {
		suffix := ""
		if f.GetIsDir() {
			suffix = "/"
		}
		lines = append(lines, fmt.Sprintf("%s%s\t%d", f.GetPath(), suffix, f.GetSize()))
	}
	return extension.ToolResultData{Content: strings.Join(lines, "\n"), Data: map[string]any{"files": res.GetFiles(), "truncated": res.GetTruncated()}}, nil
}

func (t *toolset) fileDelete(ctx context.Context, args map[string]any, callID, session, tenant string) (extension.ToolResultData, error) {
	s, err := t.resolve(args, session, tenant)
	if err != nil {
		return extension.ToolResultData{}, err
	}
	if err := extension.Require(ctx, PermFileWrite, map[string]string{"sandbox": s.Name}); err != nil {
		return extension.ToolResultData{}, err
	}
	path := argStr(args, "path")
	if _, err := t.deps.Cache.Worker(ep(s)).FileDelete(ctx, &workerv1.FileDeleteRequest{Path: path}); err != nil {
		return extension.ToolResultData{}, &extension.TypedError{Code: easybus.ToolResultErrorCodeRetryable, Message: fmt.Sprintf("file-delete: %v", err)}
	}
	return extension.ToolResultData{Content: "deleted " + path}, nil
}

func (t *toolset) fileMove(ctx context.Context, args map[string]any, callID, session, tenant string) (extension.ToolResultData, error) {
	s, err := t.resolve(args, session, tenant)
	if err != nil {
		return extension.ToolResultData{}, err
	}
	if err := extension.Require(ctx, PermFileWrite, map[string]string{"sandbox": s.Name}); err != nil {
		return extension.ToolResultData{}, err
	}
	from, to := argStr(args, "from"), argStr(args, "to")
	if _, err := t.deps.Cache.Worker(ep(s)).FileMove(ctx, &workerv1.FileMoveRequest{From: from, To: to}); err != nil {
		return extension.ToolResultData{}, &extension.TypedError{Code: easybus.ToolResultErrorCodeRetryable, Message: fmt.Sprintf("file-move: %v", err)}
	}
	return extension.ToolResultData{Content: fmt.Sprintf("moved %s -> %s", from, to)}, nil
}

func (t *toolset) fileCopy(ctx context.Context, args map[string]any, callID, session, tenant string) (extension.ToolResultData, error) {
	s, err := t.resolve(args, session, tenant)
	if err != nil {
		return extension.ToolResultData{}, err
	}
	from, to := argStr(args, "from"), argStr(args, "to")
	if _, err := t.deps.Cache.Worker(ep(s)).FileCopy(ctx, &workerv1.FileCopyRequest{From: from, To: to}); err != nil {
		return extension.ToolResultData{}, &extension.TypedError{Code: easybus.ToolResultErrorCodeRetryable, Message: fmt.Sprintf("file-copy: %v", err)}
	}
	return extension.ToolResultData{Content: fmt.Sprintf("copied %s -> %s", from, to)}, nil
}
