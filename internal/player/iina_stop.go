package player

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"syscall"
	"time"

	"github.com/tr1v3r/pkg/log"
)

// stopPollInterval is how often Stop's exit-confirmation loop checks its
// evidence sources (socket file gone, IPC EOF, launcher exit).
const stopPollInterval = 10 * time.Millisecond

// signaler is optionally implemented by commands that can deliver a signal
// to the underlying process. osCommand implements it; test fakes do not have
// to. Stop uses it to SIGTERM the iina-cli wrapper before resorting to
// SIGKILL (audit H2③): iina-cli's signal handler terminates the IINA
// instance it launched, while SIGKILL only ever reaches the wrapper.
type signaler interface {
	Signal(os.Signal) error
}

// Signal sends sig to the wrapped process, tolerating unstarted and already
// exited processes (mirrors osCommand.Kill).
func (c *osCommand) Signal(sig os.Signal) error {
	if c.cmd.Process == nil {
		return nil
	}
	if err := c.cmd.Process.Signal(sig); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return err
	}
	return nil
}

// quitIINAApp asks IINA to quit via AppleScript (audit H2④). For instances
// launched through `open -n` this is the only lever rcast holds: the wrapper
// process has already exited, so neither SIGTERM nor SIGKILL can reach IINA.
func quitIINAApp(ctx context.Context) error {
	return runCommandCtx(ctx, "/usr/bin/osascript", "-e", `tell application "IINA" to quit`)
}

// exeTracksIINA reports whether the launcher process tracks the IINA
// instance's lifetime: iina-cli --keep-running exits together with IINA, so
// its exit is meaningful exit confirmation. `open` returns immediately after
// handing the request to LaunchServices, so its exit proves nothing.
func exeTracksIINA(exe string) bool { return exe != "" && exe != iinaAppBinary }

// budgetFrom returns how much of cap remains after honoring ctx's deadline.
func budgetFrom(ctx context.Context, cap time.Duration) time.Duration {
	b := cap
	if dl, ok := ctx.Deadline(); ok {
		if u := time.Until(dl); u < b {
			b = u
		}
	}
	if b < 0 {
		return 0
	}
	return b
}

// Stop terminates the running IINA instance reliably (audits H2, M3):
//
//  1. Deliver mpv's quit command over IPC. If the connection is missing but
//     the socket may still be coming up (launch gave up while IINA was slow
//     — the H2 orphan race), poll for the socket within a bounded tail
//     budget instead of abandoning the instance.
//  2. Wait for exit confirmation (socket file removed / IPC EOF / launcher
//     exit) before closing the connection and unlinking the socket — the
//     socket used to be unlinked immediately, leaving a busy IINA alive and
//     uncontrollable (M3).
//  3. Escalate on timeout: SIGTERM the iina-cli wrapper (H2③), or ask IINA
//     to quit via AppleScript for `open`-launched instances (H2④); SIGKILL
//     the wrapper only as the last resort.
//
// The whole sequence is bounded by the caller's context deadline and the
// startup envelope (~10s at defaults), so Stop can never hang forever.
func (p *IINAPlayer) Stop(ctx context.Context) error {
	budget := budgetFrom(ctx, startupBudget())

	p.mu.Lock()
	sockPath := p.sockPath
	ic := p.conn
	cmd := p.command
	cmdDone := p.cmdDone
	exe := p.exe
	p.mu.Unlock()

	tracks := exeTracksIINA(exe)
	var extra net.Conn // transient connection acquired just to deliver quit
	quitWritten := false
	// sawSocket gates the "socket file disappeared" exit evidence: a path
	// that never existed on disk must not count as proof IINA exited.
	sawSocket := sockPath != "" && fileExists(sockPath)
	// A launcher process was started for this endpoint: only then can IINA
	// still be mid-startup and about to create the socket (audit H2).
	launchAttempted := cmd != nil || cmdDone != nil

	if sockPath != "" {
		qc := ic
		if qc != nil && p.isConnDead(qc) {
			qc = nil // stale connection: redial below to deliver quit
		}
		if qc == nil && launchAttempted {
			qc = p.waitForQuitSocket(ctx, sockPath, budget/5)
			if qc != nil {
				extra = qc
				sawSocket = true // the dial proved the socket file exists
			}
		}
		if qc != nil {
			if err := p.writeQuit(ctx, qc); err != nil {
				log.CtxWarn(ctx, "delivering iina quit command: %v", err)
			} else {
				quitWritten = true
			}
		}
	}

	// A connection whose EOF can confirm the exit (a transient quit
	// connection, when one was acquired).
	exitConn := ic
	if extra != nil {
		exitConn = extra
	}
	confirmed := false
	if quitWritten {
		confirmed = p.waitExit(budget/2, sockPath, sawSocket, exitConn, cmdDone, tracks)
	}
	if !confirmed {
		if tracks && cmd != nil {
			// Audit H2③: iina-cli's SIGTERM handler terminates IINA with it.
			if sig, ok := cmd.(signaler); ok {
				if err := sig.Signal(syscall.SIGTERM); err != nil {
					log.CtxWarn(ctx, "SIGTERM to IINA launcher: %v", err)
				} else if p.waitExit(budget/4, sockPath, sawSocket, exitConn, cmdDone, tracks) {
					confirmed = true
				}
			}
		} else if exe == iinaAppBinary && (quitWritten || fileExists(sockPath)) {
			// Audit H2④: only lever for `open`-launched instances, and only
			// when there is evidence IINA is actually alive (quit was written
			// or the socket file exists).
			if p.quitApp != nil {
				qctx, cancel := context.WithTimeout(ctx, budget/4)
				err := p.quitApp(qctx)
				cancel()
				if err != nil {
					log.CtxWarn(ctx, "AppleScript quit of IINA: %v", err)
				} else if p.waitExit(budget/4, sockPath, sawSocket, exitConn, cmdDone, tracks) {
					confirmed = true
				}
			}
		}
	}

	var killErr error
	if !confirmed && cmd != nil {
		if err := cmd.Kill(); err != nil {
			// For `open`-launched instances the wrapper is long gone and the
			// Kill is a formality; a surviving IINA would be a limitation of
			// the open path (no PID to signal).
			log.CtxWarn(ctx, "killing IINA launcher: %v", err)
			killErr = err
		} else {
			// Wait for the reaper goroutine: observing the closed cmdDone
			// guarantees p.command was cleared first (wait clears it before
			// closing), so Stop returns without leaving stale state visible.
			waitCmdDone(budget/4, cmdDone)
		}
	}

	if extra != nil {
		_ = extra.Close()
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	stopErr := p.closeLocked()
	if killErr != nil {
		if stopErr != nil {
			return fmt.Errorf("multiple errors: %w, killing process: %v", stopErr, killErr)
		}
		return fmt.Errorf("killing process: %w", killErr)
	}
	return stopErr
}

// waitForQuitSocket covers the audit H2 orphan race: launch gave up (or the
// caller timed out) while IINA was still starting, so the socket may appear
// at any moment. Poll-dial within the budget and return a connection usable
// for the quit command instead of abandoning the instance to launchd.
func (p *IINAPlayer) waitForQuitSocket(ctx context.Context, sockPath string, budget time.Duration) net.Conn {
	deadline := time.Now().Add(budget)
	delay := p.ipcPoll
	for {
		if conn, err := p.connect(sockPath); err == nil {
			return conn
		}
		remaining := time.Until(deadline)
		if remaining <= 0 || ctx.Err() != nil {
			return nil
		}
		timer := time.NewTimer(min(delay, remaining))
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
		delay *= 2
		if delay > ipcPollMax {
			delay = ipcPollMax
		}
	}
}

// writeQuit delivers mpv's quit command. It takes p.mu so the write cannot
// interleave with a concurrent send() on the same socket.
func (p *IINAPlayer) writeQuit(ctx context.Context, c net.Conn) error {
	payload, err := json.Marshal(MPVJSONIPCRequest{Command: []any{"quit"}})
	if err != nil {
		return fmt.Errorf("marshaling quit command: %w", err)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := c.SetWriteDeadline(ipcDeadline(ctx)); err != nil {
		return fmt.Errorf("setting quit write deadline: %w", err)
	}
	if _, err := c.Write(append(payload, '\n')); err != nil {
		return fmt.Errorf("writing quit command: %w", err)
	}
	return nil
}

// waitExit polls for evidence that IINA exited: mpv removes its IPC socket
// on exit (only meaningful when the socket was seen to exist), the IPC
// connection sees EOF, and — for iina-cli launches whose wrapper tracks the
// instance — the launcher process exits.
func (p *IINAPlayer) waitExit(timeout time.Duration, sockPath string, sawSocket bool, conn net.Conn, cmdDone <-chan struct{}, tracks bool) bool {
	if timeout <= 0 {
		timeout = time.Millisecond
	}
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	tick := time.NewTicker(stopPollInterval)
	defer tick.Stop()
	for {
		if sawSocket && !fileExists(sockPath) {
			return true // mpv removed its socket on exit
		}
		if conn != nil && p.isConnDead(conn) {
			return true // IPC peer closed the connection
		}
		if tracks && cmdDone != nil {
			select {
			case <-cmdDone:
				return true // iina-cli exited, taking IINA with it
			default:
			}
		}
		select {
		case <-deadline.C:
			return false
		case <-tick.C:
		}
	}
}

// waitCmdDone waits (bounded) for the launcher's reaper goroutine. Observing
// the closed channel also guarantees p.command was cleared first (wait
// clears it before closing).
func waitCmdDone(timeout time.Duration, cmdDone <-chan struct{}) {
	if cmdDone == nil || timeout <= 0 {
		return
	}
	t := time.NewTimer(timeout)
	defer t.Stop()
	select {
	case <-cmdDone:
	case <-t.C:
	}
}

// isConnDead reports whether c is still the current connection and its
// reader goroutine has seen it fail.
func (p *IINAPlayer) isConnDead(c net.Conn) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.conn == c && p.connDead
}
