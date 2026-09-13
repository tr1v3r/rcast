//go:build !darwin || !cgo

package gui

import (
	"context"
	"errors"

	"github.com/tr1v3r/rcast/internal/app"
	"github.com/tr1v3r/rcast/internal/config"
)

// Run reports that the menu bar front end is unavailable. The stub keeps the
// whole repository buildable with CGO_ENABLED=0 (and on non-darwin systems)
// so headless usage never depends on the GUI toolchain.
func Run(_ context.Context, _ config.Config, _ Deps) error {
	return ErrUnsupported
}

// defaultStartServer never runs on stub builds (Run fails before the
// controller exists); it exists so the portable controller compiles.
func defaultStartServer(*settings) func(ctx context.Context, cfg config.Config) (*app.Runtime, error) {
	return func(context.Context, config.Config) (*app.Runtime, error) {
		return nil, errors.New("gui: no default server launcher on this build")
	}
}
