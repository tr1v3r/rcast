package upnp

import (
	"io"
	"log"
	"net/http"
	"strings"

	"github.com/tr1v3r/rcast/internal/config"
	"github.com/tr1v3r/rcast/internal/state"
)

func AVTransportHandler(st *state.PlayerState, cfg config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sa := ParseSOAPAction(r.Header.Get("SOAPACTION"))
		body, _ := io.ReadAll(r.Body)
		controller := ControllerID(r)

		switch sa {
		case "SetAVTransportURI":
			if !st.AcquireOrCheckSession(controller, cfg.AllowSessionPreempt) {
				if !cfg.AllowSessionPreempt {
					WriteSOAPError(w, 712, "Session in use")
					return
				}
			}
			uri := XMLText(body, "CurrentURI")
			meta := XMLText(body, "CurrentURIMetaData")
			st.SetURI(uri, meta)
			WriteSOAPOK(w, "SetAVTransportURIResponse")

		case "Play":
			if !st.AcquireOrCheckSession(controller, cfg.AllowSessionPreempt) {
				if !cfg.AllowSessionPreempt {
					WriteSOAPError(w, 712, "Session in use")
					return
				}
			}
			uri, _ := st.GetURI()
			if uri == "" {
				WriteSOAPError(w, 714, "No content selected")
				return
			}
			if err := st.GetPlayer(strings.SplitN(r.RemoteAddr, ":", 2)[0]).Play(st.Context(), uri, st.Volume); err != nil {
				log.Printf("iina play error: %v", err)
				WriteSOAPError(w, 701, "Playback failed")
				return
			}
			st.SetTransportState("PLAYING")

			WriteSOAPOK(w, "PlayResponse")

		case "Pause":
			if !st.HasSession(controller) && !cfg.AllowSessionPreempt {
				WriteSOAPError(w, 712, "Session in use")
				return
			}
			_ = st.GetPlayer(strings.SplitN(r.RemoteAddr, ":", 2)[0]).Pause(st.Context())
			st.SetTransportState("PAUSED_PLAYBACK")
			WriteSOAPOK(w, "PauseResponse")

		case "Stop":
			if !st.HasSession(controller) && !cfg.AllowSessionPreempt {
				WriteSOAPError(w, 712, "Session in use")
				return
			}
			addr := strings.SplitN(r.RemoteAddr, ":", 2)[0]
			_ = st.GetPlayer(addr).Stop(st.Context())
			st.RemovePlayer(addr)
			st.SetTransportState("STOPPED")
			st.ReleaseSession()
			WriteSOAPOK(w, "StopResponse")

		default:
			WriteSOAPError(w, 401, "Invalid Action")
		}
	}
}
