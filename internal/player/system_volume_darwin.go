//go:build darwin

package player

import (
	"fmt"
	"os/exec"
)

// runOSA executes an AppleScript snippet via osascript. It is a package-level
// variable so tests can inject a recorder that captures the script and controls
// the returned error without invoking the real osascript binary.
var runOSA = func(script string) error {
	return exec.Command("osascript", "-e", script).Run()
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
