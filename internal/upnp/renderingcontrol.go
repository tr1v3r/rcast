package upnp

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/tr1v3r/pkg/log"

	"github.com/tr1v3r/rcast/internal/config"
	"github.com/tr1v3r/rcast/internal/player"
	"github.com/tr1v3r/rcast/internal/state"
)

// Aweme on iOS exposes eight system volume steps but changes the UPnP value by
// five points per step. 100 / (8 * 5) expands that 40-point logical range to
// the player's full 0-100 range.
const awemeIOSVolumeScale = 2.5

// systemVolumeSink/systemMuteSink are injectable so tests can exercise the
// LinkSystemOutputVolume branches without changing the host's real volume.
var (
	systemVolumeSink = player.SetSystemOutputVolume
	systemMuteSink   = player.SetSystemMute
)

func RenderingControlHandler(st *state.PlayerState, cfg config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := st.Context()
		sa := ParseSOAPAction(r.Header.Get("SOAPACTION"))
		body, ok := ReadSOAPBody(w, r)
		if !ok {
			return
		}
		controller := ControllerID(r)
		volumeScale := volumeScaleForUserAgent(r.UserAgent())

		log.CtxDebug(ctx, "get request header: %+v", r.Header)
		log.CtxDebug(ctx, "get request body: %s", string(body))

		switch sa {
		case "SetVolume":
			vStr := XMLText(body, "DesiredVolume")
			v, err := strconv.Atoi(vStr)
			if err != nil {
				WriteSOAPError(w, 402, "Invalid Args")
				return
			}
			if v < 0 {
				v = 0
			}
			if v > 100 {
				v = 100
			}
			st.Serialize(func() {
				if !requireSession(w, st, cfg, controller) {
					return
				}
				appliedVolume := st.PreviewVolumeRequest(controller, v, volumeScale)
				if p := st.GetActivePlayer(); p != nil {
					if err := p.SetVolume(ctx, appliedVolume); err != nil {
						WriteSOAPError(w, 501, "Action Failed")
						log.CtxError(ctx, "iina set volume error: %v", err)
						return
					}
				}
				if cfg.LinkSystemOutputVolume {
					if err := systemVolumeSink(appliedVolume); err != nil {
						log.CtxWarn(ctx, "set system volume: %v", err)
					}
				}
				st.CommitVolumeRequest(controller, v, volumeScale)
				if volumeScale > 1 {
					log.CtxDebug(ctx, "mapped controller volume raw=%d applied=%d user_agent=%s", v, appliedVolume, r.UserAgent())
				}
				WriteSOAPResponse(w, RenderingType, "SetVolumeResponse", "")
			})

		case "GetVolume":
			v := st.GetReportedVolume(controller, volumeScale)
			WriteSOAPResponse(w, RenderingType, "GetVolumeResponse", fmt.Sprintf("<CurrentVolume>%d</CurrentVolume>", v))

		case "SetMute":
			mStr := strings.ToLower(XMLText(body, "DesiredMute"))
			if mStr != "0" && mStr != "1" && mStr != "false" && mStr != "true" {
				WriteSOAPError(w, 402, "Invalid Args")
				return
			}
			m := mStr == "1" || mStr == "true"
			st.Serialize(func() {
				if !requireSession(w, st, cfg, controller) {
					return
				}
				if p := st.GetActivePlayer(); p != nil {
					if err := p.SetMute(ctx, m); err != nil {
						WriteSOAPError(w, 501, "Action Failed")
						return
					}
				}
				if cfg.LinkSystemOutputVolume {
					if err := systemMuteSink(m); err != nil {
						log.CtxWarn(ctx, "set system mute: %v", err)
					}
				}
				st.SetMute(m)
				WriteSOAPResponse(w, RenderingType, "SetMuteResponse", "")
			})

		case "GetMute":
			m := st.GetMute()
			val := "0"
			if m {
				val = "1"
			}
			WriteSOAPResponse(w, RenderingType, "GetMuteResponse", fmt.Sprintf("<CurrentMute>%s</CurrentMute>", val))

		default:
			WriteSOAPError(w, 401, "Invalid Action")
		}
	}
}

func volumeScaleForUserAgent(userAgent string) float64 {
	ua := strings.ToLower(userAgent)
	if strings.HasPrefix(ua, "aweme/") && strings.Contains(ua, "cfnetwork/") && strings.Contains(ua, "darwin/") {
		return awemeIOSVolumeScale
	}
	return 1
}
