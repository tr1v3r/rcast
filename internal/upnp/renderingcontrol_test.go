package upnp

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/tr1v3r/rcast/internal/config"
	"github.com/tr1v3r/rcast/internal/player"
	"github.com/tr1v3r/rcast/internal/state"
)

// overrideSystemSinks replaces the host-volume sinks with no-op stubs and
// restores the originals (and LinkSystemOutputVolume default) on cleanup.
// RenderingControl tests that touch cfg.LinkSystemOutputVolume MUST call this so
// the host's real volume is never altered.
func overrideSystemSinks(t *testing.T) (volumeCalls *[]int, muteCalls *[]bool) {
	t.Helper()
	origVol := systemVolumeSink
	origMute := systemMuteSink
	var vols []int
	var mutes []bool
	systemVolumeSink = func(v int) error {
		vols = append(vols, v)
		return nil
	}
	systemMuteSink = func(m bool) error {
		mutes = append(mutes, m)
		return nil
	}
	t.Cleanup(func() {
		systemVolumeSink = origVol
		systemMuteSink = origMute
	})
	return &vols, &mutes
}

func newRCState(t *testing.T, factory state.PlayerFactory) (*state.PlayerState, func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	if factory == nil {
		factory = func() player.Player { return newFakePlayer() }
	}
	st := state.NewWithPlayerFactory(ctx, config.Config{}, factory)
	return st, func() {
		cancel()
		st.Stop()
	}
}

func TestSetVolume_NonNumeric(t *testing.T) {
	st, cleanup := newRCState(t, nil)
	defer cleanup()
	rec := serveAction(RenderingControlHandler(st, config.Config{}), "SetVolume", soapBody(`<DesiredVolume>loud</DesiredVolume>`), "10.0.0.1:1")
	assertUPnPError(t, rec, 402)
}

func TestSetVolume_NegativeClampedToZero(t *testing.T) {
	st, cleanup := newRCState(t, nil)
	defer cleanup()
	rec := serveAction(RenderingControlHandler(st, config.Config{}), "SetVolume", soapBody(`<DesiredVolume>-5</DesiredVolume>`), "10.0.0.1:1")
	assertSOAPSuccess(t, rec, "SetVolumeResponse")
	if got := st.GetVolume(); got != 0 {
		t.Fatalf("volume=%d, want 0 (clamped)", got)
	}
}

func TestSetVolume_Over100ClampedTo100(t *testing.T) {
	st, cleanup := newRCState(t, nil)
	defer cleanup()
	rec := serveAction(RenderingControlHandler(st, config.Config{}), "SetVolume", soapBody(`<DesiredVolume>250</DesiredVolume>`), "10.0.0.1:1")
	assertSOAPSuccess(t, rec, "SetVolumeResponse")
	if got := st.GetVolume(); got != 100 {
		t.Fatalf("volume=%d, want 100 (clamped)", got)
	}
}

func TestSetVolume_NormalClientUpdatesState(t *testing.T) {
	st, cleanup := newRCState(t, nil)
	defer cleanup()
	rec := serveAction(RenderingControlHandler(st, config.Config{}), "SetVolume", soapBody(`<DesiredVolume>42</DesiredVolume>`), "10.0.0.1:1")
	assertSOAPSuccess(t, rec, "SetVolumeResponse")
	if got := st.GetVolume(); got != 42 {
		t.Fatalf("volume=%d, want 42", got)
	}
}

func TestSetVolume_WithActivePlayerCallsSetVolume(t *testing.T) {
	fake := newFakePlayer()
	st, cleanup := newRCState(t, func() player.Player { return fake })
	defer cleanup()
	st.EnsurePlayer()

	rec := serveAction(RenderingControlHandler(st, config.Config{}), "SetVolume", soapBody(`<DesiredVolume>77</DesiredVolume>`), "10.0.0.1:1")
	assertSOAPSuccess(t, rec, "SetVolumeResponse")

	fake.mu.Lock()
	vols := append([]int(nil), fake.volumes...)
	fake.mu.Unlock()
	if len(vols) != 1 || vols[0] != 77 {
		t.Fatalf("player volumes=%v, want [77]", vols)
	}
}

func TestSetVolume_PlayerFailureAbortsCommit(t *testing.T) {
	fake := newFakePlayer()
	fake.errs["SetVolume"] = errors.New("ipc down")
	st, cleanup := newRCState(t, func() player.Player { return fake })
	defer cleanup()
	st.EnsurePlayer()
	// Establish a baseline volume different from the request so we can verify
	// the state is NOT committed on failure.
	st.SetVolume(20)

	rec := serveAction(RenderingControlHandler(st, config.Config{}), "SetVolume", soapBody(`<DesiredVolume>90</DesiredVolume>`), "10.0.0.1:1")
	assertUPnPError(t, rec, 501)
	if got := st.GetVolume(); got != 20 {
		t.Fatalf("volume=%d, want 20 (commit must not happen on failure)", got)
	}
}

func TestSetVolume_NoPlayerStillUpdatesState(t *testing.T) {
	st, cleanup := newRCState(t, nil)
	defer cleanup()
	rec := serveAction(RenderingControlHandler(st, config.Config{}), "SetVolume", soapBody(`<DesiredVolume>65</DesiredVolume>`), "10.0.0.1:1")
	assertSOAPSuccess(t, rec, "SetVolumeResponse")
	if got := st.GetVolume(); got != 65 {
		t.Fatalf("volume=%d, want 65 (state updates without a player)", got)
	}
}

func TestSetVolume_AnotherControllerOwnsSessionPreemptDisabled(t *testing.T) {
	st, cleanup := newRCState(t, nil)
	defer cleanup()
	cfg := config.Config{AllowSessionPreempt: false}
	handler := RenderingControlHandler(st, cfg)

	// First controller acquires the session and sets volume.
	if rec := serveAction(handler, "SetVolume", soapBody(`<DesiredVolume>30</DesiredVolume>`), "10.0.0.1:1"); rec.Code != http.StatusOK {
		t.Fatalf("first SetVolume status=%d body=%s", rec.Code, rec.Body.String())
	}
	// Second controller refused.
	rec := serveAction(handler, "SetVolume", soapBody(`<DesiredVolume>80</DesiredVolume>`), "10.0.0.2:1")
	assertUPnPError(t, rec, 712)
	if got := st.GetVolume(); got != 30 {
		t.Fatalf("volume=%d, want 30 (unchanged by refused request)", got)
	}
}

func TestSetVolume_ControllerSwitchResetsVolumeMapping(t *testing.T) {
	st, cleanup := newRCState(t, nil)
	defer cleanup()
	// Preemption is required for a different controller to take over the
	// session; without it the second SetVolume would be refused with 712.
	cfg := config.Config{AllowSessionPreempt: true}
	handler := RenderingControlHandler(st, cfg)
	const awemeUA = "Aweme/390012 CFNetwork/3860.300.31 Darwin/25.2.0"

	// Aweme iOS controller sets a volume that establishes a compatibility mapping.
	if rec := serveActionWithUserAgent(handler, "SetVolume", soapBody(`<DesiredVolume>60</DesiredVolume>`), "10.0.0.1:1", awemeUA); rec.Code != http.StatusOK {
		t.Fatalf("aweme SetVolume status=%d body=%s", rec.Code, rec.Body.String())
	}
	// The mapping for controller 1 must report back the raw value.
	if got := st.GetReportedVolume("10.0.0.1", awemeIOSVolumeScale); got != 60 {
		t.Fatalf("aweme reported=%d, want 60", got)
	}
	// A different controller takes over (preempt); the mapping must reset so its
	// raw request maps 1:1 to the applied value.
	if rec := serveAction(handler, "SetVolume", soapBody(`<DesiredVolume>50</DesiredVolume>`), "10.0.0.2:1"); rec.Code != http.StatusOK {
		t.Fatalf("second SetVolume status=%d body=%s", rec.Code, rec.Body.String())
	}
	if got := st.GetVolume(); got != 50 {
		t.Fatalf("volume after controller switch=%d, want 50", got)
	}
	// New controller has no special mapping.
	if got := st.GetReportedVolume("10.0.0.2", awemeIOSVolumeScale); got != 50 {
		t.Fatalf("controller 2 reported=%d, want 50", got)
	}
}

func TestGetVolume_ReflectsState(t *testing.T) {
	st, cleanup := newRCState(t, nil)
	defer cleanup()
	st.SetVolume(88)
	rec := serveAction(RenderingControlHandler(st, config.Config{}), "GetVolume", soapBody(``), "10.0.0.1:1")
	assertSOAPSuccess(t, rec, "GetVolumeResponse")
	if !strings.Contains(rec.Body.String(), "<CurrentVolume>88</CurrentVolume>") {
		t.Fatalf("body=%s", rec.Body.String())
	}
}

func TestSetMute_AcceptsValidValues(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want bool
	}{
		{"zero", "0", false},
		{"one", "1", true},
		{"false_lower", "false", false},
		{"true_lower", "true", true},
		{"FALSE_upper", "FALSE", false},
		{"TRUE_upper", "TRUE", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st, cleanup := newRCState(t, nil)
			defer cleanup()
			rec := serveAction(RenderingControlHandler(st, config.Config{}), "SetMute", soapBody(`<DesiredMute>`+tt.in+`</DesiredMute>`), "10.0.0.1:1")
			assertSOAPSuccess(t, rec, "SetMuteResponse")
			if got := st.GetMute(); got != tt.want {
				t.Fatalf("mute=%v, want %v", got, tt.want)
			}
		})
	}
}

func TestSetMute_InvalidValue(t *testing.T) {
	st, cleanup := newRCState(t, nil)
	defer cleanup()
	rec := serveAction(RenderingControlHandler(st, config.Config{}), "SetMute", soapBody(`<DesiredMute>yes</DesiredMute>`), "10.0.0.1:1")
	assertUPnPError(t, rec, 402)
}

func TestSetMute_WithActivePlayerCallsSetMute(t *testing.T) {
	fake := newFakePlayer()
	st, cleanup := newRCState(t, func() player.Player { return fake })
	defer cleanup()
	st.EnsurePlayer()

	rec := serveAction(RenderingControlHandler(st, config.Config{}), "SetMute", soapBody(`<DesiredMute>true</DesiredMute>`), "10.0.0.1:1")
	assertSOAPSuccess(t, rec, "SetMuteResponse")

	fake.mu.Lock()
	mutes := append([]bool(nil), fake.mutes...)
	fake.mu.Unlock()
	if len(mutes) != 1 || !mutes[0] {
		t.Fatalf("player mutes=%v, want [true]", mutes)
	}
}

func TestSetMute_PlayerFailureAbortsStateChange(t *testing.T) {
	fake := newFakePlayer()
	fake.errs["SetMute"] = errors.New("ipc down")
	st, cleanup := newRCState(t, func() player.Player { return fake })
	defer cleanup()
	st.EnsurePlayer()
	// Baseline: mute is false.
	rec := serveAction(RenderingControlHandler(st, config.Config{}), "SetMute", soapBody(`<DesiredMute>true</DesiredMute>`), "10.0.0.1:1")
	assertUPnPError(t, rec, 501)
	if got := st.GetMute(); got {
		t.Fatalf("mute=%v, want false (state must not change on player failure)", got)
	}
}

func TestGetMute_ReflectsState(t *testing.T) {
	st, cleanup := newRCState(t, nil)
	defer cleanup()
	st.SetMute(true)
	rec := serveAction(RenderingControlHandler(st, config.Config{}), "GetMute", soapBody(``), "10.0.0.1:1")
	assertSOAPSuccess(t, rec, "GetMuteResponse")
	if !strings.Contains(rec.Body.String(), "<CurrentMute>1</CurrentMute>") {
		t.Fatalf("body=%s", rec.Body.String())
	}

	st.SetMute(false)
	rec = serveAction(RenderingControlHandler(st, config.Config{}), "GetMute", soapBody(``), "10.0.0.1:1")
	if !strings.Contains(rec.Body.String(), "<CurrentMute>0</CurrentMute>") {
		t.Fatalf("body=%s", rec.Body.String())
	}
}

func TestLinkSystemOutputVolume_SetVolumeSuccess(t *testing.T) {
	volCalls, _ := overrideSystemSinks(t)
	st, cleanup := newRCState(t, nil)
	defer cleanup()
	cfg := config.Config{LinkSystemOutputVolume: true}
	handler := RenderingControlHandler(st, cfg)

	rec := serveAction(handler, "SetVolume", soapBody(`<DesiredVolume>55</DesiredVolume>`), "10.0.0.1:1")
	assertSOAPSuccess(t, rec, "SetVolumeResponse")
	// Sink called with applied volume.
	if len(*volCalls) != 1 || (*volCalls)[0] != 55 {
		t.Fatalf("system volume calls=%v, want [55]", *volCalls)
	}
	// State committed.
	if got := st.GetVolume(); got != 55 {
		t.Fatalf("volume=%d, want 55", got)
	}
}

func TestLinkSystemOutputVolume_SetVolumeSinkErrorIsWarnOnly(t *testing.T) {
	origVol := systemVolumeSink
	origMute := systemMuteSink
	t.Cleanup(func() {
		systemVolumeSink = origVol
		systemMuteSink = origMute
	})
	systemVolumeSink = func(int) error { return errors.New("osascript missing") }
	systemMuteSink = func(bool) error { return nil }

	st, cleanup := newRCState(t, nil)
	defer cleanup()
	cfg := config.Config{LinkSystemOutputVolume: true}
	handler := RenderingControlHandler(st, cfg)

	// Sink error must NOT fail the SOAP response — it is warn-only.
	rec := serveAction(handler, "SetVolume", soapBody(`<DesiredVolume>60</DesiredVolume>`), "10.0.0.1:1")
	assertSOAPSuccess(t, rec, "SetVolumeResponse")
	// State still committed.
	if got := st.GetVolume(); got != 60 {
		t.Fatalf("volume=%d, want 60 (committed despite sink error)", got)
	}
}

func TestLinkSystemOutputVolume_SetMuteSuccess(t *testing.T) {
	_, muteCalls := overrideSystemSinks(t)
	st, cleanup := newRCState(t, nil)
	defer cleanup()
	cfg := config.Config{LinkSystemOutputVolume: true}
	handler := RenderingControlHandler(st, cfg)

	rec := serveAction(handler, "SetMute", soapBody(`<DesiredMute>true</DesiredMute>`), "10.0.0.1:1")
	assertSOAPSuccess(t, rec, "SetMuteResponse")
	if len(*muteCalls) != 1 || !(*muteCalls)[0] {
		t.Fatalf("system mute calls=%v, want [true]", *muteCalls)
	}
	if got := st.GetMute(); !got {
		t.Fatalf("mute=%v, want true", got)
	}
}

func TestLinkSystemOutputVolume_SetMuteSinkErrorIsWarnOnly(t *testing.T) {
	origVol := systemVolumeSink
	origMute := systemMuteSink
	t.Cleanup(func() {
		systemVolumeSink = origVol
		systemMuteSink = origMute
	})
	systemVolumeSink = func(int) error { return nil }
	systemMuteSink = func(bool) error { return errors.New("osascript missing") }

	st, cleanup := newRCState(t, nil)
	defer cleanup()
	cfg := config.Config{LinkSystemOutputVolume: true}
	handler := RenderingControlHandler(st, cfg)

	rec := serveAction(handler, "SetMute", soapBody(`<DesiredMute>1</DesiredMute>`), "10.0.0.1:1")
	assertSOAPSuccess(t, rec, "SetMuteResponse")
	// State still committed.
	if got := st.GetMute(); !got {
		t.Fatalf("mute=%v, want true (committed despite sink error)", got)
	}
}

func TestRenderingControl_UnknownAction(t *testing.T) {
	st, cleanup := newRCState(t, nil)
	defer cleanup()
	rec := serveAction(RenderingControlHandler(st, config.Config{}), "BogusAction", soapBody(``), "10.0.0.1:1")
	assertUPnPError(t, rec, 401)
}
