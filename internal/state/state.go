package state

import (
	"context"
	"sync"
	"time"

	"github.com/tr1v3r/pkg/log"

	"github.com/tr1v3r/rcast/internal/player"
)

type PlayerState struct {
	ctx context.Context

	mu             sync.RWMutex
	players        map[string]player.Player
	TransportURI   string
	TransportMeta  string
	TransportState string // STOPPED | PLAYING | PAUSED_PLAYBACK | TRANSITIONING
	Volume         int
	Mute           bool

	SessionOwner string
	SessionSince time.Time
}

func New(ctx context.Context) *PlayerState {
	return &PlayerState{
		ctx:     ctx,
		players: make(map[string]player.Player),

		TransportState: "STOPPED",
		Volume:         50,
		Mute:           false,
	}
}

func (s *PlayerState) Context() context.Context { return s.ctx }

func (s *PlayerState) GetPlayer(key string) player.Player {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p, ok := s.players[key]; !ok {
		s.players[key] = player.NewIINAPlayer()
		return s.players[key]
	} else {
		return p
	}
}
func (s *PlayerState) RemovePlayer(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.players, key)
}

func (s *PlayerState) Stop() {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, p := range s.players {
		if err := p.Stop(s.ctx); err != nil {
			log.CtxInfo(s.ctx, "player stop error: %v", err)
		}
	}
}

func (s *PlayerState) GetURI() (string, string) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.TransportURI, s.TransportMeta
}

func (s *PlayerState) SetURI(uri, meta string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.TransportURI = uri
	s.TransportMeta = meta
	s.TransportState = "STOPPED"
}

func (s *PlayerState) SetTransportState(st string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.TransportState = st
}

func (s *PlayerState) GetVolume() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.Volume
}

func (s *PlayerState) SetVolume(v int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Volume = v
}

func (s *PlayerState) GetMute() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.Mute
}

func (s *PlayerState) SetMute(m bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Mute = m
}

// 会话管理
func (s *PlayerState) HasSession(controller string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.SessionOwner == "" || s.SessionOwner == controller
}

func (s *PlayerState) AcquireOrCheckSession(controller string, allowPreempt bool) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.SessionOwner == "" {
		s.SessionOwner = controller
		s.SessionSince = time.Now()
		return true
	}
	if s.SessionOwner == controller {
		return true
	}
	if allowPreempt {
		s.SessionOwner = controller
		s.SessionSince = time.Now()
		return true
	}
	return false
}

func (s *PlayerState) ReleaseSession() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.SessionOwner = ""
	s.SessionSince = time.Time{}
}
