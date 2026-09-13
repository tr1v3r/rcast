package upnp

import (
	"fmt"
	"html"
	"math"
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
	// NaN compares false against every bound below, so it would slip through
	// the range check and poison the player IPC payload (audit M5, entry-side
	// gate); ±Inf must be rejected for the same reason.
	if math.IsNaN(s) || math.IsInf(s, 0) {
		return 0, fmt.Errorf("invalid time value: %s", t)
	}
	if h < 0 || m < 0 || m >= 60 || s < 0 || s >= 60 {
		return 0, fmt.Errorf("invalid time value: %s", t)
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

// errcSessionInUse is rcast's vendor-specific error for a session held by
// another control point. The UPnP 700–799 numeric range is reserved for
// standardized action errors, so custom codes must be >= 800 and carry an
// ERRC_-prefixed description to stay distinguishable (audit LOW: error codes).
const errcSessionInUse = 800

// requireSession acquires (or, when preemption is enabled, preempts) the session
// for a mutating transport action. On failure it records a UPnP error, writes a
// SOAP 800 ERRC_SESSION_IN_USE response, and returns false.
func requireSession(w http.ResponseWriter, st *state.PlayerState, cfg config.Config, controller string) bool {
	acquired, preempted := st.AcquireSession(controller, cfg.AllowSessionPreempt)
	if !acquired {
		monitoring.GetMetrics().RecordUPnPError()
		WriteSOAPError(w, errcSessionInUse, "ERRC_SESSION_IN_USE")
		return false
	}
	if preempted {
		if err := st.StopPlayer(); err != nil {
			log.CtxError(st.Context(), "stop preempted player: %v", err)
			monitoring.GetMetrics().RecordPlayerError()
			monitoring.GetMetrics().RecordUPnPError()
			WriteSOAPError(w, 501, "Action Failed")
			return false
		}
	}
	return true
}

func AVTransportHandler(st *state.PlayerState, cfg config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := st.Context()
		sa := ParseSOAPAction(r.Header.Get("SOAPACTION"))
		body, ok := ReadSOAPBody(w, r)
		if !ok {
			return
		}
		controller := ControllerID(r)

		// Record UPnP action
		monitoring.GetMetrics().RecordUPnPAction()

		log.CtxDebug(ctx, "get request header: %+v", r.Header)
		log.CtxDebug(ctx, "get request body: %s", string(body))

		switch sa {
		case "SetAVTransportURI":
			uri := XMLText(body, "CurrentURI")
			if uri == "" {
				monitoring.GetMetrics().RecordUPnPError()
				WriteSOAPError(w, 402, "Invalid Args")
				return
			}
			meta := XMLText(body, "CurrentURIMetaData")
			serializeSOAP(st, w, func(w http.ResponseWriter) {
				if !requireSession(w, st, cfg, controller) {
					return
				}
				if p := st.GetActivePlayer(); p != nil {
					if err := p.StopPlayback(ctx); err != nil {
						log.CtxWarn(ctx, "stop current playback before URI change: %v", err)
						if err := st.StopPlayer(); err != nil {
							monitoring.GetMetrics().RecordPlayerError()
							monitoring.GetMetrics().RecordUPnPError()
							WriteSOAPError(w, 501, "Action Failed")
							return
						}
					}
				}
				st.SetURI(uri, meta)
				WriteSOAPResponse(w, AVTransportType, "SetAVTransportURIResponse", "")
			})

		case "Play":
			serializeSOAP(st, w, func(w http.ResponseWriter) {
				if !requireSession(w, st, cfg, controller) {
					return
				}
				uri, meta := st.GetURI()
				if uri == "" {
					monitoring.GetMetrics().RecordUPnPError()
					WriteSOAPError(w, 702, "No content selected")
					return
				}
				st.SetTransportState("TRANSITIONING")
				p := st.EnsurePlayer()
				if err := p.Play(ctx, uri, st.GetVolume()); err != nil {
					log.CtxError(ctx, "iina play error: %v", err)
					monitoring.GetMetrics().RecordPlayerError()
					st.SetTransportState("STOPPED")
					monitoring.GetMetrics().RecordUPnPError()
					WriteSOAPError(w, 501, "Action Failed")
					return
				}
				if title := XMLText([]byte(meta), "title"); title != "" {
					if err := p.SetTitle(ctx, title); err != nil {
						log.CtxWarn(ctx, "set media title: %v", err)
					}
				}
				if st.GetMute() {
					if err := p.SetMute(ctx, true); err != nil {
						log.CtxError(ctx, "apply initial mute: %v", err)
						monitoring.GetMetrics().RecordPlayerError()
						_ = st.StopPlayer()
						st.SetTransportState("STOPPED")
						monitoring.GetMetrics().RecordUPnPError()
						WriteSOAPError(w, 501, "Action Failed")
						return
					}
				}
				st.SetTransportState("PLAYING")
				WriteSOAPResponse(w, AVTransportType, "PlayResponse", "")
			})

		case "Pause":
			serializeSOAP(st, w, func(w http.ResponseWriter) {
				if !requireSession(w, st, cfg, controller) {
					return
				}
				p := st.GetActivePlayer()
				if p == nil {
					monitoring.GetMetrics().RecordUPnPError()
					WriteSOAPError(w, 701, "Transition not available")
					return
				}
				if err := p.Pause(ctx); err != nil {
					monitoring.GetMetrics().RecordPlayerError()
					monitoring.GetMetrics().RecordUPnPError()
					WriteSOAPError(w, 501, "Action Failed")
					return
				}
				st.SetTransportState("PAUSED_PLAYBACK")
				WriteSOAPResponse(w, AVTransportType, "PauseResponse", "")
			})

		case "Stop":
			serializeSOAP(st, w, func(w http.ResponseWriter) {
				if !requireSession(w, st, cfg, controller) {
					return
				}
				if err := st.StopPlayer(); err != nil {
					monitoring.GetMetrics().RecordPlayerError()
					monitoring.GetMetrics().RecordUPnPError()
					WriteSOAPError(w, 501, "Action Failed")
					return
				}
				st.SetTransportState("STOPPED")
				st.ReleaseSession(controller)
				WriteSOAPResponse(w, AVTransportType, "StopResponse", "")
			})

		case "Seek":
			unit := XMLText(body, "Unit")
			target := XMLText(body, "Target")

			if unit != "REL_TIME" && unit != "ABS_TIME" {
				// We mainly support REL_TIME/ABS_TIME which are time strings
				// For now treat them same
				monitoring.GetMetrics().RecordUPnPError()
				WriteSOAPError(w, 710, "Seek mode not supported")
				return
			}

			seconds, err := timeToSeconds(target)
			if err != nil {
				log.CtxError(ctx, "parse seek target error: %v target=%s", err, target)
				monitoring.GetMetrics().RecordUPnPError()
				WriteSOAPError(w, 711, "Illegal seek target")
				return
			}

			serializeSOAP(st, w, func(w http.ResponseWriter) {
				if !requireSession(w, st, cfg, controller) {
					return
				}
				p := st.GetActivePlayer()
				if p == nil {
					monitoring.GetMetrics().RecordUPnPError()
					WriteSOAPError(w, 701, "Transition not available")
					return
				}
				if err := p.Seek(ctx, seconds); err != nil {
					log.CtxError(ctx, "player seek error: %v", err)
					monitoring.GetMetrics().RecordPlayerError()
					monitoring.GetMetrics().RecordUPnPError()
					WriteSOAPError(w, 501, "Action Failed")
					return
				}
				WriteSOAPResponse(w, AVTransportType, "SeekResponse", "")
			})

		case "GetTransportInfo":
			state := st.GetTransportState()
			status := "OK"
			speed := "1"
			resp := fmt.Sprintf("<CurrentTransportState>%s</CurrentTransportState><CurrentTransportStatus>%s</CurrentTransportStatus><CurrentSpeed>%s</CurrentSpeed>", state, status, speed)
			WriteSOAPResponse(w, AVTransportType, "GetTransportInfoResponse", resp)

		case "GetPositionInfo":
			// Track numbering must stay consistent with NrTracks: a renderer
			// with content loaded reports track 1, an empty one reports 0
			// (audit LOW: Track=0 vs NrTracks=1 inconsistency).
			uri, _ := st.GetURI()
			track := "0"
			if uri != "" {
				track = "1"
			}
			trackDur := "00:00:00"
			relTime := "00:00:00"
			absTime := "00:00:00"

			// Natural end-file events pin position at duration in PlayerState;
			// otherwise this queries the active player as before.
			pos, duration, posErr, durationErr := st.GetPlaybackPosition(ctx)
			if durationErr == nil {
				trackDur = durationToTime(duration)
			}
			if posErr == nil {
				relTime = durationToTime(pos)
				absTime = relTime
			}

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
			// NrTracks mirrors GetPositionInfo's Track: 0 with no URI set,
			// 1 once content is loaded (audit LOW).
			nrTracks := "0"
			if uri != "" {
				nrTracks = "1"
			}
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
			monitoring.GetMetrics().RecordUPnPError()
			WriteSOAPError(w, 401, "Invalid Action")
		}
	}
}
