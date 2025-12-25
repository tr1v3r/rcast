package player

import "context"

type Player interface {
	Play(ctx context.Context, uri string, volume int) error
	Pause(ctx context.Context) error
	Stop(ctx context.Context) error
	SetVolume(ctx context.Context, v int) error
	SetMute(ctx context.Context, m bool) error
	SetFullscreen(ctx context.Context, f bool) error
	SetTitle(ctx context.Context, title string) error
	Screenshot(ctx context.Context, path string) error
	SetSpeed(ctx context.Context, speed float64) error
	Seek(ctx context.Context, seconds float64) error
	GetPosition(ctx context.Context) (float64, error)
	GetDuration(ctx context.Context) (float64, error)
}
