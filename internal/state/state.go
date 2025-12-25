package state

import (
	"context"
	"sync"
	"time"

	"github.com/tr1v3r/pkg/log"

	"github.com/tr1v3r/rcast/internal/config"
	"github.com/tr1v3r/rcast/internal/player"
)

type PlayerState struct {
	ctx context.Context
	cfg config.Config

	mu             sync.RWMutex
	players        map[string]*playerEntry
	TransportURI   string
	TransportMeta  string
	TransportState string // STOPPED | PLAYING | PAUSED_PLAYBACK | TRANSITIONING
	Volume         int
	Mute           bool

	SessionOwner string
	SessionSince time.Time
}

type playerEntry struct {
	player    player.Player
	lastUsed  time.Time
	createdAt time.Time
}

func New(ctx context.Context, cfg config.Config) *PlayerState {
	return &PlayerState{
		ctx:     ctx,
		cfg:     cfg,
		players: make(map[string]*playerEntry),

		TransportState: "STOPPED",
		Volume:         50,
		Mute:           false,
	}
}

func (s *PlayerState) Context() context.Context { return s.ctx }

func (s *PlayerState) GetPlayer(key string) player.Player {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Clean up expired players first
	s.cleanupExpiredPlayers()

	now := time.Now()
	if entry, ok := s.players[key]; ok {
		entry.lastUsed = now
		return entry.player
	} else {
		entry := &playerEntry{
			player:    player.NewIINAPlayer(s.cfg.IINAFullscreen),
			lastUsed:  now,
			createdAt: now,
		}
		s.players[key] = entry
		return entry.player
	}
}
func (s *PlayerState) RemovePlayer(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if entry, ok := s.players[key]; ok {
		// Clean up the player resources before removing
		_ = entry.player.Stop(s.ctx)
		delete(s.players, key)
	}
}

// cleanupExpiredPlayers removes players that haven't been used for more than 10 minutes
func (s *PlayerState) cleanupExpiredPlayers() {
	now := time.Now()
	maxAge := 10 * time.Minute // Players expire after 10 minutes of inactivity

	for key, entry := range s.players {
		if now.Sub(entry.lastUsed) > maxAge {
			// Clean up expired player
			_ = entry.player.Stop(s.ctx)
			delete(s.players, key)
		}
	}
}

func (s *PlayerState) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, entry := range s.players {
		if err := entry.player.Stop(s.ctx); err != nil {
			log.CtxInfo(s.ctx, "player stop error: %v", err)
		}
	}
	// Clear players map after stopping all
	s.players = make(map[string]*playerEntry)
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

func (s *PlayerState) GetTransportState() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.TransportState
}

func (s *PlayerState) GetActivePlayer() player.Player {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.SessionOwner == "" {
		// If no session owner, try to find any active player or return nil
		// For simplicity, just return nil if no session.
		// Alternatively, return the first player found?
		for _, entry := range s.players {
			return entry.player
		}
		return nil
	}
	if entry, ok := s.players[s.SessionOwner]; ok {
		return entry.player
	}
	return nil
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
