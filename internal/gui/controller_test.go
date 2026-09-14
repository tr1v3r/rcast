package gui

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tr1v3r/pkg/log"

	"github.com/tr1v3r/rcast/internal/app"
	"github.com/tr1v3r/rcast/internal/config"
	"github.com/tr1v3r/rcast/internal/player"
	"github.com/tr1v3r/rcast/internal/ssdp"
	"github.com/tr1v3r/rcast/internal/state"
)

// fakePlayer records command calls; it never touches IINA.
type fakePlayer struct {
	mu       sync.Mutex
	calls    []string
	lastURI  string
	lastVol  int
	failMute bool
}

func (f *fakePlayer) record(format string, args ...any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, fmt.Sprintf(format, args...))
}

func (f *fakePlayer) recorded() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.calls))
	copy(out, f.calls)
	return out
}

func (f *fakePlayer) Play(_ context.Context, uri string, volume int) error {
	f.mu.Lock()
	f.lastURI, f.lastVol = uri, volume
	f.mu.Unlock()
	f.record("play %s vol=%d", uri, volume)
	return nil
}

func (f *fakePlayer) Pause(context.Context) error { f.record("pause"); return nil }

func (f *fakePlayer) StopPlayback(context.Context) error { f.record("stop-playback"); return nil }

func (f *fakePlayer) Stop(context.Context) error { f.record("stop"); return nil }

func (f *fakePlayer) SetVolume(_ context.Context, v int) error {
	f.record("set-volume %d", v)
	return nil
}

func (f *fakePlayer) SetMute(_ context.Context, m bool) error {
	f.mu.Lock()
	fail := f.failMute
	f.mu.Unlock()
	if fail {
		f.record("set-mute-failed %v", m)
		return errors.New("mute refused")
	}
	f.record("set-mute %v", m)
	return nil
}

func (f *fakePlayer) SetFullscreen(_ context.Context, fs bool) error {
	f.record("set-fullscreen %v", fs)
	return nil
}

func (f *fakePlayer) SetTitle(_ context.Context, title string) error {
	f.record("set-title %s", title)
	return nil
}

func (f *fakePlayer) Screenshot(context.Context, string) error { return nil }

func (f *fakePlayer) SetSpeed(context.Context, float64) error { return nil }

func (f *fakePlayer) Seek(context.Context, float64) error { return nil }

func (f *fakePlayer) GetPosition(context.Context) (float64, error) { return 0, nil }

func (f *fakePlayer) GetDuration(context.Context) (float64, error) { return 0, nil }

// recordingView captures renders and quit calls for assertions.
type recordingView struct {
	mu       sync.Mutex
	models   []Model
	quits    int
	onQuitMu sync.Mutex
	onQuit   func()
}

// setOnQuit installs a hook invoked inside quit(); it may be set after the
// controller is running (unlike the view reference itself, which is
// immutable once handed to the controller).
func (v *recordingView) setOnQuit(fn func()) {
	v.onQuitMu.Lock()
	v.onQuit = fn
	v.onQuitMu.Unlock()
}

func (v *recordingView) render(m Model) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.models = append(v.models, m)
}

func (v *recordingView) quit() {
	v.onQuitMu.Lock()
	hook := v.onQuit
	v.onQuitMu.Unlock()
	if hook != nil {
		hook()
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	v.quits++
}

func (v *recordingView) last() Model {
	v.mu.Lock()
	defer v.mu.Unlock()
	if len(v.models) == 0 {
		return Model{}
	}
	return v.models[len(v.models)-1]
}

func (v *recordingView) quitCount() int {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.quits
}

// guiHarness wires a controller against a real app.Runtime started with
// replaceable dependencies: a temp UUID file, loopback IP, an ephemeral
// listener, no-op discovery and the shared fakePlayer factory.
type guiHarness struct {
	t           *testing.T
	ctx         context.Context
	cancel      context.CancelFunc
	view        *recordingView
	ctl         *controller
	fp          *fakePlayer
	starts      atomic.Int32
	saved       []Settings
	saveMu      sync.Mutex
	systemMux   sync.Mutex
	systemMutes []bool
	logMu       sync.Mutex
	logLevels   []log.Level
}

func newGUIHarness(t *testing.T) *guiHarness {
	t.Helper()

	dir := t.TempDir()
	uuidPath := filepath.Join(dir, "uuid.txt")
	if err := os.WriteFile(uuidPath, []byte("test-uuid\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	h := &guiHarness{t: t, fp: &fakePlayer{}}
	h.ctx, h.cancel = context.WithCancel(context.Background())
	h.view = &recordingView{}

	deps := Deps{
		StartServer: h.startServer,
		SaveSettings: func(s Settings) error {
			h.saveMu.Lock()
			h.saved = append(h.saved, s)
			h.saveMu.Unlock()
			return nil
		},
		SetSystemMute: func(m bool) error {
			h.systemMux.Lock()
			h.systemMutes = append(h.systemMutes, m)
			h.systemMux.Unlock()
			return nil
		},
		// Recorded instead of applied: the real log.SetLevel races
		// concurrent server logging inside the upstream handler.
		SetLogLevel: func(level log.Level) {
			h.logMu.Lock()
			h.logLevels = append(h.logLevels, level)
			h.logMu.Unlock()
		},
		Version: "test",
	}

	cfg := config.Config{UUIDPath: uuidPath, HTTPPort: 0}
	ctl, err := newController(h.ctx, cfg, deps, h.view)
	if err != nil {
		t.Fatalf("newController: %v", err)
	}
	// The real view marks readiness from its onReady callback; the recording
	// view is always ready.
	ctl.markViewReady()
	h.ctl = ctl
	return h
}

func (h *guiHarness) startServer(ctx context.Context, cfg config.Config) (*app.Runtime, error) {
	h.starts.Add(1)
	return app.StartWithDependencies(ctx, cfg, app.Dependencies{
		UUIDLoader: func(path string) (string, error) {
			b, err := os.ReadFile(path)
			if err != nil {
				return "", err
			}
			return string(trimNewline(b)), nil
		},
		ResolveIP: func() (string, error) { return "127.0.0.1", nil },
		Listen:    net.Listen,
		Announce:  func(context.Context, *ssdp.BaseURLSource, string, string) {},
		Search:    func(context.Context, *ssdp.BaseURLSource, string, string) {},
		NewState: func(ctx context.Context, cfg config.Config) *state.PlayerState {
			return state.NewWithPlayerFactory(ctx, cfg, func() player.Player { return h.fp })
		},
	})
}

func trimNewline(b []byte) []byte {
	for len(b) > 0 && (b[len(b)-1] == '\n' || b[len(b)-1] == '\r') {
		b = b[:len(b)-1]
	}
	return b
}

func (h *guiHarness) close() {
	h.cancel()
	_ = h.ctl.shutdown()
}

func (h *guiHarness) state() *state.PlayerState {
	h.ctl.mu.Lock()
	rt := h.ctl.rt
	h.ctl.mu.Unlock()
	if rt == nil {
		h.t.Fatal("no runtime")
	}
	return rt.State()
}

// eventually polls until the condition holds or the deadline expires.
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestControllerPlayPauseStopMirrorTransportSemantics(t *testing.T) {
	h := newGUIHarness(t)
	defer h.close()
	st := h.state()

	// Without media, play/pause is inert.
	h.ctl.doAction(ActionPlayPause)
	if got := st.GetTransportState(); got != "STOPPED" {
		t.Fatalf("transport = %q, want STOPPED", got)
	}

	// Load media as a control point would, then play from the menu.
	st.SetURI("http://example.com/movie.mp4", `<DIDL-Lite><item><dc:title>Movie</dc:title></item></DIDL-Lite>`)
	h.ctl.doAction(ActionPlayPause)

	eventually(t, "PLAYING state", func() bool { return st.GetTransportState() == "PLAYING" })
	calls := h.fp.recorded()
	if len(calls) == 0 || calls[0] != "play http://example.com/movie.mp4 vol=50" {
		t.Fatalf("player calls = %v", calls)
	}
	// Friendly title from the DIDL metadata is applied like the UPnP Play action.
	foundTitle := false
	for _, c := range calls {
		if c == "set-title Movie" {
			foundTitle = true
		}
	}
	if !foundTitle {
		t.Errorf("set-title missing: %v", calls)
	}

	// Menu shows Playing + title + controller-less volume row. The render is
	// delivered by the subscription worker, so wait for the model.
	eventually(t, "model shows playing", func() bool {
		m := h.view.last()
		return m.Transport == "Playing" && m.PlayPause.Title == "Pause" && m.PlayPause.Enabled
	})
	m := h.view.last()
	if m.Title != "Movie" {
		t.Errorf("Title = %q, want Movie", m.Title)
	}

	// Pause flips to PAUSED_PLAYBACK.
	h.ctl.doAction(ActionPlayPause)
	eventually(t, "PAUSED_PLAYBACK", func() bool { return st.GetTransportState() == "PAUSED_PLAYBACK" })
	eventually(t, "model shows paused", func() bool { return h.view.last().PlayPause.Title == "Resume" })
	paused := false
	for _, c := range h.fp.recorded() {
		if c == "pause" {
			paused = true
		}
	}
	if !paused {
		t.Error("player Pause not called")
	}

	// Resume replays the same URI (IINAPlayer resumes same-URI playback).
	h.ctl.doAction(ActionPlayPause)
	eventually(t, "PLAYING again", func() bool { return st.GetTransportState() == "PLAYING" })

	// Stop tears the player down and clears the transport.
	h.ctl.doAction(ActionStop)
	eventually(t, "STOPPED", func() bool { return st.GetTransportState() == "STOPPED" })
	stopped := false
	for _, c := range h.fp.recorded() {
		if c == "stop" {
			stopped = true
		}
	}
	if !stopped {
		t.Error("player Stop not called")
	}
}

func TestControllerMuteMirrorsRenderingControl(t *testing.T) {
	h := newGUIHarness(t)
	defer h.close()

	h.ctl.doAction(ActionMute)
	eventually(t, "model shows muted", func() bool { return h.view.last().Muted })

	h.ctl.doAction(ActionMute)
	eventually(t, "model shows unmuted", func() bool { return !h.view.last().Muted })

	// With the system link enabled (restart applies it to the new runtime),
	// the system sink mirrors menu mutes.
	h.ctl.doToggle(ToggleLinkVolume) // idle: restarts the server now
	eventually(t, "restart with link volume", func() bool { return h.starts.Load() == 2 })
	st := h.state() // the restarted runtime owns fresh state

	h.ctl.doAction(ActionMute)
	eventually(t, "muted again", func() bool { return st.GetMute() })
	h.systemMux.Lock()
	mutes := append([]bool(nil), h.systemMutes...)
	h.systemMux.Unlock()
	if len(mutes) == 0 || !mutes[len(mutes)-1] {
		t.Errorf("system mute sink not mirrored: %v", mutes)
	}
}

func TestControllerTogglesPersistAndApply(t *testing.T) {
	h := newGUIHarness(t)
	defer h.close()

	for _, tc := range []struct {
		toggle Toggle
		field  func(Settings) bool
	}{
		{ToggleFullscreen, func(s Settings) bool { return s.IINAFullscreen }},
		{ToggleLinkVolume, func(s Settings) bool { return s.LinkSystemVolume }},
		{ToggleAllowPreempt, func(s Settings) bool { return s.AllowPreempt }},
		{ToggleDebug, func(s Settings) bool { return s.Debug }},
	} {
		h.ctl.doToggle(tc.toggle)
		h.saveMu.Lock()
		n := len(h.saved)
		h.saveMu.Unlock()
		if n == 0 || !tc.field(h.saved[n-1]) {
			t.Errorf("%s not persisted enabled: %+v", tc.toggle, h.saved)
		}
	}

	// Every toggle is reflected in rendered models.
	m := h.view.last()
	if len(m.Toggles) != 4 {
		t.Fatalf("toggles = %d", len(m.Toggles))
	}
	for _, row := range m.Toggles {
		if !row.Checked {
			t.Errorf("toggle %s not checked in model", row.ID)
		}
	}

	// Fullscreen and debug toggles never restart the server.
	if h.starts.Load() != 3 { // initial + link volume + preempt restarts
		t.Errorf("starts = %d, want 3", h.starts.Load())
	}
}

func TestControllerRestartDeferredWhilePlaying(t *testing.T) {
	h := newGUIHarness(t)
	defer h.close()
	st := h.state()

	st.SetURI("http://example.com/x.mp4", "")
	h.ctl.doAction(ActionPlayPause)
	eventually(t, "PLAYING", func() bool { return st.GetTransportState() == "PLAYING" })

	// Toggle while playing: persisted immediately, restart deferred.
	h.ctl.doToggle(ToggleAllowPreempt)
	time.Sleep(150 * time.Millisecond)
	if h.starts.Load() != 1 {
		t.Fatalf("server restarted during playback (starts=%d)", h.starts.Load())
	}
	h.saveMu.Lock()
	if len(h.saved) == 0 || !h.saved[len(h.saved)-1].AllowPreempt {
		h.saveMu.Unlock()
		t.Fatal("preempt toggle not persisted")
	}
	h.saveMu.Unlock()

	// Playback ends: the deferred restart fires.
	h.ctl.doAction(ActionStop)
	eventually(t, "deferred restart", func() bool { return h.starts.Load() == 2 })

	// The restarted runtime still exposes live state.
	h.ctl.mu.Lock()
	rt := h.ctl.rt
	h.ctl.mu.Unlock()
	if rt == nil || rt.State() == nil {
		t.Fatal("runtime missing after restart")
	}
}

func TestControllerQuitAndShutdown(t *testing.T) {
	h := newGUIHarness(t)
	defer h.close()

	h.ctl.doAction(ActionQuit)
	if h.view.quitCount() != 1 {
		t.Fatalf("quit calls = %d, want 1", h.view.quitCount())
	}
	// Quit is idempotent at the view level.
	h.ctl.requestQuit()
	if h.view.quitCount() != 1 {
		t.Fatalf("quit calls after repeat = %d, want 1", h.view.quitCount())
	}

	err := h.ctl.shutdown()
	if err != nil {
		t.Fatalf("shutdown error: %v", err)
	}
}

func TestControllerDefaultSaveSettingsPersists(t *testing.T) {
	dir := t.TempDir()
	settingsPath := filepath.Join(dir, "settings.json")
	uuidPath := filepath.Join(dir, "u.txt")

	// No SaveSettings dep: the controller falls back to config.Save writing
	// the settings.json selected by the base config.
	ctl := &controller{
		ctx:    context.Background(),
		deps:   Deps{},
		view:   &recordingView{},
		baseCf: config.Config{UUIDPath: uuidPath, SettingsPath: settingsPath},
	}
	if err := ctl.saveSettings(Settings{IINAFullscreen: true, Debug: true}); err != nil {
		t.Fatalf("saveSettings: %v", err)
	}
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("read settings.json: %v", err)
	}
	for _, want := range []string{`"iinaFullscreen": true`, `"debugLog": true`, `"linkSystemVolume": false`} {
		if !strings.Contains(string(data), want) {
			t.Errorf("settings.json missing %s: %s", want, data)
		}
	}
}

func TestControllerPlayMuteFailureMirrorsUPnPSemantics(t *testing.T) {
	h := newGUIHarness(t)
	defer h.close()
	st := h.state()

	// Mute state on and a player that refuses to mute: the GUI Play path
	// must mirror the UPnP Play action (tear the player down, transport
	// back to STOPPED) instead of lingering on TRANSITIONING.
	h.fp.mu.Lock()
	h.fp.failMute = true
	h.fp.mu.Unlock()
	st.SetURI("http://example.com/movie.mp4", "")
	st.SetMute(true)

	h.ctl.doAction(ActionPlayPause)

	eventually(t, "STOPPED after mute failure", func() bool {
		return st.GetTransportState() == "STOPPED"
	})
	foundTeardown := false
	for _, call := range h.fp.recorded() {
		if call == "stop" {
			foundTeardown = true
		}
	}
	if !foundTeardown {
		t.Errorf("player not torn down after mute failure: %v", h.fp.recorded())
	}
	eventually(t, "menu leaves Loading", func() bool {
		m := h.view.last()
		return m.Transport == "Idle" && !m.CanStop
	})
}

func TestConcurrentRestartTriggersNoDoubleOrMissed(t *testing.T) {
	h := newGUIHarness(t)
	defer h.close()

	// Hammer the restart path from many goroutines while idle: the atomic
	// claim must prevent interleaved teardowns, and the settings must end
	// applied (pendingRestart drained).
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			h.ctl.scheduleRestart()
		}()
	}
	wg.Wait()

	eventually(t, "at least one restart", func() bool { return h.starts.Load() > 1 })

	// Terminal condition: the restart state drains, and no further starts
	// happen for a quiet window afterwards (a follow-up restart can only be
	// armed through pendingRestart, which is false by then). Restarts
	// include an SSDP drain window, so the budget is generous.
	drainDeadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(drainDeadline) {
		h.ctl.mu.Lock()
		drained := !h.ctl.pendingRestart && !h.ctl.restarting
		h.ctl.mu.Unlock()
		if drained {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	h.ctl.mu.Lock()
	pending, restarting := h.ctl.pendingRestart, h.ctl.restarting
	h.ctl.mu.Unlock()
	if pending || restarting {
		t.Fatalf("restart state not drained after 15s: pending=%v restarting=%v starts=%d", pending, restarting, h.starts.Load())
	}
	stableSince := time.Now()
	last := h.starts.Load()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
		if now := h.starts.Load(); now != last {
			last = now
			stableSince = time.Now()
			continue
		}
		if time.Since(stableSince) >= 500*time.Millisecond {
			break
		}
	}
	if time.Since(stableSince) < 500*time.Millisecond {
		t.Fatalf("restarts never quiesced (starts=%d)", h.starts.Load())
	}
	if n := h.starts.Load(); n > 11 {
		t.Errorf("restart storm: starts=%d", n)
	}
	h.ctl.mu.Lock()
	rt := h.ctl.rt
	h.ctl.mu.Unlock()
	if rt == nil || rt.State() == nil {
		t.Fatal("runtime missing after concurrent restarts")
	}
}

func TestRequestQuitUnsubscribesBeforeNativeQuit(t *testing.T) {
	h := newGUIHarness(t)
	defer h.close()

	// Record the ordering F5 mandates: unsubscribe, then native quit. The
	// unsubscribe wrap is guarded by the controller mutex; the quit hook by
	// the view's own mutex (the view reference itself is immutable).
	var mu sync.Mutex
	var order []string
	h.ctl.mu.Lock()
	origUnsub := h.ctl.stopSub
	h.ctl.stopSub = func() {
		mu.Lock()
		order = append(order, "unsubscribe")
		mu.Unlock()
		origUnsub()
	}
	h.ctl.mu.Unlock()
	h.view.setOnQuit(func() {
		mu.Lock()
		order = append(order, "quit")
		mu.Unlock()
	})

	h.ctl.doAction(ActionQuit)

	mu.Lock()
	defer mu.Unlock()
	if len(order) < 2 || order[0] != "unsubscribe" || order[1] != "quit" {
		t.Fatalf("quit order = %v, want [unsubscribe quit]", order)
	}
}
