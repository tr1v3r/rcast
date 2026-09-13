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
