package main

import (
	"context"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/tr1v3r/rcast/internal/config"
	"github.com/tr1v3r/rcast/internal/ssdp"
	"github.com/tr1v3r/rcast/internal/uuid"
)

// callArgs captures the arguments passed to an SSDP fake.
type callArgs struct {
	baseURL    string
	deviceUUID string
	serverName string
}

// recordedSSDP records calls to the announce/search fakes and signals their
// arrival via buffered channels so tests can wait deterministically.
type recordedSSDP struct {
	mu       sync.Mutex
	announce []callArgs
	search   []callArgs
	annCh    chan struct{}
	srchCh   chan struct{}
}

func newRecordedSSDP() *recordedSSDP {
	return &recordedSSDP{
		annCh:  make(chan struct{}, 1),
		srchCh: make(chan struct{}, 1),
	}
}

func (r *recordedSSDP) announceFn(ctx context.Context, loc *ssdp.BaseURLSource, deviceUUID, serverName string) {
	r.mu.Lock()
	r.announce = append(r.announce, callArgs{loc.Get(), deviceUUID, serverName})
	r.mu.Unlock()
	select {
	case r.annCh <- struct{}{}:
	default:
	}
}

func (r *recordedSSDP) searchFn(ctx context.Context, loc *ssdp.BaseURLSource, deviceUUID, serverName string) {
	r.mu.Lock()
	r.search = append(r.search, callArgs{loc.Get(), deviceUUID, serverName})
	r.mu.Unlock()
	select {
	case r.srchCh <- struct{}{}:
	default:
	}
}

func (r *recordedSSDP) lastAnnounce() callArgs {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.announce) == 0 {
		return callArgs{}
	}
	return r.announce[len(r.announce)-1]
}

// waitFor fails the test if the channel does not fire within 2s. Used purely
// as a guard against hangs; the primary synchronization is the channel.
func waitFor(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
}

// runWithCancel runs runServerWithRuntime in a goroutine and returns a done
// channel plus a cancel func. The returned channel delivers the error result.
func runWithCancel(ctx context.Context, cfg config.Config, deps serverDeps) (<-chan error, context.CancelFunc) {
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- runServerWithRuntime(runCtx, cfg, deps) }()
	return done, cancel
}

// newBaseDeps returns a serverDeps wired with deterministic fakes and a tmp
// UUID path. resolveIP returns 127.0.0.1, listen is real net.Listen, and the
// SSDP fakes record into r. Overrides may be applied after calling.
func newBaseDeps(t *testing.T) (serverDeps, *recordedSSDP) {
	t.Helper()
	r := newRecordedSSDP()
	return serverDeps{
		uuidLoader: func(path string) (string, error) {
			return uuid.LoadOrCreate(path)
		},
		resolveIP: func() (string, error) { return "127.0.0.1", nil },
		listen:    net.Listen,
		announce:  r.announceFn,
		search:    r.searchFn,
	}, r
}

// newBaseConfig returns a config that uses a temp UUID path and port 0.
func newBaseConfig(t *testing.T) config.Config {
	t.Helper()
	cfg := config.Load()
	cfg.UUIDPath = filepath.Join(t.TempDir(), "dmr_uuid.txt")
	cfg.HTTPPort = 0
	return cfg
}

func TestRunServer_UUIDLoadFails(t *testing.T) {
	cfg := newBaseConfig(t)
	deps, _ := newBaseDeps(t)
	deps.uuidLoader = func(string) (string, error) {
		return "", errors.New("boom")
	}
	done, cancel := runWithCancel(context.Background(), cfg, deps)
	defer cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if !contains(err.Error(), "load device UUID") {
			t.Fatalf("expected error wrapping 'load device UUID', got %q", err.Error())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for runServer to return")
	}
}

func TestRunServer_AdvertiseIPInvalid(t *testing.T) {
	cfg := newBaseConfig(t)
	cfg.AdvertiseIP = "not-an-ip"
	deps, _ := newBaseDeps(t)
	done, cancel := runWithCancel(context.Background(), cfg, deps)
	defer cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if !contains(err.Error(), "DMR_ADVERTISE_IP must be an IPv4") {
			t.Fatalf("expected error mentioning IPv4 requirement, got %q", err.Error())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for runServer to return")
	}
}

func TestRunServer_AutoResolveFails(t *testing.T) {
	cfg := newBaseConfig(t)
	deps, _ := newBaseDeps(t)
	resolveErr := errors.New("no iface")
	deps.resolveIP = func() (string, error) { return "", resolveErr }
	done, cancel := runWithCancel(context.Background(), cfg, deps)
	defer cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if !errors.Is(err, resolveErr) {
			t.Fatalf("expected resolveIP error to be returned unwrapped, got %q", err.Error())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for runServer to return")
	}
}

func TestRunServer_ListenFails(t *testing.T) {
	cfg := newBaseConfig(t)
	deps, _ := newBaseDeps(t)
	listenErr := errors.New("cannot listen")
	deps.listen = func(string, string) (net.Listener, error) { return nil, listenErr }
	done, cancel := runWithCancel(context.Background(), cfg, deps)
	defer cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if !contains(err.Error(), "listen") {
			t.Fatalf("expected error wrapping 'listen', got %q", err.Error())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for runServer to return")
	}
}

func TestRunServer_HappyPath(t *testing.T) {
	cfg := newBaseConfig(t)
	deps, r := newBaseDeps(t)

	// Load the UUID up front so we can assert it later (the uuidLoader fake
	// delegates to the real implementation via newBaseDeps).
	wantUUID, err := uuid.LoadOrCreate(cfg.UUIDPath)
	if err != nil {
		t.Fatalf("preload uuid: %v", err)
	}
	// LoadOrCreate is idempotent: the in-process call by runServerWithRuntime
	// will read the same file and return wantUUID.

	done, cancel := runWithCancel(context.Background(), cfg, deps)
	defer cancel()

	// Wait for both SSDP fakes to be invoked.
	waitFor(t, r.annCh, "announce")
	waitFor(t, r.srchCh, "search")

	got := r.lastAnnounce()
	if got.deviceUUID != wantUUID {
		t.Errorf("deviceUUID = %q, want %q", got.deviceUUID, wantUUID)
	}
	if got.serverName != serverName {
		t.Errorf("serverName = %q, want %q", got.serverName, serverName)
	}
	// baseURL must reflect the real port bound by the :0 listener.
	wantPrefix := "http://127.0.0.1:"
	if !contains(got.baseURL, wantPrefix) {
		t.Fatalf("baseURL = %q, want prefix %q", got.baseURL, wantPrefix)
	}
	if got.baseURL == "http://127.0.0.1:0" {
		t.Fatalf("baseURL not resolved from listener: %q", got.baseURL)
	}

	// Drive graceful shutdown via the context.
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("runServer returned error on clean shutdown: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for runServer to shut down")
	}
}

func TestRunServer_ServeFailsAfterStart(t *testing.T) {
	cfg := newBaseConfig(t)
	deps, r := newBaseDeps(t)

	// Pre-create a real :0 listener that the test will close once Serve starts.
	ln, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatalf("pre-listen: %v", err)
	}
	deps.listen = func(string, string) (net.Listener, error) { return ln, nil }

	done, cancel := runWithCancel(context.Background(), cfg, deps)
	defer cancel()

	// Wait until announce is invoked — this means Serve has started and owns ln.
	waitFor(t, r.annCh, "announce (Serve started)")

	// Close the underlying listener from underneath the server. Serve will
	// return a non-ErrServerClosed error, surfacing via serverErr.
	if err := ln.Close(); err != nil {
		t.Fatalf("close listener: %v", err)
	}

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected error from failed Serve, got nil")
		}
		if !contains(err.Error(), "HTTP server") {
			t.Fatalf("expected error wrapping 'HTTP server', got %q", err.Error())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for runServer to return after Serve failure")
	}
}

// contains is a tiny local helper to avoid pulling in strings (and keeps the
// test file dependency-free).
func contains(s, sub string) bool {
	if len(sub) == 0 {
		return true
	}
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// --- Shutdown regression tests (audit LOW-8 + swallowed second SIGINT) ---

// TestRunServer_WaitsForSSDPFlushBeforeExit proves shutdown does not return
// while the announce loop is still flushing its ssdp:byebye messages (audit
// LOW-8: process exit used to race the flush and lose all 6 messages).
func TestRunServer_WaitsForSSDPFlushBeforeExit(t *testing.T) {
	cfg := newBaseConfig(t)
	deps, _ := newBaseDeps(t)

	started := make(chan struct{}, 1)
	flushed := make(chan struct{})
	deps.announce = func(ctx context.Context, _ *ssdp.BaseURLSource, _, _ string) {
		select {
		case started <- struct{}{}:
		default:
		}
		// Simulate the byebye flush taking a moment to leave the socket.
		<-ctx.Done()
		time.Sleep(150 * time.Millisecond)
		close(flushed)
	}
	deps.search = func(context.Context, *ssdp.BaseURLSource, string, string) {}

	done, cancel := runWithCancel(context.Background(), cfg, deps)
	waitFor(t, started, "announce started")
	cancel()

	select {
	case <-flushed:
		// Good: shutdown waited for the flush.
	case err := <-done:
		t.Fatalf("runServer returned before the SSDP byebye flush finished (err=%v)", err)
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for byebye flush")
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("clean shutdown returned error: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("runServer did not return after the flush")
	}
}

// TestRunServer_SSDPShutdownIsBounded proves a stuck SSDP loop cannot hang
// shutdown forever: the wait is capped by ssdpShutdownGrace.
func TestRunServer_SSDPShutdownIsBounded(t *testing.T) {
	origGrace := ssdpShutdownGrace
	ssdpShutdownGrace = 50 * time.Millisecond
	t.Cleanup(func() { ssdpShutdownGrace = origGrace })

	cfg := newBaseConfig(t)
	deps, _ := newBaseDeps(t)
	started := make(chan struct{}, 1)
	deps.announce = func(ctx context.Context, _ *ssdp.BaseURLSource, _, _ string) {
		select {
		case started <- struct{}{}:
		default:
		}
		<-ctx.Done()
		// Never flushes in time: still sleeping long after the grace ends.
		time.Sleep(2 * time.Second)
	}
	deps.search = func(context.Context, *ssdp.BaseURLSource, string, string) {}

	done, cancel := runWithCancel(context.Background(), cfg, deps)
	waitFor(t, started, "announce started")
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("shutdown returned error: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("shutdown hung waiting for a stuck SSDP loop (wait is not bounded)")
	}
}

// TestSecondSignalForcesExit runs the test binary as a child process, drives
// a real runServerWithRuntime through two SIGINTs, and asserts the second one
// force-exits (code 130) instead of being swallowed by NotifyContext.
// Without the fix the child shuts down gracefully and exits 0.
func TestSecondSignalForcesExit(t *testing.T) {
	if os.Getenv("RCAST_TEST_SECOND_SIGNAL") == "1" {
		secondSignalHelperProc(t)
		return
	}

	cmd := exec.Command(os.Args[0], "-test.run=TestSecondSignalForcesExit$", "-test.timeout=1m")
	cmd.Env = append(os.Environ(), "RCAST_TEST_SECOND_SIGNAL=1")
	start := time.Now()
	out, err := cmd.CombinedOutput()
	elapsed := time.Since(start)

	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("child did not exit on its own: err=%v\noutput:\n%s", err, out)
	}
	if code := exitErr.ExitCode(); code != 130 {
		t.Fatalf("child exit code = %d, want 130 (second signal must force exit; graceful exit would be 0)\noutput:\n%s", code, out)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("forced exit took %s; the second signal was not honored promptly", elapsed)
	}
}

// secondSignalHelperProc is the child half of TestSecondSignalForcesExit: it
// starts the server with an announce fake that lingers 600ms after ctx is
// done (simulating a slow byebye flush), then sends itself SIGINT twice.
// With the force-exit fix the second SIGINT kills the process with code 130
// before the graceful path can finish.
func secondSignalHelperProc(t *testing.T) {
	cfg := newBaseConfig(t)
	deps, _ := newBaseDeps(t)

	started := make(chan struct{}, 1)
	deps.announce = func(ctx context.Context, _ *ssdp.BaseURLSource, _, _ string) {
		select {
		case started <- struct{}{}:
		default:
		}
		<-ctx.Done()
		// Slow flush: without a forced exit the process lingers here and
		// eventually exits 0.
		time.Sleep(600 * time.Millisecond)
	}
	deps.search = func(context.Context, *ssdp.BaseURLSource, string, string) {}

	done := make(chan error, 1)
	go func() { done <- runServerWithRuntime(context.Background(), cfg, deps) }()

	<-started
	_ = syscall.Kill(os.Getpid(), syscall.SIGINT)
	time.Sleep(150 * time.Millisecond)
	_ = syscall.Kill(os.Getpid(), syscall.SIGINT)

	// Only reached when the second signal was swallowed: complete the
	// graceful path so the parent can tell the two outcomes apart by exit
	// code (0 here vs 130 with the fix).
	select {
	case <-done:
	case <-time.After(3 * time.Second):
	}
}
