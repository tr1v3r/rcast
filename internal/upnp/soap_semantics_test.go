package upnp

import (
	"net/http"
	"strings"
	"testing"

	"github.com/tr1v3r/rcast/internal/config"
)

// Audit LOW regressions for SOAP-level semantics: XMLText argument extraction
// must be bounded to the SOAP action scope, and ControllerID must normalize
// IPv6-mapped IPv4 addresses so a dual-stacked control point is one identity.

const shadowEnvelope = `<s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/"><s:Header><InstanceID>9</InstanceID><Unit>ABS_COUNT</Unit><Target>99:99:99</Target><DesiredVolume>777</DesiredVolume><CurrentURI>https://header.example/evil.mp4</CurrentURI></s:Header><s:Body><u:Seek xmlns:u="urn:schemas-upnp-org:service:AVTransport:1"><InstanceID>0</InstanceID><Unit>REL_TIME</Unit><Target>00:01:40</Target></u:Seek></s:Body></s:Envelope>`

func TestXMLText_SOAPHeaderCannotShadowActionArgs(t *testing.T) {
	for tag, want := range map[string]string{
		"Unit":       "REL_TIME",
		"Target":     "00:01:40",
		"InstanceID": "0",
	} {
		if got := XMLText([]byte(shadowEnvelope), tag); got != want {
			t.Errorf("XMLText(%q)=%q, want %q (header element shadowed the action argument)", tag, got, want)
		}
	}
	// An argument absent from the action must not fall back to the header.
	if got := XMLText([]byte(shadowEnvelope), "DesiredVolume"); got != "" {
		t.Errorf("XMLText(DesiredVolume)=%q, want empty (header value must not leak)", got)
	}
	if got := XMLText([]byte(shadowEnvelope), "CurrentURI"); got != "" {
		t.Errorf("XMLText(CurrentURI)=%q, want empty (header value must not leak)", got)
	}
}

func TestXMLText_EnvelopeLevelElementBeforeBodyCannotShadow(t *testing.T) {
	body := `<s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/"><DesiredVolume>1</DesiredVolume><s:Body><u:SetVolume xmlns:u="urn:schemas-upnp-org:service:RenderingControl:1"><DesiredVolume>55</DesiredVolume></u:SetVolume></s:Body></s:Envelope>`
	if got := XMLText([]byte(body), "DesiredVolume"); got != "55" {
		t.Fatalf("XMLText(DesiredVolume)=%q, want 55 (envelope-level element shadowed the action argument)", got)
	}
}

func TestXMLText_MalformedEnvelopeWithoutBodyFindsNothing(t *testing.T) {
	// Previously the whole document (including the header) was scanned; now a
	// header-only envelope yields nothing rather than leaking header values.
	body := `<s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/"><s:Header><Unit>evil</Unit></s:Header></s:Envelope>`
	if got := XMLText([]byte(body), "Unit"); got != "" {
		t.Fatalf("XMLText(Unit)=%q, want empty", got)
	}
}

func TestXMLText_BareDocumentStillFindsNestedArgs(t *testing.T) {
	// DIDL-Lite metadata fragments carry no SOAP envelope; the document root
	// bounds the search in that case. This is how Play extracts <dc:title>.
	const didl = `<DIDL-Lite xmlns:dc="http://purl.org/dc/elements/1.1/"><item id="1"><dc:title>标题 &amp; 副标题</dc:title></item></DIDL-Lite>`
	if got := XMLText([]byte(didl), "title"); got != "标题 & 副标题" {
		t.Fatalf("XMLText(title)=%q, want %q", got, "标题 & 副标题")
	}
	// The bare document's root itself stays matchable (pre-existing behavior).
	if got := XMLText([]byte(`<t>outer<t>inner</t></t>`), "t"); got != "outer" {
		t.Fatalf("XMLText(t)=%q, want outer", got)
	}
}

func TestXMLText_SOAP12NamespaceScoped(t *testing.T) {
	body := `<env:Envelope xmlns:env="http://www.w3.org/2003/05/soap-envelope"><env:Header><Target>00:00:01</Target></env:Header><env:Body><Seek><Target>00:10:00</Target></Seek></env:Body></env:Envelope>`
	if got := XMLText([]byte(body), "Target"); got != "00:10:00" {
		t.Fatalf("XMLText(Target)=%q, want 00:10:00 (SOAP 1.2 header shadowed the argument)", got)
	}
}

func TestControllerID_NormalizesIPv6MappedAddresses(t *testing.T) {
	cases := []struct {
		name       string
		remoteAddr string
		want       string
	}{
		{"plain ipv4", "192.0.2.1:8080", "192.0.2.1"},
		{"ipv6-mapped ipv4", "[::ffff:192.0.2.1]:8080", "192.0.2.1"},
		{"ipv6 compressed lowercase", "[2001:DB8::A]:8080", "2001:db8::a"},
		{"ipv6 loopback", "[::1]:8080", "::1"},
		{"no port ipv4", "192.0.2.7", "192.0.2.7"},
		{"unparseable host passes through", "not-an-ip:8080", "not-an-ip"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := &http.Request{RemoteAddr: tc.remoteAddr}
			if got := ControllerID(r); got != tc.want {
				t.Fatalf("ControllerID(%q)=%q, want %q", tc.remoteAddr, got, tc.want)
			}
		})
	}
}

// TestControllerID_DualStackControllerIsOneSessionOwner proves normalization
// end to end: the same host connecting via IPv4 and via its IPv6-mapped form
// is treated as the same session owner instead of preempting itself (audit
// LOW-9).
func TestControllerID_DualStackControllerIsOneSessionOwner(t *testing.T) {
	st, cleanup := newAVTState(t, nil)
	defer cleanup()
	// Preemption disabled: two distinct identities would be refused with 800.
	handler := AVTransportHandler(st, config.Config{AllowSessionPreempt: false})

	rec := serveAction(handler, "SetAVTransportURI", soapBody(`<CurrentURI>https://example.test/a.mp4</CurrentURI>`), "192.168.1.9:1111")
	if rec.Code != http.StatusOK {
		t.Fatalf("first SetURI status=%d body=%s", rec.Code, rec.Body.String())
	}

	// Same host through the IPv6-mapped socket: must be recognized as the
	// session owner, not as an intruder.
	rec = serveAction(handler, "SetAVTransportURI", soapBody(`<CurrentURI>https://example.test/b.mp4</CurrentURI>`), "[::ffff:192.168.1.9]:2222")
	assertSOAPSuccess(t, rec, "SetAVTransportURIResponse")
	if uri, _ := st.GetURI(); uri != "https://example.test/b.mp4" {
		t.Fatalf("uri=%q, want b.mp4 (mapped address must act as the same owner)", uri)
	}

	// A genuinely different controller is still refused.
	rec = serveAction(handler, "SetAVTransportURI", soapBody(`<CurrentURI>https://example.test/c.mp4</CurrentURI>`), "192.168.1.10:3333")
	assertUPnPError(t, rec, 800)
	if !strings.Contains(rec.Body.String(), "ERRC_SESSION_IN_USE") {
		t.Fatalf("error description missing vendor marker; body=%s", rec.Body.String())
	}
}
