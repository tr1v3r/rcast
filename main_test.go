package main

import (
	"context"
	"errors"
	"net"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/tr1v3r/rcast/internal/config"
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

func (r *recordedSSDP) announceFn(ctx context.Context, baseURL, deviceUUID, serverName string) {
	r.mu.Lock()
	r.announce = append(r.announce, callArgs{baseURL, deviceUUID, serverName})
	r.mu.Unlock()
	select {
	case r.annCh <- struct{}{}:
	default:
	}
}

func (r *recordedSSDP) searchFn(ctx context.Context, baseURL, deviceUUID, serverName string) {
	r.mu.Lock()
	r.search = append(r.search, callArgs{baseURL, deviceUUID, serverName})
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
