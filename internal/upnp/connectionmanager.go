package upnp

import (
	"fmt"
	"io"
	"net/http"

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
			sink := "http-get:*:video/mp4:*,http-get:*:video/mpeg:*,http-get:*:video/x-ms-wmv:*,http-get:*:video/x-ms-avi:*,http-get:*:video/mkv:*,http-get:*:audio/mpeg:*"
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
