package upnp

import (
	"fmt"
	"html"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/tr1v3r/pkg/log"

	"github.com/tr1v3r/rcast/internal/config"
	"github.com/tr1v3r/rcast/internal/monitoring"
	"github.com/tr1v3r/rcast/internal/state"
)

func durationToTime(seconds float64) string {
	if seconds < 0 {
		return "00:00:00"
	}
	h := int(seconds / 3600)
	m := int((seconds - float64(h*3600)) / 60)
	s := int(seconds - float64(h*3600) - float64(m*60))
	return fmt.Sprintf("%02d:%02d:%02d", h, m, s)
}

func timeToSeconds(t string) (float64, error) {
	parts := strings.Split(t, ":")
	if len(parts) != 3 {
		return 0, fmt.Errorf("invalid time format: %s", t)
	}
	h, err := strconv.Atoi(parts[0])
	if err != nil {
		return 0, err
	}
	m, err := strconv.Atoi(parts[1])
	if err != nil {
		return 0, err
	}
	s, err := strconv.ParseFloat(parts[2], 64)
	if err != nil {
		return 0, err
	}
	return float64(h)*3600 + float64(m)*60 + s, nil
}

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
			WriteSOAPResponse(w, AVTransportType, "SetAVTransportURIResponse", "")

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
			WriteSOAPResponse(w, AVTransportType, "PlayResponse", "")

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
			WriteSOAPResponse(w, AVTransportType, "PauseResponse", "")

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
			WriteSOAPResponse(w, AVTransportType, "StopResponse", "")

		case "Seek":
			if !st.HasSession(controller) && !cfg.AllowSessionPreempt {
				WriteSOAPError(w, 712, "Session in use")
				return
			}
			unit := XMLText(body, "Unit")
			target := XMLText(body, "Target")

			if unit != "REL_TIME" && unit != "ABS_TIME" {
				// We mainly support REL_TIME/ABS_TIME which are time strings
				// For now treat them same
				WriteSOAPError(w, 710, "Seek mode not supported")
				return
			}

			seconds, err := timeToSeconds(target)
			if err != nil {
				log.CtxError(ctx, "parse seek target error: %v target=%s", err, target)
				WriteSOAPError(w, 711, "Illegal seek target")
				return
			}

			go func() {
				playerKey := strings.SplitN(r.RemoteAddr, ":", 2)[0]
				if err := st.GetPlayer(playerKey).Seek(ctx, seconds); err != nil {
					log.CtxError(ctx, "player seek error: %v", err)
				}
			}()
			WriteSOAPResponse(w, AVTransportType, "SeekResponse", "")

		case "GetTransportInfo":
			state := st.GetTransportState()
			status := "OK"
			speed := "1"
			resp := fmt.Sprintf("<CurrentTransportState>%s</CurrentTransportState><CurrentTransportStatus>%s</CurrentTransportStatus><CurrentSpeed>%s</CurrentSpeed>", state, status, speed)
			WriteSOAPResponse(w, AVTransportType, "GetTransportInfoResponse", resp)

		case "GetPositionInfo":
			track := "0"
			trackDur := "00:00:00"
			relTime := "00:00:00"
			absTime := "00:00:00"

			// Try to get actual duration and position from active player
			if p := st.GetActivePlayer(); p != nil {
				if d, err := p.GetDuration(ctx); err == nil {
					trackDur = durationToTime(d)
				}
				if pos, err := p.GetPosition(ctx); err == nil {
					relTime = durationToTime(pos)
					absTime = relTime
				}
			}

			uri, _ := st.GetURI()

			// 暂时清空 MetaData，排除格式问题
			// meta = ""

			resp := fmt.Sprintf(`<Track>%s</Track>
<TrackDuration>%s</TrackDuration>
<TrackMetaData></TrackMetaData>
<TrackURI>%s</TrackURI>
<RelTime>%s</RelTime>
<AbsTime>%s</AbsTime>
<RelCount>0</RelCount>
<AbsCount>0</AbsCount>`, track, trackDur, html.EscapeString(uri), relTime, absTime)
			log.CtxDebug(ctx, "GetPositionInfo response duration: %s position: %s", trackDur, relTime)
			log.CtxDebug(ctx, "GetPositionInfo full response: %s", resp)
			WriteSOAPResponse(w, AVTransportType, "GetPositionInfoResponse", resp)

		case "GetMediaInfo":
			uri, meta := st.GetURI()
			nrTracks := "1"
			mediaDur := "00:00:00"
			if p := st.GetActivePlayer(); p != nil {
				if d, err := p.GetDuration(ctx); err == nil {
					mediaDur = durationToTime(d)
				}
			}

			resp := fmt.Sprintf(`<NrTracks>%s</NrTracks>
<MediaDuration>%s</MediaDuration>
<CurrentURI>%s</CurrentURI>
<CurrentURIMetaData>%s</CurrentURIMetaData>
<NextURI></NextURI>
<NextURIMetaData></NextURIMetaData>
<PlayMedium>NETWORK</PlayMedium>
<RecordMedium>NOT_IMPLEMENTED</RecordMedium>
<WriteStatus>NOT_IMPLEMENTED</WriteStatus>`, nrTracks, mediaDur, html.EscapeString(uri), html.EscapeString(meta))
			WriteSOAPResponse(w, AVTransportType, "GetMediaInfoResponse", resp)

		case "GetTransportSettings":
			resp := `<PlayMode>NORMAL</PlayMode><RecQualityMode>NOT_IMPLEMENTED</RecQualityMode>`
			WriteSOAPResponse(w, AVTransportType, "GetTransportSettingsResponse", resp)

		case "GetDeviceCapabilities":
			resp := `<PlayMedia>NETWORK</PlayMedia><RecMedia>NOT_IMPLEMENTED</RecMedia><RecQualityModes>NOT_IMPLEMENTED</RecQualityModes>`
			WriteSOAPResponse(w, AVTransportType, "GetDeviceCapabilitiesResponse", resp)

		default:
			WriteSOAPError(w, 401, "Invalid Action")
		}
	}
}
