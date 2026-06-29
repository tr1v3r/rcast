package player

import (
	"os/exec"
	"testing"
)

// TestOSCommand_KillNilProcess covers the adapter branch where the command was
// never started (Process == nil): Kill must be a no-op, mirroring the original
// inline `p.command.Process != nil` guard in Stop.
func TestOSCommand_KillNilProcess(t *testing.T) {
	c := &osCommand{cmd: &exec.Cmd{}} // Process is nil before Start
	if err := c.Kill(); err != nil {
		t.Fatalf("Kill on unstarted command = %v, want nil", err)
	}
}

// TestOSCommand_KillAlreadyExited covers the os.ErrProcessDone branch: after a
// process exits and is reaped, Kill must return nil instead of surfacing the
// "process already finished" error.
func TestOSCommand_KillAlreadyExited(t *testing.T) {
	cmd := exec.Command("true")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("wait: %v", err)
	}
	c := &osCommand{cmd: cmd}
	if err := c.Kill(); err != nil {
		t.Fatalf("Kill on exited process = %v, want nil", err)
	}
}
