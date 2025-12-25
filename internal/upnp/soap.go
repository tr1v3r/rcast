package upnp

import (
	"fmt"
	"html"
	"net"
	"net/http"
	"strings"
)

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
	builder.WriteString(desc)
	builder.WriteString(`</errorDescription>
        </UPnPError>
      </detail>
    </s:Fault>
  </s:Body>
</s:Envelope>`)

	w.Write([]byte(builder.String()))
}

func XMLText(b []byte, tag string) string {
	open := "<" + tag + ">"
	close := "</" + tag + ">"
	s := string(b)
	i := strings.Index(s, open)
	if i < 0 {
		open = "<u:" + tag + ">"
		close = "</u:" + tag + ">"
		i = strings.Index(s, open)
		if i < 0 {
			return ""
		}
	}
	i += len(open)
	j := strings.Index(s[i:], close)
	if j < 0 {
		return ""
	}
	content := strings.TrimSpace(s[i : i+j])
	// UPnP arguments are often XML-escaped. We should unescape them to get the raw string.
	// But be careful: if it's CDATA or just plain text, unescape might change meaning if not intended.
	// For CurrentURIMetaData, it's definitely escaped XML.
	// For CurrentURI, it's a URL, also might be escaped (&amp;).
	// Let's unescape it here.
	return html.UnescapeString(content)
}

func ControllerID(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
