package ssdp

import (
	"testing"

	"github.com/tr1v3r/rcast/internal/upnp"
)

func TestResponseTargets(t *testing.T) {
	const id = "uuid:test"
	all := responseTargets("ssdp:all", id)
	if len(all) != 6 {
		t.Fatalf("ssdp:all targets = %d, want 6", len(all))
	}
	cm := responseTargets(upnp.ConnectionManagerType, id)
	if len(cm) != 1 || cm[0].usn != id+"::"+upnp.ConnectionManagerType {
		t.Fatalf("connection manager response = %#v", cm)
	}
	if got := responseTargets("urn:unsupported", id); got != nil {
		t.Fatalf("unsupported target = %#v, want nil", got)
	}
}

func TestAdvertisedLocalAddr(t *testing.T) {
	addr := advertisedLocalAddr("http://192.0.2.10:8200")
	if addr == nil || addr.IP.String() != "192.0.2.10" {
		t.Fatalf("addr=%v", addr)
	}
	if addr := advertisedLocalAddr("not a URL"); addr != nil {
		t.Fatalf("invalid URL addr=%v, want nil", addr)
	}
}
