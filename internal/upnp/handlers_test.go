package upnp

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tr1v3r/rcast/internal/config"
	"github.com/tr1v3r/rcast/internal/player"
	"github.com/tr1v3r/rcast/internal/state"
)

func TestXMLTextAcceptsArbitraryNamespace(t *testing.T) {
	body := []byte(`<s:Envelope xmlns:s="soap"><s:Body><x:Action xmlns:x="service"><x:CurrentURI>https://example.test/a?x=1&amp;y=2</x:CurrentURI></x:Action></s:Body></s:Envelope>`)
	if got := XMLText(body, "CurrentURI"); got != "https://example.test/a?x=1&y=2" {
		t.Fatalf("CurrentURI = %q", got)
	}
}

func TestRenderingVolumeBeforePlayDoesNotCreatePlayer(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	created := 0
	st := state.NewWithPlayerFactory(ctx, config.Config{}, func() player.Player {
		created++
		return &handlerFakePlayer{}
	})
	defer st.Stop()

	body := soapBody(`<DesiredVolume>73</DesiredVolume>`)
	rec := serveAction(RenderingControlHandler(st, config.Config{}), "SetVolume", body, "10.0.0.1:1234")
	if rec.Code != http.StatusOK {
		t.Fatalf("SetVolume status=%d body=%s", rec.Code, rec.Body.String())
	}
	if created != 0 {
		t.Fatalf("SetVolume created %d players before playback", created)
	}
	if st.GetVolume() != 73 {
		t.Fatalf("volume=%d, want 73", st.GetVolume())
	}
}

func TestAwemeIOSVolumeStepsCoverFullPlayerRange(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fake := &handlerFakePlayer{}
	st := state.NewWithPlayerFactory(ctx, config.Config{}, func() player.Player { return fake })
	defer st.Stop()
	st.EnsurePlayer()
	handler := RenderingControlHandler(st, config.Config{})
	const userAgent = "Aweme/390012 CFNetwork/3860.300.31 Darwin/25.2.0"

	// Match the captured sequence: the controller first climbs from the
	// renderer's reported 50 to 100, then its eight iOS steps descend only to
	// raw 60. The compatibility mapping must make those eight steps span 100→0.
	for raw := 55; raw <= 100; raw += 5 {
		rec := serveActionWithUserAgent(handler, "SetVolume", soapBody(fmt.Sprintf(`<DesiredVolume>%d</DesiredVolume>`, raw)), "10.0.0.1:1", userAgent)
		if rec.Code != http.StatusOK {
			t.Fatalf("SetVolume(%d) status=%d body=%s", raw, rec.Code, rec.Body.String())
		}
	}
	for raw := 95; raw >= 60; raw -= 5 {
		serveActionWithUserAgent(handler, "SetVolume", soapBody(fmt.Sprintf(`<DesiredVolume>%d</DesiredVolume>`, raw)), "10.0.0.1:1", userAgent)
	}
	if got := st.GetVolume(); got != 0 {
		t.Fatalf("player volume after eight down steps=%d, want 0", got)
	}
	if got := st.GetReportedVolume("10.0.0.1", awemeIOSVolumeScale); got != 60 {
		t.Fatalf("Aweme reported volume=%d, want raw 60", got)
	}
	awemeVolume := serveActionWithUserAgent(handler, "GetVolume", soapBody(``), "10.0.0.1:1", userAgent)
	if !strings.Contains(awemeVolume.Body.String(), "<CurrentVolume>60</CurrentVolume>") {
		t.Fatalf("Aweme GetVolume body=%s", awemeVolume.Body.String())
	}
	standardVolume := serveActionWithUserAgent(handler, "GetVolume", soapBody(``), "10.0.0.1:1", "StandardDLNA/1.0")
	if !strings.Contains(standardVolume.Body.String(), "<CurrentVolume>0</CurrentVolume>") {
		t.Fatalf("standard GetVolume body=%s", standardVolume.Body.String())
	}

	for raw := 65; raw <= 100; raw += 5 {
		serveActionWithUserAgent(handler, "SetVolume", soapBody(fmt.Sprintf(`<DesiredVolume>%d</DesiredVolume>`, raw)), "10.0.0.1:1", userAgent)
	}
	if got := st.GetVolume(); got != 100 {
		t.Fatalf("player volume after eight up steps=%d, want 100", got)
	}
	if got := fake.volumes[len(fake.volumes)-1]; got != 100 {
		t.Fatalf("last player volume=%d, want 100", got)
	}
}

func TestVolumeScaleOnlyMatchesAwemeIOS(t *testing.T) {
	tests := []struct {
		userAgent string
		want      float64
	}{
		{"Aweme/390012 CFNetwork/3860.300.31 Darwin/25.2.0", 2.5},
		{"Aweme/390012 okhttp/4.0 Android/16", 1},
		{"Other/1 CFNetwork/3860.300.31 Darwin/25.2.0", 1},
	}
	for _, tt := range tests {
		if got := volumeScaleForUserAgent(tt.userAgent); got != tt.want {
			t.Errorf("volumeScaleForUserAgent(%q)=%v, want %v", tt.userAgent, got, tt.want)
		}
	}
}

func TestAVTransportPreemptionStopsOldPlayer(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var players []*handlerFakePlayer
	st := state.NewWithPlayerFactory(ctx, config.Config{}, func() player.Player {
		p := &handlerFakePlayer{}
		players = append(players, p)
		return p
	})
	defer st.Stop()
	cfg := config.Config{AllowSessionPreempt: true}
	handler := AVTransportHandler(st, cfg)

	set := serveAction(handler, "SetAVTransportURI", soapBody(`<CurrentURI>https://example.test/one.mp4</CurrentURI>`), "10.0.0.1:1")
	if set.Code != http.StatusOK {
		t.Fatalf("SetURI status=%d body=%s", set.Code, set.Body.String())
	}
	play := serveAction(handler, "Play", soapBody(`<Speed>1</Speed>`), "10.0.0.1:2")
	if play.Code != http.StatusOK || len(players) != 1 {
		t.Fatalf("Play status=%d players=%d body=%s", play.Code, len(players), play.Body.String())
	}
	preempt := serveAction(handler, "SetAVTransportURI", soapBody(`<CurrentURI>https://example.test/two.mp4</CurrentURI>`), "10.0.0.2:1")
	if preempt.Code != http.StatusOK {
		t.Fatalf("preempt status=%d body=%s", preempt.Code, preempt.Body.String())
	}
	if players[0].stops != 1 {
		t.Fatalf("old player stops=%d, want 1", players[0].stops)
	}
	if st.GetSessionOwner() != "10.0.0.2" {
		t.Fatalf("owner=%q", st.GetSessionOwner())
	}
}

func TestAVTransportURIChangeReusesActivePlayer(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var players []*handlerFakePlayer
	st := state.NewWithPlayerFactory(ctx, config.Config{}, func() player.Player {
		p := &handlerFakePlayer{}
		players = append(players, p)
		return p
	})
	defer st.Stop()
	handler := AVTransportHandler(st, config.Config{})
	const remote = "10.0.0.1:1"

	serveAction(handler, "SetAVTransportURI", soapBody(`<CurrentURI>https://example.test/one.mp4</CurrentURI>`), remote)
	serveAction(handler, "Play", soapBody(`<Speed>1</Speed>`), remote)
	setSecond := serveAction(handler, "SetAVTransportURI", soapBody(`<CurrentURI>https://example.test/two.mp4</CurrentURI>`), remote)
	if setSecond.Code != http.StatusOK {
		t.Fatalf("second SetURI status=%d body=%s", setSecond.Code, setSecond.Body.String())
	}
	serveAction(handler, "Play", soapBody(`<Speed>1</Speed>`), remote)

	if len(players) != 1 {
		t.Fatalf("created %d players, want one reused player", len(players))
	}
	if players[0].playbackStops != 1 {
		t.Fatalf("playback stops=%d, want 1", players[0].playbackStops)
	}
	if players[0].stops != 0 {
		t.Fatalf("player process stops=%d, want 0", players[0].stops)
	}
	if players[0].plays != 2 {
		t.Fatalf("play calls=%d, want 2", players[0].plays)
	}
}

func TestAVTransportPlayAppliesMetadataTitle(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fake := &handlerFakePlayer{}
	st := state.NewWithPlayerFactory(ctx, config.Config{}, func() player.Player { return fake })
	defer st.Stop()
	handler := AVTransportHandler(st, config.Config{})
	const remote = "10.0.0.1:1"
	const title = "周星驰新电影"
	body := `<CurrentURI>https://example.test/video.mp4</CurrentURI>` +
		`<CurrentURIMetaData>&lt;DIDL-Lite xmlns:dc="http://purl.org/dc/elements/1.1/"&gt;` +
		`&lt;item&gt;&lt;dc:title&gt;` + title + `&lt;/dc:title&gt;&lt;/item&gt;&lt;/DIDL-Lite&gt;</CurrentURIMetaData>`

	set := serveAction(handler, "SetAVTransportURI", soapBody(body), remote)
	if set.Code != http.StatusOK {
		t.Fatalf("SetURI status=%d body=%s", set.Code, set.Body.String())
	}
	play := serveAction(handler, "Play", soapBody(`<Speed>1</Speed>`), remote)
	if play.Code != http.StatusOK {
		t.Fatalf("Play status=%d body=%s", play.Code, play.Body.String())
	}
	if len(fake.titles) != 1 || fake.titles[0] != title {
		t.Fatalf("player titles=%q, want [%q]", fake.titles, title)
	}
}

func TestPauseFailureDoesNotChangeTransportState(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fake := &handlerFakePlayer{pauseErr: errors.New("ipc unavailable")}
	st := state.NewWithPlayerFactory(ctx, config.Config{}, func() player.Player { return fake })
	defer st.Stop()
	handler := AVTransportHandler(st, config.Config{})

	serveAction(handler, "SetAVTransportURI", soapBody(`<CurrentURI>https://example.test/video.mp4</CurrentURI>`), "10.0.0.1:1")
	serveAction(handler, "Play", soapBody(`<Speed>1</Speed>`), "10.0.0.1:1")
	rec := serveAction(handler, "Pause", soapBody(``), "10.0.0.1:1")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("Pause status=%d body=%s", rec.Code, rec.Body.String())
	}
	if st.GetTransportState() != "PLAYING" {
		t.Fatalf("state=%q, want PLAYING", st.GetTransportState())
	}
}

func TestSOAPHandlerRejectsWrongMethod(t *testing.T) {
	st := state.New(context.Background(), config.Config{})
	defer st.Stop()
	req := httptest.NewRequest(http.MethodGet, "/upnp/control/avtransport", nil)
	rec := httptest.NewRecorder()
	AVTransportHandler(st, config.Config{}).ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status=%d, want 405", rec.Code)
	}
}

func TestSOAPHandlerRejectsOversizedBody(t *testing.T) {
	st := state.New(context.Background(), config.Config{})
	defer st.Stop()
	req := httptest.NewRequest(http.MethodPost, "/upnp/control/avtransport", strings.NewReader(strings.Repeat("x", maxSOAPBodyBytes+1)))
	req.Header.Set("SOAPACTION", `"service#Play"`)
	rec := httptest.NewRecorder()
	AVTransportHandler(st, config.Config{}).ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), "Invalid Args") {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestEventHandlerDoesNotIssueFalseSubscription(t *testing.T) {
	req := httptest.NewRequest("SUBSCRIBE", "/upnp/event/avtransport", nil)
	req.Header.Set("CALLBACK", "<http://127.0.0.1/callback>")
	req.Header.Set("NT", "upnp:event")
	rec := httptest.NewRecorder()
	EventHandler(rec, req)
	if rec.Code != http.StatusNotImplemented || rec.Header().Get("SID") != "" {
		t.Fatalf("status=%d SID=%q", rec.Code, rec.Header().Get("SID"))
	}
}

func TestTimeToSecondsValidation(t *testing.T) {
	if got, err := timeToSeconds("01:02:03.5"); err != nil || got != 3723.5 {
		t.Fatalf("valid time=(%v, %v)", got, err)
	}
	for _, invalid := range []string{"1:60:00", "1:00:60", "-1:00:00", "garbage"} {
		if _, err := timeToSeconds(invalid); err == nil {
			t.Errorf("timeToSeconds(%q) unexpectedly succeeded", invalid)
		}
	}
}
