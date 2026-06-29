package upnp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/tr1v3r/rcast/internal/player"
)

// handlerFakePlayer is the white-box test spy for the player.Player interface.
// It records every method invocation (in arrival order, under lock) and allows
// per-method error injection so handler tests can exercise the full UPnP error
// matrix without touching a real IINA process.
type handlerFakePlayer struct {
	mu            sync.Mutex
	plays         int
	stops         int
	playbackStops int
	volumes       []int
	titles        []string
	pauseErr      error // legacy: returned by Pause when non-nil

	// New recording surface.
	errs     map[string]error // per-method error injection keyed by method name
	calls    []string         // ordered log of method names
	seeks    []float64
	mutes    []bool
	speeds   []float64
	position float64
	duration float64
	posErr   error
	durErr   error
}

// newFakePlayer constructs a spy with an initialized error map.
func newFakePlayer() *handlerFakePlayer {
	return &handlerFakePlayer{errs: make(map[string]error)}
}

func (p *handlerFakePlayer) Play(_ context.Context, _ string, _ int) error {
	p.mu.Lock()
	p.plays++
	p.calls = append(p.calls, "Play")
	err := p.errs["Play"]
	p.mu.Unlock()
	return err
}

func (p *handlerFakePlayer) Pause(context.Context) error {
	p.mu.Lock()
	p.calls = append(p.calls, "Pause")
	err := p.errs["Pause"]
	if p.pauseErr != nil {
		err = p.pauseErr
	}
	p.mu.Unlock()
	return err
}

func (p *handlerFakePlayer) StopPlayback(context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.playbackStops++
	p.calls = append(p.calls, "StopPlayback")
	return p.errs["StopPlayback"]
}

func (p *handlerFakePlayer) Stop(context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.stops++
	p.calls = append(p.calls, "Stop")
	return p.errs["Stop"]
}

func (p *handlerFakePlayer) SetVolume(_ context.Context, volume int) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.volumes = append(p.volumes, volume)
	p.calls = append(p.calls, "SetVolume")
	return p.errs["SetVolume"]
}

func (p *handlerFakePlayer) SetMute(_ context.Context, m bool) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.mutes = append(p.mutes, m)
	p.calls = append(p.calls, "SetMute")
	return p.errs["SetMute"]
}

func (p *handlerFakePlayer) SetFullscreen(_ context.Context, _ bool) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls = append(p.calls, "SetFullscreen")
	return p.errs["SetFullscreen"]
}

func (p *handlerFakePlayer) SetTitle(_ context.Context, title string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.titles = append(p.titles, title)
	p.calls = append(p.calls, "SetTitle")
	return p.errs["SetTitle"]
}

func (p *handlerFakePlayer) Screenshot(_ context.Context, _ string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls = append(p.calls, "Screenshot")
	return p.errs["Screenshot"]
}

func (p *handlerFakePlayer) SetSpeed(_ context.Context, speed float64) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.speeds = append(p.speeds, speed)
	p.calls = append(p.calls, "SetSpeed")
	return p.errs["SetSpeed"]
}

func (p *handlerFakePlayer) Seek(_ context.Context, seconds float64) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.seeks = append(p.seeks, seconds)
	p.calls = append(p.calls, "Seek")
	return p.errs["Seek"]
}

func (p *handlerFakePlayer) GetPosition(context.Context) (float64, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.position, p.posErr
}

func (p *handlerFakePlayer) GetDuration(context.Context) (float64, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.duration, p.durErr
}

// Compile-time guard: the spy must satisfy the Player interface.
var _ player.Player = (*handlerFakePlayer)(nil)

// soapBody wraps an inner XML fragment in a minimal SOAP envelope with a
// service-qualified Action element. The action element name is generic — the
// handler dispatches on the SOAPACTION header, not the body's local element.
func soapBody(inner string) string {
	return `<s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/"><s:Body><u:Action xmlns:u="service">` + inner + `</u:Action></s:Body></s:Envelope>`
}

// serveAction posts a SOAP action to the handler from the given remote address.
func serveAction(handler http.Handler, action, body, remote string) *httptest.ResponseRecorder {
	return serveActionWithUserAgent(handler, action, body, remote, "")
}

// serveActionWithUserAgent posts a SOAP action with an explicit User-Agent.
func serveActionWithUserAgent(handler http.Handler, action, body, remote, userAgent string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/control", strings.NewReader(body))
	req.Header.Set("SOAPACTION", `"service#`+action+`"`)
	req.Header.Set("User-Agent", userAgent)
	req.RemoteAddr = remote
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

// soapErrorCode extracts the UPnP errorCode from a SOAP response body. Returns
// an empty string if no errorCode element is present (i.e. a success response).
func soapErrorCode(t *testing.T, body string) string {
	t.Helper()
	return XMLText([]byte(body), "errorCode")
}

// assertSOAPSuccess asserts the recorder carries a UPnP success envelope for the
// expected response action name (HTTP 200, no errorCode, response present).
func assertSOAPSuccess(t *testing.T, rec *httptest.ResponseRecorder, wantAction string) {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if code := soapErrorCode(t, rec.Body.String()); code != "" {
		t.Fatalf("unexpected UPnP error code=%s; body=%s", code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "<u:"+wantAction+" ") && !strings.Contains(rec.Body.String(), "<u:"+wantAction+">") {
		t.Fatalf("response action %q not in body=%s", wantAction, rec.Body.String())
	}
}

// assertUPnPError asserts the recorder carries a SOAP fault with the expected
// UPnP errorCode (WriteSOAPError always returns HTTP 500).
func assertUPnPError(t *testing.T, rec *httptest.ResponseRecorder, wantCode int) {
	t.Helper()
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d, want 500; body=%s", rec.Code, rec.Body.String())
	}
	got := soapErrorCode(t, rec.Body.String())
	if got == "" {
		t.Fatalf("no errorCode in body=%s", rec.Body.String())
	}
	if got != itoa(wantCode) {
		t.Fatalf("errorCode=%s, want %d; body=%s", got, wantCode, rec.Body.String())
	}
}

// itoa is a tiny local int->string helper to avoid pulling in strconv just for
// an assertion message.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [12]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
