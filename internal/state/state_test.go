package state

import (
	"context"
	"sync"
	"testing"

	"github.com/tr1v3r/rcast/internal/config"
	"github.com/tr1v3r/rcast/internal/player"
)

type fakePlayer struct {
	mu             sync.Mutex
	stopped        int
	stopContextErr error
}

func (p *fakePlayer) Play(context.Context, string, int) error      { return nil }
func (p *fakePlayer) Pause(context.Context) error                  { return nil }
func (p *fakePlayer) StopPlayback(context.Context) error           { return nil }
func (p *fakePlayer) SetVolume(context.Context, int) error         { return nil }
func (p *fakePlayer) SetMute(context.Context, bool) error          { return nil }
func (p *fakePlayer) SetFullscreen(context.Context, bool) error    { return nil }
func (p *fakePlayer) SetTitle(context.Context, string) error       { return nil }
func (p *fakePlayer) Screenshot(context.Context, string) error     { return nil }
func (p *fakePlayer) SetSpeed(context.Context, float64) error      { return nil }
func (p *fakePlayer) Seek(context.Context, float64) error          { return nil }
func (p *fakePlayer) GetPosition(context.Context) (float64, error) { return 0, nil }
func (p *fakePlayer) GetDuration(context.Context) (float64, error) { return 0, nil }
func (p *fakePlayer) Stop(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.stopped++
	p.stopContextErr = ctx.Err()
	return nil
}

func TestSessionPreemptionAndPlayerLifecycle(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	created := 0
	var current *fakePlayer
	st := NewWithPlayerFactory(ctx, config.Config{}, func() player.Player {
		created++
		current = &fakePlayer{}
		return current
	})
	defer st.Stop()

	if acquired, preempted := st.AcquireSession("controller-a", false); !acquired || preempted {
		t.Fatalf("initial acquire = (%v, %v)", acquired, preempted)
	}
	first := st.EnsurePlayer()
	if first != st.EnsurePlayer() || created != 1 {
		t.Fatalf("player was not reused; created=%d", created)
	}
	if acquired, _ := st.AcquireSession("controller-b", false); acquired {
		t.Fatal("second controller acquired a non-preemptible session")
	}
	if acquired, preempted := st.AcquireSession("controller-b", true); !acquired || !preempted {
		t.Fatalf("preempt acquire = (%v, %v)", acquired, preempted)
	}
	if err := st.StopPlayer(); err != nil {
		t.Fatalf("stop preempted player: %v", err)
	}
	firstFake := first.(*fakePlayer)
	if firstFake.stopped != 1 {
		t.Fatalf("old player stopped %d times, want 1", firstFake.stopped)
	}
	if st.GetSessionOwner() != "controller-b" {
		t.Fatalf("owner = %q, want controller-b", st.GetSessionOwner())
	}

	st.ReleaseSession("controller-a")
	if st.GetSessionOwner() != "controller-b" {
		t.Fatal("non-owner released the session")
	}
	st.ReleaseSession("controller-b")
	if st.GetSessionOwner() != "" {
		t.Fatal("owner failed to release the session")
	}
}

func TestStopUsesLiveCleanupContextAfterAppCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	fake := &fakePlayer{}
	st := NewWithPlayerFactory(ctx, config.Config{}, func() player.Player { return fake })
	st.EnsurePlayer()

	cancel()
	st.Stop()

	if fake.stopped != 1 {
		t.Fatalf("player stopped %d times, want 1", fake.stopped)
	}
	if fake.stopContextErr != nil {
		t.Fatalf("shutdown player context was already cancelled: %v", fake.stopContextErr)
	}
}
