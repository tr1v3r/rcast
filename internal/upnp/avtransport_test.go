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

// newAVTState builds a fresh PlayerState wired to the supplied spy factory.
func newAVTState(t *testing.T, factory state.PlayerFactory) (*state.PlayerState, func()) {
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

// setupAVT sets the URI from one controller then issues Play, leaving the spy as
// the active player for follow-up actions.
func setupAVT(t *testing.T, st *state.PlayerState, handler http.Handler, remote, uri string) {
	t.Helper()
	if rec := serveAction(handler, "SetAVTransportURI", soapBody(`<CurrentURI>`+uri+`</CurrentURI>`), remote); rec.Code != http.StatusOK {
		t.Fatalf("setup SetURI status=%d body=%s", rec.Code, rec.Body.String())
	}
	if rec := serveAction(handler, "Play", soapBody(`<Speed>1</Speed>`), remote); rec.Code != http.StatusOK {
		t.Fatalf("setup Play status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestSetAVTransportURI_EmptyURI(t *testing.T) {
	st, cleanup := newAVTState(t, nil)
	defer cleanup()
	rec := serveAction(AVTransportHandler(st, config.Config{}), "SetAVTransportURI", soapBody(`<CurrentURI></CurrentURI>`), "10.0.0.1:1")
	assertUPnPError(t, rec, 402)
}

func TestSetAVTransportURI_FirstSetOK(t *testing.T) {
	st, cleanup := newAVTState(t, nil)
	defer cleanup()
	rec := serveAction(AVTransportHandler(st, config.Config{}), "SetAVTransportURI", soapBody(`<CurrentURI>https://example.test/v.mp4</CurrentURI>`), "10.0.0.1:1")
	assertSOAPSuccess(t, rec, "SetAVTransportURIResponse")
	uri, _ := st.GetURI()
	if uri != "https://example.test/v.mp4" {
		t.Fatalf("uri=%q", uri)
	}
}

func TestSetAVTransportURI_URISwitchFallsBackToStopPlayerWhenStopPlaybackFails(t *testing.T) {
	fake := newFakePlayer()
	fake.errs["StopPlayback"] = errors.New("ipc busy")
	st, cleanup := newAVTState(t, func() player.Player { return fake })
	defer cleanup()
	handler := AVTransportHandler(st, config.Config{})
	const remote = "10.0.0.1:1"

	setupAVT(t, st, handler, remote, "https://example.test/one.mp4")
	// Reset spy counters so we only assert against the second SetURI.
	fake.playbackStops = 0
	fake.stops = 0

	rec := serveAction(handler, "SetAVTransportURI", soapBody(`<CurrentURI>https://example.test/two.mp4</CurrentURI>`), remote)
	assertSOAPSuccess(t, rec, "SetAVTransportURIResponse")
	if fake.playbackStops != 1 {
		t.Fatalf("StopPlayback calls=%d, want 1", fake.playbackStops)
	}
	// StopPlayback failed → handler falls back to StopPlayer which calls Stop.
	if fake.stops != 1 {
		t.Fatalf("Stop calls=%d, want 1 (fallback from failed StopPlayback)", fake.stops)
	}
}

func TestSetAVTransportURI_StopPlaybackAndStopPlayerBothFail(t *testing.T) {
	fake := newFakePlayer()
	fake.errs["StopPlayback"] = errors.New("ipc busy")
	fake.errs["Stop"] = errors.New("process gone")
	st, cleanup := newAVTState(t, func() player.Player { return fake })
	defer cleanup()
	handler := AVTransportHandler(st, config.Config{})
	const remote = "10.0.0.1:1"

	setupAVT(t, st, handler, remote, "https://example.test/one.mp4")
	rec := serveAction(handler, "SetAVTransportURI", soapBody(`<CurrentURI>https://example.test/two.mp4</CurrentURI>`), remote)
	assertUPnPError(t, rec, 501)
}

func TestSetAVTransportURI_SessionHeldByOtherControllerPreemptDisabled(t *testing.T) {
	st, cleanup := newAVTState(t, nil)
	defer cleanup()
	cfg := config.Config{AllowSessionPreempt: false}
	handler := AVTransportHandler(st, cfg)

	// First controller acquires the session.
	if rec := serveAction(handler, "SetAVTransportURI", soapBody(`<CurrentURI>https://example.test/one.mp4</CurrentURI>`), "10.0.0.1:1"); rec.Code != http.StatusOK {
		t.Fatalf("first SetURI status=%d body=%s", rec.Code, rec.Body.String())
	}
	// Second controller should be refused (712).
	rec := serveAction(handler, "SetAVTransportURI", soapBody(`<CurrentURI>https://example.test/two.mp4</CurrentURI>`), "10.0.0.2:1")
	assertUPnPError(t, rec, 712)
	if owner := st.GetSessionOwner(); owner != "10.0.0.1" {
		t.Fatalf("owner=%q, want 10.0.0.1", owner)
	}
}

func TestSetAVTransportURI_PreemptEnabledDisplacesOwner(t *testing.T) {
	var players []*handlerFakePlayer
	st, cleanup := newAVTState(t, func() player.Player {
		p := newFakePlayer()
		players = append(players, p)
		return p
	})
	defer cleanup()
	cfg := config.Config{AllowSessionPreempt: true}
	handler := AVTransportHandler(st, cfg)

	setupAVT(t, st, handler, "10.0.0.1:1", "https://example.test/one.mp4")
	rec := serveAction(handler, "SetAVTransportURI", soapBody(`<CurrentURI>https://example.test/two.mp4</CurrentURI>`), "10.0.0.2:1")
	assertSOAPSuccess(t, rec, "SetAVTransportURIResponse")
	// Preemption stops the previous owner's player via StopPlayer (process Stop).
	if len(players) == 0 || players[0].stops != 1 {
		t.Fatalf("old player stops=%d (players=%d)", func() int {
			if len(players) == 0 {
				return -1
			}
			return players[0].stops
		}(), len(players))
	}
	if owner := st.GetSessionOwner(); owner != "10.0.0.2" {
		t.Fatalf("owner=%q, want 10.0.0.2", owner)
	}
}

func TestSetAVTransportURI_PreemptStopPlayerFailureFails(t *testing.T) {
	// When AllowSessionPreempt=true, controller A holds the session with an
	// active player whose Stop (the StopPlayer path) returns an error; when
	// controller B preempts via SetAVTransportURI, requireSession calls
	// st.StopPlayer() which fails, so requireSession must return false: the
	// handler responds with a 501 "Action Failed" SOAP error AND skips the
	// subsequent SetURI (the stored URI must still be controller A's).
	fake := newFakePlayer()
	fake.errs["Stop"] = errors.New("process gone")
	st, cleanup := newAVTState(t, func() player.Player { return fake })
	defer cleanup()
	cfg := config.Config{AllowSessionPreempt: true}
	handler := AVTransportHandler(st, cfg)

	// Controller A acquires the session and starts playback.
	setupAVT(t, st, handler, "10.0.0.1:1", "https://example.test/one.mp4")

	// Controller B preempts; StopPlayer fails on the old player.
	rec := serveAction(handler, "SetAVTransportURI", soapBody(`<CurrentURI>https://example.test/two.mp4</CurrentURI>`), "10.0.0.2:1")
	assertUPnPError(t, rec, 501)

	// Because requireSession returned false, the URI must NOT have been updated
	// to controller B's value — it must still be controller A's.
	if uri, _ := st.GetURI(); uri != "https://example.test/one.mp4" {
		t.Fatalf("uri=%q, want A's URI unchanged after preempt StopPlayer failure", uri)
	}
}

func TestPlay_NoURI(t *testing.T) {
	st, cleanup := newAVTState(t, nil)
	defer cleanup()
	// Acquire the session without setting a URI.
	serveAction(AVTransportHandler(st, config.Config{}), "SetAVTransportURI", soapBody(`<CurrentURI></CurrentURI>`), "10.0.0.1:1")
	rec := serveAction(AVTransportHandler(st, config.Config{}), "Play", soapBody(`<Speed>1</Speed>`), "10.0.0.1:1")
	assertUPnPError(t, rec, 714)
}

func TestPlay_Success(t *testing.T) {
	fake := newFakePlayer()
	st, cleanup := newAVTState(t, func() player.Player { return fake })
	defer cleanup()
	handler := AVTransportHandler(st, config.Config{})
	const remote = "10.0.0.1:1"

	serveAction(handler, "SetAVTransportURI", soapBody(`<CurrentURI>https://example.test/v.mp4</CurrentURI>`), remote)
	rec := serveAction(handler, "Play", soapBody(`<Speed>1</Speed>`), remote)
	assertSOAPSuccess(t, rec, "PlayResponse")
	if st.GetTransportState() != "PLAYING" {
		t.Fatalf("state=%q, want PLAYING", st.GetTransportState())
	}
	if fake.plays != 1 {
		t.Fatalf("plays=%d, want 1", fake.plays)
	}
}

func TestPlay_FailureSetsStateStopped(t *testing.T) {
	fake := newFakePlayer()
	fake.errs["Play"] = errors.New("iina missing")
	st, cleanup := newAVTState(t, func() player.Player { return fake })
	defer cleanup()
	handler := AVTransportHandler(st, config.Config{})
	const remote = "10.0.0.1:1"

	serveAction(handler, "SetAVTransportURI", soapBody(`<CurrentURI>https://example.test/v.mp4</CurrentURI>`), remote)
	rec := serveAction(handler, "Play", soapBody(`<Speed>1</Speed>`), remote)
	assertUPnPError(t, rec, 501)
	if st.GetTransportState() != "STOPPED" {
		t.Fatalf("state=%q, want STOPPED", st.GetTransportState())
	}
}

func TestPlay_InitialMuteAppliedToPlayer(t *testing.T) {
	fake := newFakePlayer()
	st, cleanup := newAVTState(t, func() player.Player { return fake })
	defer cleanup()
	handler := AVTransportHandler(st, config.Config{})
	const remote = "10.0.0.1:1"

	// Pre-mute via RenderingControl so Play's initial-mute branch runs.
	serveAction(RenderingControlHandler(st, config.Config{}), "SetMute", soapBody(`<DesiredMute>1</DesiredMute>`), remote)
	serveAction(handler, "SetAVTransportURI", soapBody(`<CurrentURI>https://example.test/v.mp4</CurrentURI>`), remote)
	rec := serveAction(handler, "Play", soapBody(`<Speed>1</Speed>`), remote)
	assertSOAPSuccess(t, rec, "PlayResponse")

	var mutes []bool
	fake.mu.Lock()
	mutes = append(mutes, fake.mutes...)
	fake.mu.Unlock()
	// First SetMute from Play's initial-mute path (true). The pre-mute via
	// RenderingControl ran before any player existed, so it never reached the spy.
	found := false
	for _, m := range mutes {
		if m {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("initial mute not applied to player; mutes=%v", mutes)
	}
}

func TestPlay_InitialMuteFailureStopsPlayer(t *testing.T) {
	fake := newFakePlayer()
	fake.errs["SetMute"] = errors.New("ipc down")
	st, cleanup := newAVTState(t, func() player.Player { return fake })
	defer cleanup()
	handler := AVTransportHandler(st, config.Config{})
	const remote = "10.0.0.1:1"

	serveAction(RenderingControlHandler(st, config.Config{}), "SetMute", soapBody(`<DesiredMute>1</DesiredMute>`), remote)
	serveAction(handler, "SetAVTransportURI", soapBody(`<CurrentURI>https://example.test/v.mp4</CurrentURI>`), remote)
	rec := serveAction(handler, "Play", soapBody(`<Speed>1</Speed>`), remote)
	assertUPnPError(t, rec, 501)
	if st.GetTransportState() != "STOPPED" {
		t.Fatalf("state=%q, want STOPPED", st.GetTransportState())
	}
	// Player was stopped.
	if fake.stops != 1 {
		t.Fatalf("stops=%d, want 1 (player stopped on initial-mute failure)", fake.stops)
	}
}

func TestPause_NoActivePlayer(t *testing.T) {
	st, cleanup := newAVTState(t, nil)
	defer cleanup()
	handler := AVTransportHandler(st, config.Config{})
	// Acquire session but never create a player.
	serveAction(handler, "SetAVTransportURI", soapBody(`<CurrentURI>https://example.test/v.mp4</CurrentURI>`), "10.0.0.1:1")
	rec := serveAction(handler, "Pause", soapBody(``), "10.0.0.1:1")
	assertUPnPError(t, rec, 701)
}

func TestPause_Success(t *testing.T) {
	fake := newFakePlayer()
	st, cleanup := newAVTState(t, func() player.Player { return fake })
	defer cleanup()
	handler := AVTransportHandler(st, config.Config{})
	const remote = "10.0.0.1:1"
	setupAVT(t, st, handler, remote, "https://example.test/v.mp4")

	rec := serveAction(handler, "Pause", soapBody(``), remote)
	assertSOAPSuccess(t, rec, "PauseResponse")
	if st.GetTransportState() != "PAUSED_PLAYBACK" {
		t.Fatalf("state=%q, want PAUSED_PLAYBACK", st.GetTransportState())
	}
}

func TestStop_IdempotentWhenNothingPlaying(t *testing.T) {
	st, cleanup := newAVTState(t, nil)
	defer cleanup()
	handler := AVTransportHandler(st, config.Config{})
	// Acquire the session, then Stop without ever creating a player.
	serveAction(handler, "SetAVTransportURI", soapBody(`<CurrentURI>https://example.test/v.mp4</CurrentURI>`), "10.0.0.1:1")
	rec := serveAction(handler, "Stop", soapBody(``), "10.0.0.1:1")
	assertSOAPSuccess(t, rec, "StopResponse")
	if st.GetTransportState() != "STOPPED" {
		t.Fatalf("state=%q, want STOPPED", st.GetTransportState())
	}
	if owner := st.GetSessionOwner(); owner != "" {
		t.Fatalf("owner=%q, want released (empty)", owner)
	}
}

func TestStop_SuccessReleasesSession(t *testing.T) {
	fake := newFakePlayer()
	st, cleanup := newAVTState(t, func() player.Player { return fake })
	defer cleanup()
	handler := AVTransportHandler(st, config.Config{})
	const remote = "10.0.0.1:1"
	setupAVT(t, st, handler, remote, "https://example.test/v.mp4")

	rec := serveAction(handler, "Stop", soapBody(``), remote)
	assertSOAPSuccess(t, rec, "StopResponse")
	if st.GetTransportState() != "STOPPED" {
		t.Fatalf("state=%q, want STOPPED", st.GetTransportState())
	}
	if owner := st.GetSessionOwner(); owner != "" {
		t.Fatalf("owner=%q, want empty after Stop", owner)
	}
}

func TestStop_PlayerFailure(t *testing.T) {
	fake := newFakePlayer()
	fake.errs["Stop"] = errors.New("process unresponsive")
	st, cleanup := newAVTState(t, func() player.Player { return fake })
	defer cleanup()
	handler := AVTransportHandler(st, config.Config{})
	const remote = "10.0.0.1:1"
	setupAVT(t, st, handler, remote, "https://example.test/v.mp4")

	rec := serveAction(handler, "Stop", soapBody(``), remote)
	assertUPnPError(t, rec, 501)
}

func TestSeek_RelTimeValid(t *testing.T) {
	fake := newFakePlayer()
	st, cleanup := newAVTState(t, func() player.Player { return fake })
	defer cleanup()
	handler := AVTransportHandler(st, config.Config{})
	const remote = "10.0.0.1:1"
	setupAVT(t, st, handler, remote, "https://example.test/v.mp4")

	rec := serveAction(handler, "Seek", soapBody(`<Unit>REL_TIME</Unit><Target>00:01:02</Target>`), remote)
	assertSOAPSuccess(t, rec, "SeekResponse")

	fake.mu.Lock()
	seeks := append([]float64(nil), fake.seeks...)
	fake.mu.Unlock()
	if len(seeks) != 1 || seeks[0] != 62 {
		t.Fatalf("seeks=%v, want [62]", seeks)
	}
}

func TestSeek_ABSTimeValid(t *testing.T) {
	fake := newFakePlayer()
	st, cleanup := newAVTState(t, func() player.Player { return fake })
	defer cleanup()
	handler := AVTransportHandler(st, config.Config{})
	const remote = "10.0.0.1:1"
	setupAVT(t, st, handler, remote, "https://example.test/v.mp4")

	rec := serveAction(handler, "Seek", soapBody(`<Unit>ABS_TIME</Unit><Target>00:00:30.5</Target>`), remote)
	assertSOAPSuccess(t, rec, "SeekResponse")

	fake.mu.Lock()
	seeks := append([]float64(nil), fake.seeks...)
	fake.mu.Unlock()
	if len(seeks) != 1 || seeks[0] != 30.5 {
		t.Fatalf("seeks=%v, want [30.5]", seeks)
	}
}

func TestSeek_InvalidUnit(t *testing.T) {
	st, cleanup := newAVTState(t, func() player.Player { return newFakePlayer() })
	defer cleanup()
	handler := AVTransportHandler(st, config.Config{})
	const remote = "10.0.0.1:1"
	setupAVT(t, st, handler, remote, "https://example.test/v.mp4")

	rec := serveAction(handler, "Seek", soapBody(`<Unit>TRACK_NR</Unit><Target>1</Target>`), remote)
	assertUPnPError(t, rec, 710)
}

func TestSeek_InvalidTarget(t *testing.T) {
	st, cleanup := newAVTState(t, func() player.Player { return newFakePlayer() })
	defer cleanup()
	handler := AVTransportHandler(st, config.Config{})
	const remote = "10.0.0.1:1"
	setupAVT(t, st, handler, remote, "https://example.test/v.mp4")

	rec := serveAction(handler, "Seek", soapBody(`<Unit>REL_TIME</Unit><Target>99:99:99</Target>`), remote)
	assertUPnPError(t, rec, 711)
}

func TestSeek_NoActivePlayer(t *testing.T) {
	st, cleanup := newAVTState(t, nil)
	defer cleanup()
	handler := AVTransportHandler(st, config.Config{})
	// Acquire session, no player.
	serveAction(handler, "SetAVTransportURI", soapBody(`<CurrentURI>https://example.test/v.mp4</CurrentURI>`), "10.0.0.1:1")
	rec := serveAction(handler, "Seek", soapBody(`<Unit>REL_TIME</Unit><Target>00:00:10</Target>`), "10.0.0.1:1")
	assertUPnPError(t, rec, 701)
}

func TestSeek_PlayerFailure(t *testing.T) {
	fake := newFakePlayer()
	fake.errs["Seek"] = errors.New("cannot seek")
	st, cleanup := newAVTState(t, func() player.Player { return fake })
	defer cleanup()
	handler := AVTransportHandler(st, config.Config{})
	const remote = "10.0.0.1:1"
	setupAVT(t, st, handler, remote, "https://example.test/v.mp4")

	rec := serveAction(handler, "Seek", soapBody(`<Unit>REL_TIME</Unit><Target>00:00:10</Target>`), remote)
	assertUPnPError(t, rec, 501)
}

func TestGetTransportInfo_ReflectsState(t *testing.T) {
	st, cleanup := newAVTState(t, nil)
	defer cleanup()
	handler := AVTransportHandler(st, config.Config{})

	// Default state is STOPPED.
	rec := serveAction(handler, "GetTransportInfo", soapBody(``), "10.0.0.1:1")
	assertSOAPSuccess(t, rec, "GetTransportInfoResponse")
	if !strings.Contains(rec.Body.String(), "<CurrentTransportState>STOPPED</CurrentTransportState>") {
		t.Fatalf("body=%s", rec.Body.String())
	}

	// Move to PLAYING and re-query.
	setupAVT(t, st, handler, "10.0.0.1:1", "https://example.test/v.mp4")
	rec = serveAction(handler, "GetTransportInfo", soapBody(``), "10.0.0.1:1")
	if !strings.Contains(rec.Body.String(), "<CurrentTransportState>PLAYING</CurrentTransportState>") {
		t.Fatalf("body=%s", rec.Body.String())
	}
}

func TestGetPositionInfo_FormatsFromSpy(t *testing.T) {
	fake := newFakePlayer()
	fake.position = 125.0
	fake.duration = 3600.0
	st, cleanup := newAVTState(t, func() player.Player { return fake })
	defer cleanup()
	handler := AVTransportHandler(st, config.Config{})
	const remote = "10.0.0.1:1"
	// Use a URI with a query string; escape the ampersand so the SOAP body
	// remains valid XML while the stored URI keeps the raw form.
	const uri = "https://example.test/v.mp4?token=abc&x=1"
	uriBody := soapBody(`<CurrentURI>https://example.test/v.mp4?token=abc&amp;x=1</CurrentURI>`)
	if rec := serveAction(handler, "SetAVTransportURI", uriBody, remote); rec.Code != http.StatusOK {
		t.Fatalf("setup SetURI status=%d body=%s", rec.Code, rec.Body.String())
	}
	if rec := serveAction(handler, "Play", soapBody(`<Speed>1</Speed>`), remote); rec.Code != http.StatusOK {
		t.Fatalf("setup Play status=%d body=%s", rec.Code, rec.Body.String())
	}

	rec := serveAction(handler, "GetPositionInfo", soapBody(``), remote)
	assertSOAPSuccess(t, rec, "GetPositionInfoResponse")
	body := rec.Body.String()
	if !strings.Contains(body, "<TrackDuration>01:00:00</TrackDuration>") {
		t.Fatalf("duration missing/wrong; body=%s", body)
	}
	if !strings.Contains(body, "<RelTime>00:02:05</RelTime>") {
		t.Fatalf("reltime missing/wrong; body=%s", body)
	}
	// TrackURI must come from SetURI and be HTML-escaped in the response.
	if !strings.Contains(body, "<TrackURI>https://example.test/v.mp4?token=abc&amp;x=1</TrackURI>") {
		t.Fatalf("trackuri missing/wrong; body=%s", body)
	}
}

func TestGetPositionInfo_NoPlayerReturnsZeros(t *testing.T) {
	st, cleanup := newAVTState(t, nil)
	defer cleanup()
	handler := AVTransportHandler(st, config.Config{})
	serveAction(handler, "SetAVTransportURI", soapBody(`<CurrentURI>https://example.test/v.mp4</CurrentURI>`), "10.0.0.1:1")
	rec := serveAction(handler, "GetPositionInfo", soapBody(``), "10.0.0.1:1")
	assertSOAPSuccess(t, rec, "GetPositionInfoResponse")
	if !strings.Contains(rec.Body.String(), "<TrackDuration>00:00:00</TrackDuration>") || !strings.Contains(rec.Body.String(), "<RelTime>00:00:00</RelTime>") {
		t.Fatalf("body=%s", rec.Body.String())
	}
}

func TestGetPositionInfo_PlayerErrorsReturnZeros(t *testing.T) {
	fake := newFakePlayer()
	fake.posErr = errors.New("pos ipc")
	fake.durErr = errors.New("dur ipc")
	st, cleanup := newAVTState(t, func() player.Player { return fake })
	defer cleanup()
	handler := AVTransportHandler(st, config.Config{})
	const remote = "10.0.0.1:1"
	setupAVT(t, st, handler, remote, "https://example.test/v.mp4")

	rec := serveAction(handler, "GetPositionInfo", soapBody(``), remote)
	assertSOAPSuccess(t, rec, "GetPositionInfoResponse")
	if !strings.Contains(rec.Body.String(), "<TrackDuration>00:00:00</TrackDuration>") || !strings.Contains(rec.Body.String(), "<RelTime>00:00:00</RelTime>") {
		t.Fatalf("body=%s", rec.Body.String())
	}
}

func TestGetMediaInfo(t *testing.T) {
	fake := newFakePlayer()
	fake.duration = 7200.0
	st, cleanup := newAVTState(t, func() player.Player { return fake })
	defer cleanup()
	handler := AVTransportHandler(st, config.Config{})
	const remote = "10.0.0.1:1"
	const uri = "https://example.test/v.mp4"
	setupAVT(t, st, handler, remote, uri)

	rec := serveAction(handler, "GetMediaInfo", soapBody(``), remote)
	assertSOAPSuccess(t, rec, "GetMediaInfoResponse")
	body := rec.Body.String()
	if !strings.Contains(body, "<NrTracks>1</NrTracks>") {
		t.Fatalf("NrTracks missing; body=%s", body)
	}
	if !strings.Contains(body, "<MediaDuration>02:00:00</MediaDuration>") {
		t.Fatalf("MediaDuration missing/wrong; body=%s", body)
	}
	if !strings.Contains(body, "<CurrentURI>"+uri+"</CurrentURI>") {
		t.Fatalf("CurrentURI missing; body=%s", body)
	}
}

func TestGetTransportSettings(t *testing.T) {
	st, cleanup := newAVTState(t, nil)
	defer cleanup()
	rec := serveAction(AVTransportHandler(st, config.Config{}), "GetTransportSettings", soapBody(``), "10.0.0.1:1")
	assertSOAPSuccess(t, rec, "GetTransportSettingsResponse")
	if !strings.Contains(rec.Body.String(), "<PlayMode>NORMAL</PlayMode>") {
		t.Fatalf("body=%s", rec.Body.String())
	}
}

func TestGetDeviceCapabilities(t *testing.T) {
	st, cleanup := newAVTState(t, nil)
	defer cleanup()
	rec := serveAction(AVTransportHandler(st, config.Config{}), "GetDeviceCapabilities", soapBody(``), "10.0.0.1:1")
	assertSOAPSuccess(t, rec, "GetDeviceCapabilitiesResponse")
	if !strings.Contains(rec.Body.String(), "<PlayMedia>NETWORK</PlayMedia>") {
		t.Fatalf("body=%s", rec.Body.String())
	}
}

func TestAVTransport_UnknownAction(t *testing.T) {
	st, cleanup := newAVTState(t, nil)
	defer cleanup()
	rec := serveAction(AVTransportHandler(st, config.Config{}), "BogusAction", soapBody(``), "10.0.0.1:1")
	assertUPnPError(t, rec, 401)
}
