package player

import (
	"context"
	"os/exec"
)

// runCommandCtx runs an external command to completion, bounded by ctx
// (exec.Cmd kills the process when ctx is done). When the process had to be
// killed by the context, the context error is surfaced instead of the bare
// "signal: killed", so callers see the deadline. Shared by the AppleScript
// helpers so no osascript invocation can outlive its budget and wedge the
// caller (audit L3).
func runCommandCtx(ctx context.Context, name string, args ...string) error {
	err := exec.CommandContext(ctx, name, args...).Run()
	if err != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}
