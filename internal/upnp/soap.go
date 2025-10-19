package upnp

import (
	"fmt"
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
	WriteSOAPOKWithBody(w, respName, "")
}

func WriteSOAPOKWithBody(w http.ResponseWriter, respName, inner string) {
	w.Header().Set("Content-Type", `text/xml; charset="utf-8"`)
	env := fmt.Sprintf(`<?xml version="1.0" encoding="utf-8"?>
<s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/" s:encodingStyle="http://schemas.xmlsoap.org/soap/encoding/">
  <s:Body>
    <u:%s xmlns:u="urn:schemas-upnp-org:service:AVTransport:1">%s</u:%s>
  </s:Body>
</s:Envelope>`, respName, inner, respName)
	_, _ = w.Write([]byte(env))
}

func WriteSOAPError(w http.ResponseWriter, code int, desc string) {
	w.Header().Set("Content-Type", `text/xml; charset="utf-8"`)
	w.WriteHeader(500)
	env := fmt.Sprintf(`<?xml version="1.0" encoding="utf-8"?>
<s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/" s:encodingStyle="http://schemas.xmlsoap.org/soap/encoding/">
  <s:Body>
    <s:Fault>
      <faultcode>s:Client</faultcode>
      <faultstring>UPnPError</faultstring>
      <detail>
        <UPnPError xmlns="urn:schemas-upnp-org:control-1-0">
          <errorCode>%d</errorCode>
          <errorDescription>%s</errorDescription>
        </UPnPError>
      </detail>
    </s:Fault>
  </s:Body>
</s:Envelope>`, code, desc)
	_, _ = w.Write([]byte(env))
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
	return strings.TrimSpace(s[i : i+j])
}

func ControllerID(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
