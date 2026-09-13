package player

import (
	"encoding/json"

	"github.com/tr1v3r/pkg/log"
)

// eventQueueLen bounds how many undelivered events may pile up between the
// IPC reader and the registered handler.
const eventQueueLen = 64

// Event describes one asynchronous mpv JSON IPC event (a message of the form
// {"event": "...", ...}) received from the running IINA instance.
//
// See https://mpv.io/manual/stable/#events for the event catalogue; e.g.
// finished playback surfaces as {"event": "end-file", "reason": "eof"}.
//
// This type is part of the stable player API (consumed by transport-state
// tracking): Name and Reason are fixed; anything else lives in Fields.
type Event struct {
	// Name is the mpv event name, e.g. "end-file" or "file-loaded".
	Name string
	// Reason carries the "reason" member of events that have one, most
	// notably end-file ("eof", "stop", "quit", "error", "redirect",
	// "unknown"). Empty when the event carries no reason.
	Reason string
	// Fields holds the full decoded JSON object of the event, including
	// "event" and "reason". Treat it as read-only.
	Fields map[string]any
}

// OnEvent registers fn as the handler for asynchronous mpv events (audit M2,
// player half: the IPC read loop used to discard events whenever no request
// was in flight, so playback end was invisible to callers).
//
// Semantics — stable API:
//   - fn is invoked from a dedicated dispatcher goroutine, one event at a
//     time in arrival order. It runs without internal locks held, so it may
//     call back into the player (e.g. query GetPosition).
//   - Delivery never blocks the IPC transport: if fn cannot keep up, events
//     are dropped with a log line rather than queued indefinitely.
//   - The registration survives IPC reconnects and player restarts for the
//     lifetime of this IINAPlayer; register once, ideally right after
//     construction. Registering nil disables delivery.
func (p *IINAPlayer) OnEvent(fn func(Event)) {
	p.mu.Lock()
	p.eventFn = fn
	p.mu.Unlock()
}

// dispatchEvent decodes one raw IPC message known to be an event and queues
// it for the dispatcher. It never blocks: a full queue means the handler
// cannot keep up, and dropping (with a log line) is strictly better than
// stalling the IPC reader or any send (audits M2/L1).
func (p *IINAPlayer) dispatchEvent(line []byte) {
	var fields map[string]any
	if err := json.Unmarshal(line, &fields); err != nil {
		log.Warn("unmarshal iina ipc event fail: %v data=%s", err, string(line))
		return
	}
	name, _ := fields["event"].(string)
	if name == "" {
		return
	}
	reason, _ := fields["reason"].(string)
	ev := Event{Name: name, Reason: reason, Fields: fields}
	select {
	case p.events <- ev:
	default:
		log.Warn("iina ipc event dropped (handler backlog): %s", name)
	}
}

// dispatchLoop delivers queued events to the registered handler, one at a
// time in arrival order, until the player is closed. It is started once in
// NewIINAPlayer and survives IPC reconnects and Stop/Play cycles.
func (p *IINAPlayer) dispatchLoop() {
	for {
		select {
		case <-p.done:
			return
		case ev := <-p.events:
			p.mu.Lock()
			fn := p.eventFn
			p.mu.Unlock()
			if fn != nil {
				fn(ev)
			}
		}
	}
}

// shutdownEventsLocked terminates the dispatcher goroutine; events arriving
// afterwards are dropped by the bounded queue. Caller must hold p.mu.
func (p *IINAPlayer) shutdownEventsLocked() {
	select {
	case <-p.done:
	default:
		close(p.done)
	}
}
