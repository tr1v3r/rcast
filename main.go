package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/tr1v3r/pkg/log"
	"github.com/urfave/cli/v3"

	"github.com/tr1v3r/rcast/internal/app"
	"github.com/tr1v3r/rcast/internal/config"
	"github.com/tr1v3r/rcast/internal/gui"
	"github.com/tr1v3r/rcast/internal/ssdp"
	"github.com/tr1v3r/rcast/internal/state"
)

const serverName = app.ServerName

// ssdpShutdownGrace bounds how long shutdown waits for the announce/search
// loops to flush their ssdp:byebye messages before exiting anyway.
var ssdpShutdownGrace = 2 * time.Second

var (
	version   = "dev"
	buildTime = "unknown"
	gitCommit = "unknown"
	goVersion = "unknown"
)

// guiRun is the seam over internal/gui.Run; tests replace it to exercise the
// gui subcommand wiring without starting the real menu bar app.
var guiRun = func(ctx context.Context, cfg config.Config, deps gui.Deps) error {
	return gui.Run(ctx, cfg, deps)
}

func main() {
	defer log.Close()

	cfg := config.Load()
	cmd := newRootCommand(cfg)

	if err := cmd.Run(context.Background(), os.Args); err != nil {
		// tr1v3r/pkg/log's Fatal only logs; flush and exit explicitly so
		// error paths (including `rcast gui` on stub builds) exit non-zero.
		log.Fatal("run app failed: %v", err)
		log.Close()
		os.Exit(1)
	}
}

// newRootCommand builds the rcast command tree: the default action runs the
// headless server, and `rcast gui` runs the macOS menu bar front end.
func newRootCommand(cfg config.Config) *cli.Command {
	return &cli.Command{
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
		Commands: []*cli.Command{newGUICommand(cfg)},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			if cmd.Bool("debug") {
				log.SetLevel(log.DebugLevel)
			}
			cfg.IINAFullscreen = cmd.Bool("fullscreen")

			return runServer(ctx, cfg)
		},
	}
}

// newGUICommand builds the `rcast gui` subcommand. The same flags as the
// root command are accepted on the subcommand (rcast gui --debug --fs) and
// root-level spellings (rcast --debug gui) are honored through the root
// lookup, mirroring headless semantics.
func newGUICommand(cfg config.Config) *cli.Command {
	return &cli.Command{
		Name:  "gui",
		Usage: "run as a macOS menu bar app (server + status menu)",
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
			debug := cmd.Bool("debug") || cmd.Root().Bool("debug")
			fullscreen := cmd.Bool("fullscreen") || cmd.Root().Bool("fullscreen")

			if debug {
				log.SetLevel(log.DebugLevel)
			}
			cfg.IINAFullscreen = fullscreen

			return runGUI(ctx, cmd, cfg, debug)
		},
	}
}

// runGUI drives the menu bar app. Signals cancel the context and the GUI
// controller turns that into a graceful quit (server drain, then the native
// loop exits); on stub builds (no cgo / non-darwin) gui.Run reports
// gui.ErrUnsupported and main exits non-zero after logging it. While the
// graceful quit is in flight, a second SIGINT/SIGTERM force-exits the
// process, matching the headless semantics from #8. gui.Run stays on the
// main goroutine (the systray event loop owns the main thread); the signal
// watch runs beside it.
func runGUI(ctx context.Context, cmd *cli.Command, cfg config.Config, debug bool) error {
	ctx, stopSignals := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stopSignals()

	guiDone := make(chan struct{})
	go watchGUIForceExit(ctx, guiDone, armForceExit)

	err := guiRun(ctx, cfg, gui.Deps{
		Version:      version,
		InitialDebug: debug,
	})
	close(guiDone)
	return err
}

// watchGUIForceExit arms the force exit once the first signal starts the
// graceful quit, and disarms it when guiRun has returned. The arm function
// is injected so tests can observe the arm/disarm ordering without signals.
func watchGUIForceExit(ctx context.Context, guiDone <-chan struct{}, arm func() (disarm func())) {
	<-ctx.Done()
	disarm := arm()
	<-guiDone
	disarm()
}

// serverDeps preserves the main package's deterministic test seam. Production
// dependency wiring lives in internal/app.
type serverDeps struct {
	uuidLoader func(path string) (string, error)
	resolveIP  func() (string, error)
	listen     func(network, addr string) (net.Listener, error)
	announce   func(ctx context.Context, loc *ssdp.BaseURLSource, deviceUUID, serverName string)
	search     func(ctx context.Context, loc *ssdp.BaseURLSource, deviceUUID, serverName string)
}

func runServer(ctx context.Context, cfg config.Config) error {
	return runServerRuntime(ctx, func(runCtx context.Context) (*app.Runtime, error) {
		return app.Start(runCtx, cfg)
	})
}

func runServerWithRuntime(ctx context.Context, cfg config.Config, deps serverDeps) error {
	return runServerRuntime(ctx, func(runCtx context.Context) (*app.Runtime, error) {
		return app.StartWithDependencies(runCtx, cfg, app.Dependencies{
			UUIDLoader:        deps.uuidLoader,
			ResolveIP:         deps.resolveIP,
			Listen:            deps.listen,
			Announce:          deps.announce,
			Search:            deps.search,
			NewState:          state.New,
			SSDPShutdownGrace: ssdpShutdownGrace,
		})
	})
}

// runServerRuntime owns headless-only signal semantics around the reusable app
// runtime. The runtime itself deliberately installs no handlers so GUI signals
// can flow through the native event loop.
func runServerRuntime(ctx context.Context, start func(context.Context) (*app.Runtime, error)) error {
	ctx, stopSignals := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stopSignals()

	runtime, err := start(ctx)
	if err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- runtime.Wait() }()

	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		// NotifyContext consumes later signals. Once graceful shutdown starts,
		// arm a fresh handler so a second signal can force an immediate exit.
		disarmForceExit := armForceExit()
		defer disarmForceExit()
		return <-done
	}
}

// armForceExit installs a signal handler that hard-exits the process when a
// second SIGINT/SIGTERM arrives while shutdown is in flight. The returned
// function disarms the handler once graceful shutdown completes.
func armForceExit() (disarm func()) {
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	done := make(chan struct{})
	go func() {
		select {
		case sig := <-sigs:
			log.Warn("signal %v during shutdown; forcing exit", sig)
			os.Exit(130)
		case <-done:
		}
	}()
	var once sync.Once
	return func() {
		once.Do(func() {
			close(done)
			signal.Stop(sigs)
		})
	}
}
