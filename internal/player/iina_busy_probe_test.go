package player

// Regression tests for audit M4 (busy IINA must not be mistaken for dead and
// restarted), M5 (json.Marshal errors must fail fast instead of writing a
// bare newline that hangs the IPC budget), L2 (reuse-path SetVolume errors
// must propagate) and L5 (Screenshot must honor its path argument).

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"math"
	"net"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

// scriptedMPVServer is a unix-socket fake with knobs for reply delay,
// per-command failure injection, and the value reported for get_property
// path. It records every command it receives.
type scriptedMPVServer struct {
	listener *net.UnixListener
	sockPath string

	mu         sync.Mutex
	commands   [][]any
	conns      []net.Conn
	replyDelay time.Duration
	failing    map[string]string
	pathProp   string
}

func newScriptedMPVServer(t *testing.T) *scriptedMPVServer {
	t.Helper()
	sockPath := sockPathPrefix + "probe-" + uuid.NewString()
	_ = os.Remove(sockPath)
	addr, err := net.ResolveUnixAddr("unix", sockPath)
	if err != nil {
		t.Fatalf("resolve unix addr: %v", err)
	}
	l, err := net.ListenUnix("unix", addr)
	if err != nil {
		t.Fatalf("listen unix: %v", err)
	}
	t.Cleanup(func() {
		_ = l.Close()
		_ = os.Remove(sockPath)
	})
	s := &scriptedMPVServer{listener: l, sockPath: sockPath, failing: map[string]string{}}
	go s.serve()
	return s
}

func (s *scriptedMPVServer) serve() {
	for {
		conn, err := s.listener.AcceptUnix()
		if err != nil {
			return
		}
		s.mu.Lock()
		s.conns = append(s.conns, conn)
		s.mu.Unlock()
		go s.handle(conn)
	}
}

func (s *scriptedMPVServer) handle(conn net.Conn) {
	defer func() { _ = conn.Close() }()
	reader := bufio.NewReader(conn)
	for {
		line, err := reader.ReadBytes('\n')
		if err != nil {
			return
		}
		var req MPVJSONIPCRequest
		if err := json.Unmarshal(line, &req); err != nil {
			continue
		}

		s.mu.Lock()
		s.commands = append(s.commands, req.Command)
		delay := s.replyDelay
		var failErr string
		var pathProp = s.pathProp
		if len(req.Command) > 0 {
			failErr = s.failing[req.Command[0].(string)]
		}
		s.mu.Unlock()

		go func(req MPVJSONIPCRequest) {
			time.Sleep(delay)
			resp := MPVJSONIPCResponse{RequestID: req.RequestID, Error: "success"}
			if failErr != "" {
				resp.Error = failErr
			} else if len(req.Command) >= 2 && req.Command[0] == "get_property" && req.Command[1] == "path" {
				resp.Data = pathProp
			}
			out, _ := json.Marshal(resp)
			_, _ = conn.Write(append(out, '\n'))
		}(req)
	}
}

func (s *scriptedMPVServer) setReplyDelay(d time.Duration) {
	s.mu.Lock()
	s.replyDelay = d
	s.mu.Unlock()
}

func (s *scriptedMPVServer) setFailing(cmd, mpvErr string) {
	s.mu.Lock()
	s.failing[cmd] = mpvErr
	s.mu.Unlock()
}

func (s *scriptedMPVServer) setPathProp(v string) {
	s.mu.Lock()
	s.pathProp = v
	s.mu.Unlock()
}

func (s *scriptedMPVServer) pushLine(line string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.conns {
		_, _ = c.Write([]byte(line + "\n"))
	}
}

func (s *scriptedMPVServer) dropConns() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.conns {
		_ = c.Close()
	}
	s.conns = nil
}

func (s *scriptedMPVServer) closeAndRemove() {
	_ = s.listener.Close()
	_ = os.Remove(s.sockPath)
	s.dropConns()
}

func (s *scriptedMPVServer) recordedCommands() [][]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([][]any, len(s.commands))
	copy(out, s.commands)
	return out
}

func (s *scriptedMPVServer) commandNames() []string {
	var names []string
	for _, c := range s.recordedCommands() {
		if len(c) > 0 {
			if n, ok := c[0].(string); ok {
				names = append(names, n)
			}
		}
	}
	return names
}

func hasCommand(names []string, want string) bool {
	for _, n := range names {
		if n == want {
			return true
		}
	}
	return false
}

// probeTestPlayer returns a player whose endpoint points at the scripted
// server, with counting hooks for launches and activation.
func probeTestPlayer(t *testing.T, s *scriptedMPVServer) (*IINAPlayer, *int) {
	t.Helper()
	p := NewIINAPlayer(false)
	p.retryDelay = time.Millisecond
	p.ipcPoll = time.Millisecond
	p.find = func() (string, error) { return "/opt/homebrew/bin/iina-cli", nil }
	p.activate = func(context.Context) error { return nil }
	launches := 0
	p.commandFactory = func(ctx context.Context, exe string, args []string) command {
		fc := newFakeCommand()
		launches++
		t.Cleanup(func() {
			select {
			case <-fc.exited:
			default:
				close(fc.exited)
			}
		})
		return fc
	}
	p.mu.Lock()
	p.sockPath = s.sockPath
	p.mu.Unlock()
	return p, &launches
}

// TestPlay_BusyIPCDoesNotRestart covers audit M4: when the path probe times
// out (IINA busy demuxing), Play must NOT Stop+relaunch the instance — the
// restart tore down a healthy, merely busy window and restarted playback
// from scratch.
func TestPlay_BusyIPCDoesNotRestart(t *testing.T) {
	withSmallIPCTimeout(t, 100*time.Millisecond)
	s := newScriptedMPVServer(t)
	s.setReplyDelay(800 * time.Millisecond) // far beyond the probe budget
	s.setPathProp("https://example.test/other.mp4")
	p, launches := probeTestPlayer(t, s)

	start := time.Now()
	err := p.Play(context.Background(), "https://example.test/cast.mp4", 50)
	elapsed := time.Since(start)

	if err == nil || !contains(err.Error(), "busy") {
		t.Fatalf("Play err = %v, want busy error", err)
	}
	if *launches != 0 {
		t.Fatalf("launched %d times, want 0 (busy IINA must not be restarted)", *launches)
	}
	names := s.commandNames()
	if hasCommand(names, "quit") || hasCommand(names, "loadfile") {
		t.Fatalf("server saw %v, want no quit/loadfile (instance kept running)", names)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("Play took %v, want it bounded well below the reply delay", elapsed)
	}
}

// TestPlay_DeadConnectionRestarts covers audit M4's counterpart: a genuinely
// dead endpoint (socket gone, dial refused) must fall through to a fresh
// launch.
func TestPlay_DeadConnectionRestarts(t *testing.T) {
	withSmallIPCTimeout(t, 150*time.Millisecond)
	dead := newScriptedMPVServer(t)
	dead.closeAndRemove()

	// The relaunched instance gets a fresh endpoint routed to a live server.
	live := newScriptedMPVServer(t)
	live.setPathProp("https://example.test/cast.mp4")

	p := NewIINAPlayer(false)
	p.retryDelay = time.Millisecond
	p.ipcPoll = time.Millisecond
	p.find = func() (string, error) { return "/opt/homebrew/bin/iina-cli", nil }
	p.activate = func(context.Context) error { return nil }
	launches := 0
	p.commandFactory = func(ctx context.Context, exe string, args []string) command {
		fc := newFakeCommand()
		launches++
		t.Cleanup(func() {
			select {
			case <-fc.exited:
			default:
				close(fc.exited)
			}
		})
		return fc
	}
	deadPath := dead.sockPath
	p.dial = func(network, addr string) (net.Conn, error) {
		if addr == deadPath {
			return nil, errors.New("connection refused")
		}
		return net.Dial("unix", live.sockPath)
	}
	p.mu.Lock()
	p.sockPath = deadPath
	p.mu.Unlock()

	if err := p.Play(context.Background(), "https://example.test/cast.mp4", 50); err != nil {
		t.Fatalf("Play after dead connection: %v", err)
	}
	if launches != 1 {
		t.Fatalf("launched %d times, want 1 (restart after dead connection)", launches)
	}
}

// TestSeekNaN_FailsFastWithoutWrite covers audit M5 (player half): a command
// containing NaN cannot be marshaled; the error must surface immediately
// instead of writing a bare newline to the socket and hanging for the full
// IPC budget (previously two 3s attempts = 6s, plus a healthy connection
// torn down on each timeout).
func TestSeekNaN_FailsFastWithoutWrite(t *testing.T) {
	s := newScriptedMPVServer(t)
	p, _ := probeTestPlayer(t, s)

	start := time.Now()
	err := p.Seek(context.Background(), math.NaN())
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("Seek(NaN) err = nil, want marshal error")
	}
	if !contains(err.Error(), "marshal") {
		t.Fatalf("Seek(NaN) err = %v, want it to mention the marshal failure", err)
	}
	if elapsed > time.Second {
		t.Fatalf("Seek(NaN) took %v, want immediate failure (no IPC round trips)", elapsed)
	}
	if got := len(s.recordedCommands()); got != 0 {
		t.Fatalf("server received %d commands, want 0 (nothing must reach the socket)", got)
	}
}

// TestScreenshot_RespectsPath covers audit L5: the path argument used to be
// discarded; with a path, mpv's screenshot-to-file must be used.
func TestScreenshot_RespectsPath(t *testing.T) {
	s := newScriptedMPVServer(t)
	p, _ := probeTestPlayer(t, s)
	ctx := context.Background()

	if err := p.Screenshot(ctx, "/tmp/rcast-shot.png"); err != nil {
		t.Fatalf("Screenshot(path): %v", err)
	}
	cmds := s.recordedCommands()
	if len(cmds) != 1 || len(cmds[0]) != 2 || cmds[0][0] != "screenshot-to-file" || cmds[0][1] != "/tmp/rcast-shot.png" {
		t.Fatalf("commands = %v, want [screenshot-to-file /tmp/rcast-shot.png]", cmds)
	}

	if err := p.Screenshot(ctx, ""); err != nil {
		t.Fatalf("Screenshot(empty): %v", err)
	}
	cmds = s.recordedCommands()
	if len(cmds) != 2 || len(cmds[1]) != 1 || cmds[1][0] != "screenshot" {
		t.Fatalf("commands = %v, want second entry [screenshot]", cmds)
	}
}

// TestPlay_ReuseSetVolumeErrorPropagates covers audit L2: volume failures on
// the reuse paths (same-URI resume and loadfile-into-existing) were silently
// swallowed.
func TestPlay_ReuseSetVolumeErrorPropagates(t *testing.T) {
	const uri = "https://example.test/video.mp4"

	t.Run("same uri resume path", func(t *testing.T) {
		s := newScriptedMPVServer(t)
		s.setPathProp(uri)
		s.setFailing("set_property", "parameter unavailable")
		p, _ := probeTestPlayer(t, s)

		err := p.Play(context.Background(), uri, 50)
		if err == nil || !contains(err.Error(), "setting volume") {
			t.Fatalf("Play err = %v, want propagated set volume failure", err)
		}
		// Resume must not have been attempted after the volume failure.
		for _, c := range s.recordedCommands() {
			if len(c) >= 2 && c[0] == "set_property" && c[1] == "pause" {
				t.Fatalf("resume issued after volume failure: %v", c)
			}
		}
	})

	t.Run("loadfile path", func(t *testing.T) {
		s := newScriptedMPVServer(t)
		s.setPathProp("https://example.test/old.mp4")
		s.setFailing("set_property", "parameter unavailable")
		p, _ := probeTestPlayer(t, s)

		err := p.Play(context.Background(), "https://example.test/new.mp4", 50)
		if err == nil || !contains(err.Error(), "setting volume") {
			t.Fatalf("Play err = %v, want propagated set volume failure", err)
		}
		if !hasCommand(s.commandNames(), "loadfile") {
			t.Fatalf("loadfile was not issued before the volume failure: %v", s.commandNames())
		}
	})
}
