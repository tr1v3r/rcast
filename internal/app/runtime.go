// Package app owns the reusable RCast server lifecycle.
package app

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/tr1v3r/pkg/log"

	"github.com/tr1v3r/rcast/internal/config"
	"github.com/tr1v3r/rcast/internal/httpserver"
	"github.com/tr1v3r/rcast/internal/netutil"
	"github.com/tr1v3r/rcast/internal/ssdp"
	"github.com/tr1v3r/rcast/internal/state"
	"github.com/tr1v3r/rcast/internal/uuid"
)

const ServerName = "RCast-DMR/1.1"

const shutdownTimeout = 3 * time.Second

// Dependencies contains the external collaborators used to start a Runtime.
// Callers normally use Start or Run; this type exists so lifecycle tests can
// replace network and discovery implementations deterministically.
type Dependencies struct {
	UUIDLoader func(path string) (string, error)
	ResolveIP  func() (string, error)
	Listen     func(network, addr string) (net.Listener, error)
	Announce   func(ctx context.Context, baseURL, deviceUUID, serverName string)
	Search     func(ctx context.Context, baseURL, deviceUUID, serverName string)
	NewState   func(ctx context.Context, cfg config.Config) *state.PlayerState
}

// Runtime is a running RCast server. Its State is shared by the HTTP/UPnP
// handlers and optional frontends such as the menu bar application.
type Runtime struct {
	state  *state.PlayerState
	cancel context.CancelFunc
	done   chan struct{}

	mu  sync.RWMutex
	err error
}

// Start initializes and starts the server using production dependencies.
// Initialization failures are returned synchronously. Errors that occur after
// startup are returned by Wait.
func Start(ctx context.Context, cfg config.Config) (*Runtime, error) {
	return StartWithDependencies(ctx, cfg, defaultDependencies())
}

// Run starts the server and blocks until the context is cancelled or a server
// error occurs.
func Run(ctx context.Context, cfg config.Config) error {
	runtime, err := Start(ctx, cfg)
	if err != nil {
		return err
	}
	return runtime.Wait()
}

// StartWithDependencies is Start with replaceable external collaborators.
func StartWithDependencies(ctx context.Context, cfg config.Config, deps Dependencies) (*Runtime, error) {
	if err := validateDependencies(deps); err != nil {
		return nil, err
	}

	runCtx, cancel := context.WithCancel(ctx)
	cleanup := true
	defer func() {
		if cleanup {
			cancel()
		}
	}()

	deviceUUID, err := deps.UUIDLoader(cfg.UUIDPath)
	if err != nil {
		return nil, fmt.Errorf("load device UUID: %w", err)
	}

	ip := cfg.AdvertiseIP
	if ip != "" {
		parsed := net.ParseIP(ip)
		if parsed == nil || parsed.To4() == nil {
			return nil, fmt.Errorf("DMR_ADVERTISE_IP must be an IPv4 address: %q", ip)
		}
		ip = parsed.To4().String()
	} else {
		ip, err = deps.ResolveIP()
		if err != nil {
			log.Error("no IPv4: %v", err)
			return nil, err
		}
	}

	ln, err := deps.Listen("tcp", fmt.Sprintf(":%d", cfg.HTTPPort))
	if err != nil {
		return nil, fmt.Errorf("listen: %w", err)
	}

	port := cfg.HTTPPort
	if port == 0 {
		if addr, ok := ln.Addr().(*net.TCPAddr); ok {
			port = addr.Port
		}
	}
	baseURL := fmt.Sprintf("http://%s:%d", ip, port)

	st := deps.NewState(runCtx, cfg)
	mux := httpserver.NewMux()
	httpserver.RegisterHTTP(mux, baseURL, deviceUUID, st, cfg)
	srv := &http.Server{
		Addr:              fmt.Sprintf(":%d", cfg.HTTPPort),
		Handler:           httpserver.LogMiddleware(mux),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	runtime := &Runtime{
		state:  st,
		cancel: cancel,
		done:   make(chan struct{}),
	}
	cleanup = false

	go deps.Announce(runCtx, baseURL, deviceUUID, ServerName)
	go deps.Search(runCtx, baseURL, deviceUUID, ServerName)

	serveResult := make(chan error, 1)
	go func() {
		log.Info("HTTP listening on %s", srv.Addr)
		serveResult <- srv.Serve(ln)
	}()

	go runtime.manage(runCtx, srv, serveResult)
	return runtime, nil
}

// RunWithDependencies is Run with replaceable external collaborators.
func RunWithDependencies(ctx context.Context, cfg config.Config, deps Dependencies) error {
	runtime, err := StartWithDependencies(ctx, cfg, deps)
	if err != nil {
		return err
	}
	return runtime.Wait()
}

// State returns the player state used by the running server.
func (r *Runtime) State() *state.PlayerState {
	return r.state
}

// Stop requests a graceful shutdown. It is safe to call more than once.
func (r *Runtime) Stop() {
	r.cancel()
}

// Wait blocks until shutdown completes and returns any lifecycle error. It is
// safe for multiple callers.
func (r *Runtime) Wait() error {
	<-r.done
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.err
}

func (r *Runtime) manage(ctx context.Context, srv *http.Server, serveResult <-chan error) {
	var runErr error
	select {
	case <-ctx.Done():
	case err := <-serveResult:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			runErr = fmt.Errorf("HTTP server: %w", err)
		}
	}

	r.cancel()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	if err := srv.Shutdown(shutdownCtx); err != nil && runErr == nil {
		runErr = fmt.Errorf("shutting down HTTP server: %w", err)
	}
	cancel()
	r.state.Stop()

	r.mu.Lock()
	r.err = runErr
	close(r.done)
	r.mu.Unlock()
	log.Info("bye")
}

func defaultDependencies() Dependencies {
	return Dependencies{
		UUIDLoader: uuid.LoadOrCreate,
		ResolveIP:  netutil.FirstUsableIPv4,
		Listen:     net.Listen,
		Announce:   ssdp.Announce,
		Search:     ssdp.SearchResponder,
		NewState:   state.New,
	}
}

func validateDependencies(deps Dependencies) error {
	switch {
	case deps.UUIDLoader == nil:
		return errors.New("app: UUIDLoader dependency is nil")
	case deps.ResolveIP == nil:
		return errors.New("app: ResolveIP dependency is nil")
	case deps.Listen == nil:
		return errors.New("app: Listen dependency is nil")
	case deps.Announce == nil:
		return errors.New("app: Announce dependency is nil")
	case deps.Search == nil:
		return errors.New("app: Search dependency is nil")
	case deps.NewState == nil:
		return errors.New("app: NewState dependency is nil")
	default:
		return nil
	}
}
