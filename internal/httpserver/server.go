package httpserver

import (
	"net/http"
	"time"

	"github.com/tr1v3r/pkg/log"

	"github.com/tr1v3r/rcast/internal/config"
	"github.com/tr1v3r/rcast/internal/monitoring"
	"github.com/tr1v3r/rcast/internal/state"
	"github.com/tr1v3r/rcast/internal/upnp"
)

func NewMux() *http.ServeMux {
	return http.NewServeMux()
}

func RegisterHTTP(mux *http.ServeMux, baseURL, deviceUUID string, st *state.PlayerState, cfg config.Config) {
	mux.HandleFunc("/device.xml", staticXML(func() string { return upnp.DeviceDescriptionXML(baseURL, deviceUUID) }))
	mux.HandleFunc("/upnp/service/avtransport.xml", staticXML(upnp.SCPDAVTransportXML))
	mux.HandleFunc("/upnp/service/renderingcontrol.xml", staticXML(upnp.SCPDRenderingXML))
	mux.HandleFunc("/upnp/service/connectionmanager.xml", staticXML(upnp.SCPDConnectionManagerXML))

	mux.HandleFunc("/upnp/control/avtransport", upnp.AVTransportHandler(st, cfg))
	mux.HandleFunc("/upnp/control/renderingcontrol", upnp.RenderingControlHandler(st, cfg))
	mux.HandleFunc("/upnp/control/connectionmanager", upnp.ConnectionManagerHandler(st, cfg))

	// 事件端点
	mux.HandleFunc("/upnp/event/avtransport", upnp.EventHandler)
	mux.HandleFunc("/upnp/event/renderingcontrol", upnp.EventHandler)
	mux.HandleFunc("/upnp/event/connectionmanager", upnp.EventHandler)

	// 指标
	mux.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		if r.Method != http.MethodHead {
			_, _ = w.Write([]byte(monitoring.GetMetrics().RenderText()))
		}
	})

	// 根
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "text/plain")
		if r.Method != http.MethodHead {
			_, _ = w.Write([]byte("RCast DMR running\n"))
		}
	})
}

func staticXML(render func() string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/xml; charset=utf-8")
		if r.Method != http.MethodHead {
			_, _ = w.Write([]byte(render()))
		}
	}
}

func LogMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Enhanced logging with structured information
		log.Debug("HTTP request method=%s path=%s remote_addr=%s user_agent=%s",
			r.Method, r.URL.Path, r.RemoteAddr, r.UserAgent())

		start := time.Now()
		next.ServeHTTP(w, r)
		duration := time.Since(start)

		// Record metrics
		monitoring.GetMetrics().RecordHTTPRequest(r.Method, duration)

		log.Debug("HTTP request completed method=%s path=%s duration=%s",
			r.Method, r.URL.Path, duration.String())
	})
}
