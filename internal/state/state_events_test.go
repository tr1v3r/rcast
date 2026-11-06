package state

import (
	"context"
	"sync"
	"testing"

	"github.com/tr1v3r/rcast/internal/player"
)

type eventPlayer struct {
	fakePlayer

	mu       sync.Mutex
	onEvent  func(player.Event)
	position float64
	duration float64
}

func (p *eventPlayer) OnEvent(fn func(player.Event)) {
	p.mu.Lock()
	p.onEvent = fn
	p.mu.Unlock()
}

func (p *eventPlayer) emit(event player.Event) {
	p.mu.Lock()
	fn := p.onEvent
	p.mu.Unlock()
	if fn != nil {
		fn(event)
	}
}

func (p *eventPlayer) GetPosition(context.Context) (float64, error) {
	return p.position, nil
}

func (p *eventPlayer) GetDuration(context.Context) (float64, error) {
	return p.duration, nil
}

func TestEndFileEventStopsTransportAtDuration(t *testing.T) {
	fake := &eventPlayer{position: 12, duration: 95}
	st := newState(t, func() player.Player { return fake })
	st.SetURI("https://example.test/movie.mp4", "")
	st.EnsurePlayer()
	st.SetTransportState("PLAYING")

	fake.emit(player.Event{Name: "end-file", Reason: "eof"})

	if got := st.GetTransportState(); got != "STOPPED" {
		t.Fatalf("transport state = %q, want STOPPED", got)
	}
	position, duration, positionErr, durationErr := st.GetPlaybackPosition(context.Background())
	if positionErr != nil || durationErr != nil {
		t.Fatalf("GetPlaybackPosition errors = %v/%v", positionErr, durationErr)
	}
	if position != 95 || duration != 95 {
		t.Fatalf("position/duration = %v/%v, want 95/95", position, duration)
	}
}

func TestLateEndFileFromDetachedPlayerIsIgnored(t *testing.T) {
	first := &eventPlayer{duration: 10}
	second := &eventPlayer{duration: 20}
	created := 0
	st := newState(t, func() player.Player {
		created++
		if created == 1 {
			return first
		}
		return second
	})
	st.EnsurePlayer()
	if err := st.StopPlayer(); err != nil {
		t.Fatalf("StopPlayer: %v", err)
	}
	st.EnsurePlayer()
	st.SetTransportState("PLAYING")

	first.emit(player.Event{Name: "end-file", Reason: "eof"})

	if got := st.GetTransportState(); got != "PLAYING" {
		t.Fatalf("late old-player event changed state to %q", got)
	}
}

type heldFramePlayer struct {
	eventPlayer
	atEOF  bool
	closed bool
}

func (p *heldFramePlayer) PlaybackEnded(_ context.Context, natural bool) (bool, error) {
	return natural && p.atEOF, nil
}

func (p *heldFramePlayer) Close(context.Context) error {
	p.closed = true
	return nil
}

func TestKeepOpenEOFAndStaleMediaEvents(t *testing.T) {
	fake := &heldFramePlayer{eventPlayer: eventPlayer{duration: 95}}
	st := newState(t, func() player.Player { return fake })
	st.SetURI("https://example.test/first", "")
	st.EnsurePlayer()
	st.SetTransportState("PLAYING")
	event := player.Event{Name: "property-change", Fields: map[string]any{"name": "eof-reached", "data": true}}
	fake.atEOF = true
	fake.emit(event)
	if st.GetTransportState() != "STOPPED" {
		t.Fatal("last frame held by keep-open did not stop transport")
	}
	st.SetURI("https://example.test/next", "")
	st.SetTransportState("PLAYING")
	fake.atEOF = false
	for _, stale := range []player.Event{event, {Name: "end-file", Reason: "stop"}, {Name: "end-file", Reason: "redirect"}} {
		fake.emit(stale)
		if st.GetTransportState() != "PLAYING" {
			t.Fatalf("late event %v stopped the next media", stale)
		}
	}
	if err := st.StopPlayer(); err != nil || !fake.closed {
		t.Fatalf("detached player disposal: closed=%t err=%v", fake.closed, err)
	}
}
