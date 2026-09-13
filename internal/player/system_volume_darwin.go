//go:build darwin

package player

import (
	"context"
	"fmt"
	"time"
)

// osaTimeout bounds each osascript invocation (audit L3): without it a
// wedged AppleScript would block forever and hold the state Serialize lock
// hostage for every later command.
var osaTimeout = 2 * time.Second

// runOSA executes an AppleScript snippet via osascript. It is a package-level
// variable so tests can inject a recorder that captures the script and controls
// the returned error without invoking the real osascript binary.
var runOSA = func(script string) error {
	ctx, cancel := context.WithTimeout(context.Background(), osaTimeout)
	defer cancel()
	return runOSACtx(ctx, script)
}

// runOSACtx is the context-aware core of runOSA.
func runOSACtx(ctx context.Context, script string) error {
	return runCommandCtx(ctx, "/usr/bin/osascript", "-e", script)
}

func SetSystemOutputVolume(v int) error {
	return runOSA(fmt.Sprintf(`set volume output volume %d`, v))
}

func SetSystemMute(m bool) error {
	if m {
		return runOSA(`set volume with output muted`)
	}
	return runOSA(`set volume without output muted`)
}
