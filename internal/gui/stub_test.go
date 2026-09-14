//go:build !darwin || !cgo

package gui

import (
	"context"
	"errors"
	"testing"

	"github.com/tr1v3r/rcast/internal/config"
)

// TestStubRunReturnsUnsupported pins the stub contract: without the macOS
// front end, Run fails with a clear error instead of breaking headless
// builds. This file only compiles on stub builds, so running the suite with
// CGO_ENABLED=0 exercises it.
func TestStubRunReturnsUnsupported(t *testing.T) {
	deps := Deps{SaveSettings: func(Settings) error { return nil }}
	err := Run(context.Background(), config.Config{}, deps)
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("Run = %v, want ErrUnsupported", err)
	}
	if got := ErrUnsupported.Error(); got != "GUI requires macOS with cgo enabled" {
		t.Errorf("ErrUnsupported = %q", got)
	}
}
