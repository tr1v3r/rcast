package player

// Regression tests for audits H2 (orphaned IINA instances / double launch on
// slow startup) and M3 (Stop must confirm IINA's exit before cleaning up,
// with SIGTERM/AppleScript/SIGKILL escalation). All scenarios run against a
// fake unix-socket mpv server; no real IINA or osascript is ever spawned.

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

// quitMPVServer is a unix-socket fake that records commands, can delay its
// reaction to mpv's quit command, and can delay the moment its socket starts
// listening (simulating a slow-starting IINA).
type quitMPVServer struct {
	listener *net.UnixListener
	sockPath string

	mu        sync.Mutex
	commands  [][]any
	quitSeen  bool
	quitDelay time.Duration
}

// newQuitMPVServerDelayed returns a fake whose listener appears after
// startDelay (0 = immediately). The socket path mirrors the production
// prefix so sun_path length limits stay realistic.
func newQuitMPVServerDelayed(t *testing.T, startDelay time.Duration) *quitMPVServer {
	t.Helper()
	s := &quitMPVServer{sockPath: sockPathPrefix + "stop-" + uuid.NewString()}
	start := func() {
		addr, err := net.ResolveUnixAddr("unix", s.sockPath)
		if err != nil {
			t.Errorf("resolve unix addr: %v", err)
			return
		}
		l, err := net.ListenUnix("unix", addr)
		if err != nil {
			t.Errorf("listen unix: %v", err)
			return
		}
		s.mu.Lock()
		s.listener = l
		s.mu.Unlock()
		go s.serve()
	}
	if startDelay > 0 {
		time.AfterFunc(startDelay, start)
	} else {
		start()
	}
	t.Cleanup(func() {
		s.mu.Lock()
		l := s.listener
		s.mu.Unlock()
		if l != nil {
			_ = l.Close()
		}
		_ = os.Remove(s.sockPath)
	})
	return s
}

func (s *quitMPVServer) serve() {
	for {
		s.mu.Lock()
		l := s.listener
		s.mu.Unlock()
		if l == nil {
			return
		}
		conn, err := l.AcceptUnix()
		if err != nil {
			return
		}
		go s.handle(conn)
	}
}

func (s *quitMPVServer) handle(conn *net.UnixConn) {
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
		isQuit := len(req.Command) > 0 && req.Command[0] == "quit"
		delay := s.quitDelay
		seen := s.quitSeen
		s.quitSeen = true
		s.mu.Unlock()
		if !isQuit {
			resp := MPVJSONIPCResponse{RequestID: req.RequestID, Error: "success"}
			out, _ := json.Marshal(resp)
			if _, err := conn.Write(append(out, '\n')); err != nil {
				return
			}
			continue
		}
		if seen {
			continue // second quit: ignore
		}
		// IINA semantics: honor quit after a configurable delay, then close
		// the connection and remove the socket file.
		go func() {
			time.Sleep(delay)
			_ = conn.Close()
			s.mu.Lock()
			l := s.listener
			s.mu.Unlock()
			if l != nil {
				_ = l.Close()
			}
			_ = os.Remove(s.sockPath)
		}()
	}
}

func (s *quitMPVServer) setQuitDelay(d time.Duration) {
	s.mu.Lock()
	s.quitDelay = d
	s.mu.Unlock()
}

func (s *quitMPVServer) recordedCommands() [][]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([][]any, len(s.commands))
	copy(out, s.commands)
	return out
}

// stopTestPlayer wires a player that looks mid-flight for Stop tests: an
// endpoint, maybe a live connection, and a launcher command with its done
// channel — mimicking what launch() leaves behind.
func stopTestPlayer(t *testing.T, exe, sockPath string, cmd command, done chan struct{}) *IINAPlayer {
	t.Helper()
	p := NewIINAPlayer(false)
	p.retryDelay = time.Millisecond
	p.ipcPoll = time.Millisecond
	p.find = func() (string, error) { return "/opt/homebrew/bin/iina-cli", nil }
	p.activate = func(context.Context) error { return nil }
	p.quitApp = func(context.Context) error { return nil }
	p.mu.Lock()
	p.sockPath = sockPath
	p.exe = exe
	p.command = cmd
	p.cmdDone = done
	p.mu.Unlock()
	if cmd != nil {
		go p.wait(cmd, done)
	}
	return p
}

// TestStop_WaitsForLateSocketAndDeliversQuit covers audit H2②: the launch
// gave up while IINA was still creating its socket. Stop must poll for the
// late socket and deliver quit to it — instead of only SIGKILLing the
// wrapper, which leaves the (still starting) IINA orphaned.
func TestStop_WaitsForLateSocketAndDeliversQuit(t *testing.T) {
	// IINA "finally" creates its socket 60ms from now.
	s := newQuitMPVServerDelayed(t, 60*time.Millisecond)
	s.setQuitDelay(30 * time.Millisecond)

	fc := newFakeCommand()
	done := make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-done:
		default:
			close(fc.exited)
		}
	})
	p := stopTestPlayer(t, "/opt/homebrew/bin/iina-cli", s.sockPath, fc, done)

	if err := p.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	// quit was actually delivered to the late-appearing socket...
	cmds := s.recordedCommands()
	if len(cmds) != 1 || len(cmds[0]) == 0 || cmds[0][0] != "quit" {
		t.Fatalf("server commands = %v, want exactly one quit", cmds)
	}
	// ...and the graceful path confirmed the exit, so the wrapper was never
	// SIGKILLed (an orphaned-wrapper kill would also orphan IINA on real
	// iina-cli, whose SIGKILL does not reach the app).
	if fc.killedCount() != 0 {
		t.Fatalf("fake killed %d times, want 0 (quit confirmed gracefully)", fc.killedCount())
	}
	if _, err := os.Stat(s.sockPath); !os.IsNotExist(err) {
		t.Fatalf("socket %s still present after Stop: %v", s.sockPath, err)
	}
}

// TestStop_SlowQuitKeepsSocketUntilConfirmed covers audit M3: when IINA is
// slow to honor quit, Stop must wait for exit confirmation before closing
// the connection and unlinking the socket. Unlinking early leaves a busy
// IINA alive but permanently uncontrollable.
func TestStop_SlowQuitKeepsSocketUntilConfirmed(t *testing.T) {
	s := newQuitMPVServerDelayed(t, 0)
	s.setQuitDelay(250 * time.Millisecond)

	p := NewIINAPlayer(false)
	p.mu.Lock()
	p.sockPath = s.sockPath
	p.mu.Unlock()

	// Establish a live connection (and its reader) the way a real session
	// would have one.
	if err := p.SetVolume(context.Background(), 10); err != nil {
		t.Fatalf("SetVolume before Stop: %v", err)
	}

	socketStillThere := make(chan bool, 1)
	go func() {
		time.Sleep(120 * time.Millisecond)
		_, err := os.Stat(s.sockPath)
		socketStillThere <- err == nil // quit delay is 250ms: must still exist
	}()

	start := time.Now()
	if err := p.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	elapsed := time.Since(start)

	if !<-socketStillThere {
		t.Fatal("socket file was unlinked before IINA confirmed exit (premature cleanup)")
	}
	if elapsed < 250*time.Millisecond {
		t.Fatalf("Stop returned after %v, want >= the 250ms quit delay (must wait for confirmation)", elapsed)
	}
	p.mu.Lock()
	cleared := p.sockPath == ""
	p.mu.Unlock()
	if !cleared {
		t.Fatal("sockPath not cleared after Stop")
	}
	if _, err := os.Stat(s.sockPath); !os.IsNotExist(err) {
		t.Fatalf("socket %s still present after Stop: %v", s.sockPath, err)
	}
}

// termCommand is a fakeCommand that also satisfies signaler, recording
// SIGTERM deliveries and optionally honoring them by exiting.
type termCommand struct {
	*fakeCommand
	smu          sync.Mutex
	signaled     int
	exitOnSignal bool
}

func newTermCommand(exitOnSignal bool) *termCommand {
	return &termCommand{fakeCommand: newFakeCommand(), exitOnSignal: exitOnSignal}
}

func (c *termCommand) Signal(os.Signal) error {
	c.smu.Lock()
	c.signaled++
	exit := c.exitOnSignal
	c.smu.Unlock()
	if exit {
		// iina-cli semantics: the wrapper takes IINA down with it. Exit
		// without going through Kill() so the kill count stays 0.
		select {
		case <-c.exited:
		default:
			close(c.exited)
		}
	}
	return nil
}

func (c *termCommand) signalCount() int {
	c.smu.Lock()
	defer c.smu.Unlock()
	return c.signaled
}

// TestStop_EscalatesSIGTERMBeforeKill covers audits M3/H2③: when quit cannot
// be delivered (no socket) and the launcher ignores nothing else, Stop must
// SIGTERM the iina-cli wrapper (whose handler terminates IINA) and only
// SIGKILL if that fails too.
func TestStop_EscalatesSIGTERMBeforeKill(t *testing.T) {
	t.Run("sigterm honored", func(t *testing.T) {
		withSmallIPCTimeout(t, 100*time.Millisecond)
		tc := newTermCommand(true)
		done := make(chan struct{})
		t.Cleanup(func() {
			select {
			case <-done:
			default:
				close(tc.exited)
			}
		})
		p := stopTestPlayer(t, "/opt/homebrew/bin/iina-cli", sockPathPrefix+"gone-"+uuid.NewString(), tc, done)

		if err := p.Stop(context.Background()); err != nil {
			t.Fatalf("Stop: %v", err)
		}
		if tc.signalCount() != 1 {
			t.Fatalf("SIGTERM count = %d, want 1", tc.signalCount())
		}
		if tc.killedCount() != 0 {
			t.Fatalf("killed = %d, want 0 (SIGTERM path confirmed exit)", tc.killedCount())
		}
	})

	t.Run("sigterm ignored escalates to kill", func(t *testing.T) {
		withSmallIPCTimeout(t, 100*time.Millisecond)
		tc := newTermCommand(false)
		done := make(chan struct{})
		t.Cleanup(func() {
			select {
			case <-done:
			default:
				close(tc.exited)
			}
		})
		p := stopTestPlayer(t, "/opt/homebrew/bin/iina-cli", sockPathPrefix+"gone-"+uuid.NewString(), tc, done)

		if err := p.Stop(context.Background()); err != nil {
			t.Fatalf("Stop: %v", err)
		}
		if tc.signalCount() != 1 {
			t.Fatalf("SIGTERM count = %d, want 1", tc.signalCount())
		}
		if tc.killedCount() != 1 {
			t.Fatalf("killed = %d, want 1 (SIGTERM did not stop the launcher)", tc.killedCount())
		}
	})
}

// TestStop_OpenPathFallsBackToAppleScript covers audit H2④: instances
// launched via `open -n` have no signalable wrapper, so when the IPC socket
// shows IINA is alive, Stop must fall back to an AppleScript quit.
func TestStop_OpenPathFallsBackToAppleScript(t *testing.T) {
	withSmallIPCTimeout(t, 100*time.Millisecond)

	// A socket file that exists but cannot be connected to (IINA alive, IPC
	// wedged): plain file is enough — only its existence gates the fallback.
	sock := sockPathPrefix + "apple-" + uuid.NewString()
	if err := os.WriteFile(sock, []byte("x"), 0o600); err != nil {
		t.Fatalf("write socket file: %v", err)
	}

	appleCalls := 0
	fc := newFakeCommand()
	done := make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-done:
		default:
			close(fc.exited)
		}
	})
	p := stopTestPlayer(t, iinaAppBinary, sock, fc, done)
	p.quitApp = func(context.Context) error {
		appleCalls++
		return nil
	}

	if err := p.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if appleCalls == 0 {
		t.Fatal("AppleScript quit was never attempted although the socket showed IINA alive")
	}
	if fc.killedCount() != 1 {
		t.Fatalf("killed = %d, want 1 (AppleScript did not confirm exit)", fc.killedCount())
	}
}

// TestStop_OpenPathWithoutEvidenceSkipsAppleScript: with no connection, no
// quit delivered and no socket file, there is no evidence IINA is alive, so
// the AppleScript fallback must not fire (it would be pure noise).
func TestStop_OpenPathWithoutEvidenceSkipsAppleScript(t *testing.T) {
	withSmallIPCTimeout(t, 100*time.Millisecond)

	appleCalls := 0
	fc := newFakeCommand()
	done := make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-done:
		default:
			close(fc.exited)
		}
	})
	p := stopTestPlayer(t, iinaAppBinary, sockPathPrefix+"nowhere-"+uuid.NewString(), fc, done)
	p.quitApp = func(context.Context) error {
		appleCalls++
		return nil
	}

	if err := p.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if appleCalls != 0 {
		t.Fatalf("AppleScript quit called %d times, want 0", appleCalls)
	}
	if fc.killedCount() != 1 {
		t.Fatalf("killed = %d, want 1", fc.killedCount())
	}
}

// TestPlay_SlowSocketStartupSingleInstance covers audit H2①: waitForIPC gets
// a ~10s budget (derived from ipcTimeout), so a socket that appears well
// after one IPC round-trip still yields a single successful launch instead
// of a failed attempt plus a second IINA instance.
func TestPlay_SlowSocketStartupSingleInstance(t *testing.T) {
	// Startup budget = ipcTimeout*10/3 = 800ms; socket appears at ~300ms —
	// far beyond the old 3s→ipcTimeout budget, comfortably inside the new one.
	withSmallIPCTimeout(t, 240*time.Millisecond)
	s := newQuitMPVServerDelayed(t, 300*time.Millisecond)

	p := NewIINAPlayer(false)
	p.retryDelay = time.Millisecond
	p.ipcPoll = 5 * time.Millisecond
	p.find = func() (string, error) { return "/opt/homebrew/bin/iina-cli", nil }
	p.activate = func(context.Context) error { return nil }
	// launch() derives its own random socket path; route every dial to the
	// fake server's path so the "IINA socket" appears only when the server
	// starts listening (300ms).
	p.dial = func(network, addr string) (net.Conn, error) {
		return net.Dial("unix", s.sockPath)
	}
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

	if err := p.Play(context.Background(), "https://example.test/slow.mp4", 50); err != nil {
		t.Fatalf("Play with slow-starting IINA: %v", err)
	}
	if launches != 1 {
		t.Fatalf("launcher started %d times, want 1 (no double instance)", launches)
	}

	// Cleanup: graceful quit through the live connection, no Kill.
	if err := p.Stop(context.Background()); err != nil {
		t.Fatalf("cleanup Stop: %v", err)
	}
	cmds := s.recordedCommands()
	if len(cmds) != 1 || len(cmds[0]) == 0 || cmds[0][0] != "quit" {
		t.Fatalf("server commands = %v, want exactly one quit", cmds)
	}
}
