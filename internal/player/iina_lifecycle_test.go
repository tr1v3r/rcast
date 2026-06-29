package player

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// fakeCommand implements command. Wait blocks until exited is closed (by Kill
// or by an explicit Close of the channel). It records Start/Kill call counts
// so tests can assert lifecycle behavior without spawning a real process.
type fakeCommand struct {
	mu       sync.Mutex
	started  int
	killed   int
	startErr error
	killErr  error
	exited   chan struct{}
}

func newFakeCommand() *fakeCommand { return &fakeCommand{exited: make(chan struct{})} }

func (c *fakeCommand) Start() error {
	c.mu.Lock()
	c.started++
	c.mu.Unlock()
	return c.startErr
}

func (c *fakeCommand) Wait() error {
	<-c.exited
	return nil
}

func (c *fakeCommand) Kill() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.killed++
	select {
	case <-c.exited:
	default:
		close(c.exited)
	}
	return c.killErr
}

func (c *fakeCommand) startedCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.started
}

func (c *fakeCommand) killedCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.killed
}

// closedPipeConn returns a net.Conn whose peer is already closed, so any later
// send() write/read fails fast (EOF/broken pipe) without spawning a server.
func closedPipeConn() net.Conn {
	a, b := net.Pipe()
	_ = b.Close()
	return a
}

// errorCloseConn wraps a net.Conn and returns a configured error from Close,
// so Stop's closeLocked path can be exercised to produce a combined error.
type errorCloseConn struct {
	net.Conn
	closeErr error
}

func (c *errorCloseConn) Close() error { return c.closeErr }

// withSmallIPCTimeout shrinks the package-level ipcTimeout for the duration of
// a test, so any accidental send() cannot block the test for seconds.
func withSmallIPCTimeout(t *testing.T, d time.Duration) {
	t.Helper()
	orig := ipcTimeout
	ipcTimeout = d
	t.Cleanup(func() { ipcTimeout = orig })
}

// newTestPlayer returns a fresh IINAPlayer with tiny retry/poll durations so
// lifecycle tests are fast. The find hook returns a CLI exe so activate is
// exercised only when the test wires a counting activate closure.
func newTestPlayer(t *testing.T) *IINAPlayer {
	t.Helper()
	p := NewIINAPlayer(false)
	p.retryDelay = 1 * time.Millisecond
	p.ipcPoll = 1 * time.Millisecond
	p.dial = func(network, addr string) (net.Conn, error) {
		return nil, errors.New("dial disabled")
	}
	p.find = func() (string, error) { return "/opt/homebrew/bin/iina-cli", nil }
	p.activate = func(context.Context) error { return nil }
	return p
}

// --- find / launch / waitForIPC ---

func TestFind_NotFoundReturnsError(t *testing.T) {
	p := newTestPlayer(t)
	p.find = func() (string, error) { return "", errors.New("none") }

	var started int
	p.commandFactory = func(ctx context.Context, exe string, args []string) command {
		f := newFakeCommand()
		started++
		return f
	}
	err := p.Play(context.Background(), "x", 50)
	if err == nil || !contains(err.Error(), "IINA not found") {
		t.Fatalf("Play err = %v, want error containing \"IINA not found\"", err)
	}
	if started != 0 {
		t.Fatalf("command started %d times, want 0 (find failed before launch)", started)
	}
}

func TestLaunch_StartFails(t *testing.T) {
	p := newTestPlayer(t)
	fc := newFakeCommand()
	fc.startErr = errors.New("boom")
	calls := 0
	p.commandFactory = func(ctx context.Context, exe string, args []string) command {
		calls++
		return fc
	}

	err := p.Play(context.Background(), "x", 50)
	if err == nil || !contains(err.Error(), "starting IINA process") {
		t.Fatalf("Play err = %v, want error wrapping \"starting IINA process\"", err)
	}
	if fc.startedCount() != 2 { // 2 attempts in Play retry loop
		t.Fatalf("fake started %d times, want 2", fc.startedCount())
	}
	_ = calls
}

func TestLaunch_Success(t *testing.T) {
	p := newTestPlayer(t)
	fc := newFakeCommand()
	p.commandFactory = func(ctx context.Context, exe string, args []string) command {
		return fc
	}
	p.dial = func(network, addr string) (net.Conn, error) {
		return closedPipeConn(), nil
	}

	if err := p.Play(context.Background(), "x", 50); err != nil {
		t.Fatalf("Play: %v", err)
	}
	defer func() { _ = p.Stop(context.Background()) }()

	p.mu.Lock()
	hasCommand := p.command != nil
	hasConn := p.conn != nil
	p.mu.Unlock()
	if !hasCommand {
		t.Fatal("p.command == nil after successful launch")
	}
	if !hasConn {
		t.Fatal("p.conn == nil after successful launch")
	}
}

func TestWaitForIPC_DelayedSocket(t *testing.T) {
	p := newTestPlayer(t)
	fc := newFakeCommand()
	p.commandFactory = func(ctx context.Context, exe string, args []string) command {
		return fc
	}
	dials := 0
	p.dial = func(network, addr string) (net.Conn, error) {
		dials++
		if dials < 3 {
			return nil, errors.New("not yet")
		}
		return closedPipeConn(), nil
	}
	p.ipcPoll = 1 * time.Millisecond

	if err := p.Play(context.Background(), "x", 50); err != nil {
		t.Fatalf("Play: %v", err)
	}
	defer func() { _ = p.Stop(context.Background()) }()

	if dials < 3 {
		t.Fatalf("dial called %d times, want >= 3 (waitForIPC retried)", dials)
	}
}

func TestWaitForIPC_Timeout(t *testing.T) {
	withSmallIPCTimeout(t, 50*time.Millisecond)
	p := newTestPlayer(t)
	fc := newFakeCommand()
	p.commandFactory = func(ctx context.Context, exe string, args []string) command {
		return fc
	}
	p.dial = func(network, addr string) (net.Conn, error) {
		return nil, errors.New("no socket")
	}
	p.ipcPoll = 1 * time.Millisecond

	err := p.Play(context.Background(), "x", 50)
	if err == nil || !contains(err.Error(), "waiting for IINA IPC") {
		t.Fatalf("Play err = %v, want error wrapping \"waiting for IINA IPC\"", err)
	}
	// Stop reaps the fake process; Kill closes the wait goroutine's exited
	// channel so it does not leak.
	if err := p.Stop(context.Background()); err != nil {
		t.Logf("cleanup Stop: %v", err)
	}
}

func TestWaitForIPC_ContextCancel(t *testing.T) {
	p := newTestPlayer(t)
	fc := newFakeCommand()
	p.commandFactory = func(ctx context.Context, exe string, args []string) command {
		return fc
	}
	p.dial = func(network, addr string) (net.Conn, error) {
		return nil, errors.New("no socket")
	}
	p.ipcPoll = 5 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	// Cancel shortly after the first poll so waitForIPC observes ctx.Done().
	go func() {
		time.Sleep(10 * time.Millisecond)
		cancel()
	}()

	err := p.Play(ctx, "x", 50)
	if err == nil {
		t.Fatal("Play err = nil, want ctx error from waitForIPC")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Play err = %v, want it to wrap context.Canceled", err)
	}
	if err := p.Stop(context.Background()); err != nil {
		t.Logf("cleanup Stop: %v", err)
	}
}

func TestWaitForIPC_EndpointChanged(t *testing.T) {
	p := newTestPlayer(t)
	fc := newFakeCommand()
	p.commandFactory = func(ctx context.Context, exe string, args []string) command {
		return fc
	}

	// Simulate a concurrent restart changing the IPC endpoint mid-launch:
	// waitForIPC captures sockPath as a local at launch time, then dials it,
	// then under p.mu compares p.sockPath != sockPath. dial runs in the same
	// goroutine as that subsequent locked read, so a direct (unlocked) write
	// here is race-free: no other goroutine touches p.sockPath during launch.
	// We do NOT take p.mu here because send() calls connect() while already
	// holding p.mu, which would deadlock.
	p.dial = func(network, addr string) (net.Conn, error) {
		p.sockPath = sockPathPrefix + "moved"
		return closedPipeConn(), nil
	}

	err := p.Play(context.Background(), "x", 50)
	if err == nil || !contains(err.Error(), "endpoint changed") {
		t.Fatalf("Play err = %v, want error containing \"endpoint changed\"", err)
	}
	if err := p.Stop(context.Background()); err != nil {
		t.Logf("cleanup Stop: %v", err)
	}
}

// --- Play retry ---

func TestPlay_RetriesAndSucceeds(t *testing.T) {
	p := newTestPlayer(t)
	p.retryDelay = 1 * time.Millisecond

	bad := newFakeCommand()
	bad.startErr = errors.New("boom")
	good := newFakeCommand()
	created := 0
	p.commandFactory = func(ctx context.Context, exe string, args []string) command {
		created++
		if created == 1 {
			return bad
		}
		return good
	}
	p.dial = func(network, addr string) (net.Conn, error) {
		return closedPipeConn(), nil
	}

	if err := p.Play(context.Background(), "x", 50); err != nil {
		t.Fatalf("Play: %v", err)
	}
	defer func() { _ = p.Stop(context.Background()) }()

	if created != 2 {
		t.Fatalf("factory called %d times, want 2", created)
	}
	// The first (failed) command never reached p.command assignment: launch
	// bails at cmd.Start() before setting p.command, so Stop's kill block is
	// correctly skipped. Assert the bad attempt was Started (so the factory
	// actually returned it) and that the good attempt is the one that stuck.
	if bad.startedCount() != 1 {
		t.Fatalf("bad command started %d times, want 1", bad.startedCount())
	}
	p.mu.Lock()
	currentCmd := p.command
	p.mu.Unlock()
	if currentCmd != good {
		t.Fatal("p.command is not the second (good) fake after retry succeeded")
	}
}

func TestPlay_BothAttemptsFail(t *testing.T) {
	p := newTestPlayer(t)
	p.retryDelay = 1 * time.Millisecond

	p.commandFactory = func(ctx context.Context, exe string, args []string) command {
		fc := newFakeCommand()
		fc.startErr = errors.New("boom")
		return fc
	}

	err := p.Play(context.Background(), "x", 50)
	if err == nil || !contains(err.Error(), "failed to start IINA after retry") {
		t.Fatalf("Play err = %v, want error wrapping \"failed to start IINA after retry\"", err)
	}
}

func TestPlay_AppExeSkipsActivate(t *testing.T) {
	p := newTestPlayer(t)
	fc := newFakeCommand()
	p.commandFactory = func(ctx context.Context, exe string, args []string) command {
		return fc
	}
	p.dial = func(network, addr string) (net.Conn, error) {
		return closedPipeConn(), nil
	}
	p.find = func() (string, error) { return iinaAppBinary, nil }
	activated := 0
	p.activate = func(context.Context) error {
		activated++
		return nil
	}

	if err := p.Play(context.Background(), "x", 50); err != nil {
		t.Fatalf("Play: %v", err)
	}
	defer func() { _ = p.Stop(context.Background()) }()

	if activated != 0 {
		t.Fatalf("activate called %d times, want 0 when exe == iinaAppBinary", activated)
	}
}

func TestPlay_CLIExeActivates(t *testing.T) {
	p := newTestPlayer(t)
	fc := newFakeCommand()
	p.commandFactory = func(ctx context.Context, exe string, args []string) command {
		return fc
	}
	p.dial = func(network, addr string) (net.Conn, error) {
		return closedPipeConn(), nil
	}
	const cli = "/opt/homebrew/bin/iina-cli"
	p.find = func() (string, error) { return cli, nil }
	activated := 0
	p.activate = func(context.Context) error {
		activated++
		return nil
	}

	if err := p.Play(context.Background(), "x", 50); err != nil {
		t.Fatalf("Play: %v", err)
	}
	defer func() { _ = p.Stop(context.Background()) }()

	if activated != 1 {
		t.Fatalf("activate called %d times, want 1 for CLI exe", activated)
	}
}

// --- Stop / Close ---

func TestStop_KillsCommandAndCleans(t *testing.T) {
	p := newTestPlayer(t)
	fc := newFakeCommand()
	p.commandFactory = func(ctx context.Context, exe string, args []string) command {
		return fc
	}
	p.dial = func(network, addr string) (net.Conn, error) {
		return closedPipeConn(), nil
	}

	if err := p.Play(context.Background(), "x", 50); err != nil {
		t.Fatalf("Play: %v", err)
	}

	p.mu.Lock()
	sock := p.sockPath
	p.mu.Unlock()
	if sock == "" {
		t.Fatal("sockPath empty after launch")
	}

	if err := p.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	if fc.killedCount() != 1 {
		t.Fatalf("fake killed %d times, want 1", fc.killedCount())
	}
	p.mu.Lock()
	hasCommand := p.command != nil
	hasConn := p.conn != nil
	p.mu.Unlock()
	if hasCommand {
		t.Fatal("p.command != nil after Stop")
	}
	if hasConn {
		t.Fatal("p.conn != nil after Stop")
	}
	// The launch created a UUID-based socket path that never existed on disk
	// (closedPipeConn, no real listener). os.Remove returns NotExist, which
	// Stop tolerates. Assert the path is gone either way.
	if _, err := os.Stat(sock); err == nil {
		t.Fatalf("socket file %s still present after Stop", sock)
	}

	// Idempotent: a second Stop must not panic or error.
	if err := p.Stop(context.Background()); err != nil {
		t.Fatalf("second Stop: %v", err)
	}
}

func TestStop_NoEndpointNoQuit(t *testing.T) {
	withSmallIPCTimeout(t, 50*time.Millisecond)
	p := newTestPlayer(t)
	// A fresh player has an empty sockPath; Stop must short-circuit before the
	// quit send and return nil.
	p.mu.Lock()
	p.sockPath = ""
	p.mu.Unlock()

	if err := p.Stop(context.Background()); err != nil {
		t.Fatalf("Stop with empty sockPath: %v", err)
	}
}

func TestClose_RemovesSocket(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "rcast-test-ipc-sock")
	if err := os.WriteFile(sock, []byte("x"), 0o644); err != nil {
		t.Fatalf("write socket file: %v", err)
	}
	p := newTestPlayer(t)
	p.mu.Lock()
	p.sockPath = sock
	p.mu.Unlock()

	if err := p.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := os.Stat(sock); !os.IsNotExist(err) {
		t.Fatalf("os.Stat(sock) err = %v, want NotExist", err)
	}
}

func TestStop_ConnCloseAndKillErrorsCombine(t *testing.T) {
	withSmallIPCTimeout(t, 50*time.Millisecond)
	p := newTestPlayer(t)

	// Install a live conn whose Close returns an error, plus a fake process
	// whose Kill also returns an error. Stop must report the combined
	// "multiple errors" form.
	fc := newFakeCommand()
	fc.killErr = errors.New("kill failed")
	p.mu.Lock()
	p.sockPath = "" // skip the quit send path; exercise closeLocked + Kill
	p.conn = &errorCloseConn{Conn: closedPipeConn(), closeErr: errors.New("close failed")}
	p.command = fc
	p.mu.Unlock()

	err := p.Stop(context.Background())
	if err == nil {
		t.Fatal("Stop err = nil, want combined error")
	}
	if !contains(err.Error(), "multiple errors") {
		t.Fatalf("Stop err = %v, want it to contain \"multiple errors\"", err)
	}
}

// contains is a tiny strings.Contains wrapper to keep these tests independent
// of the strings import.
func contains(s, substr string) bool {
	if len(substr) == 0 {
		return true
	}
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
