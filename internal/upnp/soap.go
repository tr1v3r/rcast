package upnp

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
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

	w.Write([]byte(builder.String()))
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
	builder.WriteString(fmt.Sprintf("%d", code))
	builder.WriteString(`</errorCode>
          <errorDescription>`)
	builder.WriteString(html.EscapeString(desc))
	builder.WriteString(`</errorDescription>
        </UPnPError>
      </detail>
    </s:Fault>
  </s:Body>
</s:Envelope>`)

	w.Write([]byte(builder.String()))
}

func XMLText(b []byte, tag string) string {
	decoder := xml.NewDecoder(bytes.NewReader(b))
	for {
		token, err := decoder.Token()
		if err != nil {
			return ""
		}
		start, ok := token.(xml.StartElement)
		if !ok || start.Name.Local != tag {
			continue
		}
		var value string
		if err := decoder.DecodeElement(&value, &start); err != nil {
			return ""
		}
		return strings.TrimSpace(value)
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

func ControllerID(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
