package state

import (
	"bytes"
	"encoding/xml"
	"html"
	"strings"
	"sync"
)

// Snapshot is an immutable view of the player state for user interfaces and
// other observers.
type Snapshot struct {
	TransportState string
	Title          string
	Volume         int
	Mute           bool
	SessionOwner   string
	TransportURI   string
}

// subscriptionQueueCapacity is deliberately one: snapshots describe the
// complete current state, so while a subscriber is busy only the newest pending
// value has any meaning. This bounds memory independently of producer rate.
const subscriptionQueueCapacity = 1

type subscription struct {
	callback func(Snapshot)

	mu      sync.Mutex
	ready   *sync.Cond
	queue   []Snapshot
	stopped bool
	done    chan struct{}
}

// Snapshot returns a consistent view of the current player state.
func (s *PlayerState) Snapshot() Snapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.snapshotLocked()
}

// Subscribe registers callback for state snapshots. The initial snapshot and
// later changes are delivered asynchronously and serially for this subscriber.
// While callback is busy, pending changes are coalesced to the newest complete
// snapshot. The returned function is safe to call more than once to unsubscribe.
func (s *PlayerState) Subscribe(callback func(Snapshot)) func() {
	if callback == nil {
		return func() {}
	}

	sub := &subscription{
		callback: callback,
		queue:    make([]Snapshot, 0, subscriptionQueueCapacity),
		done:     make(chan struct{}),
	}
	sub.ready = sync.NewCond(&sub.mu)

	s.mu.Lock()
	if s.subscribers == nil {
		s.subscribers = make(map[uint64]*subscription)
	}
	id := s.nextSubscriberID
	s.nextSubscriberID++
	s.subscribers[id] = sub
	initial := s.snapshotLocked()
	s.mu.Unlock()

	go sub.run(initial)

	return func() {
		s.mu.Lock()
		delete(s.subscribers, id)
		s.mu.Unlock()
		sub.stop()
	}
}

// notificationLocked captures both the state and current subscribers while s.mu
// is write-locked. Enqueue with notify before unlocking to preserve mutation
// order; callbacks themselves always run in the subscription worker.
func (s *PlayerState) notificationLocked() (Snapshot, []*subscription) {
	snapshot := s.snapshotLocked()
	subscribers := make([]*subscription, 0, len(s.subscribers))
	for _, sub := range s.subscribers {
		subscribers = append(subscribers, sub)
	}
	return snapshot, subscribers
}

func (s *PlayerState) snapshotLocked() Snapshot {
	return Snapshot{
		TransportState: s.transportState,
		Title:          transportTitle(s.transportMeta),
		Volume:         s.volume,
		Mute:           s.mute,
		SessionOwner:   s.sessionOwner,
		TransportURI:   s.transportURI,
	}
}

func notify(snapshot Snapshot, subscribers []*subscription) {
	for _, sub := range subscribers {
		sub.enqueue(snapshot)
	}
}

func (s *subscription) run(initial Snapshot) {
	defer close(s.done)
	s.callback(initial)
	for {
		s.mu.Lock()
		for len(s.queue) == 0 && !s.stopped {
			s.ready.Wait()
		}
		if s.stopped {
			s.mu.Unlock()
			return
		}
		snapshot := s.queue[0]
		s.queue[0] = Snapshot{}
		s.queue = s.queue[1:]
		s.mu.Unlock()
		s.callback(snapshot)
	}
}

// enqueue never waits for callback execution or queue capacity. The pending
// slot is coalesced to the newest complete snapshot while the subscriber is
// busy, so a slow UI remains eventually consistent without unbounded growth.
func (s *subscription) enqueue(snapshot Snapshot) {
	s.mu.Lock()
	if !s.stopped {
		if len(s.queue) == subscriptionQueueCapacity {
			s.queue[len(s.queue)-1] = snapshot
		} else {
			s.queue = append(s.queue, snapshot)
		}
		s.ready.Signal()
	}
	s.mu.Unlock()
}

func (s *subscription) stop() {
	s.mu.Lock()
	s.stopped = true
	s.queue = nil
	s.ready.Broadcast()
	s.mu.Unlock()
}

func transportTitle(meta string) string {
	for range 3 {
		if title := xmlText(meta, "title"); title != "" {
			return title
		}
		decoded := html.UnescapeString(meta)
		if decoded == meta {
			return ""
		}
		meta = decoded
	}
	return ""
}

func xmlText(document, tag string) string {
	decoder := xml.NewDecoder(bytes.NewBufferString(document))
	for {
		token, err := decoder.Token()
		if err != nil {
			return ""
		}
		start, ok := token.(xml.StartElement)
		if !ok || start.Name.Local != tag {
			continue
		}
		var value string
		if err := decoder.DecodeElement(&value, &start); err != nil {
			return ""
		}
		return strings.TrimSpace(value)
	}
}
