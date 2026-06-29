package httpserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tr1v3r/rcast/internal/config"
	"github.com/tr1v3r/rcast/internal/monitoring"
	"github.com/tr1v3r/rcast/internal/state"
)

// newTestMux builds a registered mux backed by a fresh, isolated player state.
// The returned cancel func must be deferred to release the state's background
// goroutines.
func newTestMux(t *testing.T) (*http.ServeMux, *state.PlayerState) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	st := state.New(ctx, config.Config{})
	t.Cleanup(st.Stop)
	mux := NewMux()
	RegisterHTTP(mux, "http://127.0.0.1:8200", "uuid:test", st, config.Config{})
	return mux, st
}

func TestStaticRoutesAndFallback(t *testing.T) {
	mux, _ := newTestMux(t)

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

func TestRootRouteMatrix(t *testing.T) {
	mux, _ := newTestMux(t)

	t.Run("GET returns 200 with RCast body", func(t *testing.T) {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("status=%d, want 200", rec.Code)
		}
		if !strings.Contains(rec.Body.String(), "RCast") {
			t.Fatalf("body missing 'RCast': %q", rec.Body.String())
		}
	})

	t.Run("HEAD returns 200 with empty body", func(t *testing.T) {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodHead, "/", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("status=%d, want 200", rec.Code)
		}
		if rec.Body.Len() != 0 {
			t.Fatalf("HEAD body non-empty: %q", rec.Body.String())
		}
	})

	t.Run("POST returns 405 with Allow", func(t *testing.T) {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/", nil))
		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("status=%d, want 405", rec.Code)
		}
		if got := rec.Header().Get("Allow"); got != "GET, HEAD" {
			t.Fatalf("Allow=%q, want %q", got, "GET, HEAD")
		}
	})

	t.Run("unknown subpath 404", func(t *testing.T) {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/nope", nil))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("status=%d, want 404", rec.Code)
		}
	})
}

func TestStaticXMLRoutes(t *testing.T) {
	mux, _ := newTestMux(t)

	routes := []string{
		"/device.xml",
		"/upnp/service/avtransport.xml",
		"/upnp/service/renderingcontrol.xml",
		"/upnp/service/connectionmanager.xml",
	}

	for _, route := range routes {
		route := route
		t.Run(route+" GET", func(t *testing.T) {
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, route, nil))
			if rec.Code != http.StatusOK {
				t.Fatalf("status=%d, want 200", rec.Code)
			}
			ct := rec.Header().Get("Content-Type")
			if !strings.Contains(ct, "application/xml") {
				t.Fatalf("Content-Type=%q, want contains application/xml", ct)
			}
			if rec.Body.Len() == 0 {
				t.Fatalf("GET body empty")
			}
		})

		t.Run(route+" HEAD empty body", func(t *testing.T) {
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, httptest.NewRequest(http.MethodHead, route, nil))
			if rec.Code != http.StatusOK {
				t.Fatalf("status=%d, want 200", rec.Code)
			}
			if rec.Body.Len() != 0 {
				t.Fatalf("HEAD body non-empty: %q", rec.Body.String())
			}
		})

		t.Run(route+" POST 405", func(t *testing.T) {
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, route, nil))
			if rec.Code != http.StatusMethodNotAllowed {
				t.Fatalf("status=%d, want 405", rec.Code)
			}
			if got := rec.Header().Get("Allow"); got != "GET, HEAD" {
				t.Fatalf("Allow=%q, want %q", got, "GET, HEAD")
			}
		})
	}
}

func TestMetricsRoute(t *testing.T) {
	mux, _ := newTestMux(t)

	t.Run("GET returns text/plain metrics", func(t *testing.T) {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("status=%d, want 200", rec.Code)
		}
		ct := rec.Header().Get("Content-Type")
		if !strings.HasPrefix(ct, "text/plain") {
			t.Fatalf("Content-Type=%q, want prefix text/plain", ct)
		}
		if !strings.Contains(rec.Body.String(), "rcast_") {
			t.Fatalf("body missing rcast_ metric: %q", rec.Body.String())
		}
	})

	t.Run("HEAD empty body", func(t *testing.T) {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodHead, "/metrics", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("status=%d, want 200", rec.Code)
		}
		if rec.Body.Len() != 0 {
			t.Fatalf("HEAD body non-empty: %q", rec.Body.String())
		}
	})

	t.Run("POST 405", func(t *testing.T) {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/metrics", nil))
		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("status=%d, want 405", rec.Code)
		}
	})
}

// A POST with a SOAPACTION header to a control endpoint must route into the
// registered handler — i.e. NOT return 404 (no such route) or 405-from-mux
// (the route exists but rejects POST). The handler may answer 200 or a SOAP
// fault, both are acceptable here; the test only asserts routing is correct.
func TestControlEndpointsRoute(t *testing.T) {
	mux, _ := newTestMux(t)

	endpoints := []string{
		"/upnp/control/avtransport",
		"/upnp/control/renderingcontrol",
		"/upnp/control/connectionmanager",
	}

	soapBody := `<s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/"><s:Body><u:Play xmlns:u="service"><InstanceID>0</InstanceID><Speed>1</Speed></u:Play></s:Body></s:Envelope>`

	for _, ep := range endpoints {
		ep := ep
		t.Run(ep, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, ep, strings.NewReader(soapBody))
			req.Header.Set("SOAPACTION", `"service#Play"`)
			req.RemoteAddr = "127.0.0.1:1234"
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			if rec.Code == http.StatusNotFound {
				t.Fatalf("control endpoint returned 404 (route missing); body=%s", rec.Body.String())
			}
			if rec.Code == http.StatusMethodNotAllowed {
				t.Fatalf("control endpoint returned 405 (POST not routed to handler); body=%s", rec.Body.String())
			}
		})
	}
}

func TestEventEndpointRoutes(t *testing.T) {
	mux, _ := newTestMux(t)

	// Event endpoints exist and route to EventHandler: a GET must produce the
	// handler's 405 (Allow: SUBSCRIBE, UNSUBSCRIBE), proving the route exists
	// and the handler (not the mux) answered.
	t.Run("GET /upnp/event/avtransport -> handler 405", func(t *testing.T) {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/upnp/event/avtransport", nil))
		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("status=%d, want 405 from EventHandler", rec.Code)
		}
		if got := rec.Header().Get("Allow"); got != "SUBSCRIBE, UNSUBSCRIBE" {
			t.Fatalf("Allow=%q, want SUBSCRIBE, UNSUBSCRIBE", got)
		}
	})
}

// TestLogMiddlewareRecordsMetrics asserts the global metrics singleton grew by
// the number of requests the middleware observed, using a before/after delta
// (the singleton is process-global and cannot be reset).
func TestLogMiddlewareRecordsMetrics(t *testing.T) {
	m := monitoring.GetMetrics()
	beforeTotal := m.HTTPRequestsTotal
	beforeGET := m.HTTPRequestsByMethod[http.MethodGet]
	beforePOST := m.HTTPRequestsByMethod[http.MethodPost]

	wrapped := LogMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))

	requests := []string{
		http.MethodGet,
		http.MethodPost,
		http.MethodGet,
	}
	for _, method := range requests {
		rec := httptest.NewRecorder()
		wrapped.ServeHTTP(rec, httptest.NewRequest(method, "/anything", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("status=%d for %s, want 200", rec.Code, method)
		}
	}

	afterTotal := m.HTTPRequestsTotal
	afterGET := m.HTTPRequestsByMethod[http.MethodGet]
	afterPOST := m.HTTPRequestsByMethod[http.MethodPost]

	if delta := afterTotal - beforeTotal; delta != int64(len(requests)) {
		t.Fatalf("HTTPRequestsTotal delta=%d, want %d", delta, len(requests))
	}
	if delta := afterGET - beforeGET; delta != 2 {
		t.Fatalf("GET delta=%d, want 2", delta)
	}
	if delta := afterPOST - beforePOST; delta != 1 {
		t.Fatalf("POST delta=%d, want 1", delta)
	}
}
