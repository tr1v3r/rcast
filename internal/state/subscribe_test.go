package state

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tr1v3r/rcast/internal/config"
	"github.com/tr1v3r/rcast/internal/player"
)

const subscriptionTestTimeout = 2 * time.Second

func newSubscriptionTestState(t *testing.T) *PlayerState {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	return NewWithPlayerFactory(ctx, config.Config{}, func() player.Player { return &fakePlayer{} })
}

func TestSnapshotIncludesFriendlyMediaTitle(t *testing.T) {
	st := newSubscriptionTestState(t)
	meta := `&lt;DIDL-Lite xmlns:dc=&quot;http://purl.org/dc/elements/1.1/&quot;&gt;` +
		`&lt;item&gt;&lt;dc:title&gt;Tom &amp;amp; Jerry&lt;/dc:title&gt;&lt;/item&gt;&lt;/DIDL-Lite&gt;`

	st.SetURI("https://example.test/video.mp4", meta)
	st.SetTransportState("PLAYING")
	st.SetVolume(73)
	st.SetMute(true)
	st.AcquireSession("192.0.2.10", false)

	got := st.Snapshot()
	want := Snapshot{
		TransportState: "PLAYING",
		Title:          "Tom & Jerry",
		Volume:         73,
		Mute:           true,
		SessionOwner:   "192.0.2.10",
		TransportURI:   "https://example.test/video.mp4",
	}
	if got != want {
		t.Fatalf("Snapshot() = %+v, want %+v", got, want)
	}
}

func TestSubscribeReceivesInitialAndChangedSnapshots(t *testing.T) {
	st := newSubscriptionTestState(t)
	updates := make(chan Snapshot, 8)
	unsubscribe := st.Subscribe(func(snapshot Snapshot) { updates <- snapshot })
	defer unsubscribe()

	initial := receiveSnapshot(t, updates)
	if initial.TransportState != "STOPPED" || initial.Volume != 50 {
		t.Fatalf("initial snapshot = %+v", initial)
	}

	meta := `<DIDL-Lite xmlns:dc="http://purl.org/dc/elements/1.1/"><item><dc:title>Movie Night</dc:title></item></DIDL-Lite>`
	st.SetURI("https://example.test/movie.mkv", meta)
	got := receiveSnapshot(t, updates)
	if got.Title != "Movie Night" || got.TransportURI != "https://example.test/movie.mkv" {
		t.Fatalf("SetURI snapshot = %+v", got)
	}

	st.SetTransportState("PAUSED_PLAYBACK")
	if got := receiveSnapshot(t, updates); got.TransportState != "PAUSED_PLAYBACK" {
		t.Fatalf("SetTransportState snapshot = %+v", got)
	}

	st.CommitVolumeRequest("controller", 61, 1)
	if got := receiveSnapshot(t, updates); got.Volume != 61 {
		t.Fatalf("CommitVolumeRequest snapshot = %+v", got)
	}

	st.SetMute(true)
	if got := receiveSnapshot(t, updates); !got.Mute {
		t.Fatalf("SetMute snapshot = %+v", got)
	}

	st.AcquireSession("controller", false)
	if got := receiveSnapshot(t, updates); got.SessionOwner != "controller" {
		t.Fatalf("AcquireSession snapshot = %+v", got)
	}

	st.ReleaseSession("controller")
	if got := receiveSnapshot(t, updates); got.SessionOwner != "" {
		t.Fatalf("ReleaseSession snapshot = %+v", got)
	}
}

func TestSubscriberSeriallyDeliversChangesWhileKeepingUp(t *testing.T) {
	st := newSubscriptionTestState(t)
	updates := make(chan int, 1)
	unsubscribe := st.Subscribe(func(snapshot Snapshot) { updates <- snapshot.Volume })
	defer unsubscribe()
	if initial := receiveVolume(t, updates); initial != 50 {
		t.Fatalf("initial volume = %d, want 50", initial)
	}

	for want := range 32 {
		st.SetVolume(want)
		if got := receiveVolume(t, updates); got != want {
			t.Fatalf("volume notification = %d, want %d", got, want)
		}
	}
}

func TestSubscriptionQueueCoalescesLatestWhenFull(t *testing.T) {
	sub := &subscription{
		queue: make([]Snapshot, 0, subscriptionQueueCapacity),
		done:  make(chan struct{}),
	}
	sub.ready = sync.NewCond(&sub.mu)

	for volume := range subscriptionQueueCapacity + 10 {
		sub.enqueue(Snapshot{Volume: volume})
	}

	sub.mu.Lock()
	defer sub.mu.Unlock()
	if len(sub.queue) != subscriptionQueueCapacity || cap(sub.queue) > subscriptionQueueCapacity {
		t.Fatalf("pending queue len/cap = %d/%d, want bounded by %d", len(sub.queue), cap(sub.queue), subscriptionQueueCapacity)
	}
	if got, want := sub.queue[len(sub.queue)-1].Volume, subscriptionQueueCapacity+9; got != want {
		t.Fatalf("coalesced volume = %d, want newest %d", got, want)
	}
}

func TestSlowSubscriberDoesNotBlockProducersAndReceivesLatest(t *testing.T) {
	st := newSubscriptionTestState(t)
	callbackStarted := make(chan struct{}, 1)
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	delivered := make(chan Snapshot, 2)

	unsubscribe := st.Subscribe(func(snapshot Snapshot) {
		select {
		case callbackStarted <- struct{}{}:
		default:
		}
		<-release
		delivered <- snapshot
	})
	defer func() {
		unblock()
		unsubscribe()
	}()
	select {
	case <-callbackStarted:
	case <-time.After(subscriptionTestTimeout):
		t.Fatal("initial callback did not start")
	}

	sub := soleSubscription(t, st)
	const updates = 1024
	producerDone := make(chan struct{})
	go func() {
		for volume := 1; volume <= updates; volume++ {
			st.SetVolume(volume)
		}
		close(producerDone)
	}()
	select {
	case <-producerDone:
	case <-time.After(subscriptionTestTimeout):
		t.Fatal("state producer blocked behind slow subscriber")
	}

	sub.mu.Lock()
	queueLen, queueCap := len(sub.queue), cap(sub.queue)
	var latest Snapshot
	if queueLen > 0 {
		latest = sub.queue[queueLen-1]
	}
	sub.mu.Unlock()
	if queueLen != 1 || queueCap > subscriptionQueueCapacity {
		t.Fatalf("slow-subscriber queue len/cap = %d/%d, want 1/<=%d", queueLen, queueCap, subscriptionQueueCapacity)
	}
	if latest.Volume != updates {
		t.Fatalf("pending snapshot volume = %d, want latest %d", latest.Volume, updates)
	}

	unblock()
	if initial := receiveSnapshot(t, delivered); initial.Volume != 50 {
		t.Fatalf("initial snapshot volume = %d, want 50", initial.Volume)
	}
	if got := receiveSnapshot(t, delivered); got.Volume != updates {
		t.Fatalf("coalesced snapshot volume = %d, want latest %d", got.Volume, updates)
	}
}

func TestUnsubscribeClearsQueueAndWorkerExits(t *testing.T) {
	st := newSubscriptionTestState(t)
	callbackStarted := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	var callbackCount atomic.Int32

	unsubscribe := st.Subscribe(func(Snapshot) {
		if callbackCount.Add(1) == 1 {
			close(callbackStarted)
			<-release
		}
	})
	defer func() {
		unblock()
		unsubscribe()
	}()
	select {
	case <-callbackStarted:
	case <-time.After(subscriptionTestTimeout):
		t.Fatal("initial callback did not start")
	}

	sub := soleSubscription(t, st)
	st.SetVolume(1)
	st.SetVolume(2)

	cancelReturned := make(chan struct{})
	go func() {
		unsubscribe()
		unsubscribe()
		close(cancelReturned)
	}()
	select {
	case <-cancelReturned:
	case <-time.After(subscriptionTestTimeout):
		t.Fatal("unsubscribe blocked on active callback")
	}

	sub.mu.Lock()
	stopped, pending := sub.stopped, len(sub.queue)
	sub.mu.Unlock()
	if !stopped || pending != 0 {
		t.Fatalf("subscription after cancel: stopped=%v pending=%d, want true/0", stopped, pending)
	}

	unblock()
	select {
	case <-sub.done:
	case <-time.After(subscriptionTestTimeout):
		t.Fatal("subscription worker did not exit after callback returned")
	}
	st.SetVolume(99)
	if got := callbackCount.Load(); got != 1 {
		t.Fatalf("callback count after unsubscribe = %d, want 1", got)
	}
}

func TestSubscriberCallbackCanReadAndMutateState(t *testing.T) {
	st := newSubscriptionTestState(t)
	done := make(chan struct{})
	var once sync.Once
	unsubscribe := st.Subscribe(func(snapshot Snapshot) {
		if snapshot.Volume != 75 {
			return
		}
		if got := st.GetVolume(); got != snapshot.Volume {
			t.Errorf("GetVolume() in callback = %d, want %d", got, snapshot.Volume)
		}
		once.Do(func() {
			st.SetMute(true)
			close(done)
		})
	})
	defer unsubscribe()

	st.SetVolume(75)
	select {
	case <-done:
	case <-time.After(subscriptionTestTimeout):
		t.Fatal("callback deadlocked while calling state Get/Set methods")
	}
	if !st.GetMute() {
		t.Fatal("callback SetMute(true) did not update state")
	}
}

func TestUnsubscribeIsIdempotentAndStopsLaterChanges(t *testing.T) {
	st := newSubscriptionTestState(t)
	updates := make(chan Snapshot, 2)
	unsubscribe := st.Subscribe(func(snapshot Snapshot) { updates <- snapshot })
	_ = receiveSnapshot(t, updates)

	unsubscribe()
	unsubscribe()
	st.SetVolume(99)

	select {
	case got := <-updates:
		t.Fatalf("received snapshot after unsubscribe: %+v", got)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestConcurrentSubscribeUnsubscribeAndMutation(t *testing.T) {
	st := newSubscriptionTestState(t)
	seen := make(chan struct{}, 1)
	const workers = 24
	const iterations = 100

	var wg sync.WaitGroup
	wg.Add(workers)
	for worker := range workers {
		go func(worker int) {
			defer wg.Done()
			for iteration := range iterations {
				unsubscribe := st.Subscribe(func(snapshot Snapshot) {
					_ = st.GetTransportState()
					select {
					case seen <- struct{}{}:
					default:
					}
				})
				st.SetTransportState(fmt.Sprintf("worker-%d-%d", worker, iteration))
				unsubscribe()
			}
		}(worker)
	}
	wg.Wait()

	select {
	case <-seen:
	case <-time.After(subscriptionTestTimeout):
		t.Fatal("concurrent subscribers never received a callback")
	}
}

func TestSubscriberObservesNaturalPlaybackEnd(t *testing.T) {
	fake := &eventPlayer{duration: 95}
	st := newState(t, func() player.Player { return fake })
	st.SetURI("https://example.test/movie.mp4", "")
	st.EnsurePlayer()
	st.SetTransportState("PLAYING")

	updates := make(chan Snapshot, 2)
	unsubscribe := st.Subscribe(func(snapshot Snapshot) { updates <- snapshot })
	defer unsubscribe()
	if initial := receiveSnapshot(t, updates); initial.TransportState != "PLAYING" {
		t.Fatalf("initial snapshot = %+v, want PLAYING", initial)
	}

	fake.emit(player.Event{Name: "end-file", Reason: "eof"})
	if got := receiveSnapshot(t, updates); got.TransportState != "STOPPED" {
		t.Fatalf("end-file snapshot = %+v, want STOPPED", got)
	}
}

func TestSubscriberObservesPreemptedTransportClear(t *testing.T) {
	fake := &fakePlayer{}
	st := newState(t, func() player.Player { return fake })
	st.AcquireSession("first", false)
	st.SetURI("https://example.test/first.mp4", "<title>first</title>")
	st.EnsurePlayer()
	st.SetTransportState("PLAYING")

	updates := make(chan Snapshot, 3)
	unsubscribe := st.Subscribe(func(snapshot Snapshot) { updates <- snapshot })
	defer unsubscribe()
	_ = receiveSnapshot(t, updates)

	acquired, preempted := st.AcquireSession("second", true)
	if !acquired || !preempted {
		t.Fatalf("AcquireSession = (%v, %v), want (true, true)", acquired, preempted)
	}
	if got := receiveSnapshot(t, updates); got.TransportState != "STOPPED" || got.SessionOwner != "second" {
		t.Fatalf("preemption snapshot = %+v", got)
	}
	if err := st.StopPlayer(); err != nil {
		t.Fatalf("StopPlayer: %v", err)
	}
	if got := receiveSnapshot(t, updates); got.TransportURI != "" || got.Title != "" {
		t.Fatalf("cleared transport snapshot = %+v", got)
	}
}

func soleSubscription(t *testing.T, st *PlayerState) *subscription {
	t.Helper()
	st.mu.RLock()
	defer st.mu.RUnlock()
	if len(st.subscribers) != 1 {
		t.Fatalf("subscriber count = %d, want 1", len(st.subscribers))
	}
	for _, sub := range st.subscribers {
		return sub
	}
	return nil
}

func receiveSnapshot(t *testing.T, updates <-chan Snapshot) Snapshot {
	t.Helper()
	select {
	case snapshot := <-updates:
		return snapshot
	case <-time.After(subscriptionTestTimeout):
		t.Fatal("timed out waiting for state snapshot")
		return Snapshot{}
	}
}

func receiveVolume(t *testing.T, updates <-chan int) int {
	t.Helper()
	select {
	case volume := <-updates:
		return volume
	case <-time.After(subscriptionTestTimeout):
		t.Fatal("timed out waiting for volume notification")
		return 0
	}
}
