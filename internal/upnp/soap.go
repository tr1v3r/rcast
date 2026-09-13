package upnp

import (
	"bytes"
	"encoding/xml"
	"html"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
)

const maxSOAPBodyBytes = 1 << 20

func ParseSOAPAction(sa string) string {
	sa = strings.Trim(sa, "\"")
	if i := strings.LastIndex(sa, "#"); i >= 0 {
		return sa[i+1:]
	}
	return sa
}

func WriteSOAPOK(w http.ResponseWriter, respName string) {
	WriteSOAPResponse(w, AVTransportType, respName, "")
}

func WriteSOAPResponse(w http.ResponseWriter, namespace, respName, inner string) {
	w.Header().Set("Content-Type", `text/xml; charset="utf-8"`)

	var builder strings.Builder
	builder.Grow(256 + len(respName)*2 + len(inner)) // Pre-allocate buffer

	builder.WriteString(`<?xml version="1.0" encoding="utf-8"?>
<s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/" s:encodingStyle="http://schemas.xmlsoap.org/soap/encoding/">
  <s:Body>
    <u:`)
	builder.WriteString(respName)
	builder.WriteString(` xmlns:u="`)
	builder.WriteString(namespace)
	builder.WriteString(`">`)
	builder.WriteString(inner)
	builder.WriteString(`</u:`)
	builder.WriteString(respName)
	builder.WriteString(`>
  </s:Body>
</s:Envelope>`)

	_, _ = w.Write([]byte(builder.String()))
}

func WriteSOAPError(w http.ResponseWriter, code int, desc string) {
	w.Header().Set("Content-Type", `text/xml; charset="utf-8"`)
	w.WriteHeader(500)

	var builder strings.Builder
	builder.Grow(512 + len(desc)) // Pre-allocate buffer

	builder.WriteString(`<?xml version="1.0" encoding="utf-8"?>
<s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/" s:encodingStyle="http://schemas.xmlsoap.org/soap/encoding/">
  <s:Body>
    <s:Fault>
      <faultcode>s:Client</faultcode>
      <faultstring>UPnPError</faultstring>
      <detail>
        <UPnPError xmlns="urn:schemas-upnp-org:control-1-0">
          <errorCode>`)
	builder.WriteString(strconv.Itoa(code))
	builder.WriteString(`</errorCode>
          <errorDescription>`)
	builder.WriteString(html.EscapeString(desc))
	builder.WriteString(`</errorDescription>
        </UPnPError>
      </detail>
    </s:Fault>
  </s:Body>
</s:Envelope>`)

	_, _ = w.Write([]byte(builder.String()))
}

// SOAP 1.1 (required by UPnP) and SOAP 1.2 envelope namespaces, accepted for
// scoping XMLText to the action element.
const (
	soapEnvelopeNS1_1 = "http://schemas.xmlsoap.org/soap/envelope/"
	soapEnvelopeNS1_2 = "http://www.w3.org/2003/05/soap-envelope"
)

func isSOAPFrameElement(name xml.Name, local string) bool {
	return (name.Space == soapEnvelopeNS1_1 || name.Space == soapEnvelopeNS1_2) && name.Local == local
}

// XMLText extracts the trimmed text of the first element named tag inside the
// SOAP action scope of the document: when a SOAP envelope is present the search
// is bounded to the first element child of the SOAP Body (the action element)
// and its descendants; for bare documents (DIDL-Lite metadata fragments, test
// fixtures) the document root bounds the search. Elements outside that scope —
// a SOAP Header or an envelope-level element preceding the Body — can no longer
// shadow action arguments (audit LOW: XMLText scope).
func XMLText(b []byte, tag string) string {
	decoder := xml.NewDecoder(bytes.NewReader(b))
	depth := 0
	bodyDepth := 0   // depth of the SOAP Body element, 0 while outside it
	actionDepth := 0 // depth of the scope root, 0 before entering any scope
	for {
		token, err := decoder.Token()
		if err != nil {
			return ""
		}
		switch tok := token.(type) {
		case xml.StartElement:
			depth++
			if actionDepth == 0 {
				switch {
				case bodyDepth == 0 && isSOAPFrameElement(tok.Name, "Body"):
					// First SOAP Body at any depth bounds the scope.
					bodyDepth = depth
				case bodyDepth != 0 && depth == bodyDepth+1:
					// First element child of the Body is the action element.
					actionDepth = depth
				case depth == 1 && !isSOAPFrameElement(tok.Name, "Envelope"):
					// Bare (non-envelope) document: the root bounds the search.
					actionDepth = depth
				}
				// An Envelope root matches none of the cases above, so the
				// scan keeps descending until the Body (or EOF).
			}
			if actionDepth != 0 && tok.Name.Local == tag {
				var value string
				if err := decoder.DecodeElement(&value, &tok); err != nil {
					return ""
				}
				return strings.TrimSpace(value)
			}
		case xml.EndElement:
			if bodyDepth != 0 && depth == bodyDepth {
				bodyDepth = 0
			}
			if actionDepth != 0 && depth == actionDepth {
				actionDepth = 0
			}
			depth--
		}
	}
}

func ReadSOAPBody(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return nil, false
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxSOAPBodyBytes)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		WriteSOAPError(w, 402, "Invalid Args")
		return nil, false
	}
	return body, true
}

// ControllerID returns the canonical session key for the control point behind
// the request. Addresses are normalized via net.IP so an IPv4 control point
// reaching the renderer through an IPv6-mapped socket (::ffff:1.2.3.4) and the
// same host connecting with a plain IPv4 address collapse to one identity
// instead of preempting itself (audit LOW-9).
func ControllerID(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return host
	}
	if v4 := ip.To4(); v4 != nil {
		return v4.String()
	}
	return ip.To16().String()
}
