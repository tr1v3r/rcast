package state

import (
	"testing"
	"time"

	"github.com/tr1v3r/rcast/internal/player"
)

func TestReaperDoesNotStopIdlePlayingPlayer(t *testing.T) {
	fake := &fakePlayer{}
	st := newState(t, func() player.Player { return fake })
	st.AcquireSession("controller", false)
	st.EnsurePlayer()
	st.SetTransportState("PLAYING")
	backdatePlayerLastUsed(st, playerMaxIdle+time.Minute)

	st.reapExpiredPlayer()

	if got := fake.stops(); got != 0 {
		t.Fatalf("playing player stopped %d times, want 0", got)
	}
	if got := st.GetTransportState(); got != "PLAYING" {
		t.Fatalf("transport state = %q, want PLAYING", got)
	}
	if st.GetActivePlayer() == nil {
		t.Fatal("playing player was reaped after controller inactivity")
	}
}

func TestReaperStopsIdlePausedPlayerAndResetsTransport(t *testing.T) {
	fake := &fakePlayer{}
	st := newState(t, func() player.Player { return fake })
	st.AcquireSession("controller", false)
	st.EnsurePlayer()
	st.SetTransportState("PAUSED_PLAYBACK")
	backdatePlayerLastUsed(st, playerMaxIdle+time.Minute)

	st.reapExpiredPlayer()

	if got := fake.stops(); got != 1 {
		t.Fatalf("paused player stopped %d times, want 1", got)
	}
	if got := st.GetTransportState(); got != "STOPPED" {
		t.Fatalf("transport state = %q, want STOPPED", got)
	}
	if st.GetActivePlayer() != nil {
		t.Fatal("expired paused player remains attached")
	}
}
