package upnp

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestParseSOAPAction(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"qualified AVTransport Play", "urn:schemas-upnp-org:service:AVTransport:1#Play", "Play"},
		{"quoted with surrounding double quotes", `"urn:schemas-upnp-org:service:AVTransport:1#Stop"`, "Stop"},
		{"bare action without hash", "Play", "Play"},
		{"quoted bare action", `"Play"`, "Play"},
		{"empty string", "", ""},
		{"namespace without hash", "urn:schemas-upnp-org:service:AVTransport:1", "urn:schemas-upnp-org:service:AVTransport:1"},
		{"only hash", "#", ""},
		{"trailing hash", "service#Action#", ""},
		{"last segment wins when multiple hashes", "a#b#c", "c"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ParseSOAPAction(tc.in); got != tc.want {
				t.Fatalf("ParseSOAPAction(%q)=%q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestControllerID(t *testing.T) {
	t.Run("host:port form returns host", func(t *testing.T) {
		r := &http.Request{RemoteAddr: "10.0.0.5:51234"}
		if got := ControllerID(r); got != "10.0.0.5" {
			t.Fatalf("ControllerID=%q, want 10.0.0.5", got)
		}
	})
	t.Run("no port returns whole RemoteAddr", func(t *testing.T) {
		// net.SplitHostPort("10.0.0.5") errors because there is no port.
		r := &http.Request{RemoteAddr: "10.0.0.5"}
		if got := ControllerID(r); got != "10.0.0.5" {
			t.Fatalf("ControllerID=%q, want 10.0.0.5", got)
		}
	})
	t.Run("ipv6 with port returns host", func(t *testing.T) {
		r := &http.Request{RemoteAddr: "[::1]:51234"}
		if got := ControllerID(r); got != "::1" {
			t.Fatalf("ControllerID=%q, want ::1", got)
		}
	})
}

func TestXMLTextTable(t *testing.T) {
	cases := []struct {
		name string
		body string
		tag  string
		want string
	}{
		{"namespace agnostic", `<x:Foo xmlns:x="ns"><x:Bar>hi</x:Bar></x:Foo>`, "Bar", "hi"},
		{"decodes ampersand entity", `<a>&amp;</a>`, "a", "&"},
		{"decodes lt entity", `<a>&lt;tag&gt;</a>`, "a", "<tag>"},
		{"missing tag returns empty", `<root><other>v</other></root>`, "missing", ""},
		{"empty body returns empty", ``, "any", ""},
		{"whitespace trimmed", `<t>   spaced   </t>`, "t", "spaced"},
		{"self-closing returns empty", `<t/>`, "t", ""},
		{"nested same tag returns outer first", `<t>outer<t>inner</t></t>`, "t", "outer"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := XMLText([]byte(tc.body), tc.tag); got != tc.want {
				t.Fatalf("XMLText(%q,%q)=%q, want %q", tc.body, tc.tag, got, tc.want)
			}
		})
	}
}

// readBodyHandler builds a tiny handler that exercises ReadSOAPBody and returns
// the bytes it accepted. Mirrors the production control-handler shape.
func readBodyHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, ok := ReadSOAPBody(w, r)
		if !ok {
			return
		}
		WriteSOAPResponse(w, AVTransportType, "Echo", string(body))
	})
}

func TestReadSOAPBody_RejectsGETWithAllow(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/control", nil)
	readBodyHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status=%d, want 405", rec.Code)
	}
	if got := rec.Header().Get("Allow"); got != http.MethodPost {
		t.Fatalf("Allow=%q, want POST", got)
	}
}

func TestReadSOAPBody_OversizedReturnsInvalidArgs(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/control", strings.NewReader(strings.Repeat("x", maxSOAPBodyBytes+1)))
	readBodyHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d, want 500", rec.Code)
	}
	if code := soapErrorCode(t, rec.Body.String()); code != "402" {
		t.Fatalf("errorCode=%q, want 402; body=%s", code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Invalid Args") {
		t.Fatalf("body missing 'Invalid Args': %s", rec.Body.String())
	}
}

func TestReadSOAPBody_NormalPOSTReturnsBytes(t *testing.T) {
	rec := httptest.NewRecorder()
	payload := "<CurrentURI>hello</CurrentURI>"
	req := httptest.NewRequest(http.MethodPost, "/control", strings.NewReader(payload))
	readBodyHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), payload) {
		t.Fatalf("response does not echo body; got %s", rec.Body.String())
	}
}

func TestWriteSOAPResponse(t *testing.T) {
	t.Run("headers and envelope", func(t *testing.T) {
		rec := httptest.NewRecorder()
		WriteSOAPResponse(rec, AVTransportType, "GetVolume", "<CurrentVolume>42</CurrentVolume>")
		if rec.Code != http.StatusOK {
			t.Fatalf("status=%d, want 200", rec.Code)
		}
		ct := rec.Header().Get("Content-Type")
		if !strings.Contains(ct, "text/xml") {
			t.Fatalf("Content-Type=%q, want contains text/xml", ct)
		}
		body := rec.Body.String()
		if !strings.Contains(body, "<u:GetVolume ") {
			t.Fatalf("response missing <u:GetVolume; body=%s", body)
		}
		if !strings.Contains(body, `xmlns:u="`+AVTransportType+`"`) {
			t.Fatalf("response missing namespace declaration; body=%s", body)
		}
		if !strings.Contains(body, "<CurrentVolume>42</CurrentVolume>") {
			t.Fatalf("response missing inner payload; body=%s", body)
		}
	})

	t.Run("empty inner payload still valid", func(t *testing.T) {
		rec := httptest.NewRecorder()
		WriteSOAPResponse(rec, RenderingType, "GetMute", "")
		body := rec.Body.String()
		if !strings.Contains(body, "<u:GetMute ") || !strings.Contains(body, "</u:GetMute>") {
			t.Fatalf("response missing empty envelope; body=%s", body)
		}
	})
}

func TestWriteSOAPError(t *testing.T) {
	t.Run("status code and numeric errorCode", func(t *testing.T) {
		rec := httptest.NewRecorder()
		WriteSOAPError(rec, 718, "action not authorized")
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("status=%d, want 500", rec.Code)
		}
		if got := soapErrorCode(t, rec.Body.String()); got != "718" {
			t.Fatalf("errorCode=%q, want 718", got)
		}
		if !strings.Contains(rec.Body.String(), "action not authorized") {
			t.Fatalf("body missing description: %s", rec.Body.String())
		}
	})

	t.Run("escapes XML special chars in description", func(t *testing.T) {
		rec := httptest.NewRecorder()
		desc := `<&>"'`
		WriteSOAPError(rec, 402, desc)
		body := rec.Body.String()
		// The description must not appear unescaped inside <errorDescription>.
		// html.EscapeString escapes <, >, &, ', " to entities.
		if !strings.Contains(body, "&lt;&amp;&gt;&#34;&#39;") {
			t.Fatalf("body does not contain escaped description: %s", body)
		}
		// And the raw '<' must not leak as an unescaped description start.
		// (The body itself contains legitimate '<' from envelope tags, so we
		// assert the specific unescaped sequence does not appear.)
		if strings.Contains(body, "<&>") {
			t.Fatalf("body contains unescaped <&>: %s", body)
		}
	})
}
