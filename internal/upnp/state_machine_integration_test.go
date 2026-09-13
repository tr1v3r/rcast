package upnp

import (
	"strings"
	"sync"
	"testing"

	"github.com/tr1v3r/rcast/internal/config"
	"github.com/tr1v3r/rcast/internal/player"
)

type handlerEventPlayer struct {
	*handlerFakePlayer
	mu      sync.Mutex
	onEvent func(player.Event)
}

func (p *handlerEventPlayer) OnEvent(fn func(player.Event)) {
	p.mu.Lock()
	p.onEvent = fn
	p.mu.Unlock()
}

func (p *handlerEventPlayer) emit(event player.Event) {
	p.mu.Lock()
	fn := p.onEvent
	p.mu.Unlock()
	if fn != nil {
		fn(event)
	}
}

func TestNaturalEndUpdatesTransportAndPositionInfo(t *testing.T) {
	base := newFakePlayer()
	base.duration = 95
	base.position = 12
	fake := &handlerEventPlayer{handlerFakePlayer: base}
	st, cleanup := newAVTState(t, func() player.Player { return fake })
	defer cleanup()
	handler := AVTransportHandler(st, config.Config{})
	setupAVT(t, st, handler, "10.0.0.1:1", "https://example.test/movie.mp4")

	fake.emit(player.Event{Name: "end-file", Reason: "eof"})

	transport := serveAction(handler, "GetTransportInfo", soapBody(""), "10.0.0.1:1")
	if !strings.Contains(transport.Body.String(), "<CurrentTransportState>STOPPED</CurrentTransportState>") {
		t.Fatalf("transport response = %s", transport.Body.String())
	}
	position := serveAction(handler, "GetPositionInfo", soapBody(""), "10.0.0.1:1")
	if !strings.Contains(position.Body.String(), "<TrackDuration>00:01:35</TrackDuration>") ||
		!strings.Contains(position.Body.String(), "<RelTime>00:01:35</RelTime>") {
		t.Fatalf("position response = %s", position.Body.String())
	}
}

func TestPreemptThenBarePlayCannotReplayPreviousURI(t *testing.T) {
	fake := newFakePlayer()
	st, cleanup := newAVTState(t, func() player.Player { return fake })
	defer cleanup()
	handler := AVTransportHandler(st, config.Config{AllowSessionPreempt: true})
	setupAVT(t, st, handler, "10.0.0.1:1", "https://example.test/first.mp4")

	response := serveAction(handler, "Play", soapBody(`<Speed>1</Speed>`), "10.0.0.2:1")
	assertUPnPError(t, response, 702)

	fake.mu.Lock()
	plays := fake.plays
	fake.mu.Unlock()
	if plays != 1 {
		t.Fatalf("player Play calls = %d, want only controller A's original call", plays)
	}
	if uri, metadata := st.GetURI(); uri != "" || metadata != "" {
		t.Fatalf("transport after preemption = (%q, %q), want empty", uri, metadata)
	}
}
