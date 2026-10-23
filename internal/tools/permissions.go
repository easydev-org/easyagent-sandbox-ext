package tools

import (
	"context"

	easybus "github.com/easydev-org/easybus-sdk"
	"github.com/easydev-org/easybus-sdk/extension"
)

// Permission enforcement, sandbox side.
//
// This is the second extension to use the permission mechanism, and it is deliberately a
// different SHAPE from the git one: there the target is a repository slug, here it is a
// sandbox name. Nothing about the mechanism changes — the agent still only asks "does this
// session hold a grant for this key", and the extension still decides whether that grant
// covers THIS call. What differs is only what "this call" resolves to, which is exactly the
// knowledge that has to live here.

// Permission keys this extension declares.
const (
	// PermExec allows running commands in a sandbox.
	PermExec = "exec"
	// PermFileWrite allows creating, replacing, moving and deleting files.
	PermFileWrite = "file.write"
)

// requireSandbox checks a permission against the sandbox a call targets.
//
// The target is the sandbox NAME, resolved the same way the tool itself resolves it — from
// the `sandbox` argument, falling back to the tenant's configured default. Deriving it any
// other way would let a caller name one sandbox in the arguments and be authorised for
// another.
//
// A call that cannot be resolved to a sandbox is REFUSED rather than allowed: the whole point
// of the check is to know which sandbox is being touched, and "unknown" cannot be authorised.
func (t *toolset) requireSandbox(ctx context.Context, perm string, args map[string]any, session, tenant string) error {
	s, err := t.resolve(args, session, tenant)
	if err != nil {
		return err
	}
	name := s.Name
	if name == "" {
		return &extension.TypedError{
			Code:    easybus.ToolResultErrorCodeInvalidArgument,
			Message: "cannot determine which sandbox this call targets, so " + perm + " cannot be authorised",
		}
	}
	return extension.Require(ctx, perm, map[string]string{"sandbox": name})
}
