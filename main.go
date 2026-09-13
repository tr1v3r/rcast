package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/signal"
	"syscall"

	"github.com/tr1v3r/pkg/log"
	"github.com/urfave/cli/v3"

	"github.com/tr1v3r/rcast/internal/app"
	"github.com/tr1v3r/rcast/internal/config"
	"github.com/tr1v3r/rcast/internal/state"
)

const serverName = app.ServerName

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

// serverDeps preserves the main package's existing deterministic test seam.
// Production wiring lives in internal/app.
type serverDeps struct {
	uuidLoader func(path string) (string, error)
	resolveIP  func() (string, error)
	listen     func(network, addr string) (net.Listener, error)
	announce   func(ctx context.Context, baseURL, deviceUUID, serverName string)
	search     func(ctx context.Context, baseURL, deviceUUID, serverName string)
}

func runServer(ctx context.Context, cfg config.Config) error {
	ctx, stopSignals := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stopSignals()
	return app.Run(ctx, cfg)
}

// runServerWithRuntime preserves the focused main package test seam while the
// reusable lifecycle itself lives in internal/app.
func runServerWithRuntime(ctx context.Context, cfg config.Config, deps serverDeps) error {
	return app.RunWithDependencies(ctx, cfg, app.Dependencies{
		UUIDLoader: deps.uuidLoader,
		ResolveIP:  deps.resolveIP,
		Listen:     deps.listen,
		Announce:   deps.announce,
		Search:     deps.search,
		NewState:   state.New,
	})
}
