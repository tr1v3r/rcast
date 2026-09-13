package upnp

import (
	"testing"

	"github.com/tr1v3r/rcast/internal/config"
	"github.com/tr1v3r/rcast/internal/monitoring"
	"github.com/tr1v3r/rcast/internal/player"
)

// Audit M1 regressions: RenderingControl actions are independent of the
// AVTransport session. A second control point adjusting volume or mute while
// another controller is playing must neither preempt the session, stop (or
// restart) playback, nor be refused — regardless of AllowSessionPreempt.

func TestSetVolume_SecondControllerKeepsPlaybackAlive(t *testing.T) {
	fake := newFakePlayer()
	st, cleanup := newRCState(t, func() player.Player { return fake })
	defer cleanup()
	avt := AVTransportHandler(st, config.Config{AllowSessionPreempt: true})
	rcs := RenderingControlHandler(st, config.Config{AllowSessionPreempt: true})
	const a = "10.0.0.1:1000"
	const b = "10.0.0.2:2000"

	// Controller A is playing.
	setupAVT(t, st, avt, a, "https://example.test/video.mp4")
	if st.GetTransportState() != "PLAYING" {
		t.Fatalf("prerequisite: state=%q, want PLAYING", st.GetTransportState())
	}

	fake.mu.Lock()
	plays, stops, playbackStops := fake.plays, fake.stops, fake.playbackStops
	fake.mu.Unlock()

	// Controller B adjusts volume while A plays.
	rec := serveAction(rcs, "SetVolume", soapBody(`<DesiredVolume>30</DesiredVolume>`), b)
	assertSOAPSuccess(t, rec, "SetVolumeResponse")

	fake.mu.Lock()
	gotPlays, gotStops, gotPlaybackStops := fake.plays, fake.stops, fake.playbackStops
	fake.mu.Unlock()
	if gotPlays != plays {
		t.Fatalf("plays=%d, want %d (volume must not restart playback)", gotPlays, plays)
	}
	if gotStops != stops || gotPlaybackStops != playbackStops {
		t.Fatalf("stops=%d playbackStops=%d, want %d/%d (volume must not stop playback)", gotStops, gotPlaybackStops, stops, playbackStops)
	}
	if owner := st.GetSessionOwner(); owner != "10.0.0.1" {
		t.Fatalf("owner=%q, want 10.0.0.1 (volume must not preempt the session)", owner)
	}
	if st.GetTransportState() != "PLAYING" {
		t.Fatalf("state=%q, want PLAYING (volume must not change transport state)", st.GetTransportState())
	}
	if got := st.GetVolume(); got != 30 {
		t.Fatalf("volume=%d, want 30 (second controller's volume applied)", got)
	}

	// The volume change reached the (still alive) player owned by A's session.
	fake.mu.Lock()
	vols := append([]int(nil), fake.volumes...)
	fake.mu.Unlock()
	if len(vols) != 1 || vols[0] != 30 {
		t.Fatalf("player volumes=%v, want [30]", vols)
	}

	// A still owns the transport afterwards.
	rec = serveAction(avt, "Pause", soapBody(``), a)
	assertSOAPSuccess(t, rec, "PauseResponse")
}

func TestSetMute_SecondControllerKeepsPlaybackAlive(t *testing.T) {
	fake := newFakePlayer()
	st, cleanup := newRCState(t, func() player.Player { return fake })
	defer cleanup()
	avt := AVTransportHandler(st, config.Config{AllowSessionPreempt: true})
	rcs := RenderingControlHandler(st, config.Config{AllowSessionPreempt: true})
	const a = "10.0.0.1:1000"

	setupAVT(t, st, avt, a, "https://example.test/video.mp4")

	fake.mu.Lock()
	stops, playbackStops := fake.stops, fake.playbackStops
	fake.mu.Unlock()

	// Controller B mutes while A plays.
	rec := serveAction(rcs, "SetMute", soapBody(`<DesiredMute>1</DesiredMute>`), "10.0.0.2:2000")
	assertSOAPSuccess(t, rec, "SetMuteResponse")

	fake.mu.Lock()
	gotStops, gotPlaybackStops := fake.stops, fake.playbackStops
	fake.mu.Unlock()
	if gotStops != stops || gotPlaybackStops != playbackStops {
		t.Fatalf("stops=%d playbackStops=%d, want %d/%d (mute must not stop playback)", gotStops, gotPlaybackStops, stops, playbackStops)
	}
	if owner := st.GetSessionOwner(); owner != "10.0.0.1" {
		t.Fatalf("owner=%q, want 10.0.0.1 (mute must not preempt the session)", owner)
	}
	if st.GetTransportState() != "PLAYING" {
		t.Fatalf("state=%q, want PLAYING", st.GetTransportState())
	}
	if !st.GetMute() {
		t.Fatal("mute not applied")
	}
}

func TestRCS_PreemptDisabledStillAllowsSecondController(t *testing.T) {
	fake := newFakePlayer()
	st, cleanup := newRCState(t, func() player.Player { return fake })
	defer cleanup()
	// Preemption disabled: AVT actions from B would be refused with 800, but
	// volume/mute must still work for any controller.
	avt := AVTransportHandler(st, config.Config{AllowSessionPreempt: false})
	rcs := RenderingControlHandler(st, config.Config{AllowSessionPreempt: false})
	const a = "10.0.0.1:1000"
	const b = "10.0.0.2:2000"

	setupAVT(t, st, avt, a, "https://example.test/video.mp4")

	rec := serveAction(rcs, "SetVolume", soapBody(`<DesiredVolume>11</DesiredVolume>`), b)
	assertSOAPSuccess(t, rec, "SetVolumeResponse")
	rec = serveAction(rcs, "SetMute", soapBody(`<DesiredMute>0</DesiredMute>`), b)
	assertSOAPSuccess(t, rec, "SetMuteResponse")

	if owner := st.GetSessionOwner(); owner != "10.0.0.1" {
		t.Fatalf("owner=%q, want 10.0.0.1", owner)
	}
	if st.GetTransportState() != "PLAYING" {
		t.Fatalf("state=%q, want PLAYING", st.GetTransportState())
	}
	if got := st.GetVolume(); got != 11 {
		t.Fatalf("volume=%d, want 11", got)
	}
}

// TestRCS_MetricsRecorded verifies RenderingControl actions and errors feed
// the UPnP counters (audit LOW: RCS/CM metrics missing).
func TestRCS_MetricsRecorded(t *testing.T) {
	st, cleanup := newRCState(t, nil)
	defer cleanup()
	m := monitoring.GetMetrics()
	actionsBefore, errorsBefore := m.UPnPActionsTotal, m.UPnPErrorsTotal
	rcs := RenderingControlHandler(st, config.Config{})

	serveAction(rcs, "SetVolume", soapBody(`<DesiredVolume>42</DesiredVolume>`), "10.0.0.1:1")
	if m.UPnPActionsTotal != actionsBefore+1 {
		t.Fatalf("upnp actions=%d, want %d after one SetVolume", m.UPnPActionsTotal, actionsBefore+1)
	}

	serveAction(rcs, "SetVolume", soapBody(`<DesiredVolume>loud</DesiredVolume>`), "10.0.0.1:1")
	if m.UPnPErrorsTotal != errorsBefore+1 {
		t.Fatalf("upnp errors=%d, want %d after invalid SetVolume", m.UPnPErrorsTotal, errorsBefore+1)
	}

	serveAction(rcs, "GetVolume", soapBody(``), "10.0.0.1:1")
	// Three requests reached the handler (including the invalid one, which is
	// counted as an action before argument validation fails).
	if m.UPnPActionsTotal != actionsBefore+3 {
		t.Fatalf("upnp actions=%d, want %d after GetVolume", m.UPnPActionsTotal, actionsBefore+3)
	}
}
