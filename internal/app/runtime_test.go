package app_test

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/tr1v3r/rcast/internal/app"
	"github.com/tr1v3r/rcast/internal/config"
	"github.com/tr1v3r/rcast/internal/ssdp"
	"github.com/tr1v3r/rcast/internal/state"
)

const runtimeTestTimeout = 2 * time.Second

type discoveryCall struct {
	baseURL    string
	deviceUUID string
	serverName string
}

type runtimeTestDeps struct {
	deps      app.Dependencies
	announce  chan discoveryCall
	search    chan discoveryCall
	stateSeen chan *state.PlayerState
}

func newRuntimeTestDeps() runtimeTestDeps {
	announce := make(chan discoveryCall, 1)
	search := make(chan discoveryCall, 1)
	stateSeen := make(chan *state.PlayerState, 1)
	return runtimeTestDeps{
		announce:  announce,
		search:    search,
		stateSeen: stateSeen,
		deps: app.Dependencies{
			UUIDLoader: func(string) (string, error) { return "test-uuid", nil },
			ResolveIP:  func() (string, error) { return "127.0.0.1", nil },
			Listen:     net.Listen,
			Announce: func(_ context.Context, location *ssdp.BaseURLSource, deviceUUID, serverName string) {
				announce <- discoveryCall{location.Get(), deviceUUID, serverName}
			},
			Search: func(_ context.Context, location *ssdp.BaseURLSource, deviceUUID, serverName string) {
				search <- discoveryCall{location.Get(), deviceUUID, serverName}
			},
			NewState: func(ctx context.Context, cfg config.Config) *state.PlayerState {
				st := state.New(ctx, cfg)
				stateSeen <- st
				return st
			},
		},
	}
}

func runtimeTestConfig() config.Config {
	return config.Config{HTTPPort: 0}
}

func TestStartSharesStateAndStopsFromContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fakes := newRuntimeTestDeps()

	runtime, err := app.StartWithDependencies(ctx, runtimeTestConfig(), fakes.deps)
	if err != nil {
		t.Fatalf("StartWithDependencies() error = %v", err)
	}

	var created *state.PlayerState
	select {
	case created = <-fakes.stateSeen:
	case <-time.After(runtimeTestTimeout):
		t.Fatal("timed out waiting for state creation")
	}
	if runtime.State() != created {
		t.Fatal("Runtime.State() does not expose the state used by the server")
	}

	var announced discoveryCall
	select {
	case announced = <-fakes.announce:
	case <-time.After(runtimeTestTimeout):
		t.Fatal("timed out waiting for SSDP announce")
	}
	if announced.deviceUUID != "test-uuid" || announced.serverName != app.ServerName {
		t.Fatalf("announce call = %+v", announced)
	}
	if !strings.HasPrefix(announced.baseURL, "http://127.0.0.1:") || strings.HasSuffix(announced.baseURL, ":0") {
		t.Fatalf("announce base URL = %q, want resolved listener port", announced.baseURL)
	}
	select {
	case <-fakes.search:
	case <-time.After(runtimeTestTimeout):
		t.Fatal("timed out waiting for SSDP search responder")
	}

	cancel()
	waitRuntime(t, runtime, nil)
	// Wait is deliberately repeatable for frontends and shutdown coordinators.
	waitRuntime(t, runtime, nil)
}

func TestRuntimeStopRequestsGracefulShutdown(t *testing.T) {
	fakes := newRuntimeTestDeps()
	runtime, err := app.StartWithDependencies(context.Background(), runtimeTestConfig(), fakes.deps)
	if err != nil {
		t.Fatalf("StartWithDependencies() error = %v", err)
	}

	runtime.Stop()
	waitRuntime(t, runtime, nil)
}

func TestStartReturnsInitializationErrorSynchronously(t *testing.T) {
	fakes := newRuntimeTestDeps()
	want := errors.New("UUID unavailable")
	fakes.deps.UUIDLoader = func(string) (string, error) { return "", want }

	runtime, err := app.StartWithDependencies(context.Background(), runtimeTestConfig(), fakes.deps)
	if runtime != nil {
		t.Fatal("StartWithDependencies() returned a runtime after initialization failed")
	}
	if !errors.Is(err, want) || !strings.Contains(err.Error(), "load device UUID") {
		t.Fatalf("StartWithDependencies() error = %v, want wrapped UUID error", err)
	}
}

func TestWaitReturnsHTTPServeError(t *testing.T) {
	fakes := newRuntimeTestDeps()
	listener, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatalf("net.Listen() error = %v", err)
	}
	fakes.deps.Listen = func(string, string) (net.Listener, error) { return listener, nil }

	runtime, err := app.StartWithDependencies(context.Background(), runtimeTestConfig(), fakes.deps)
	if err != nil {
		t.Fatalf("StartWithDependencies() error = %v", err)
	}
	select {
	case <-fakes.announce:
	case <-time.After(runtimeTestTimeout):
		t.Fatal("timed out waiting for runtime startup")
	}
	if err := listener.Close(); err != nil {
		t.Fatalf("listener.Close() error = %v", err)
	}

	waitRuntime(t, runtime, func(err error) bool {
		return err != nil && strings.Contains(err.Error(), "HTTP server")
	})
}

func TestStartRejectsIncompleteDependencies(t *testing.T) {
	_, err := app.StartWithDependencies(context.Background(), runtimeTestConfig(), app.Dependencies{})
	if err == nil || !strings.Contains(err.Error(), "UUIDLoader") {
		t.Fatalf("StartWithDependencies() error = %v, want missing dependency error", err)
	}
}

func waitRuntime(t *testing.T, runtime *app.Runtime, want any) {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- runtime.Wait() }()
	select {
	case err := <-done:
		switch expected := want.(type) {
		case nil:
			if err != nil {
				t.Fatalf("Runtime.Wait() error = %v", err)
			}
		case func(error) bool:
			if !expected(err) {
				t.Fatalf("Runtime.Wait() error = %v, does not match expectation", err)
			}
		default:
			t.Fatalf("unsupported wait expectation %T", want)
		}
	case <-time.After(runtimeTestTimeout):
		t.Fatal("timed out waiting for runtime shutdown")
	}
}
