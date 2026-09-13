package upnp

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tr1v3r/rcast/internal/config"
	"github.com/tr1v3r/rcast/internal/monitoring"
	"github.com/tr1v3r/rcast/internal/player"
)

// Audit regressions for AVTransport semantics: standard vs vendor error codes
// (table-driven), non-finite seek targets (M5 entry-side gate), Track/NrTracks
// consistency, and UPnP metrics coverage.

func TestAVTErrorCodes_Table(t *testing.T) {
	tests := []struct {
		name    string
		wantErr int
		run     func(t *testing.T) *httptest.ResponseRecorder
	}{
		{
			name:    "Play without URI returns standard 702",
			wantErr: 702,
			run: func(t *testing.T) *httptest.ResponseRecorder {
				st, cleanup := newAVTState(t, nil)
				defer cleanup()
				handler := AVTransportHandler(st, config.Config{})
				// Fail to set a URI (402), then attempt Play.
				serveAction(handler, "SetAVTransportURI", soapBody(`<CurrentURI></CurrentURI>`), "10.0.0.1:1")
				return serveAction(handler, "Play", soapBody(`<Speed>1</Speed>`), "10.0.0.1:1")
			},
		},
		{
			name:    "session held with preempt disabled returns vendor 800",
			wantErr: 800,
			run: func(t *testing.T) *httptest.ResponseRecorder {
				st, cleanup := newAVTState(t, nil)
				defer cleanup()
				handler := AVTransportHandler(st, config.Config{AllowSessionPreempt: false})
				serveAction(handler, "SetAVTransportURI", soapBody(`<CurrentURI>https://example.test/a.mp4</CurrentURI>`), "10.0.0.1:1")
				return serveAction(handler, "SetAVTransportURI", soapBody(`<CurrentURI>https://example.test/b.mp4</CurrentURI>`), "10.0.0.2:1")
			},
		},
		{
			name:    "Play with preempt disabled against foreign session returns vendor 800",
			wantErr: 800,
			run: func(t *testing.T) *httptest.ResponseRecorder {
				st, cleanup := newAVTState(t, nil)
				defer cleanup()
				handler := AVTransportHandler(st, config.Config{AllowSessionPreempt: false})
				serveAction(handler, "SetAVTransportURI", soapBody(`<CurrentURI>https://example.test/a.mp4</CurrentURI>`), "10.0.0.1:1")
				return serveAction(handler, "Play", soapBody(`<Speed>1</Speed>`), "10.0.0.2:1")
			},
		},
		{
			name:    "Seek with unsupported unit returns 710",
			wantErr: 710,
			run: func(t *testing.T) *httptest.ResponseRecorder {
				st, cleanup := newAVTState(t, nil)
				defer cleanup()
				handler := AVTransportHandler(st, config.Config{})
				setupAVT(t, st, handler, "10.0.0.1:1", "https://example.test/a.mp4")
				return serveAction(handler, "Seek", soapBody(`<Unit>TRACK_NR</Unit><Target>1</Target>`), "10.0.0.1:1")
			},
		},
		{
			name:    "Seek with malformed target returns 711",
			wantErr: 711,
			run: func(t *testing.T) *httptest.ResponseRecorder {
				st, cleanup := newAVTState(t, nil)
				defer cleanup()
				handler := AVTransportHandler(st, config.Config{})
				setupAVT(t, st, handler, "10.0.0.1:1", "https://example.test/a.mp4")
				return serveAction(handler, "Seek", soapBody(`<Unit>REL_TIME</Unit><Target>not-a-time</Target>`), "10.0.0.1:1")
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := tt.run(t)
			assertUPnPError(t, rec, tt.wantErr)
		})
	}
}

// TestAVT_VendorSessionErrorShape pins the vendor error convention: numeric
// code >= 800 with an ERRC_-prefixed description, so custom errors stay
// distinguishable from the standardized 700–799 range.
func TestAVT_VendorSessionErrorShape(t *testing.T) {
	st, cleanup := newAVTState(t, nil)
	defer cleanup()
	handler := AVTransportHandler(st, config.Config{AllowSessionPreempt: false})
	serveAction(handler, "SetAVTransportURI", soapBody(`<CurrentURI>https://example.test/a.mp4</CurrentURI>`), "10.0.0.1:1")

	rec := serveAction(handler, "Play", soapBody(`<Speed>1</Speed>`), "10.0.0.2:1")
	assertUPnPError(t, rec, 800)
	if code := soapErrorCode(t, rec.Body.String()); code != "800" {
		t.Fatalf("errorCode=%q, want 800", code)
	}
	if !strings.Contains(rec.Body.String(), "ERRC_SESSION_IN_USE") {
		t.Fatalf("errorDescription missing ERRC_ prefix; body=%s", rec.Body.String())
	}
}

func TestTimeToSeconds_RejectsNonFiniteSeconds(t *testing.T) {
	for _, in := range []string{"0:0:NaN", "0:0:nan", "0:0:Inf", "0:0:+Inf", "0:0:-Inf", "0:0:-0x1p9999"} {
		if _, err := timeToSeconds(in); err == nil {
			t.Errorf("timeToSeconds(%q) accepted a non-finite value", in)
		}
	}
	// Finite values keep working, including fractions.
	if got, err := timeToSeconds("01:02:03.5"); err != nil || got != 3723.5 {
		t.Fatalf("timeToSeconds(01:02:03.5)=(%v, %v), want 3723.5, nil", got, err)
	}
}

// TestSeek_RejectsNaNAndInfTargets is the handler-level gate for audit M5:
// "0:0:NaN" used to pass every range comparison (NaN compares false) and
// reached the player IPC layer, hanging the connection. It must be rejected
// with a SOAP fault before any player interaction.
func TestSeek_RejectsNaNAndInfTargets(t *testing.T) {
	fake := newFakePlayer()
	st, cleanup := newAVTState(t, func() player.Player { return fake })
	defer cleanup()
	handler := AVTransportHandler(st, config.Config{})
	const remote = "10.0.0.1:1"
	setupAVT(t, st, handler, remote, "https://example.test/a.mp4")

	for _, target := range []string{"0:0:NaN", "0:0:nan", "0:0:Inf", "0:0:+Inf"} {
		rec := serveAction(handler, "Seek", soapBody(`<Unit>REL_TIME</Unit><Target>`+target+`</Target>`), remote)
		assertUPnPError(t, rec, 711)
	}

	fake.mu.Lock()
	seeks := len(fake.seeks)
	fake.mu.Unlock()
	if seeks != 0 {
		t.Fatalf("non-finite seeks reached the player: %d", seeks)
	}

	// A valid fractional target still passes through.
	rec := serveAction(handler, "Seek", soapBody(`<Unit>REL_TIME</Unit><Target>0:0:59.5</Target>`), remote)
	assertSOAPSuccess(t, rec, "SeekResponse")
}

func TestTrackAndNrTracks_Consistency(t *testing.T) {
	st, cleanup := newAVTState(t, nil)
	defer cleanup()
	handler := AVTransportHandler(st, config.Config{})
	const remote = "10.0.0.1:1"

	// Empty renderer: zero tracks on both queries.
	rec := serveAction(handler, "GetMediaInfo", soapBody(``), remote)
	assertSOAPSuccess(t, rec, "GetMediaInfoResponse")
	if !strings.Contains(rec.Body.String(), "<NrTracks>0</NrTracks>") {
		t.Fatalf("empty renderer NrTracks; body=%s", rec.Body.String())
	}
	rec = serveAction(handler, "GetPositionInfo", soapBody(``), remote)
	assertSOAPSuccess(t, rec, "GetPositionInfoResponse")
	if !strings.Contains(rec.Body.String(), "<Track>0</Track>") {
		t.Fatalf("empty renderer Track; body=%s", rec.Body.String())
	}

	// With content loaded: exactly one track on both queries.
	setupAVT(t, st, handler, remote, "https://example.test/a.mp4")
	rec = serveAction(handler, "GetMediaInfo", soapBody(``), remote)
	if !strings.Contains(rec.Body.String(), "<NrTracks>1</NrTracks>") {
		t.Fatalf("loaded renderer NrTracks; body=%s", rec.Body.String())
	}
	rec = serveAction(handler, "GetPositionInfo", soapBody(``), remote)
	if !strings.Contains(rec.Body.String(), "<Track>1</Track>") {
		t.Fatalf("loaded renderer Track; body=%s", rec.Body.String())
	}
}

// TestAVT_MetricsRecordedOnErrorPaths verifies UPnP error counters on paths
// that previously skipped them (audit LOW: avtransport.go empty-URI 402 and
// unknown-action 401 paths).
func TestAVT_MetricsRecordedOnErrorPaths(t *testing.T) {
	st, cleanup := newAVTState(t, nil)
	defer cleanup()
	m := monitoring.GetMetrics()
	errorsBefore := m.UPnPErrorsTotal
	actionsBefore := m.UPnPActionsTotal
	handler := AVTransportHandler(st, config.Config{})

	serveAction(handler, "SetAVTransportURI", soapBody(`<CurrentURI></CurrentURI>`), "10.0.0.1:1")
	if m.UPnPErrorsTotal != errorsBefore+1 {
		t.Fatalf("upnp errors=%d, want %d after empty-URI SetURI", m.UPnPErrorsTotal, errorsBefore+1)
	}

	serveAction(handler, "BogusAction", soapBody(``), "10.0.0.1:1")
	if m.UPnPErrorsTotal != errorsBefore+2 {
		t.Fatalf("upnp errors=%d, want %d after unknown action", m.UPnPErrorsTotal, errorsBefore+2)
	}
	if m.UPnPActionsTotal != actionsBefore+2 {
		t.Fatalf("upnp actions=%d, want %d", m.UPnPActionsTotal, actionsBefore+2)
	}
}

// TestCM_MetricsRecorded verifies ConnectionManager actions and errors feed
// the UPnP counters (audit LOW: CM metrics missing).
func TestCM_MetricsRecorded(t *testing.T) {
	st, cleanup := newCMState(t)
	defer cleanup()
	m := monitoring.GetMetrics()
	actionsBefore, errorsBefore := m.UPnPActionsTotal, m.UPnPErrorsTotal
	handler := ConnectionManagerHandler(st, config.Config{})

	serveAction(handler, "GetProtocolInfo", soapBody(``), "10.0.0.1:1")
	if m.UPnPActionsTotal != actionsBefore+1 {
		t.Fatalf("upnp actions=%d, want %d after GetProtocolInfo", m.UPnPActionsTotal, actionsBefore+1)
	}

	serveAction(handler, "GetCurrentConnectionInfo", soapBody(`<ConnectionID>5</ConnectionID>`), "10.0.0.1:1")
	if m.UPnPErrorsTotal != errorsBefore+1 {
		t.Fatalf("upnp errors=%d, want %d after invalid connection id", m.UPnPErrorsTotal, errorsBefore+1)
	}
}
