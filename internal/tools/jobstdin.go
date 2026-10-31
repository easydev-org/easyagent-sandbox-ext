package tools

import (
	"context"
	"fmt"

	easybus "github.com/easydev-org/easybus-sdk"
	"github.com/easydev-org/easybus-sdk/extension"

	workerv1 "github.com/easydev-org/easyagent-sandbox-ext/gen/worker/v1"
	"github.com/easydev-org/easyagent-sandbox-ext/internal/i18n"
)

// jobStdin writes to a running job standard input.
//
// IT MOVED HERE when the dataflow tools were removed. It has nothing to do with the flow graph — it is
// one RPC, JobStdin, which still exists — and deleting the file it happened to live in would have taken
// a working tool with it.
func (t *toolset) jobStdin(ctx context.Context, args map[string]any, callID, session, tenant string) (extension.ToolResultData, error) {
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
