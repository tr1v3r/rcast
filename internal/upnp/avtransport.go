package upnp

import (
	"io"
	"net/http"
	"strings"

	"github.com/tr1v3r/pkg/log"

	"github.com/tr1v3r/rcast/internal/config"
	"github.com/tr1v3r/rcast/internal/monitoring"
	"github.com/tr1v3r/rcast/internal/state"
)

// CurrentURIMetaData:
// <DIDL-Lite
// 	xmlns="urn:schemas-upnp-org:metadata-1-0/DIDL-Lite/"
// 	xmlns:upnp="urn:schemas-upnp-org:metadata-1-0/upnp/"
// 	xmlns:dc="http://purl.org/dc/elements/1.1/"
// 	xmlns:sec="http://www.sec.co.kr/">
// 	<item id="byteCast_2c806b8d1f5475544e564deab13f501f" parentID="video/*" restricted="1">
// 		<dc:title>大跳水</dc:title>
// 		<upnp:class>object.item.videoItem</upnp:class>
// 		<res protocolInfo="http-get:*:video/*:DLNA.ORG_OP=01;DLNA.ORG_FLAGS=01700000000000000000000000000000">http://1.2.3.4:123/video</res>
// 	</item>
// </DIDL-Lite>

func AVTransportHandler(st *state.PlayerState, cfg config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := st.Context()
		sa := ParseSOAPAction(r.Header.Get("SOAPACTION"))
		body, _ := io.ReadAll(r.Body)
		controller := ControllerID(r)

		// Record UPnP action
		monitoring.GetMetrics().RecordUPnPAction()

		log.CtxDebug(ctx, "get request header: %+v", r.Header)
		log.CtxDebug(ctx, "get request body: %s", string(body))

		switch sa {
		case "SetAVTransportURI":
			if !st.AcquireOrCheckSession(controller, cfg.AllowSessionPreempt) {
				if !cfg.AllowSessionPreempt {
					monitoring.GetMetrics().RecordUPnPError()
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
				monitoring.GetMetrics().RecordUPnPError()
				WriteSOAPError(w, 714, "No content selected")
				return
			}

			// Start playback asynchronously
			go func() {
				playerKey := strings.SplitN(r.RemoteAddr, ":", 2)[0]
				if err := st.GetPlayer(playerKey).Play(ctx, uri, st.Volume); err != nil {
					log.CtxError(ctx, "iina play error: %v", err)
					monitoring.GetMetrics().RecordPlayerError()
					// Note: Can't send error response here since HTTP response already sent
					return
				}
				st.SetTransportState("PLAYING")
			}()
			WriteSOAPOK(w, "PlayResponse")

		case "Pause":
			if !st.HasSession(controller) && !cfg.AllowSessionPreempt {
				WriteSOAPError(w, 712, "Session in use")
				return
			}
			// Pause asynchronously
			go func() {
				playerKey := strings.SplitN(r.RemoteAddr, ":", 2)[0]
				_ = st.GetPlayer(playerKey).Pause(ctx)
				st.SetTransportState("PAUSED_PLAYBACK")
			}()
			WriteSOAPOK(w, "PauseResponse")

		case "Stop":
			if !st.HasSession(controller) && !cfg.AllowSessionPreempt {
				WriteSOAPError(w, 712, "Session in use")
				return
			}
			// Stop asynchronously
			go func() {
				addr := strings.SplitN(r.RemoteAddr, ":", 2)[0]
				_ = st.GetPlayer(addr).Stop(ctx)
				st.RemovePlayer(addr)
				st.SetTransportState("STOPPED")
				st.ReleaseSession()
			}()
			WriteSOAPOK(w, "StopResponse")

		default:
			WriteSOAPError(w, 401, "Invalid Action")
		}
	}
}
