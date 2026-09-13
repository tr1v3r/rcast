package player

// Regression tests for the mpv event surface (audit M2, player half): a
// background reader consumes asynchronous events for the whole connection
// lifetime and delivers them to the OnEvent handler, without ever blocking
// the send path and across IPC reconnects.

import (
	"context"
	"sync"
	"testing"
	"time"
)

// TestOnEvent_EndFileDelivered: an end-file event pushed by the peer reaches
// the registered handler, decoded into Name/Reason/Fields.
func TestOnEvent_EndFileDelivered(t *testing.T) {
	s := newScriptedMPVServer(t)
	p, _ := probeTestPlayer(t, s)

	got := make(chan Event, 8)
	p.OnEvent(func(ev Event) { got <- ev })

	// Establish the connection and its background reader.
	if err := p.SetVolume(context.Background(), 10); err != nil {
		t.Fatalf("SetVolume: %v", err)
	}

	s.pushLine(`{"event":"end-file","reason":"eof","playlist_entry_id":7}`)

	select {
	case ev := <-got:
		if ev.Name != "end-file" {
			t.Fatalf("event name = %q, want end-file", ev.Name)
		}
		if ev.Reason != "eof" {
			t.Fatalf("event reason = %q, want eof", ev.Reason)
		}
		if id, ok := ev.Fields["playlist_entry_id"].(float64); !ok || id != 7 {
			t.Fatalf("Fields[playlist_entry_id] = %v, want 7", ev.Fields["playlist_entry_id"])
		}
	case <-time.After(2 * time.Second):
		t.Fatal("end-file event was not delivered to the OnEvent handler")
	}
}

// TestOnEvent_EventStormDoesNotBlockSend: while the handler is slow and the
// peer floods events, concurrent commands must all succeed — events may be
// dropped, sends may never stall.
func TestOnEvent_EventStormDoesNotBlockSend(t *testing.T) {
	s := newScriptedMPVServer(t)
	p, _ := probeTestPlayer(t, s)

	var received int
	var mu sync.Mutex
	p.OnEvent(func(Event) {
		mu.Lock()
		received++
		mu.Unlock()
		time.Sleep(2 * time.Millisecond) // deliberately slow consumer
	})

	if err := p.SetVolume(context.Background(), 10); err != nil {
		t.Fatalf("initial SetVolume: %v", err)
	}

	const flood = 200
	for range flood {
		s.pushLine(`{"event":"playback-restart"}`)
	}

	const workers = 8
	errs := make([]error, workers)
	var wg sync.WaitGroup
	wg.Add(workers)
	for i := range workers {
		go func(i int) {
			defer wg.Done()
			errs[i] = p.SetVolume(context.Background(), i)
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("worker %d SetVolume during event storm: %v", i, err)
		}
	}

	deadline := time.After(3 * time.Second)
	for {
		mu.Lock()
		n := received
		mu.Unlock()
		if n > 0 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("no events delivered at all during the storm")
		case <-time.After(20 * time.Millisecond):
		}
	}
}

// TestOnEvent_SurvivesReconnect: the handler registration and the dispatcher
// are player-lifetime constructs; after the connection dies and a later
// command reconnects, events on the new connection still arrive.
func TestOnEvent_SurvivesReconnect(t *testing.T) {
	s := newScriptedMPVServer(t)
	p, _ := probeTestPlayer(t, s)

	got := make(chan Event, 8)
	p.OnEvent(func(ev Event) { got <- ev })

	if err := p.SetVolume(context.Background(), 10); err != nil {
		t.Fatalf("SetVolume #1: %v", err)
	}

	s.dropConns() // server closes the connection: client reader sees EOF

	if err := p.SetVolume(context.Background(), 20); err != nil {
		t.Fatalf("SetVolume #2 after reconnect: %v", err)
	}

	s.pushLine(`{"event":"end-file","reason":"stop"}`)
	select {
	case ev := <-got:
		if ev.Name != "end-file" || ev.Reason != "stop" {
			t.Fatalf("event = %+v, want end-file/stop", ev)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("event after reconnect was not delivered")
	}
}

// TestOnEvent_NilHandlerSafe: registering nil disables delivery without
// breaking the transport.
func TestOnEvent_NilHandlerSafe(t *testing.T) {
	s := newScriptedMPVServer(t)
	p, _ := probeTestPlayer(t, s)

	p.OnEvent(nil)
	if err := p.SetVolume(context.Background(), 10); err != nil {
		t.Fatalf("SetVolume with nil handler: %v", err)
	}
	s.pushLine(`{"event":"idle"}`)
	if err := p.SetVolume(context.Background(), 20); err != nil {
		t.Fatalf("SetVolume after event with nil handler: %v", err)
	}
}
