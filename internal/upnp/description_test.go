package upnp

import (
	"encoding/xml"
	"strings"
	"testing"
)

// Device description decode targets. Defined locally to avoid coupling to the
// production xml.go types (which model DIDL, not the device description).

type descRoot struct {
	XMLName xml.Name   `xml:"root"`
	Device  descDevice `xml:"device"`
}

type descDevice struct {
	DeviceType   string       `xml:"deviceType"`
	FriendlyName string       `xml:"friendlyName"`
	Manufacturer string       `xml:"manufacturer"`
	ModelName    string       `xml:"modelName"`
	UDN          string       `xml:"UDN"`
	ServiceList  descServices `xml:"serviceList"`
	Presentation string       `xml:"presentationURL"`
}

type descServices struct {
	Services []descService `xml:"service"`
}

type descService struct {
	ServiceType string `xml:"serviceType"`
	ServiceID   string `xml:"serviceId"`
	SCPDURL     string `xml:"SCPDURL"`
	ControlURL  string `xml:"controlURL"`
	EventSubURL string `xml:"eventSubURL"`
}

func parseDevice(t *testing.T, xmlStr string) descRoot {
	t.Helper()
	var r descRoot
	if err := xml.Unmarshal([]byte(xmlStr), &r); err != nil {
		t.Fatalf("unmarshal device xml: %v; xml=%s", err, xmlStr)
	}
	return r
}

func TestDeviceDescriptionXML(t *testing.T) {
	const base = "http://127.0.0.1:8200"
	const uuid = "uuid:abcd-1234"
	r := parseDevice(t, DeviceDescriptionXML(base, uuid))

	d := r.Device
	if d.DeviceType != DeviceType {
		t.Errorf("deviceType=%q, want %q", d.DeviceType, DeviceType)
	}
	if d.UDN != uuid {
		t.Errorf("UDN=%q, want %q", d.UDN, uuid)
	}
	if d.FriendlyName != "RCast" {
		t.Errorf("friendlyName=%q, want RCast", d.FriendlyName)
	}
	if !strings.Contains(d.Presentation, base+"/") {
		t.Errorf("presentationURL=%q, want contains %s/", d.Presentation, base)
	}

	svcs := d.ServiceList.Services
	if len(svcs) != 3 {
		t.Fatalf("len(services)=%d, want 3", len(svcs))
	}

	wantByType := map[string]descService{
		AVTransportType: {
			SCPDURL:     "/upnp/service/avtransport.xml",
			ControlURL:  "/upnp/control/avtransport",
			EventSubURL: "/upnp/event/avtransport",
		},
		RenderingType: {
			SCPDURL:     "/upnp/service/renderingcontrol.xml",
			ControlURL:  "/upnp/control/renderingcontrol",
			EventSubURL: "/upnp/event/renderingcontrol",
		},
		ConnectionManagerType: {
			SCPDURL:     "/upnp/service/connectionmanager.xml",
			ControlURL:  "/upnp/control/connectionmanager",
			EventSubURL: "/upnp/event/connectionmanager",
		},
	}

	gotTypes := map[string]bool{}
	for _, s := range svcs {
		gotTypes[s.ServiceType] = true
		exp, ok := wantByType[s.ServiceType]
		if !ok {
			t.Errorf("unexpected serviceType %q", s.ServiceType)
			continue
		}
		if s.SCPDURL != exp.SCPDURL {
			t.Errorf("serviceType=%q SCPDURL=%q, want %q", s.ServiceType, s.SCPDURL, exp.SCPDURL)
		}
		if s.ControlURL != exp.ControlURL {
			t.Errorf("serviceType=%q controlURL=%q, want %q", s.ServiceType, s.ControlURL, exp.ControlURL)
		}
		if s.EventSubURL != exp.EventSubURL {
			t.Errorf("serviceType=%q eventSubURL=%q, want %q", s.ServiceType, s.EventSubURL, exp.EventSubURL)
		}
	}
	for typ := range wantByType {
		if !gotTypes[typ] {
			t.Errorf("missing serviceType %q", typ)
		}
	}
}

// SCPD decode targets.

type scpdRoot struct {
	XMLName    xml.Name       `xml:"scpd"`
	ActionList scpdActionList `xml:"actionList"`
	StateTable scpdStateTable `xml:"serviceStateTable"`
}

type scpdActionList struct {
	Actions []scpdAction `xml:"action"`
}

type scpdAction struct {
	Name string `xml:"name"`
}

type scpdStateTable struct {
	Vars []scpdStateVar `xml:"stateVariable"`
}

type scpdStateVar struct {
	Name             string        `xml:"name"`
	AllowedValueList []scpdAllowed `xml:"allowedValueList>allowedValue"`
}

type scpdAllowed struct {
	Value string `xml:",chardata"`
}

func parseSCPD(t *testing.T, xmlStr string) scpdRoot {
	t.Helper()
	var r scpdRoot
	if err := xml.Unmarshal([]byte(xmlStr), &r); err != nil {
		t.Fatalf("unmarshal scpd xml: %v; xml head=%s", err, head(xmlStr))
	}
	return r
}

func head(s string) string {
	if len(s) > 200 {
		return s[:200]
	}
	return s
}

func actionNames(r scpdRoot) map[string]bool {
	out := make(map[string]bool, len(r.ActionList.Actions))
	for _, a := range r.ActionList.Actions {
		out[a.Name] = true
	}
	return out
}

func assertActionsExact(t *testing.T, r scpdRoot, want []string) {
	t.Helper()
	got := actionNames(r)
	if len(got) != len(want) {
		t.Fatalf("action count=%d, want %d (got=%v want=%v)", len(got), len(want), got, want)
	}
	for _, w := range want {
		if !got[w] {
			t.Errorf("missing action %q; got=%v", w, got)
		}
	}
}

func TestSCPDAVTransportXML(t *testing.T) {
	r := parseSCPD(t, SCPDAVTransportXML())
	assertActionsExact(t, r, []string{
		"SetAVTransportURI",
		"Play",
		"Pause",
		"Stop",
		"Seek",
		"GetTransportInfo",
		"GetPositionInfo",
		"GetMediaInfo",
		"GetTransportSettings",
		"GetDeviceCapabilities",
	})

	// A_ARG_TYPE_SeekMode allowedValueList must be exactly {REL_TIME, ABS_TIME}.
	for _, sv := range r.StateTable.Vars {
		if sv.Name != "A_ARG_TYPE_SeekMode" {
			continue
		}
		var vals []string
		for _, a := range sv.AllowedValueList {
			vals = append(vals, strings.TrimSpace(a.Value))
		}
		if len(vals) != 2 || vals[0] != "REL_TIME" || vals[1] != "ABS_TIME" {
			t.Errorf("A_ARG_TYPE_SeekMode allowed=%v, want [REL_TIME ABS_TIME]", vals)
		}
		return
	}
	t.Errorf("A_ARG_TYPE_SeekMode stateVariable not found")
}

func TestSCPDRenderingXML(t *testing.T) {
	r := parseSCPD(t, SCPDRenderingXML())
	assertActionsExact(t, r, []string{
		"SetVolume",
		"GetVolume",
		"SetMute",
		"GetMute",
	})
}

func TestSCPDConnectionManagerXML(t *testing.T) {
	r := parseSCPD(t, SCPDConnectionManagerXML())
	assertActionsExact(t, r, []string{
		"GetProtocolInfo",
		"GetCurrentConnectionIDs",
		"GetCurrentConnectionInfo",
	})

	// A_ARG_TYPE_Direction allowed values must be exactly {Output, Input}.
	for _, sv := range r.StateTable.Vars {
		if sv.Name != "A_ARG_TYPE_Direction" {
			continue
		}
		var vals []string
		for _, a := range sv.AllowedValueList {
			vals = append(vals, strings.TrimSpace(a.Value))
		}
		if len(vals) != 2 || vals[0] != "Output" || vals[1] != "Input" {
			t.Errorf("A_ARG_TYPE_Direction allowed=%v, want [Output Input]", vals)
		}
		return
	}
	t.Errorf("A_ARG_TYPE_Direction stateVariable not found")
}
