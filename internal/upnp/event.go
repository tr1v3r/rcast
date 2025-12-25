package upnp

import (
	"net/http"

	"github.com/google/uuid"
	"github.com/tr1v3r/pkg/log"
)

func EventHandler(w http.ResponseWriter, r *http.Request) {
	log.Debug("Event request method=%s path=%s header=%v", r.Method, r.URL.Path, r.Header)

	if r.Method == "SUBSCRIBE" {
		callback := r.Header.Get("CALLBACK")
		nt := r.Header.Get("NT")
		sid := r.Header.Get("SID") // For renewal

		if sid != "" {
			// Renewal
			w.Header().Set("SID", sid)
			w.Header().Set("TIMEOUT", "Second-1800")
			w.WriteHeader(http.StatusOK)
			return
		}

		if callback != "" && nt == "upnp:event" {
			// New subscription
			newSID := "uuid:" + uuid.New().String()
			w.Header().Set("SID", newSID)
			w.Header().Set("TIMEOUT", "Second-1800")
			w.WriteHeader(http.StatusOK)
			return
		}

		w.WriteHeader(http.StatusBadRequest)
		return
	}

	if r.Method == "UNSUBSCRIBE" {
		sid := r.Header.Get("SID")
		if sid != "" {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusPreconditionFailed)
		return
	}

	w.WriteHeader(http.StatusOK)
}
