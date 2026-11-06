package gui

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/tr1v3r/pkg/log"

	"github.com/tr1v3r/rcast/internal/app"
	"github.com/tr1v3r/rcast/internal/config"
	"github.com/tr1v3r/rcast/internal/monitoring"
	"github.com/tr1v3r/rcast/internal/state"
)

// ErrUnsupported is returned by Run on builds without the macOS front end
// (non-darwin, or darwin with CGO_ENABLED=0).
var ErrUnsupported = errors.New("GUI requires macOS with cgo enabled")

// Deps are the external collaborators of the menu bar application. Tests
// inject fakes; production wiring is provided by the platform layers.
type Deps struct {
	// StartServer launches the shared server runtime. The production
	// implementation wraps app.StartWithDependencies with a player factory
	// that reads the live settings holder, so the fullscreen toggle applies
	// to players created after the toggle without restarting the server.
	StartServer func(ctx context.Context, cfg config.Config) (*app.Runtime, error)

	// SaveSettings persists the toggles. It defaults to config.Save writing
	// the settings.json selected by the base config (internal/config).
	SaveSettings func(Settings) error

	// SetSystemMute mirrors the system output sink used by the
	// RenderingControl handler when Link System Volume is enabled. It
	// defaults to the internal/player implementation on darwin.
	SetSystemMute func(m bool) error

	// SetLogLevel applies the Debug Logging toggle. The native view installs
	// a level-filtering handler before starting any server goroutines.
	SetLogLevel func(level log.Level)

	// Version is shown in the About row; "dev" when empty.
	Version string
}

func (d Deps) version() string {
	if d.Version == "" {
		return "dev"
	}
	return d.Version
}

// menuView is the platform rendering surface driven by the controller.
type menuView interface {
	// render applies a menu model to the front end.
	render(Model)
	// quit stops the native event loop.
	quit()
}

// settings is the goroutine-safe holder for toggleable preferences.
type settings struct {
	mu  sync.Mutex
	val Settings
}

func (s *settings) snapshot() Settings {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.val
}

func (s *settings) update(fn func(*Settings)) Settings {
	s.mu.Lock()
	defer s.mu.Unlock()
	fn(&s.val)
	return s.val
}

// controller owns the server runtime for the GUI lifetime and translates
// menu interactions into the same semantics the UPnP handlers implement.
type controller struct {
	ctx    context.Context
	cancel context.CancelFunc
	deps   Deps
	set    *settings
	view   menuView
	baseCf config.Config

	lifecycleMu      sync.Mutex // serializes runtime replacement and final shutdown
	mu               sync.Mutex
	rt               *app.Runtime
	stopSub          func()
	lastSnap         state.Snapshot
	pendingRestart   bool
	linkSystemVolume bool // setting captured by the current runtime
	fatal            error

	quitOnce  sync.Once
	viewReady chan struct{}
	readyOnce sync.Once
}

// newController starts the server and wires state subscriptions. The view
// buffers renders until markViewReady, so early snapshots (the subscription
// delivers an initial one immediately) are not lost. A nil Deps.StartServer
// selects the platform default production wiring.
func newController(ctx context.Context, cfg config.Config, deps Deps, view menuView) (*controller, error) {
	ctx, cancel := context.WithCancel(ctx)
	c := &controller{
		cancel:    cancel,
		ctx:       ctx,
		deps:      deps,
		set:       &settings{val: settingsFromConfig(cfg)},
		view:      view,
		baseCf:    cfg,
		viewReady: make(chan struct{}),
	}
	if c.deps.SetLogLevel == nil {
		c.deps.SetLogLevel = installGUILogHandler()
	}
	if c.deps.StartServer == nil {
		c.deps.StartServer = defaultStartServer(c.set)
	}
	if c.deps.SaveSettings == nil {
		c.deps.SaveSettings = c.saveSettings
	}
	// Keep the actual log level consistent with the seeded toggle state
	// (settings.json may enable debug without a CLI flag).
	if c.set.snapshot().Debug {
		c.deps.SetLogLevel(log.DebugLevel)
	}
	if err := c.startServer(); err != nil {
		cancel()
		return nil, err
	}
	go func() {
		<-ctx.Done()
		c.requestQuit()
	}()
	return c, nil
}

// markViewReady releases the shutdown gate once the platform view exists.
func (c *controller) markViewReady() {
	c.readyOnce.Do(func() { close(c.viewReady) })
}

// startServer launches the runtime with the current settings and subscribes
// menu refreshes to state changes. Replacements hold lifecycleMu.
func (c *controller) startServer() error {
	if err := c.ctx.Err(); err != nil {
		return err
	}
	cfg := c.set.snapshot().applyTo(c.baseCf)
	rt, err := c.deps.StartServer(c.ctx, cfg)
	if err != nil {
		return err
	}
	c.mu.Lock()
	c.rt = rt
	c.linkSystemVolume = cfg.LinkSystemOutputVolume
	c.stopSub = rt.State().Subscribe(func(snap state.Snapshot) { c.onSnapshot(rt, snap) })
	c.mu.Unlock()
	go c.watchRuntime(rt)
	return nil
}

// watchRuntime exits the menu bar app when the server dies on its own.
func (c *controller) watchRuntime(rt *app.Runtime) {
	err := rt.Wait()
	if err == nil {
		return
	}
	c.mu.Lock()
	if c.rt != rt || c.fatal != nil {
		c.mu.Unlock()
		return
	}
	c.fatal = fmt.Errorf("server: %w", err)
	c.mu.Unlock()
	c.requestQuit()
}

// onSnapshot renders the menu for each state change. It runs on the
// subscription worker, so renders are serialized per subscriber (t3).
func (c *controller) onSnapshot(rt *app.Runtime, snap state.Snapshot) {
	c.mu.Lock()
	if c.rt != rt || c.ctx.Err() != nil {
		c.mu.Unlock()
		return
	}
	c.lastSnap = snap
	c.view.render(BuildModel(snap, c.set.snapshot(), c.deps.version()))
	pending := c.pendingRestart
	c.mu.Unlock()

	if pending && snap.TransportState == "STOPPED" {
		c.restartWhenIdle()
	}
}

func (c *controller) refresh() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ctx.Err() == nil {
		c.view.render(BuildModel(c.lastSnap, c.set.snapshot(), c.deps.version()))
	}
}

// doAction executes a menu command with the same semantics as the UPnP
// handlers: serialized with remote commands, transport state transitions
// included.
func (c *controller) doAction(a Action) {
	c.mu.Lock()
	rt := c.rt
	linkSystemVolume := c.linkSystemVolume
	c.mu.Unlock()
	if a == ActionQuit {
		c.requestQuit()
		return
	}
	if rt == nil {
		return
	}
	st := rt.State()
	ctx := st.Context()

	switch a {
	case ActionPlayPause:
		st.Serialize(func() {
			if ctx.Err() != nil {
				return
			}
			snap := st.Snapshot()
			switch snap.TransportState {
			case "PLAYING":
				p := st.GetActivePlayer()
				if p == nil {
					return
				}
				if err := p.Pause(ctx); err != nil {
					c.playerError("pause", err)
					return
				}
				st.SetTransportState("PAUSED_PLAYBACK")
			case "PAUSED_PLAYBACK", "STOPPED":
				if snap.TransportURI == "" {
					return
				}
				st.SetTransportState("TRANSITIONING")
				p := st.EnsurePlayer()
				if err := p.Play(ctx, snap.TransportURI, st.GetVolume()); err != nil {
					c.playerError("play", err)
					st.SetTransportState("STOPPED")
					return
				}
				if snap.Title != "" {
					if err := p.SetTitle(ctx, snap.Title); err != nil {
						log.CtxWarn(ctx, "gui: set media title: %v", err)
					}
				}
				if st.GetMute() {
					if err := p.SetMute(ctx, true); err != nil {
						// Mirror the UPnP Play action: a player that cannot be
						// muted is torn down and the transport returns to
						// STOPPED so the menu does not linger on Loading.
						c.playerError("apply mute", err)
						_ = st.StopPlayer()
						st.SetTransportState("STOPPED")
						return
					}
				}
				st.SetTransportState("PLAYING")
			}
		})

	case ActionStop:
		st.Serialize(func() {
			if ctx.Err() != nil {
				return
			}
			if err := st.StopPlayer(); err != nil {
				c.playerError("stop", err)
				return
			}
			st.SetTransportState("STOPPED")
			if owner := st.Snapshot().SessionOwner; owner != "" {
				st.ReleaseSession(owner)
			}
		})

	case ActionMute:
		st.Serialize(func() {
			if ctx.Err() != nil {
				return
			}
			m := !st.GetMute()
			if p := st.GetActivePlayer(); p != nil {
				if err := p.SetMute(ctx, m); err != nil {
					c.playerError("mute", err)
					return
				}
			}
			if linkSystemVolume && c.deps.SetSystemMute != nil {
				if err := c.deps.SetSystemMute(m); err != nil {
					log.CtxWarn(ctx, "gui: set system mute: %v", err)
				}
			}
			st.SetMute(m)
		})

	}
}

// doToggle flips a settings row, persists it and applies immediate effects.
// Toggles the running server cannot adopt (preemption, linked system volume
// are captured by the UPnP handlers) schedule a server restart; the restart
// is deferred while media is playing and happens as soon as it stops.
func (c *controller) doToggle(t Toggle) {
	needsRestart := false
	c.set.update(func(s *Settings) {
		switch t {
		case ToggleFullscreen:
			s.IINAFullscreen = !s.IINAFullscreen
		case ToggleLinkVolume:
			s.LinkSystemVolume = !s.LinkSystemVolume
			needsRestart = true
		case ToggleAllowPreempt:
			s.AllowPreempt = !s.AllowPreempt
			needsRestart = true
		case ToggleDebug:
			s.Debug = !s.Debug
			level := log.InfoLevel
			if s.Debug {
				level = log.DebugLevel
			}
			c.deps.SetLogLevel(level)
		}
	})

	if err := c.deps.SaveSettings(c.set.snapshot()); err != nil {
		log.CtxError(c.ctx, "gui: save settings: %v", err)
	}
	if needsRestart {
		c.scheduleRestart()
	}
	c.refresh()
}

// applyTo overlays the toggleable settings onto a base config for (re)
// starting the runtime and for persistence.
func (s Settings) applyTo(cfg config.Config) config.Config {
	cfg.IINAFullscreen = s.IINAFullscreen
	cfg.LinkSystemOutputVolume = s.LinkSystemVolume
	cfg.AllowSessionPreempt = s.AllowPreempt
	cfg.DebugLog = s.Debug
	return cfg
}

// saveSettings is the production persistence adapter: config.Save writes the
// four GUI booleans to the settings.json selected by the base config.
func (c *controller) saveSettings(s Settings) error {
	return config.Save(s.applyTo(c.baseCf))
}

// scheduleRestart requests a server restart for setting changes the running
// server cannot adopt. It takes effect immediately while idle, and is
// deferred until playback stops otherwise. Idleness is read from the live
// player state, not the last delivered snapshot, so a lagging subscription
// cannot trigger a restart mid-playback.
func (c *controller) scheduleRestart() {
	c.mu.Lock()
	c.pendingRestart = true
	c.mu.Unlock()
	c.restartWhenIdle()
}

// Commands and restart admission share the state command lock. An admitted
// command finishes before the idle check. Later commands see the cancelled
// runtime context. Wait must run outside that lock so state.Stop can drain.
func (c *controller) restartWhenIdle() {
	c.lifecycleMu.Lock()
	err := c.replaceIdleRuntime()
	c.lifecycleMu.Unlock()
	if err != nil && c.ctx.Err() == nil {
		c.setFatal(fmt.Errorf("restart server: %w", err))
		c.requestQuit()
	}
}

func (c *controller) replaceIdleRuntime() error {
	c.mu.Lock()
	rt := c.rt
	pending := c.pendingRestart
	c.mu.Unlock()
	if !pending || rt == nil || c.ctx.Err() != nil || rt.State().GetTransportState() != "STOPPED" {
		return nil
	}

	restart := false
	rt.State().Serialize(func() {
		if rt.State().GetTransportState() != "STOPPED" || rt.State().Context().Err() != nil {
			return
		}
		c.mu.Lock()
		c.pendingRestart = false
		c.stopSub()
		c.stopSub = nil
		c.rt = nil
		c.mu.Unlock()
		rt.Stop()
		restart = true
	})
	if !restart {
		return nil // keep pending; the next STOPPED notification will retry
	}
	if err := rt.Wait(); err != nil {
		log.CtxWarn(c.ctx, "gui: old server stopped with error: %v", err)
	}
	if c.ctx.Err() != nil {
		return nil
	}
	if err := c.startServer(); err == nil {
		return nil
	}
	// A listener may need a moment to release its port. Quit cancels the retry.
	select {
	case <-c.ctx.Done():
		return nil
	case <-time.After(500 * time.Millisecond):
		return c.startServer()
	}
}

func (c *controller) setFatal(err error) {
	c.mu.Lock()
	if c.fatal == nil {
		c.fatal = err
	}
	c.mu.Unlock()
}

// playerError records a failed player command the way the UPnP handlers do.
func (c *controller) playerError(what string, err error) {
	monitoring.GetMetrics().RecordPlayerError()
	log.CtxError(c.ctx, "gui: %s: %v", what, err)
}

// requestQuit stops the native loop exactly once; final cleanup happens in
// shutdown after the platform loop has returned. It waits for the native
// view first: [NSApp stop:] posted before the event loop starts would be
// lost and Run would hang. The state subscription is cancelled before the
// loop is stopped so an in-flight render cannot call into Cocoa after the
// native event loop has exited.
func (c *controller) requestQuit() {
	c.quitOnce.Do(func() {
		c.cancel()
		select {
		case <-c.viewReady:
		case <-time.After(10 * time.Second):
		}
		c.lifecycleMu.Lock()
		defer c.lifecycleMu.Unlock()
		c.mu.Lock()
		stopSub := c.stopSub
		c.stopSub = nil
		c.mu.Unlock()
		if stopSub != nil {
			stopSub()
		}
		c.view.quit()
	})
}

// shutdown tears the server down after the native menu loop has exited and
// reports the lifecycle result. The fyne systray onExit callback is never
// invoked on the Quit path (verified in the t1 spike), so all cleanup lives
// here, after Run returns.
func (c *controller) shutdown() error {
	c.cancel()
	c.lifecycleMu.Lock()
	defer c.lifecycleMu.Unlock()
	c.mu.Lock()
	rt := c.rt
	stopSub := c.stopSub
	fatal := c.fatal
	c.mu.Unlock()

	if stopSub != nil {
		stopSub()
	}
	if rt != nil {
		rt.Stop()
		if err := rt.Wait(); err != nil && fatal == nil {
			fatal = err
		}
	}
	return fatal
}
