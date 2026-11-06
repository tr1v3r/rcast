package gui

import (
	"context"
	"sync/atomic"

	"github.com/tr1v3r/pkg/log"
)

// guiLogHandler filters records before forwarding them to a stream handler
// whose level stays fixed for its lifetime. The menu can then change verbosity
// while HTTP and discovery goroutines log through the same handler.
type guiLogHandler struct {
	*log.StreamHandler
	level atomic.Uint32
}

func (h *guiLogHandler) SetLevel(level log.Level) { h.level.Store(uint32(level)) }

func (h *guiLogHandler) Output(level log.Level, ctx context.Context, format string, args ...any) {
	if uint32(level) >= h.level.Load() {
		h.StreamHandler.Output(level, ctx, format, args...)
	}
}

// installGUILogHandler runs before the first runtime is started. Handler
// registration in the upstream package is unsafe once concurrent logging begins.
func installGUILogHandler() func(log.Level) {
	handler := &guiLogHandler{StreamHandler: log.NewStreamHandler(log.TraceLevel)}
	handler.SetLevel(log.InfoLevel)
	log.ClearHandler()
	log.RegisterHandler(handler)
	return handler.SetLevel
}
