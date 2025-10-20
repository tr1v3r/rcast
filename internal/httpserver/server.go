package httpserver

import (
	"net/http"

	"github.com/tr1v3r/pkg/log"

	"github.com/tr1v3r/rcast/internal/config"
	"github.com/tr1v3r/rcast/internal/state"
	"github.com/tr1v3r/rcast/internal/upnp"
)

func NewMux() *http.ServeMux {
	return http.NewServeMux()
}

func RegisterHTTP(mux *http.ServeMux, baseURL, deviceUUID string, st *state.PlayerState, cfg config.Config) {
	mux.HandleFunc("/device.xml", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml; charset=utf-8")
		_, _ = w.Write([]byte(upnp.DeviceDescriptionXML(baseURL, deviceUUID)))
	})
	mux.HandleFunc("/upnp/service/avtransport.xml", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml; charset=utf-8")
		_, _ = w.Write([]byte(upnp.SCPDAVTransportXML()))
	})
	mux.HandleFunc("/upnp/service/renderingcontrol.xml", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml; charset=utf-8")
		_, _ = w.Write([]byte(upnp.SCPDRenderingXML()))
	})

	mux.HandleFunc("/upnp/control/avtransport", upnp.AVTransportHandler(st, cfg))
	mux.HandleFunc("/upnp/control/renderingcontrol", upnp.RenderingControlHandler(st, cfg))

	// 事件端点占位
	mux.HandleFunc("/upnp/event/avtransport", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("/upnp/event/renderingcontrol", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })

	// 根
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("Go DLNA DMR running\n"))
	})
}

func LogMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 简易日志
		// 你也可以替换为更完善的 logger
		// 这里保持简洁
		log.Info("%s %s from %s", r.Method, r.URL.Path, r.RemoteAddr)
		next.ServeHTTP(w, r)
	})
}
