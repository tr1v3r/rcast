package upnp

import (
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/tr1v3r/pkg/log"

	"github.com/tr1v3r/rcast/internal/config"
	"github.com/tr1v3r/rcast/internal/state"
)

func ConnectionManagerHandler(st *state.PlayerState, cfg config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := st.Context()
		sa := ParseSOAPAction(r.Header.Get("SOAPACTION"))
		body, _ := io.ReadAll(r.Body)

		log.CtxDebug(ctx, "cm request header: %+v", r.Header)
		log.CtxDebug(ctx, "cm request body: %s", string(body))

		switch sa {
		case "GetProtocolInfo":
			// We support http-get for various types.
			// Commonly supported types for a renderer.
			// DLNA.ORG_OP=01 means range seek supported
			// DLNA.ORG_FLAGS=01700000000000000000000000000000 means various support flags (streaming, etc)
			dlnaParams := "DLNA.ORG_PN=AVC_MP4_BL_CIF15_AAC_520;DLNA.ORG_OP=01;DLNA.ORG_FLAGS=01700000000000000000000000000000"

			// Construct sink string with DLNA params for common types
			types := []string{
				"video/mp4",
				"video/mpeg",
				"video/x-ms-wmv",
				"video/x-ms-avi",
				"video/mkv",
				"audio/mpeg",
				"application/x-mpegurl",
				"application/vnd.apple.mpegurl",
			}

			var sinks []string
			for _, t := range types {
				sinks = append(sinks, fmt.Sprintf("http-get:*:%s:%s", t, dlnaParams))
			}

			sink := "http-get:*:*:*,http-get:*:video/*:*," + fmt.Sprint(strings.Join(sinks, ","))
			source := "" // We are a renderer (sink), not a source.

			resp := fmt.Sprintf("<Source>%s</Source><Sink>%s</Sink>", source, sink)
			WriteSOAPResponse(w, ConnectionManagerType, "GetProtocolInfoResponse", resp)

		case "GetCurrentConnectionIDs":
			// ConnectionID 0 is the default.
			WriteSOAPResponse(w, ConnectionManagerType, "GetCurrentConnectionIDsResponse", "<ConnectionIDs>0</ConnectionIDs>")

		case "GetCurrentConnectionInfo":
			// We only support connection 0
			cid := XMLText(body, "ConnectionID")
			if cid != "0" {
				WriteSOAPError(w, 706, "Invalid connection reference")
				return
			}

			// Return info for connection 0
			resp := `<RcsID>0</RcsID>
<AVTransportID>0</AVTransportID>
<ProtocolInfo>http-get:*:video/mp4:*</ProtocolInfo>
<PeerConnectionManager></PeerConnectionManager>
<PeerConnectionID>-1</PeerConnectionID>
<Direction>Input</Direction>
<Status>OK</Status>`
			WriteSOAPResponse(w, ConnectionManagerType, "GetCurrentConnectionInfoResponse", resp)

		default:
			WriteSOAPError(w, 401, "Invalid Action")
		}
	}
}
