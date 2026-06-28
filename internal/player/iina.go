package player

import (
	"bufio"
	"context"
	"encoding/json"
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

func NewIINAPlayer(fullscreen bool) *IINAPlayer {
	return &IINAPlayer{
		fullscreen: fullscreen,
		activate:   activateIINA,
	}
}

type IINAPlayer struct {
	mu       sync.Mutex
	conn     net.Conn
	reader   *bufio.Reader
	sockPath string

	requestIDCount int

	command    *exec.Cmd
	fullscreen bool
	activate   func(context.Context) error
}

func (p *IINAPlayer) Close(_ context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.closeLocked()
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
		p.reader = nil
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

// resetConnLocked drops the current connection so the next send reconnects.
// Caller must hold p.mu.
func (p *IINAPlayer) resetConnLocked() {
	if p.conn != nil {
		_ = p.conn.Close()
		p.conn = nil
		p.reader = nil
	}
}

func (p *IINAPlayer) Play(ctx context.Context, uri string, volume int) error {
	log.CtxDebug(ctx, "IINAPlayer Play: uri=%s volume=%d", uri, volume)

	p.mu.Lock()
	hasEndpoint := p.sockPath != ""
	p.mu.Unlock()

	if hasEndpoint {
		// iina-cli may exit after handing the request to IINA, so IPC—not the
		// launcher process—is the source of truth for a reusable player.
		loadIntoExisting := false
		if val, err := p.getProperty(ctx, "path"); err == nil {
			if currentPath, ok := val.(string); ok && currentPath == uri {
				_ = p.SetVolume(ctx, volume)
				if err := p.Resume(ctx); err != nil {
					return err
				}
				p.bringToFront(ctx)
				return nil
			} else {
				log.CtxDebug(ctx, "path mismatch or invalid type: current=%v target=%s", val, uri)
			}
			loadIntoExisting = true
		} else {
			log.CtxDebug(ctx, "get path property failed: %v", err)
			// mpv reports path as unavailable after a "stop" command even
			// though the IPC connection remains healthy.
			loadIntoExisting = p.hasLiveConnection()
		}

		if loadIntoExisting {
			if err := p.sendOK(ctx, []any{"loadfile", uri, "replace"}, "loadfile"); err == nil {
				_ = p.SetVolume(ctx, volume)
				p.bringToFront(ctx)
				return nil
			} else {
				log.CtxWarn(ctx, "reuse IINA ipc loadfile failed: %v", err)
			}
		}

		log.CtxWarn(ctx, "failed to reuse IINA instance, restarting")
		_ = p.Stop(ctx)
	}

	// Launch a fresh, IPC-controllable IINA instance.
	exe, err := findIINA()
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
			timer := time.NewTimer(150 * time.Millisecond)
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

func (p *IINAPlayer) launch(ctx context.Context, exe, uri string, volume int) error {
	p.mu.Lock()
	p.sockPath = sockPathPrefix + uuid.NewString()
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

	cmd := iinaLaunchCommand(ctx, exe, args)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting IINA process: %w", err)
	}

	p.mu.Lock()
	p.conn = nil
	p.command = cmd
	p.mu.Unlock()
	go p.wait(cmd)
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

func (p *IINAPlayer) wait(cmd *exec.Cmd) {
	_ = cmd.Wait()
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.command == cmd {
		p.command = nil
	}
}

func (p *IINAPlayer) waitForIPC(ctx context.Context, sockPath string) error {
	deadline := ipcDeadline(ctx)
	var lastErr error
	for {
		conn, err := p.connect(sockPath)
		if err == nil {
			p.mu.Lock()
			if p.sockPath != sockPath {
				p.mu.Unlock()
				_ = conn.Close()
				return fmt.Errorf("IINA IPC endpoint changed while starting")
			}
			if p.conn == nil {
				p.conn = conn
				p.reader = bufio.NewReader(conn)
			} else {
				_ = conn.Close()
			}
			p.mu.Unlock()
			return nil
		}
		lastErr = err
		if time.Now().After(deadline) {
			return lastErr
		}
		timer := time.NewTimer(25 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func (p *IINAPlayer) connect(sockPath string) (net.Conn, error) {
	if sockPath == "" {
		return nil, fmt.Errorf("iina ipc socket path is empty")
	}
	conn, err := net.Dial("unix", sockPath)
	if err != nil {
		return nil, fmt.Errorf("connect to iina ipc socket fail: %w", err)
	}
	return conn, nil
}

func (p *IINAPlayer) hasLiveConnection() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.conn != nil
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

func (p *IINAPlayer) Stop(ctx context.Context) error {
	p.mu.Lock()
	hasEndpoint := p.sockPath != ""
	p.mu.Unlock()
	if hasEndpoint {
		quitCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
		_, _ = p.send(quitCtx, []any{"quit"})
		cancel()
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	// Close IPC + remove socket, then kill the process. wait() owns cmd.Wait so
	// every child is reaped exactly once.
	stopErr := p.closeLocked()

	if p.command != nil && p.command.Process != nil {
		if err := p.command.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
			if stopErr != nil {
				return fmt.Errorf("multiple errors: %w, killing process: %v", stopErr, err)
			}
			return fmt.Errorf("killing process: %w", err)
		}
		p.command = nil
	}
	return stopErr
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

func (p *IINAPlayer) Screenshot(ctx context.Context, _ string) error {
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

// send allocates a request id under the lock, writes the command, and reads
// back the matching response. Each read/write is capped by ipcTimeout and the
// context deadline, so a stalled IINA cannot hold the lock indefinitely.
func (p *IINAPlayer) send(ctx context.Context, command []any) (any, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.sockPath == "" {
		return nil, fmt.Errorf("iina ipc socket path is empty")
	}

	// Allocate the request id inside the critical section: concurrent callers
	// (every transport handler dispatches its own goroutine) can no longer race
	// on requestIDCount or collide ids.
	p.requestIDCount++
	requestID := p.requestIDCount

	data, _ := json.Marshal(MPVJSONIPCRequest{
		RequestID: requestID,
		Command:   command,
	})
	data = append(data, '\n')

	var lastErr error
	for range 2 { // 1 initial attempt + 1 reconnect retry
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		if p.conn == nil {
			conn, err := p.connect(p.sockPath)
			if err != nil {
				lastErr = err
				continue
			}
			p.conn = conn
			p.reader = bufio.NewReader(conn)
		}

		if err := p.conn.SetDeadline(ipcDeadline(ctx)); err != nil {
			lastErr = fmt.Errorf("set write deadline fail: %w", err)
			p.resetConnLocked()
			continue
		}
		if _, err := p.conn.Write(data); err != nil {
			lastErr = fmt.Errorf("writing to iina ipc socket fail: %w", err)
			p.resetConnLocked()
			continue
		}

		// Read until we find the response matching our request id.
		for {
			if err := p.conn.SetDeadline(ipcDeadline(ctx)); err != nil {
				lastErr = fmt.Errorf("set read deadline fail: %w", err)
				p.resetConnLocked()
				break
			}
			respBytes, err := p.reader.ReadBytes('\n')
			if err != nil {
				lastErr = fmt.Errorf("reading from iina ipc socket fail: %w", err)
				p.resetConnLocked()
				break
			}

			var resp MPVJSONIPCResponse
			if err := json.Unmarshal(respBytes, &resp); err != nil {
				// Unparseable line (noise/partial) — skip and keep reading.
				log.Warn("unmarshal iina ipc response fail: %v data=%s", err, string(respBytes))
				continue
			}
			if resp.Event != "" {
				continue // asynchronous mpv event, not our reply
			}
			if resp.RequestID != requestID {
				continue // stale or out-of-order reply for another request
			}
			if resp.Error != "success" {
				return nil, fmt.Errorf("iina ipc response error: %s %s", resp.Error, string(respBytes))
			}
			return resp.Data, nil
		}
		// Reached only after the read loop broke on error → retry the outer loop.
	}

	return nil, lastErr
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
