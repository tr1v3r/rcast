//go:build darwin

package player

import (
	"fmt"
	"os/exec"
)

func SetSystemOutputVolume(v int) error {
	script := fmt.Sprintf(`set volume output volume %d`, v)
	return exec.Command("osascript", "-e", script).Run()
}

func SetSystemMute(m bool) error {
	if m {
		return exec.Command("osascript", "-e", `set volume with output muted`).Run()
	}
	return exec.Command("osascript", "-e", `set volume without output muted`).Run()
}
