package state

import (
	"errors"
	"testing"

	"github.com/tr1v3r/rcast/internal/player"
)

func TestPreemptionClearsPreviousTransport(t *testing.T) {
	st := newState(t, func() player.Player { return &fakePlayer{} })
	st.AcquireSession("first", false)
	st.SetURI("https://example.test/first.mp4", "<title>first</title>")
	st.EnsurePlayer()
	st.SetTransportState("PLAYING")

	acquired, preempted := st.AcquireSession("second", true)
	if !acquired || !preempted {
		t.Fatalf("AcquireSession = (%v, %v), want (true, true)", acquired, preempted)
	}
	if err := st.StopPlayer(); err != nil {
		t.Fatalf("stop preempted player: %v", err)
	}
	uri, metadata := st.GetURI()
	if uri != "" || metadata != "" {
		t.Fatalf("preempted transport = (%q, %q), want empty", uri, metadata)
	}
	if got := st.GetTransportState(); got != "STOPPED" {
		t.Fatalf("transport state = %q, want STOPPED", got)
	}
}

func TestStopPlayerFailureKeepsPlayerReachable(t *testing.T) {
	stopErr := errors.New("stop failed")
	fake := &fakePlayer{stopErr: stopErr}
	st := newState(t, func() player.Player { return fake })
	st.EnsurePlayer()
	st.SetTransportState("PLAYING")

	if err := st.StopPlayer(); !errors.Is(err, stopErr) {
		t.Fatalf("StopPlayer error = %v, want %v", err, stopErr)
	}
	if got := st.GetActivePlayer(); got != fake {
		t.Fatalf("player after failed stop = %v, want original %v", got, fake)
	}
	if got := st.GetTransportState(); got != "PLAYING" {
		t.Fatalf("failed stop changed transport state to %q", got)
	}

	fake.mu.Lock()
	fake.stopErr = nil
	fake.mu.Unlock()
	if err := st.StopPlayer(); err != nil {
		t.Fatalf("retry StopPlayer: %v", err)
	}
	if st.GetActivePlayer() != nil {
		t.Fatal("player remains attached after successful retry")
	}
	if got := st.GetTransportState(); got != "STOPPED" {
		t.Fatalf("successful stop state = %q, want STOPPED", got)
	}
}
