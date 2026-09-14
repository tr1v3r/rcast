package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/urfave/cli/v3"

	"github.com/tr1v3r/rcast/internal/config"
	"github.com/tr1v3r/rcast/internal/gui"
)

// recordedGUI captures what the gui subcommand hands to internal/gui.
type recordedGUI struct {
	called bool
	cfg    config.Config
	deps   gui.Deps
	err    error // returned to the command runner
}

func withRecordedGUI(t *testing.T) *recordedGUI {
	t.Helper()
	rec := &recordedGUI{}
	prev := guiRun
	guiRun = func(ctx context.Context, cfg config.Config, deps gui.Deps) error {
		rec.called = true
		rec.cfg = cfg
		rec.deps = deps
		return rec.err
	}
	t.Cleanup(func() { guiRun = prev })
	return rec
}

func findCommand(cmds []*cli.Command, name string) *cli.Command {
	for _, c := range cmds {
		if c.Name == name {
			return c
		}
	}
	return nil
}

func TestGUICommandRegistered(t *testing.T) {
	root := newRootCommand(config.Config{})
	if gui := findCommand(root.Commands, "gui"); gui == nil {
		t.Fatal("root command has no gui subcommand")
	}
}

func TestGUICommandFlagsAvailable(t *testing.T) {
	guiCmd := findCommand(newRootCommand(config.Config{}).Commands, "gui")
	if guiCmd == nil {
		t.Fatal("gui subcommand missing")
	}
	names := map[string]bool{}
	for _, f := range guiCmd.Flags {
		for _, n := range f.Names() {
			names[n] = true
		}
	}
	for _, want := range []string{"debug", "fullscreen", "fs"} {
		if !names[want] {
			t.Errorf("gui subcommand missing flag %q (has %v)", want, names)
		}
	}
}

func TestGUICommandFullscreenDefaultFromConfig(t *testing.T) {
	guiCmd := findCommand(newRootCommand(config.Config{IINAFullscreen: true}).Commands, "gui")
	if guiCmd == nil {
		t.Fatal("gui subcommand missing")
	}
	for _, f := range guiCmd.Flags {
		if f.Names()[0] == "fullscreen" {
			if bf, ok := f.(*cli.BoolFlag); !ok || bf.Value != true {
				t.Errorf("gui fullscreen default = %+v, want true (from config)", f)
			}
		}
	}
}

func TestGUICommandFlagPassthrough(t *testing.T) {
	rec := withRecordedGUI(t)
	root := newRootCommand(config.Config{})

	err := root.Run(context.Background(), []string{"rcast", "gui", "--debug", "--fs"})
	if err != nil {
		t.Fatalf("run gui subcommand: %v", err)
	}
	if !rec.called {
		t.Fatal("gui action did not reach the guiRun seam")
	}
	if !rec.deps.InitialDebug {
		t.Error("InitialDebug = false, want true from --debug")
	}
	if !rec.cfg.IINAFullscreen {
		t.Error("cfg.IINAFullscreen = false, want true from --fs")
	}
	if rec.deps.Version != version {
		t.Errorf("Deps.Version = %q, want the injected %q", rec.deps.Version, version)
	}
}

func TestGUICommandDefaultsFromConfig(t *testing.T) {
	rec := withRecordedGUI(t)
	root := newRootCommand(config.Config{IINAFullscreen: true})

	if err := root.Run(context.Background(), []string{"rcast", "gui"}); err != nil {
		t.Fatalf("run gui subcommand: %v", err)
	}
	if rec.deps.InitialDebug {
		t.Error("InitialDebug = true, want false without flags")
	}
	if !rec.cfg.IINAFullscreen {
		t.Error("cfg.IINAFullscreen = false, want config default true")
	}
}

func TestGUICommandRootFlagsHonored(t *testing.T) {
	rec := withRecordedGUI(t)
	root := newRootCommand(config.Config{})

	if err := root.Run(context.Background(), []string{"rcast", "--debug", "--fullscreen", "gui"}); err != nil {
		t.Fatalf("run gui subcommand: %v", err)
	}
	if !rec.deps.InitialDebug {
		t.Error("InitialDebug = false, want true from root --debug")
	}
	if !rec.cfg.IINAFullscreen {
		t.Error("cfg.IINAFullscreen = false, want true from root --fullscreen")
	}
}

func TestGUICommandErrorPropagates(t *testing.T) {
	rec := withRecordedGUI(t)
	rec.err = gui.ErrUnsupported
	root := newRootCommand(config.Config{})

	err := root.Run(context.Background(), []string{"rcast", "gui"})
	if err == nil {
		t.Fatal("gui action swallowed the error")
	}
	if !errors.Is(err, gui.ErrUnsupported) {
		t.Fatalf("err = %v, want gui.ErrUnsupported", err)
	}
	if !strings.Contains(err.Error(), "cgo") {
		t.Errorf("stub error not friendly: %q", err.Error())
	}
}

func TestRootCommandHeadlessSurfaceUnchanged(t *testing.T) {
	root := newRootCommand(config.Config{})
	if root.Action == nil {
		t.Fatal("root action missing")
	}
	names := map[string]bool{}
	for _, f := range root.Flags {
		for _, n := range f.Names() {
			names[n] = true
		}
	}
	for _, want := range []string{"debug", "fullscreen", "fs"} {
		if !names[want] {
			t.Errorf("root flag %q disappeared", want)
		}
	}
}

// TestGUIUnsupportedOnStubBuilds pins the stub contract from the CLI side:
// internal/gui.ErrUnsupported is what main logs before exiting non-zero on
// builds without the macOS front end. The full stub behavior is asserted in
// internal/gui with CGO_ENABLED=0; here we verify the wiring carries the
// error through untouched.
func TestGUIUnsupportedOnStubBuilds(t *testing.T) {
	rec := withRecordedGUI(t)
	rec.err = gui.ErrUnsupported
	root := newRootCommand(config.Config{})

	err := root.Run(context.Background(), []string{"rcast", "gui"})
	if !errors.Is(err, gui.ErrUnsupported) {
		t.Fatalf("err = %v, want ErrUnsupported passed through for log.Fatal", err)
	}
}

// TestWatchGUIForceExitArmsAfterFirstSignal pins F6: the force exit is
// armed once the first signal starts the graceful quit and disarmed when
// the GUI has finished, without touching real signals.
func TestWatchGUIForceExitArmsAfterFirstSignal(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	guiDone := make(chan struct{})

	events := make(chan string, 4)
	arm := func() func() {
		events <- "arm"
		return func() { events <- "disarm" }
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		watchGUIForceExit(ctx, guiDone, arm)
	}()

	select {
	case <-events:
		t.Fatal("armed before the first signal")
	case <-time.After(50 * time.Millisecond):
	}

	cancel()
	select {
	case ev := <-events:
		if ev != "arm" {
			t.Fatalf("event after cancel = %q, want arm", ev)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("not armed after the first signal")
	}

	select {
	case ev := <-events:
		t.Fatalf("disarmed before guiRun returned: %q", ev)
	case <-time.After(50 * time.Millisecond):
	}

	close(guiDone)
	select {
	case ev := <-events:
		if ev != "disarm" {
			t.Fatalf("event after guiDone = %q, want disarm", ev)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("not disarmed after the GUI returned")
	}
	<-done
}
