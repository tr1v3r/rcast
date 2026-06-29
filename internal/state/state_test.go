package state

import (
	"context"
	"errors"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/tr1v3r/rcast/internal/config"
	"github.com/tr1v3r/rcast/internal/player"
)

type fakePlayer struct {
	mu             sync.Mutex
	stopped        int
	stopErr        error
	stopContextErr error
}

func (p *fakePlayer) Play(context.Context, string, int) error { return nil }

func (p *fakePlayer) Pause(context.Context) error { return nil }

func (p *fakePlayer) StopPlayback(context.Context) error { return nil }

func (p *fakePlayer) SetVolume(context.Context, int) error { return nil }

func (p *fakePlayer) SetMute(context.Context, bool) error { return nil }

func (p *fakePlayer) SetFullscreen(context.Context, bool) error { return nil }

func (p *fakePlayer) SetTitle(context.Context, string) error { return nil }

func (p *fakePlayer) Screenshot(context.Context, string) error { return nil }

func (p *fakePlayer) SetSpeed(context.Context, float64) error { return nil }

func (p *fakePlayer) Seek(context.Context, float64) error { return nil }

func (p *fakePlayer) GetPosition(context.Context) (float64, error) { return 0, nil }

func (p *fakePlayer) GetDuration(context.Context) (float64, error) { return 0, nil }

func (p *fakePlayer) Stop(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.stopped++
	p.stopContextErr = ctx.Err()
	return p.stopErr
}

func (p *fakePlayer) stops() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.stopped
}

// newState builds a PlayerState whose background reaper exits immediately so it
// cannot interfere with explicit reapExpiredPlayer calls. The returned context
// is NOT cancelled — Stop on a fake player needs a live ctx.
func newState(t *testing.T, factory PlayerFactory) *PlayerState {
	t.Helper()
	reaperCtx, cancel := context.WithCancel(context.Background())
	cancel() // reaper goroutine returns on ctx.Done immediately
	return NewWithPlayerFactory(reaperCtx, config.Config{}, factory)
}

// backdatePlayerLastUsed sets s.playerLastUsed to `ago` in the past under the
// state mutex, mimicking idle passage of time without sleeping.
func backdatePlayerLastUsed(s *PlayerState, ago time.Duration) {
	s.mu.Lock()
	s.playerLastUsed = time.Now().Add(-ago)
	s.mu.Unlock()
}

func backdateSessionUsed(s *PlayerState, ago time.Duration) {
	s.mu.Lock()
	s.sessionUsed = time.Now().Add(-ago)
	s.mu.Unlock()
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
	if firstFake.stops() != 1 {
		t.Fatalf("old player stopped %d times, want 1", firstFake.stops())
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

	if fake.stops() != 1 {
		t.Fatalf("player stopped %d times, want 1", fake.stops())
	}
	if fake.stopContextErr != nil {
		t.Fatalf("shutdown player context was already cancelled: %v", fake.stopContextErr)
	}
}

// --- Player creation / idempotence ---

func TestEnsurePlayerIdempotent(t *testing.T) {
	created := 0
	st := newState(t, func() player.Player {
		created++
		return &fakePlayer{}
	})
	if p := st.EnsurePlayer(); p == nil {
		t.Fatal("EnsurePlayer returned nil")
	}
	if p := st.EnsurePlayer(); p == nil {
		t.Fatal("second EnsurePlayer returned nil")
	}
	if created != 1 {
		t.Fatalf("factory invoked %d times, want 1", created)
	}
}

func TestGetActivePlayerRefreshesLastUsedAndNilWhenNone(t *testing.T) {
	st := newState(t, func() player.Player { return &fakePlayer{} })

	if p := st.GetActivePlayer(); p != nil {
		t.Fatalf("GetActivePlayer before EnsurePlayer = %v, want nil", p)
	}
	st.EnsurePlayer()
	// Backdate, then call GetActivePlayer and verify it refreshed.
	backdatePlayerLastUsed(st, 5*time.Minute)
	st.mu.RLock()
	before := st.playerLastUsed
	st.mu.RUnlock()
	if p := st.GetActivePlayer(); p == nil {
		t.Fatal("GetActivePlayer returned nil after EnsurePlayer")
	}
	st.mu.RLock()
	after := st.playerLastUsed
	st.mu.RUnlock()
	if !after.After(before) {
		t.Fatalf("GetActivePlayer did not refresh playerLastUsed: before=%v after=%v", before, after)
	}
}

func TestStopPlayerNoPlayerReturnsNil(t *testing.T) {
	st := newState(t, func() player.Player { return &fakePlayer{} })
	if err := st.StopPlayer(); err != nil {
		t.Fatalf("StopPlayer with no player = %v, want nil", err)
	}
}

func TestStopPlayerErrorDoesNotDeadlock(t *testing.T) {
	fake := &fakePlayer{stopErr: errors.New("boom")}
	st := newState(t, func() player.Player { return fake })
	st.EnsurePlayer()
	if err := st.StopPlayer(); err == nil || err.Error() != "boom" {
		t.Fatalf("StopPlayer err = %v, want \"boom\"", err)
	}
	if fake.stops() != 1 {
		t.Fatalf("Stop invoked %d times, want 1", fake.stops())
	}
	// Subsequent StopPlayer should now find no player.
	if err := st.StopPlayer(); err != nil {
		t.Fatalf("StopPlayer after stop = %v, want nil", err)
	}
}

// --- Expiration via backdated timestamps ---

func TestReapExpiredPlayerStopsAndClears(t *testing.T) {
	fake := &fakePlayer{}
	st := newState(t, func() player.Player { return fake })

	st.AcquireSession("controller-a", false)
	st.EnsurePlayer()
	backdatePlayerLastUsed(st, 11*time.Minute) // > playerMaxIdle (10m)

	st.reapExpiredPlayer()

	if fake.stops() != 1 {
		t.Fatalf("player stopped %d times, want 1", fake.stops())
	}
	if p := st.GetActivePlayer(); p != nil {
		t.Fatalf("player not cleared, got %v", p)
	}
	if owner := st.GetSessionOwner(); owner != "" {
		t.Fatalf("session owner = %q, want empty", owner)
	}
}

func TestReapExpiredPlayerNotExpiredUnchanged(t *testing.T) {
	fake := &fakePlayer{}
	st := newState(t, func() player.Player { return fake })

	st.AcquireSession("controller-a", false)
	st.EnsurePlayer()
	backdatePlayerLastUsed(st, 30*time.Second) // < playerMaxIdle

	st.reapExpiredPlayer()

	if fake.stops() != 0 {
		t.Fatalf("player stopped %d times, want 0 (not expired)", fake.stops())
	}
	if p := st.GetActivePlayer(); p == nil {
		t.Fatal("player cleared despite not being expired")
	}
	if owner := st.GetSessionOwner(); owner != "controller-a" {
		t.Fatalf("session owner = %q, want controller-a", owner)
	}
}

func TestReapExpiredSessionOnlyClearsSessionNoStop(t *testing.T) {
	fake := &fakePlayer{}
	st := newState(t, func() player.Player { return fake })

	// Session but no player — the session-only expiry branch.
	st.AcquireSession("controller-a", false)
	backdateSessionUsed(st, 11*time.Minute)

	st.reapExpiredPlayer()

	if fake.stops() != 0 {
		t.Fatalf("player stopped %d times, want 0 (no player)", fake.stops())
	}
	if owner := st.GetSessionOwner(); owner != "" {
		t.Fatalf("session owner = %q, want empty", owner)
	}
}

// --- Volume mapping ---

func TestMapVolumeRequestPlainScaleIdentity(t *testing.T) {
	// scale <= 1 → passthrough, mapping deactivated.
	applied, mapping := mapVolumeRequest(50, volumeMapping{}, "c1", 42, 1.0)
	if applied != 42 {
		t.Fatalf("applied = %d, want 42", applied)
	}
	if mapping.active {
		t.Fatal("mapping should not be active for plain scale")
	}
	// scale 0 also passthrough.
	applied, _ = mapVolumeRequest(50, volumeMapping{}, "c1", 7, 0)
	if applied != 7 {
		t.Fatalf("scale 0 applied = %d, want 7", applied)
	}
}

func TestMapVolumeRequestAwemeDeltaClampAndPersistence(t *testing.T) {
	const controller = "douyin-ios"
	const scale = 5.0

	// Step 1: controller has volume 50 raw → starts mapping at 50 applied.
	_, mapping := mapVolumeRequest(50, volumeMapping{}, controller, 50, scale)
	if !mapping.active || mapping.controller != controller {
		t.Fatalf("mapping not initialized: %+v", mapping)
	}
	if mapping.raw != 50 || mapping.applied != 50 {
		t.Fatalf("initial mapping raw/applied = %v/%v, want 50/50", mapping.raw, mapping.applied)
	}

	// Step 2: raw 50 → 51 (delta +1 * scale 5) → applied 55.
	applied, mapping := mapVolumeRequest(50, mapping, controller, 51, scale)
	if applied != 55 {
		t.Fatalf("delta +1*5 applied = %d, want 55", applied)
	}

	// Step 3: clamp high: raw 100 → applied overshoots 100.
	applied, mapping = mapVolumeRequest(55, mapping, controller, 100, scale)
	// raw was 51, applied 55. delta = (100-51)*5 = 245 → 55+245=300 → clamp 100.
	if applied != 100 {
		t.Fatalf("high clamp applied = %d, want 100", applied)
	}

	// Step 4: clamp low: raw 0 from applied 100, raw 100 → delta (0-100)*5 = -500.
	applied, mapping = mapVolumeRequest(100, mapping, controller, 0, scale)
	if applied != 0 {
		t.Fatalf("low clamp applied = %d, want 0", applied)
	}
}

func TestMapVolumeRequestControllerSwitchResets(t *testing.T) {
	const scale = 4.0
	// Existing mapping owned by c1 at applied 50, raw 50.
	prev := volumeMapping{active: true, controller: "c1", raw: 50, applied: 50}
	// New controller c2 arrives with raw 50 → mapping resets with applied=currentVolume.
	applied, mapping := mapVolumeRequest(50, prev, "c2", 50, scale)
	if !mapping.active || mapping.controller != "c2" {
		t.Fatalf("mapping did not reset to c2: %+v", mapping)
	}
	// currentVolume 50, requested 50, scale>1 with fresh mapping → applied = 50 + (50-50)*4 = 50.
	if applied != 50 {
		t.Fatalf("switched controller applied = %d, want 50", applied)
	}
	if mapping.raw != 50 || mapping.applied != 50 {
		t.Fatalf("post-switch mapping = %+v, want raw=50 applied=50", mapping)
	}
}

func TestMapVolumeRequestDirectionReversal(t *testing.T) {
	const scale = 2.0
	// Begin at raw 60, applied 60.
	_, mapping := mapVolumeRequest(60, volumeMapping{}, "c", 60, scale)
	// Up to raw 62 → applied 60 + (62-60)*2 = 64.
	applied, mapping := mapVolumeRequest(60, mapping, "c", 62, scale)
	if applied != 64 {
		t.Fatalf("up applied = %d, want 64", applied)
	}
	// Reverse back to raw 60 → applied 64 + (60-62)*2 = 60.
	applied, mapping = mapVolumeRequest(64, mapping, "c", 60, scale)
	if applied != 60 {
		t.Fatalf("reversal applied = %d, want 60", applied)
	}
}

func TestPreviewAndCommitVolumeRequestRoundTrip(t *testing.T) {
	st := newState(t, func() player.Player { return &fakePlayer{} })
	const scale = 3.0

	// Preview must not mutate state.
	preview1 := st.PreviewVolumeRequest("c", 50, scale)
	if preview1 != 50 {
		t.Fatalf("preview = %d, want 50", preview1)
	}
	if got := st.GetVolume(); got != 50 {
		t.Fatalf("volume changed after preview: %d", got)
	}
	// Commit applies.
	applied := st.CommitVolumeRequest("c", 51, scale)
	if applied != 53 {
		t.Fatalf("commit applied = %d, want 53 (50 + (51-50)*3)", applied)
	}
	if got := st.GetVolume(); got != 53 {
		t.Fatalf("volume = %d, want 53", got)
	}
	// Reported volume reflects raw under active mapping for the controller.
	if reported := st.GetReportedVolume("c", scale); reported != 51 {
		t.Fatalf("reported = %d, want 51", reported)
	}
	// Plain (scale <= 1) returns applied directly.
	if reported := st.GetReportedVolume("c", 1.0); reported != 53 {
		t.Fatalf("plain reported = %d, want 53", reported)
	}
}

func TestSetVolumeAndReleaseSessionResetMapping(t *testing.T) {
	st := newState(t, func() player.Player { return &fakePlayer{} })
	const scale = 3.0

	// Acquire the session so the controller is the owner; otherwise
	// ReleaseSession is a no-op and would not reset the mapping.
	st.AcquireSession("c", false)

	// Establish active mapping owned by "c". Initial volume 50, raw 51 →
	// applied = 50 + (51-50)*3 = 53, mapping.raw = 51.
	st.CommitVolumeRequest("c", 51, scale)
	if reported := st.GetReportedVolume("c", scale); reported != 51 {
		t.Fatalf("pre-reset reported = %d, want 51", reported)
	}

	// SetVolume clears the mapping and sets volume directly.
	st.SetVolume(20)
	if reported := st.GetReportedVolume("c", scale); reported != 20 {
		t.Fatalf("post-SetVolume reported = %d, want 20", reported)
	}

	// Re-establish mapping (controller "c", volume 20, raw 25 → applied 35).
	st.CommitVolumeRequest("c", 25, scale)
	if reported := st.GetReportedVolume("c", scale); reported != 25 {
		t.Fatalf("re-established reported = %d, want 25", reported)
	}
	// Owner ReleaseSession clears the mapping → reported returns applied 35.
	st.ReleaseSession("c")
	if reported := st.GetReportedVolume("c", scale); reported != 35 {
		t.Fatalf("post-ReleaseSession reported = %d, want 35 (applied volume)", reported)
	}

	// Non-owner ReleaseSession is a no-op: re-acquire and confirm no change.
	st.AcquireSession("c", false)
	st.CommitVolumeRequest("c", 25, scale)
	st.ReleaseSession("non-owner")
	if reported := st.GetReportedVolume("c", scale); reported != 25 {
		t.Fatalf("non-owner release altered reported = %d, want 25", reported)
	}
}

// --- Sessions ---

func TestAcquireSessionFirstOwner(t *testing.T) {
	st := newState(t, func() player.Player { return &fakePlayer{} })
	acquired, preempted := st.AcquireSession("alpha", false)
	if !acquired || preempted {
		t.Fatalf("first acquire = (%v, %v)", acquired, preempted)
	}
	if owner := st.GetSessionOwner(); owner != "alpha" {
		t.Fatalf("owner = %q", owner)
	}
}

func TestAcquireSessionSameOwnerRefreshNoPreempt(t *testing.T) {
	st := newState(t, func() player.Player { return &fakePlayer{} })
	st.AcquireSession("alpha", false)
	backdateSessionUsed(st, 5*time.Minute)
	before := func() time.Time {
		st.mu.RLock()
		defer st.mu.RUnlock()
		return st.sessionUsed
	}()
	time.Sleep(2 * time.Millisecond) // ensure clock advances past backdated value
	acquired, preempted := st.AcquireSession("alpha", false)
	if !acquired || preempted {
		t.Fatalf("same-owner acquire = (%v, %v)", acquired, preempted)
	}
	st.mu.RLock()
	after := st.sessionUsed
	st.mu.RUnlock()
	if !after.After(before) {
		t.Fatal("same-owner acquire did not refresh sessionUsed")
	}
}

func TestAcquireSessionPreemptDisabledRejects(t *testing.T) {
	st := newState(t, func() player.Player { return &fakePlayer{} })
	st.AcquireSession("alpha", false)
	acquired, preempted := st.AcquireSession("beta", false)
	if acquired || preempted {
		t.Fatalf("preempt-disabled other controller = (%v, %v), want (false,false)", acquired, preempted)
	}
	if owner := st.GetSessionOwner(); owner != "alpha" {
		t.Fatalf("owner changed on rejected acquire: %q", owner)
	}
}

func TestAcquireSessionPreemptEnabledDisplaces(t *testing.T) {
	st := newState(t, func() player.Player { return &fakePlayer{} })
	st.AcquireSession("alpha", false)
	acquired, preempted := st.AcquireSession("beta", true)
	if !acquired || !preempted {
		t.Fatalf("preempt-enabled other controller = (%v, %v), want (true,true)", acquired, preempted)
	}
	if owner := st.GetSessionOwner(); owner != "beta" {
		t.Fatalf("owner = %q, want beta", owner)
	}
	// Transport state reset to STOPPED on preempt.
	if ts := st.GetTransportState(); ts != "STOPPED" {
		t.Fatalf("transport state = %q, want STOPPED", ts)
	}
}

func TestReleaseSessionNonOwnerNoop(t *testing.T) {
	st := newState(t, func() player.Player { return &fakePlayer{} })
	st.AcquireSession("alpha", false)
	st.ReleaseSession("beta")
	if owner := st.GetSessionOwner(); owner != "alpha" {
		t.Fatalf("non-owner release changed owner to %q", owner)
	}
}

func TestHasSession(t *testing.T) {
	st := newState(t, func() player.Player { return &fakePlayer{} })
	// Empty owner → any controller allowed.
	if !st.HasSession("anyone") {
		t.Fatal("HasSession on empty owner should be true")
	}
	st.AcquireSession("alpha", false)
	if !st.HasSession("alpha") {
		t.Fatal("HasSession for owner should be true")
	}
	if st.HasSession("beta") {
		t.Fatal("HasSession for non-owner should be false")
	}
}

// --- Transport / URI / mute basic coverage ---

func TestURIAndTransportState(t *testing.T) {
	st := newState(t, func() player.Player { return &fakePlayer{} })
	if u, m := st.GetURI(); u != "" || m != "" {
		t.Fatalf("initial URI = (%q,%q), want empty", u, m)
	}
	if ts := st.GetTransportState(); ts != "STOPPED" {
		t.Fatalf("initial transport state = %q, want STOPPED", ts)
	}
	st.SetURI("http://example/foo.mp4", "<meta/>")
	if u, m := st.GetURI(); u != "http://example/foo.mp4" || m != "<meta/>" {
		t.Fatalf("GetURI = (%q,%q)", u, m)
	}
	if ts := st.GetTransportState(); ts != "STOPPED" {
		t.Fatalf("SetURI should reset transport state; got %q", ts)
	}
	st.SetTransportState("PLAYING")
	if ts := st.GetTransportState(); ts != "PLAYING" {
		t.Fatalf("transport state = %q, want PLAYING", ts)
	}
}

func TestVolumeGetSet(t *testing.T) {
	st := newState(t, func() player.Player { return &fakePlayer{} })
	if v := st.GetVolume(); v != 50 {
		t.Fatalf("default volume = %d, want 50", v)
	}
	st.SetVolume(73)
	if v := st.GetVolume(); v != 73 {
		t.Fatalf("volume = %d, want 73", v)
	}
}

func TestMute(t *testing.T) {
	st := newState(t, func() player.Player { return &fakePlayer{} })
	if st.GetMute() {
		t.Fatal("default mute should be false")
	}
	st.SetMute(true)
	if !st.GetMute() {
		t.Fatal("mute not set")
	}
}

func TestSerializeRunsMutatorsInOrder(t *testing.T) {
	st := newState(t, func() player.Player { return &fakePlayer{} })
	var seen []int
	var mu sync.Mutex
	for i := 0; i < 5; i++ {
		i := i
		go st.Serialize(func() {
			mu.Lock()
			seen = append(seen, i)
			mu.Unlock()
		})
	}
	// Spin-wait for all five Serialize calls (no sleep) — Serialize is the
	// synchronization primitive under test; total wall time is negligible.
	for {
		mu.Lock()
		n := len(seen)
		mu.Unlock()
		if n == 5 {
			break
		}
		runtime.Gosched()
	}
}
