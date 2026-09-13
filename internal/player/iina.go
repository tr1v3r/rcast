package player

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/tr1v3r/pkg/log"
)

// https://mpv.io/manual/stable/#properties

const sockPathPrefix = "/tmp/rcast_iina-ipc-sock_"

const iinaAppBinary = "/Applications/IINA.app/Contents/MacOS/iina"

// ipcTimeout caps how long a single IPC write/read may block. Without it, a
// hung IINA would hold the player lock forever and stall every later command.
// It is a var (not a const) so tests can shrink it.
var ipcTimeout = 3 * time.Second

// command is the small surface of an OS process that IINAPlayer uses, so
// launch/Stop can be exercised without spawning a real IINA.
type command interface {
	Start() error
	Wait() error
	Kill() error
}

// osCommand adapts exec.Cmd to command. Kill mirrors the prior Stop logic:
// tolerate a nil process and treat an already-exited process as success.
type osCommand struct{ cmd *exec.Cmd }

func (c *osCommand) Start() error { return c.cmd.Start() }

func (c *osCommand) Wait() error { return c.cmd.Wait() }

func (c *osCommand) Kill() error {
	if c.cmd.Process == nil {
		return nil
	}
	if err := c.cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return err
	}
	return nil
}

func NewIINAPlayer(fullscreen bool) *IINAPlayer {
	p := &IINAPlayer{
		fullscreen: fullscreen,
		activate:   activateIINA,
		find:       findIINA,
		commandFactory: func(ctx context.Context, exe string, args []string) command {
			return &osCommand{iinaLaunchCommand(ctx, exe, args)}
		},
		dial:       net.Dial,
		quitApp:    quitIINAApp,
		retryDelay: 150 * time.Millisecond,
		ipcPoll:    25 * time.Millisecond,
		events:     make(chan Event, eventQueueLen),
		done:       make(chan struct{}),
	}
	go p.dispatchLoop()
	return p
}

type IINAPlayer struct {
	mu             sync.Mutex
	conn           net.Conn
	connDead       bool // readLoop saw the current conn hit a terminal read error
	connGen        int  // bumped on every connection install; tags pending replies
	pending        map[int]pendingWait
	sockPath       string
	requestIDCount int

	command    command // was *exec.Cmd
	cmdDone    chan struct{}
	exe        string // launcher executable behind command ("" before first launch)
	fullscreen bool

	// runtime hooks (unexported; production defaults above)
	find           func() (string, error)
	commandFactory func(ctx context.Context, exe string, args []string) command
	dial           func(network, addr string) (net.Conn, error)
	activate       func(context.Context) error
	quitApp        func(context.Context) error
	retryDelay     time.Duration
	ipcPoll        time.Duration

	events  chan Event
	eventFn func(Event)
	done    chan struct{}
}

func (p *IINAPlayer) Close(_ context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	err := p.closeLocked()
	p.shutdownEventsLocked()
	return err
}

// closeLocked tears down the IPC connection and removes the socket file.
// Caller must hold p.mu.
func (p *IINAPlayer) closeLocked() error {
	var closeErr error
	if p.conn != nil {
		if err := p.conn.Close(); err != nil {
			closeErr = fmt.Errorf("closing iina ipc socket fail: %w", err)
		}
		p.conn = nil
		p.connDead = false
	}
	if p.sockPath != "" {
		if err := os.Remove(p.sockPath); err != nil && !os.IsNotExist(err) {
			if closeErr != nil {
				return fmt.Errorf("multiple errors: %w, socket removal: %v", closeErr, err)
			}
			return fmt.Errorf("removing socket file: %w", err)
		}
		p.sockPath = ""
	}
	return closeErr
}

func (p *IINAPlayer) Play(ctx context.Context, uri string, volume int) error {
	log.CtxDebug(ctx, "IINAPlayer Play: uri=%s volume=%d", uri, volume)

	p.mu.Lock()
	hasEndpoint := p.sockPath != ""
	p.mu.Unlock()

	if hasEndpoint {
		// iina-cli may exit after handing the request to IINA, so IPC—not the
		// launcher process—is the source of truth for a reusable player.
		path, known, probeErr := p.probeCurrentPath(ctx)
		if isIPCBusy(probeErr) {
			// Audit M4: an i/o timeout means IINA is alive but busy (large
			// file demux, slow network stream). Probe once more to smooth
			// transient load; a second timeout must NOT restart the window.
			log.CtxWarn(ctx, "IINA ipc probe timed out, retrying once: %v", probeErr)
			path, known, probeErr = p.probeCurrentPath(ctx)
			if isIPCBusy(probeErr) {
				log.CtxWarn(ctx, "IINA ipc still busy, keeping live instance: %v", probeErr)
				return fmt.Errorf("IINA ipc busy, refusing to restart live instance: %w", probeErr)
			}
		}

		healthy := probeErr == nil || !errors.Is(probeErr, errIPCTransport)
		if healthy {
			if known && path == uri {
				// Audit L2: a failed volume update on the reuse path used to
				// be swallowed; surface it to the caller instead.
				if err := p.SetVolume(ctx, volume); err != nil {
					log.CtxWarn(ctx, "reuse IINA set volume failed: %v", err)
					return fmt.Errorf("setting volume on reused IINA: %w", err)
				}
				if err := p.Resume(ctx); err != nil {
					return err
				}
				p.bringToFront(ctx)
				return nil
			}
			if probeErr != nil {
				// e.g. "property unavailable" right after a stop: the
				// connection is healthy, only the path property is gone.
				log.CtxDebug(ctx, "get path property failed: %v", probeErr)
			} else {
				log.CtxDebug(ctx, "path mismatch: current=%q target=%s", path, uri)
			}
			if err := p.sendOK(ctx, []any{"loadfile", uri, "replace"}, "loadfile"); err == nil {
				if err := p.SetVolume(ctx, volume); err != nil {
					p.bringToFront(ctx)
					log.CtxWarn(ctx, "reuse IINA set volume failed: %v", err)
					return fmt.Errorf("setting volume on reused IINA: %w", err)
				}
				p.bringToFront(ctx)
				return nil
			} else {
				log.CtxWarn(ctx, "reuse IINA ipc loadfile failed: %v", err)
			}
		} else {
			// Audit M4: EOF/ECONNREFUSED/write failure — the connection is
			// really gone; restart below.
			log.CtxDebug(ctx, "reuse probe failed (%v), treating instance as gone", probeErr)
		}

		log.CtxWarn(ctx, "failed to reuse IINA instance, restarting")
		_ = p.Stop(ctx)
	}

	// Launch a fresh, IPC-controllable IINA instance.
	exe, err := p.find()
	if err != nil {
		return fmt.Errorf("IINA not found: %w", err)
	}

	var launchErr error
	for attempt := range 2 {
		if launchErr = p.launch(ctx, exe, uri, volume); launchErr == nil {
			// `open -n` activates the newly created app instance itself. Calling
			// `open -a IINA` here could focus an orphaned older instance.
			if exe != iinaAppBinary {
				p.bringToFront(ctx)
			}
			return nil
		}
		log.CtxWarn(ctx, "IINA launch attempt %d failed: %v", attempt+1, launchErr)
		_ = p.Stop(ctx)
		if err := ctx.Err(); err != nil {
			return err
		}
		if attempt == 0 {
			timer := time.NewTimer(p.retryDelay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
		}
	}
	return fmt.Errorf("failed to start IINA after retry: %w", launchErr)
}

// probeCurrentPath asks mpv which URI is currently loaded. known is false
// when the property is absent or empty (e.g. right after a stop) even though
// the connection is healthy.
func (p *IINAPlayer) probeCurrentPath(ctx context.Context) (path string, known bool, err error) {
	val, err := p.getProperty(ctx, "path")
	if err != nil {
		return "", false, err
	}
	s, ok := val.(string)
	if !ok || s == "" {
		return "", false, nil
	}
	return s, true, nil
}

func (p *IINAPlayer) launch(ctx context.Context, exe, uri string, volume int) error {
	p.mu.Lock()
	p.sockPath = sockPathPrefix + uuid.NewString()
	p.exe = exe
	sockPath := p.sockPath
	p.mu.Unlock()

	args := []string{
		"--keep-running",
		"--mpv-input-ipc-server=" + sockPath,
		"--mpv-volume=" + strconv.Itoa(volume),
		"--mpv-keep-open=yes",
	}
	if p.fullscreen {
		args = append(args, "--mpv-fs=yes")
	}
	args = append(args, uri)

	cmd := p.commandFactory(ctx, exe, args)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting IINA process: %w", err)
	}

	done := make(chan struct{})
	p.mu.Lock()
	p.resetConnLocked()
	p.command = cmd
	p.cmdDone = done
	p.exe = exe
	p.mu.Unlock()
	go p.wait(cmd, done)
	if err := p.waitForIPC(ctx, sockPath); err != nil {
		return fmt.Errorf("waiting for IINA IPC: %w", err)
	}
	return nil
}

func iinaLaunchCommand(ctx context.Context, exe string, args []string) *exec.Cmd {
	if exe == iinaAppBinary {
		openArgs := []string{"-n", "-a", "IINA", "--args"}
		openArgs = append(openArgs, args...)
		// -n forces a separate application instance. Without it, LaunchServices
		// may forward the request to an IINA left over from a previous Rcast run,
		// and that instance will not create our new mpv IPC socket.
		return exec.CommandContext(ctx, "/usr/bin/open", openArgs...)
	}
	return exec.CommandContext(ctx, exe, args...)
}

func activateIINA(ctx context.Context) error {
	return exec.CommandContext(ctx, "/usr/bin/open", "-a", "IINA").Run()
}

func (p *IINAPlayer) bringToFront(ctx context.Context) {
	if p.activate == nil {
		return
	}
	activateCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if err := p.activate(activateCtx); err != nil {
		// Playback is already ready at this point, so focus failure should not
		// turn a successful cast into a SOAP error.
		log.CtxWarn(ctx, "activate IINA window: %v", err)
	}
}

// wait reaps the launcher process exactly once and closes done so Stop can
// observe the exit (audit M3: exit confirmation). It clears p.command before
// closing done, so anything that observes done also observes the cleared
// field (channel close provides the happens-before edge).
func (p *IINAPlayer) wait(cmd command, done chan struct{}) {
	_ = cmd.Wait()
	p.mu.Lock()
	if p.command == cmd {
		p.command = nil
		p.cmdDone = nil
	}
	p.mu.Unlock()
	close(done)
}

// startupBudget is the overall envelope launch() may spend waiting for the
// mpv IPC socket to appear: cold-starting IINA (first launch after boot,
// heavy system load) takes far longer than a single IPC round trip, and
// giving up early is exactly what orphans instances (audit H2). It derives
// from ipcTimeout (≈10s at the default 3s) so tests that shrink ipcTimeout
// get a proportionally small budget.
func startupBudget() time.Duration { return ipcTimeout * 10 / 3 }

func startupDeadline(ctx context.Context) time.Time {
	dl := time.Now().Add(startupBudget())
	if ctxDL, ok := ctx.Deadline(); ok && ctxDL.Before(dl) {
		return ctxDL
	}
	return dl
}

// waitForIPC polls for the mpv IPC socket with exponential backoff (audit H2:
// ~10s budget instead of 3s, so slow IINA cold starts are not abandoned —
// an abandoned launch is what produced orphaned double instances).
func (p *IINAPlayer) waitForIPC(ctx context.Context, sockPath string) error {
	deadline := startupDeadline(ctx)
	var lastErr error
	delay := p.ipcPoll
	for {
		conn, err := p.connect(sockPath)
		if err == nil {
			p.mu.Lock()
			if p.sockPath != sockPath {
				p.mu.Unlock()
				_ = conn.Close()
				return fmt.Errorf("IINA IPC endpoint changed while starting")
			}
			p.installConnLocked(conn)
			p.mu.Unlock()
			return nil
		}
		lastErr = err
		if !time.Now().Before(deadline) {
			return lastErr
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
		delay *= 2
		if delay > ipcPollMax {
			delay = ipcPollMax
		}
	}
}

// connect dials the mpv IPC socket and hardens its file permissions (audit
// L6, best effort). IINA creates the socket per umask, and the socket is the
// only control channel for the player, so any local user could otherwise
// drive playback. The chmod closes most of the window but is inherently racy
// (TOCTOU between mpv's bind and our chmod); a fully tight setup would need
// the socket in a private directory.
func (p *IINAPlayer) connect(sockPath string) (net.Conn, error) {
	if sockPath == "" {
		return nil, fmt.Errorf("iina ipc socket path is empty")
	}
	conn, err := p.dial("unix", sockPath)
	if err != nil {
		return nil, fmt.Errorf("connect to iina ipc socket fail: %w", err)
	}
	if err := os.Chmod(sockPath, 0o600); err != nil {
		log.Debug("chmod iina ipc socket %s: %v", sockPath, err)
	}
	return conn, nil
}

func (p *IINAPlayer) Pause(ctx context.Context) error {
	return p.sendOK(ctx, []any{"set_property", "pause", true}, "pause")
}

func (p *IINAPlayer) StopPlayback(ctx context.Context) error {
	return p.sendOK(ctx, []any{"stop"}, "stop playback")
}

func (p *IINAPlayer) Resume(ctx context.Context) error {
	return p.sendOK(ctx, []any{"set_property", "pause", false}, "resume")
}

func (p *IINAPlayer) SetVolume(ctx context.Context, v int) error {
	return p.sendOK(ctx, []any{"set_property", "volume", v}, "set volume")
}

func (p *IINAPlayer) SetMute(ctx context.Context, m bool) error {
	return p.sendOK(ctx, []any{"set_property", "mute", m}, "set mute")
}

func (p *IINAPlayer) SetFullscreen(ctx context.Context, f bool) error {
	return p.sendOK(ctx, []any{"set_property", "fullscreen", f}, "set fullscreen")
}

func (p *IINAPlayer) SetTitle(ctx context.Context, title string) error {
	return p.sendOK(ctx, []any{"set_property", "force-media-title", title}, "set title")
}

// Screenshot saves a screenshot to path when given (mpv screenshot-to-file),
// falling back to mpv's configured screenshot directory otherwise (audit L5:
// the path argument used to be silently ignored).
func (p *IINAPlayer) Screenshot(ctx context.Context, path string) error {
	if path != "" {
		return p.sendOK(ctx, []any{"screenshot-to-file", path}, "screenshot")
	}
	return p.sendOK(ctx, []any{"screenshot"}, "screenshot")
}

func (p *IINAPlayer) SetSpeed(ctx context.Context, speed float64) error {
	return p.sendOK(ctx, []any{"set_property", "speed", speed}, "set speed")
}

func (p *IINAPlayer) Seek(ctx context.Context, seconds float64) error {
	return p.sendOK(ctx, []any{"seek", seconds, "absolute"}, "seek")
}

func (p *IINAPlayer) GetPosition(ctx context.Context) (float64, error) {
	return p.getPropertyNum(ctx, "time-pos")
}

func (p *IINAPlayer) GetDuration(ctx context.Context) (float64, error) {
	return p.getPropertyNum(ctx, "duration")
}

func (p *IINAPlayer) getProperty(ctx context.Context, name string) (any, error) {
	return p.send(ctx, []any{"get_property", name})
}

func (p *IINAPlayer) getPropertyNum(ctx context.Context, name string) (float64, error) {
	val, err := p.send(ctx, []any{"get_property", name})
	if err != nil {
		return 0, err
	}
	if v, ok := val.(float64); ok {
		return v, nil
	}
	return 0, fmt.Errorf("unexpected type for %s: %T", name, val)
}

// sendOK issues a command and wraps any error with the action name.
func (p *IINAPlayer) sendOK(ctx context.Context, command []any, action string) error {
	if _, err := p.send(ctx, command); err != nil {
		return fmt.Errorf("calling iina %s failed: %w", action, err)
	}
	return nil
}

// ipcDeadline returns the earlier of ipcTimeout-from-now and the context deadline.
func ipcDeadline(ctx context.Context) time.Time {
	dl := time.Now().Add(ipcTimeout)
	if ctxDL, ok := ctx.Deadline(); ok && ctxDL.Before(dl) {
		return ctxDL
	}
	return dl
}

// findIINA locates an executable, IPC-controllable IINA binary: it prefers
// iina-cli, then the IINA.app internal binary. It returns a real error when
// nothing is installed, so callers surface "IINA not found" instead of failing
// later on an empty socket path.
func findIINA() (string, error) {
	for _, c := range []string{
		"/opt/homebrew/bin/iina-cli",
		"/usr/local/bin/iina-cli",
	} {
		if fileExists(c) {
			return c, nil
		}
	}
	if fileExists(iinaAppBinary) {
		return iinaAppBinary, nil
	}
	return "", fmt.Errorf("IINA not installed (looked for iina-cli and %s)", iinaAppBinary)
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
