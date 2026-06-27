package upnp

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/tr1v3r/rcast/internal/config"
	"github.com/tr1v3r/rcast/internal/player"
	"github.com/tr1v3r/rcast/internal/state"
)

type handlerFakePlayer struct {
	mu       sync.Mutex
	plays    int
	stops    int
	pauseErr error
}

func (p *handlerFakePlayer) Play(context.Context, string, int) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.plays++
	return nil
}
func (p *handlerFakePlayer) Pause(context.Context) error               { return p.pauseErr }
func (p *handlerFakePlayer) SetVolume(context.Context, int) error      { return nil }
func (p *handlerFakePlayer) SetMute(context.Context, bool) error       { return nil }
func (p *handlerFakePlayer) SetFullscreen(context.Context, bool) error { return nil }
func (p *handlerFakePlayer) SetTitle(context.Context, string) error    { return nil }
func (p *handlerFakePlayer) Screenshot(context.Context, string) error  { return nil }
func (p *handlerFakePlayer) SetSpeed(context.Context, float64) error   { return nil }
func (p *handlerFakePlayer) Seek(context.Context, float64) error       { return nil }
func (p *handlerFakePlayer) GetPosition(context.Context) (float64, error) {
	return 0, nil
}
func (p *handlerFakePlayer) GetDuration(context.Context) (float64, error) {
	return 0, nil
}
func (p *handlerFakePlayer) Stop(context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.stops++
	return nil
}

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

func soapBody(inner string) string {
	return `<s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/"><s:Body><u:Action xmlns:u="service">` + inner + `</u:Action></s:Body></s:Envelope>`
}

func serveAction(handler http.Handler, action, body, remote string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/control", strings.NewReader(body))
	req.Header.Set("SOAPACTION", `"service#`+action+`"`)
	req.RemoteAddr = remote
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}
