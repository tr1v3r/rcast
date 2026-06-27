package upnp

import (
	"net/http"

	"github.com/tr1v3r/pkg/log"
)

func EventHandler(w http.ResponseWriter, r *http.Request) {
	log.Debug("Event request method=%s path=%s header=%v", r.Method, r.URL.Path, r.Header)

	switch r.Method {
	case "SUBSCRIBE", "UNSUBSCRIBE":
		// Do not hand out a SID unless callbacks are actually tracked and
		// notified. A false successful subscription makes control points wait
		// forever for state changes that will never arrive.
		http.Error(w, "UPnP eventing is not implemented", http.StatusNotImplemented)
	default:
		w.Header().Set("Allow", "SUBSCRIBE, UNSUBSCRIBE")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}
