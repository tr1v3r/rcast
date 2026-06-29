package upnp

import (
	"context"
	"strings"
	"testing"

	"github.com/tr1v3r/rcast/internal/config"
	"github.com/tr1v3r/rcast/internal/state"
)

func newCMState(t *testing.T) (*state.PlayerState, func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	st := state.New(ctx, config.Config{})
	return st, func() {
		cancel()
		st.Stop()
	}
}

func TestGetProtocolInfo(t *testing.T) {
	st, cleanup := newCMState(t)
	defer cleanup()
	rec := serveAction(ConnectionManagerHandler(st, config.Config{}), "GetProtocolInfo", soapBody(``), "10.0.0.1:1")
	assertSOAPSuccess(t, rec, "GetProtocolInfoResponse")
	body := rec.Body.String()
	// Sink advertises a common mp4 profile with range-seek DLNA params.
	if !strings.Contains(body, "http-get:*:video/mp4:") {
		t.Fatalf("mp4 sink missing; body=%s", body)
	}
	// Source must be empty (renderer is a sink only).
	if !strings.Contains(body, "<Source></Source>") {
		t.Fatalf("Source must be empty; body=%s", body)
	}
	if !strings.Contains(body, "<Sink>") || strings.Contains(body, "<Sink></Sink>") {
		t.Fatalf("Sink must be non-empty; body=%s", body)
	}
}

func TestGetCurrentConnectionIDs(t *testing.T) {
	st, cleanup := newCMState(t)
	defer cleanup()
	rec := serveAction(ConnectionManagerHandler(st, config.Config{}), "GetCurrentConnectionIDs", soapBody(``), "10.0.0.1:1")
	assertSOAPSuccess(t, rec, "GetCurrentConnectionIDsResponse")
	if !strings.Contains(rec.Body.String(), "<ConnectionIDs>0</ConnectionIDs>") {
		t.Fatalf("body=%s", rec.Body.String())
	}
}

func TestGetCurrentConnectionInfo_ValidID(t *testing.T) {
	st, cleanup := newCMState(t)
	defer cleanup()
	rec := serveAction(ConnectionManagerHandler(st, config.Config{}), "GetCurrentConnectionInfo", soapBody(`<ConnectionID>0</ConnectionID>`), "10.0.0.1:1")
	assertSOAPSuccess(t, rec, "GetCurrentConnectionInfoResponse")
	body := rec.Body.String()
	if !strings.Contains(body, "<Direction>Input</Direction>") {
		t.Fatalf("Direction missing; body=%s", body)
	}
	if !strings.Contains(body, "<Status>OK</Status>") {
		t.Fatalf("Status missing; body=%s", body)
	}
}

func TestGetCurrentConnectionInfo_InvalidID(t *testing.T) {
	st, cleanup := newCMState(t)
	defer cleanup()
	rec := serveAction(ConnectionManagerHandler(st, config.Config{}), "GetCurrentConnectionInfo", soapBody(`<ConnectionID>5</ConnectionID>`), "10.0.0.1:1")
	assertUPnPError(t, rec, 706)
}

func TestConnectionManager_UnknownAction(t *testing.T) {
	st, cleanup := newCMState(t)
	defer cleanup()
	rec := serveAction(ConnectionManagerHandler(st, config.Config{}), "BogusAction", soapBody(``), "10.0.0.1:1")
	assertUPnPError(t, rec, 401)
}
