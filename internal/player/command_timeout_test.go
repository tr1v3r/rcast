package player

// Tests for the bounded external-command helper (audit L3: osascript and any
// other helper invocation must be killed by its context instead of blocking
// the caller forever) and the signal support osCommand gained for Stop's
// SIGTERM escalation (audit H2③).

import (
	"context"
	"errors"
	"os/exec"
	"syscall"
	"testing"
	"time"
)

func TestRunCommandCtx_Success(t *testing.T) {
	if err := runCommandCtx(context.Background(), "/usr/bin/true"); err != nil {
		t.Fatalf("runCommandCtx(true): %v", err)
	}
}

func TestRunCommandCtx_TimesOutAndKills(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	start := time.Now()
	err := runCommandCtx(ctx, "/bin/sleep", "30")
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("runCommandCtx(sleep 30) err = nil, want deadline error")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want context.DeadlineExceeded", err)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("command took %v to be killed, want prompt termination", elapsed)
	}
}

func TestOSCommand_SignalNilProcess(t *testing.T) {
	c := &osCommand{cmd: &exec.Cmd{}} // Process is nil before Start
	if err := c.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("Signal on unstarted command = %v, want nil", err)
	}
}

func TestOSCommand_SignalAlreadyExited(t *testing.T) {
	cmd := exec.Command("/usr/bin/true")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("wait: %v", err)
	}
	c := &osCommand{cmd: cmd}
	if err := c.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("Signal on exited process = %v, want nil", err)
	}
}
