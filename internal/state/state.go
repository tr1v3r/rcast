package state

import (
	"context"
	"sync"
	"time"

	"github.com/tr1v3r/pkg/log"

	"github.com/tr1v3r/rcast/internal/config"
	"github.com/tr1v3r/rcast/internal/monitoring"
	"github.com/tr1v3r/rcast/internal/player"
)

const playerMaxIdle = 10 * time.Minute

type PlayerFactory func() player.Player

type PlayerState struct {
	ctx context.Context

	commandMu sync.Mutex
	mu        sync.RWMutex

	player         player.Player
	playerLastUsed time.Time
	playerFactory  PlayerFactory

	transportURI   string
	transportMeta  string
	transportState string
	volume         int
	mute           bool

	sessionOwner string
	sessionSince time.Time
	sessionUsed  time.Time
}

func New(ctx context.Context, cfg config.Config) *PlayerState {
	return NewWithPlayerFactory(ctx, cfg, func() player.Player {
		return player.NewIINAPlayer(cfg.IINAFullscreen)
	})
}

func NewWithPlayerFactory(ctx context.Context, _ config.Config, factory PlayerFactory) *PlayerState {
	s := &PlayerState{
		ctx:            ctx,
		playerFactory:  factory,
		transportState: "STOPPED",
		volume:         50,
	}
	go s.reaper()
	return s
}

func (s *PlayerState) Context() context.Context { return s.ctx }

// Serialize ensures mutating UPnP actions execute in arrival order instead of
// racing independent player goroutines.
func (s *PlayerState) Serialize(fn func()) {
	s.commandMu.Lock()
	defer s.commandMu.Unlock()
	fn()
}

func (s *PlayerState) EnsurePlayer() player.Player {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.playerLastUsed = time.Now()
	if s.player == nil {
		s.player = s.playerFactory()
		monitoring.GetMetrics().RecordPlayerSession()
	}
	return s.player
}

func (s *PlayerState) GetActivePlayer() player.Player {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.player != nil {
		s.playerLastUsed = time.Now()
	}
	return s.player
}

func (s *PlayerState) StopPlayer() error {
	s.mu.Lock()
	p := s.player
	s.player = nil
	s.playerLastUsed = time.Time{}
	s.mu.Unlock()
	if p == nil {
		return nil
	}
	return p.Stop(s.ctx)
}

func (s *PlayerState) Stop() {
	s.commandMu.Lock()
	defer s.commandMu.Unlock()

	if err := s.StopPlayer(); err != nil {
		log.CtxInfo(s.ctx, "player stop error: %v", err)
	}
	s.mu.Lock()
	s.sessionOwner = ""
	s.sessionSince = time.Time{}
	s.sessionUsed = time.Time{}
	s.mu.Unlock()
}

func (s *PlayerState) reaper() {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
			s.reapExpiredPlayer()
		}
	}
}

func (s *PlayerState) reapExpiredPlayer() {
	s.commandMu.Lock()
	defer s.commandMu.Unlock()

	s.mu.Lock()
	playerExpired := s.player != nil && time.Since(s.playerLastUsed) > playerMaxIdle
	sessionExpired := s.player == nil && s.sessionOwner != "" && time.Since(s.sessionUsed) > playerMaxIdle
	expired := playerExpired || sessionExpired
	if expired {
		s.sessionOwner = ""
		s.sessionSince = time.Time{}
		s.sessionUsed = time.Time{}
	}
	s.mu.Unlock()
	if expired {
		_ = s.StopPlayer()
	}
}

func (s *PlayerState) GetURI() (string, string) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.transportURI, s.transportMeta
}

func (s *PlayerState) SetURI(uri, meta string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.transportURI = uri
	s.transportMeta = meta
	s.transportState = "STOPPED"
}

func (s *PlayerState) SetTransportState(st string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.transportState = st
}

func (s *PlayerState) GetTransportState() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.transportState
}

func (s *PlayerState) GetVolume() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.volume
}

func (s *PlayerState) SetVolume(v int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.volume = v
}

func (s *PlayerState) GetMute() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.mute
}

func (s *PlayerState) SetMute(m bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.mute = m
}

func (s *PlayerState) HasSession(controller string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.sessionOwner == "" || s.sessionOwner == controller
}

// AcquireSession returns whether the controller owns the session and whether
// an existing controller was displaced. The caller must stop the old player
// when preempted before executing the new action.
func (s *PlayerState) AcquireSession(controller string, allowPreempt bool) (acquired, preempted bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sessionOwner == "" {
		now := time.Now()
		s.sessionOwner = controller
		s.sessionSince = now
		s.sessionUsed = now
		return true, false
	}
	if s.sessionOwner == controller {
		s.sessionUsed = time.Now()
		return true, false
	}
	if !allowPreempt {
		return false, false
	}
	now := time.Now()
	s.sessionOwner = controller
	s.sessionSince = now
	s.sessionUsed = now
	s.transportState = "STOPPED"
	return true, true
}

func (s *PlayerState) ReleaseSession(controller string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sessionOwner != controller {
		return
	}
	s.sessionOwner = ""
	s.sessionSince = time.Time{}
	s.sessionUsed = time.Time{}
}

func (s *PlayerState) GetSessionOwner() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.sessionOwner
}
