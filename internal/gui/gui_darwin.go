//go:build darwin && cgo

package gui

import (
	"context"
	_ "embed"
	"fmt"
	"net"
	"sync"

	"fyne.io/systray"

	"github.com/tr1v3r/rcast/internal/app"
	"github.com/tr1v3r/rcast/internal/config"
	"github.com/tr1v3r/rcast/internal/netutil"
	"github.com/tr1v3r/rcast/internal/player"
	"github.com/tr1v3r/rcast/internal/ssdp"
	"github.com/tr1v3r/rcast/internal/state"
	"github.com/tr1v3r/rcast/internal/uuid"
)

//go:embed assets/icon.png
var templateIcon []byte

// Run starts the server runtime and the macOS menu bar app, and blocks until
// the user quits, the context is cancelled or the server fails. It must be
// called from the main goroutine (the systray event loop owns the main
// thread); the server itself runs on background goroutines.
func Run(ctx context.Context, cfg config.Config, deps Deps) error {
	deps = withDarwinDefaults(deps)

	view := newSystrayView()
	c, err := newController(ctx, cfg, deps, view)
	if err != nil {
		return err
	}

	// onExit is intentionally nil: the fyne systray never invokes it on the
	// Quit path (t1 spike: quit goes through [NSApp stop:], which does not
	// send applicationWillTerminate:). Graceful shutdown happens after Run
	// returns instead.
	systray.Run(func() { view.ready(c) }, nil)

	return c.shutdown()
}

// withDarwinDefaults fills the production collaborators the caller omitted.
func withDarwinDefaults(deps Deps) Deps {
	if deps.SetSystemVolume == nil {
		deps.SetSystemVolume = player.SetSystemOutputVolume
	}
	if deps.SetSystemMute == nil {
		deps.SetSystemMute = player.SetSystemMute
	}
	return deps
}

// defaultStartServer builds the production runtime launcher. The player
// factory reads the live settings holder, so the fullscreen toggle applies
// to players created later without a server restart.
func defaultStartServer(live *settings) func(ctx context.Context, cfg config.Config) (*app.Runtime, error) {
	return func(ctx context.Context, cfg config.Config) (*app.Runtime, error) {
		return app.StartWithDependencies(ctx, cfg, app.Dependencies{
			UUIDLoader: uuid.LoadOrCreate,
			ResolveIP:  netutil.FirstUsableIPv4,
			Listen:     net.Listen,
			Announce:   ssdp.Announce,
			Search:     ssdp.SearchResponder,
			NewState: func(ctx context.Context, cfg config.Config) *state.PlayerState {
				return state.NewWithPlayerFactory(ctx, cfg, func() player.Player {
					return player.NewIINAPlayer(live.snapshot().IINAFullscreen)
				})
			},
		})
	}
}

// systrayView renders the menu model with fyne.io/systray. Menu items are
// created once (the layout is static) and later renders only push state
// changes, avoiding flicker while the menu is open.
type systrayView struct {
	mStatus    *systray.MenuItem
	mTitle     *systray.MenuItem
	mVolume    *systray.MenuItem
	mOwner     *systray.MenuItem
	mPlayPause *systray.MenuItem
	mStop      *systray.MenuItem
	mMute      *systray.MenuItem
	mToggles   map[Toggle]*systray.MenuItem
	mAbout     *systray.MenuItem
	mQuit      *systray.MenuItem

	mu      sync.Mutex
	first   bool
	last    Model
	pending *Model
	cntrl   *controller
}

func newSystrayView() *systrayView {
	return &systrayView{mToggles: make(map[Toggle]*systray.MenuItem), first: true}
}

// ready builds the static menu inside the systray onReady callback and wires
// click handling. It runs on a systray goroutine; menu APIs are goroutine
// safe (t1 spike). Items are built through locals and published to the view
// under the lock in one shot: a render racing with ready must observe either
// no menu at all (model buffered as pending) or the complete menu, never a
// half-built one.
func (v *systrayView) ready(c *controller) {
	v.cntrl = c

	systray.SetTemplateIcon(templateIcon, templateIcon)
	systray.SetTooltip(DeviceName + " — DLNA renderer")

	// Status area: informational rows are disabled.
	mStatus := systray.AddMenuItem(DeviceName, "renderer status")
	mStatus.Disable()
	mTitle := systray.AddMenuItem("Title", "media title")
	mTitle.Disable()
	mTitle.Hide()
	mVolume := systray.AddMenuItem("Volume", "volume")
	mVolume.Disable()
	mOwner := systray.AddMenuItem("Controller", "session owner")
	mOwner.Disable()
	mOwner.Hide()

	systray.AddSeparator()

	// Transport controls.
	mPlayPause := systray.AddMenuItem("Play", "play, pause or resume")
	mStop := systray.AddMenuItem("Stop", "stop playback")
	mStop.Disable()
	mMute := systray.AddMenuItem("Mute", "mute")

	systray.AddSeparator()

	// Settings submenu (Macast-style).
	mSettings := systray.AddMenuItem("Settings", "settings")
	toggles := make(map[Toggle]*systray.MenuItem, 4)
	for _, row := range []struct {
		id      Toggle
		title   string
		tooltip string
	}{
		{ToggleFullscreen, "Open IINA in Fullscreen", "launch IINA fullscreen"},
		{ToggleLinkVolume, "Link System Volume", "mirror volume to the macOS output; restarts the server when idle"},
		{ToggleAllowPreempt, "Allow Session Preemption", "let a new controller take over the session; restarts the server when idle"},
		{ToggleDebug, "Debug Logging", "verbose logs"},
	} {
		toggles[row.id] = mSettings.AddSubMenuItemCheckbox(row.title, row.tooltip, false)
	}

	systray.AddSeparator()

	mAbout := systray.AddMenuItem("About", "version")
	mAbout.Disable()
	mQuit := systray.AddMenuItem("Quit "+DeviceName, "quit")

	// Publish the complete menu, then swap out any model buffered while it
	// was being built (the state subscription delivers its initial snapshot
	// immediately).
	c.markViewReady()
	v.mu.Lock()
	v.mStatus, v.mTitle, v.mVolume, v.mOwner = mStatus, mTitle, mVolume, mOwner
	v.mPlayPause, v.mStop, v.mMute = mPlayPause, mStop, mMute
	v.mToggles, v.mAbout, v.mQuit = toggles, mAbout, mQuit
	pending := v.pending
	v.pending = nil
	v.mu.Unlock()
	if pending != nil {
		v.render(*pending)
	}
	go v.pump()
}

// pump fans menu clicks into the controller from one goroutine.
func (v *systrayView) pump() {
	c := v.cntrl
	for {
		select {
		case <-v.mPlayPause.ClickedCh:
			c.doAction(ActionPlayPause)
		case <-v.mStop.ClickedCh:
			c.doAction(ActionStop)
		case <-v.mMute.ClickedCh:
			c.doAction(ActionMute)
		case <-v.mToggles[ToggleFullscreen].ClickedCh:
			c.doToggle(ToggleFullscreen)
		case <-v.mToggles[ToggleLinkVolume].ClickedCh:
			c.doToggle(ToggleLinkVolume)
		case <-v.mToggles[ToggleAllowPreempt].ClickedCh:
			c.doToggle(ToggleAllowPreempt)
		case <-v.mToggles[ToggleDebug].ClickedCh:
			c.doToggle(ToggleDebug)
		case <-v.mQuit.ClickedCh:
			c.doAction(ActionQuit)
		}
	}
}

// render applies a model by diffing against the previous one and pushing
// only changed state, so open menus do not flicker. The first render applies
// everything unconditionally. Models rendered before the menu exists are
// buffered and applied by ready.
func (v *systrayView) render(m Model) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.mStatus == nil {
		model := m
		v.pending = &model
		return
	}
	first := v.first
	prev := v.last
	v.first = false
	v.last = m

	if first {
		v.mAbout.SetTitle(m.About)
	}

	status := fmt.Sprintf("%s — %s", m.Device, m.Transport)
	if first || status != fmt.Sprintf("%s — %s", prev.Device, prev.Transport) {
		v.mStatus.SetTitle(status)
	}

	v.applyOptional(v.mTitle, "Title: "+m.Title, m.Title, prev.Title, first)
	vol := fmt.Sprintf("Volume: %d%%", m.VolumePct)
	if first || vol != fmt.Sprintf("Volume: %d%%", prev.VolumePct) {
		v.mVolume.SetTitle(vol)
	}
	v.applyOptional(v.mOwner, "Controller: "+m.Controller, m.Controller, prev.Controller, first)

	if first || prev.PlayPause.Title != m.PlayPause.Title {
		v.mPlayPause.SetTitle(m.PlayPause.Title)
	}
	if first || prev.PlayPause.Enabled != m.PlayPause.Enabled {
		v.setEnabled(v.mPlayPause, m.PlayPause.Enabled)
	}
	if first || prev.CanStop != m.CanStop {
		v.setEnabled(v.mStop, m.CanStop)
	}
	if first || prev.Muted != m.Muted {
		v.setChecked(v.mMute, m.Muted)
	}
	if first || !sameToggleChecks(prev.Toggles, m.Toggles) {
		for i, row := range m.Toggles {
			if first || (i < len(prev.Toggles) && prev.Toggles[i].Checked != row.Checked) {
				if item, ok := v.mToggles[row.ID]; ok {
					v.setChecked(item, row.Checked)
				}
			}
		}
	}
}

// applyOptional pushes an informational row that is hidden when empty.
func (v *systrayView) applyOptional(item *systray.MenuItem, text, value, prevValue string, first bool) {
	switch {
	case value == "":
		if first || prevValue != "" {
			item.Hide()
		}
	case first || prevValue != value:
		item.SetTitle(text)
		item.Show()
	}
}

func (v *systrayView) setEnabled(item *systray.MenuItem, enabled bool) {
	if enabled {
		item.Enable()
	} else {
		item.Disable()
	}
}

func (v *systrayView) setChecked(item *systray.MenuItem, checked bool) {
	if checked {
		item.Check()
	} else {
		item.Uncheck()
	}
}

func sameToggleChecks(a, b []ToggleRow) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Checked != b[i].Checked {
			return false
		}
		if a[i].ID != b[i].ID {
			return false
		}
	}
	return true
}

// quit stops the native event loop; systray.Quit is goroutine safe (t1).
func (v *systrayView) quit() {
	systray.Quit()
}
