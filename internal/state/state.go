package state

import (
	"context"
	"math"
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

	player               player.Player
	playerLastUsed       time.Time
	playerFactory        PlayerFactory
	playerGeneration     uint64
	clearTransportOnStop bool

	transportURI   string
	transportMeta  string
	transportState string
	// finalPosition/finalDuration pin where the last playback naturally ended
	// (audit M2, state half): media that finishes by itself leaves the
	// keep-open player paused at the end, and once that window is gone a late
	// observer should still be able to report RelTime=duration instead of
	// 00:00:00.
	finalPosition      float64
	finalDuration      float64
	finalPositionValid bool
	volume             int
	volumeMapping      volumeMapping
	mute               bool

	subscribers      map[uint64]*subscription
	nextSubscriberID uint64

	sessionOwner string
	sessionSince time.Time
	sessionUsed  time.Time
}

type volumeMapping struct {
	active     bool
	controller string
	raw        int
	applied    float64
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

// Serialize ensures mutating actions execute in arrival order.
func (s *PlayerState) Serialize(fn func()) {
	SerializeResult(s, func() struct{} {
		fn()
		return struct{}{}
	})
}

// SerializeResult runs a mutating command in arrival order and returns the
// computed result after releasing the command lock. Callers can therefore
// render a response into memory in fn and perform potentially slow network I/O
// only after this function returns.
func SerializeResult[T any](s *PlayerState, fn func() T) T {
	s.commandMu.Lock()
	defer s.commandMu.Unlock()
	return fn()
}

func (s *PlayerState) EnsurePlayer() player.Player {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.playerLastUsed = time.Now()
	if s.player == nil {
		p := s.playerFactory()
		s.player = p
		s.playerGeneration++
		generation := s.playerGeneration
		s.finalPositionValid = false
		if events, ok := p.(interface{ OnEvent(func(player.Event)) }); ok {
			events.OnEvent(func(event player.Event) {
				s.handlePlayerEvent(p, generation, event)
			})
		}
		monitoring.GetMetrics().RecordPlayerSession()
	}
	return s.player
}

func (s *PlayerState) handlePlayerEvent(source player.Player, generation uint64, event player.Event) {
	if event.Name != "end-file" {
		return
	}

	// Query without s.mu held: event handlers are allowed to call back into the
	// player, and an IPC round trip must never block state readers.
	duration, err := source.GetDuration(s.ctx)
	if err != nil {
		log.CtxWarn(s.ctx, "get duration after playback end: %v", err)
	}

	s.mu.Lock()
	// A late event from a preempted/stopped player must not overwrite the new
	// player's state.
	if s.player == nil || s.playerGeneration != generation {
		s.mu.Unlock()
		return
	}
	s.transportState = "STOPPED"
	if err == nil && duration >= 0 {
		s.finalPosition = duration
		s.finalDuration = duration
		s.finalPositionValid = true
	}
	snapshot, subscribers := s.notificationLocked()
	notify(snapshot, subscribers)
	s.mu.Unlock()
}

// GetPlaybackPosition returns the last known position and duration. Natural
// playback completion is pinned at the media duration even after the player
// window has been reaped.
func (s *PlayerState) GetPlaybackPosition(ctx context.Context) (position, duration float64, positionErr, durationErr error) {
	s.mu.Lock()
	if s.finalPositionValid {
		position, duration = s.finalPosition, s.finalDuration
		s.mu.Unlock()
		return position, duration, nil, nil
	}
	p := s.player
	if p != nil {
		s.playerLastUsed = time.Now()
	}
	s.mu.Unlock()
	if p == nil {
		return 0, 0, nil, nil
	}
	position, positionErr = p.GetPosition(ctx)
	duration, durationErr = p.GetDuration(ctx)
	return position, duration, positionErr, durationErr
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
	return s.stopPlayer(s.ctx)
}

// stopPlayer stops the current player before detaching it. A failed stop keeps
// the reference reachable so callers can retry instead of orphaning IINA.
func (s *PlayerState) stopPlayer(ctx context.Context) error {
	s.mu.RLock()
	p := s.player
	generation := s.playerGeneration
	s.mu.RUnlock()
	if p == nil {
		s.mu.Lock()
		if s.clearTransportOnStop {
			s.clearTransportLocked()
			snapshot, subscribers := s.notificationLocked()
			notify(snapshot, subscribers)
		}
		s.mu.Unlock()
		return nil
	}
	if err := p.Stop(ctx); err != nil {
		return err
	}
	s.mu.Lock()
	if s.player != nil && s.playerGeneration == generation {
		s.player = nil
		s.playerLastUsed = time.Time{}
		s.transportState = "STOPPED"
		if s.clearTransportOnStop {
			s.clearTransportLocked()
		}
		snapshot, subscribers := s.notificationLocked()
		notify(snapshot, subscribers)
	}
	s.mu.Unlock()
	return nil
}

func (s *PlayerState) clearTransportLocked() {
	s.transportURI = ""
	s.transportMeta = ""
	s.finalPositionValid = false
	s.clearTransportOnStop = false
}

func (s *PlayerState) Stop() {
	s.commandMu.Lock()
	defer s.commandMu.Unlock()

	// The application context is already cancelled during shutdown. Give the
	// player a fresh window to deliver mpv's quit command so IINA does not
	// survive with an orphaned, unlinked IPC socket.
	stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	if err := s.stopPlayer(stopCtx); err != nil {
		log.CtxInfo(s.ctx, "player stop error: %v", err)
	}
	cancel()

	s.mu.Lock()
	s.sessionOwner = ""
	s.sessionSince = time.Time{}
	s.sessionUsed = time.Time{}
	s.volumeMapping = volumeMapping{}
	s.transportState = "STOPPED"
	snapshot, subscribers := s.notificationLocked()
	notify(snapshot, subscribers)
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

	s.mu.RLock()
	// playerLastUsed measures controller traffic, not playback activity. Never
	// reap a playing instance merely because its controller went quiet.
	playerExpired := s.player != nil && s.transportState != "PLAYING" && time.Since(s.playerLastUsed) > playerMaxIdle
	sessionExpired := s.player == nil && s.sessionOwner != "" && time.Since(s.sessionUsed) > playerMaxIdle
	s.mu.RUnlock()

	if playerExpired {
		if err := s.StopPlayer(); err != nil {
			log.CtxWarn(s.ctx, "reap expired player: %v", err)
			return
		}
	}
	if !playerExpired && !sessionExpired {
		return
	}

	s.mu.Lock()
	s.sessionOwner = ""
	s.sessionSince = time.Time{}
	s.sessionUsed = time.Time{}
	s.volumeMapping = volumeMapping{}
	s.transportState = "STOPPED"
	snapshot, subscribers := s.notificationLocked()
	notify(snapshot, subscribers)
	s.mu.Unlock()
}

func (s *PlayerState) GetURI() (string, string) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.transportURI, s.transportMeta
}

func (s *PlayerState) SetURI(uri, meta string) {
	s.mu.Lock()
	s.transportURI = uri
	s.transportMeta = meta
	s.transportState = "STOPPED"
	s.finalPositionValid = false
	snapshot, subscribers := s.notificationLocked()
	notify(snapshot, subscribers)
	s.mu.Unlock()
}

func (s *PlayerState) SetTransportState(st string) {
	s.mu.Lock()
	s.transportState = st
	if st == "PLAYING" {
		s.finalPositionValid = false
	}
	snapshot, subscribers := s.notificationLocked()
	notify(snapshot, subscribers)
	s.mu.Unlock()
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
	s.volume = v
	s.volumeMapping = volumeMapping{}
	snapshot, subscribers := s.notificationLocked()
	notify(snapshot, subscribers)
	s.mu.Unlock()
}

// PreviewVolumeRequest translates a controller-domain volume without changing
// state. CommitVolumeRequest must be called after the player accepts the value.
func (s *PlayerState) PreviewVolumeRequest(controller string, requested int, scale float64) int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	applied, _ := mapVolumeRequest(s.volume, s.volumeMapping, controller, requested, scale)
	return applied
}

func (s *PlayerState) CommitVolumeRequest(controller string, requested int, scale float64) int {
	s.mu.Lock()
	applied, mapping := mapVolumeRequest(s.volume, s.volumeMapping, controller, requested, scale)
	s.volume = applied
	s.volumeMapping = mapping
	snapshot, subscribers := s.notificationLocked()
	notify(snapshot, subscribers)
	s.mu.Unlock()
	return applied
}

func (s *PlayerState) GetReportedVolume(controller string, scale float64) int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if scale > 1 && s.volumeMapping.active && s.volumeMapping.controller == controller {
		return s.volumeMapping.raw
	}
	return s.volume
}

func mapVolumeRequest(currentVolume int, current volumeMapping, controller string, requested int, scale float64) (int, volumeMapping) {
	if scale <= 1 {
		return requested, volumeMapping{}
	}
	if !current.active || current.controller != controller {
		current = volumeMapping{
			active:     true,
			controller: controller,
			raw:        currentVolume,
			applied:    float64(currentVolume),
		}
	}
	current.applied += float64(requested-current.raw) * scale
	current.applied = min(max(current.applied, 0), 100)
	current.raw = requested
	return int(math.Round(current.applied)), current
}

func (s *PlayerState) GetMute() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.mute
}

func (s *PlayerState) SetMute(m bool) {
	s.mu.Lock()
	s.mute = m
	snapshot, subscribers := s.notificationLocked()
	notify(snapshot, subscribers)
	s.mu.Unlock()
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
	if s.sessionOwner == "" {
		now := time.Now()
		s.sessionOwner = controller
		s.sessionSince = now
		s.sessionUsed = now
		s.volumeMapping = volumeMapping{}
		snapshot, subscribers := s.notificationLocked()
		notify(snapshot, subscribers)
		s.mu.Unlock()
		return true, false
	}
	if s.sessionOwner == controller {
		s.sessionUsed = time.Now()
		// If the previous preemption could not stop the displaced player, keep
		// reporting preempted so requireSession retries cleanup before allowing
		// the new owner to act on the predecessor's transport.
		preempted := s.clearTransportOnStop
		s.mu.Unlock()
		return true, preempted
	}
	if !allowPreempt {
		s.mu.Unlock()
		return false, false
	}
	now := time.Now()
	s.sessionOwner = controller
	s.sessionSince = now
	s.sessionUsed = now
	s.transportState = "STOPPED"
	s.clearTransportOnStop = true
	s.volumeMapping = volumeMapping{}
	snapshot, subscribers := s.notificationLocked()
	notify(snapshot, subscribers)
	s.mu.Unlock()
	return true, true
}

func (s *PlayerState) ReleaseSession(controller string) {
	s.mu.Lock()
	if s.sessionOwner != controller {
		s.mu.Unlock()
		return
	}
	s.sessionOwner = ""
	s.sessionSince = time.Time{}
	s.sessionUsed = time.Time{}
	s.volumeMapping = volumeMapping{}
	snapshot, subscribers := s.notificationLocked()
	notify(snapshot, subscribers)
	s.mu.Unlock()
}

func (s *PlayerState) GetSessionOwner() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.sessionOwner
}
