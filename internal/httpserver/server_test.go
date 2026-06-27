package httpserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/tr1v3r/rcast/internal/config"
	"github.com/tr1v3r/rcast/internal/state"
)

func TestStaticRoutesAndFallback(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	st := state.New(ctx, config.Config{})
	defer st.Stop()
	mux := NewMux()
	RegisterHTTP(mux, "http://127.0.0.1:8200", "uuid:test", st, config.Config{})

	t.Run("unknown path", func(t *testing.T) {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/missing", nil))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("status=%d, want 404", rec.Code)
		}
	})

	t.Run("static route method", func(t *testing.T) {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/device.xml", nil))
		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("status=%d, want 405", rec.Code)
		}
	})

	t.Run("head has no body", func(t *testing.T) {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodHead, "/device.xml", nil))
		if rec.Code != http.StatusOK || rec.Body.Len() != 0 {
			t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
		}
	})
}
