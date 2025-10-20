package upnp

import (
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/tr1v3r/pkg/log"

	"github.com/tr1v3r/rcast/internal/config"
	"github.com/tr1v3r/rcast/internal/player"
	"github.com/tr1v3r/rcast/internal/state"
)

func RenderingControlHandler(st *state.PlayerState, cfg config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := st.Context()
		sa := ParseSOAPAction(r.Header.Get("SOAPACTION"))
		body, _ := io.ReadAll(r.Body)
		controller := ControllerID(r)

		log.CtxDebug(ctx, "get request header: %+v", r.Header)
		log.CtxDebug(ctx, "get request body: %s", string(body))

		switch sa {
		case "SetVolume":
			if !st.HasSession(controller) && !cfg.AllowSessionPreempt {
				WriteSOAPError(w, 712, "Session in use")
				return
			}
			vStr := XMLText(body, "DesiredVolume")
			v, _ := strconv.Atoi(vStr)
			if v < 0 {
				v = 0
			}
			if v > 100 {
				v = 100
			}
			if err := st.GetPlayer(strings.SplitN(r.RemoteAddr, ":", 2)[0]).SetVolume(ctx, v); err != nil {
				WriteSOAPError(w, 501, "Action Failed")
				log.CtxError(ctx, "iina set volume error: %v", err)
				return
			}
			if cfg.LinkSystemOutputVolume {
				_ = player.SetSystemOutputVolume(v)
			}
			st.SetVolume(v)
			WriteSOAPOK(w, "SetVolumeResponse")

		case "GetVolume":
			v := st.GetVolume()
			WriteSOAPOKWithBody(w, "GetVolumeResponse", fmt.Sprintf("<CurrentVolume>%d</CurrentVolume>", v))

		case "SetMute":
			if !st.HasSession(controller) && !cfg.AllowSessionPreempt {
				WriteSOAPError(w, 712, "Session in use")
				return
			}
			mStr := strings.ToLower(XMLText(body, "DesiredMute"))
			m := mStr == "1" || mStr == "true"
			if err := st.GetPlayer(strings.SplitN(r.RemoteAddr, ":", 2)[0]).SetMute(ctx, m); err != nil {
				WriteSOAPError(w, 501, "Action Failed")
				return
			}
			if cfg.LinkSystemOutputVolume {
				_ = player.SetSystemMute(m)
			}
			st.SetMute(m)
			WriteSOAPOK(w, "SetMuteResponse")

		case "GetMute":
			m := st.GetMute()
			val := "0"
			if m {
				val = "1"
			}
			WriteSOAPOKWithBody(w, "GetMuteResponse", fmt.Sprintf("<CurrentMute>%s</CurrentMute>", val))

		default:
			WriteSOAPError(w, 401, "Invalid Action")
		}
	}
}
