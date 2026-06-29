package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/tr1v3r/pkg/log"
	"github.com/urfave/cli/v3"

	"github.com/tr1v3r/rcast/internal/config"
	"github.com/tr1v3r/rcast/internal/httpserver"
	"github.com/tr1v3r/rcast/internal/netutil"
	"github.com/tr1v3r/rcast/internal/ssdp"
	"github.com/tr1v3r/rcast/internal/state"
	"github.com/tr1v3r/rcast/internal/uuid"
)

const serverName = "RCast-DMR/1.1" // "GoDLNA-DMR/1.1"

var (
	version   = "dev"
	buildTime = "unknown"
	gitCommit = "unknown"
	goVersion = "unknown"
)

func main() {
	defer log.Close()

	cfg := config.Load()

	cmd := &cli.Command{
		Name:    "rcast",
		Usage:   "RCast DMR",
		Version: fmt.Sprintf("%s (commit %s, built %s, %s)", version, gitCommit, buildTime, goVersion),
		Flags: []cli.Flag{
			&cli.BoolFlag{
				Name:  "debug",
				Usage: "enable debug logging",
			},
			&cli.BoolFlag{
				Name:    "fullscreen",
				Aliases: []string{"fs"},
				Usage:   "open iina in fullscreen",
				Value:   cfg.IINAFullscreen,
			},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			if cmd.Bool("debug") {
				log.SetLevel(log.DebugLevel)
			}
			cfg.IINAFullscreen = cmd.Bool("fullscreen")

			return runServer(ctx, cfg)
		},
	}

	if err := cmd.Run(context.Background(), os.Args); err != nil {
		log.Fatal("run app failed: %v", err)
	}
}

// serverDeps bundles the external collaborators of runServer so they can be
// replaced in tests. All fields are required; production wiring lives in
// runServer.
type serverDeps struct {
	uuidLoader func(path string) (string, error)
	resolveIP  func() (string, error)
	listen     func(network, addr string) (net.Listener, error)
	announce   func(ctx context.Context, baseURL, deviceUUID, serverName string)
	search     func(ctx context.Context, baseURL, deviceUUID, serverName string)
}

func runServer(ctx context.Context, cfg config.Config) error {
	return runServerWithRuntime(ctx, cfg, serverDeps{
		uuidLoader: uuid.LoadOrCreate,
		resolveIP:  netutil.FirstUsableIPv4,
		listen:     net.Listen,
		announce:   ssdp.Announce,
		search:     ssdp.SearchResponder,
	})
}

func runServerWithRuntime(ctx context.Context, cfg config.Config, deps serverDeps) error {
	ctx, stopSignals := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stopSignals()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// 设备 UUID
	deviceUUID, err := deps.uuidLoader(cfg.UUIDPath)
	if err != nil {
		return fmt.Errorf("load device UUID: %w", err)
	}

	// 网卡 IP
	ip := cfg.AdvertiseIP
	if ip != "" {
		parsed := net.ParseIP(ip)
		if parsed == nil || parsed.To4() == nil {
			return fmt.Errorf("DMR_ADVERTISE_IP must be an IPv4 address: %q", ip)
		}
		ip = parsed.To4().String()
	} else {
		ip, err = deps.resolveIP()
		if err != nil {
			log.Error("no IPv4: %v", err)
			return err
		}
	}

	// HTTP listener (created before baseURL so port 0 resolves to the real port)
	ln, err := deps.listen("tcp", fmt.Sprintf(":%d", cfg.HTTPPort))
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	port := cfg.HTTPPort
	if port == 0 {
		if a, ok := ln.Addr().(*net.TCPAddr); ok {
			port = a.Port
		}
	}
	baseURL := fmt.Sprintf("http://%s:%d", ip, port)

	// 状态
	st := state.New(ctx, cfg)
	defer st.Stop()

	// HTTP
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

	// SSDP
	go deps.announce(ctx, baseURL, deviceUUID, serverName)
	go deps.search(ctx, baseURL, deviceUUID, serverName)

	// 启动 HTTP
	serverErr := make(chan error, 1)
	go func() {
		log.Info("HTTP listening on %s", srv.Addr)
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			serverErr <- err
		}
	}()

	// 优雅退出
	var runErr error
	select {
	case <-ctx.Done():
	case err := <-serverErr:
		runErr = fmt.Errorf("HTTP server: %w", err)
	}
	cancel()
	ctxShutdown, cancel2 := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel2()
	if err := srv.Shutdown(ctxShutdown); err != nil && runErr == nil {
		runErr = fmt.Errorf("shutting down HTTP server: %w", err)
	}
	log.Info("bye")

	return runErr
}
