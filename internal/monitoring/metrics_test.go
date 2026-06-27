package monitoring

import (
	"strings"
	"testing"
	"time"
)

func TestMetricsRender(t *testing.T) {
	m := &Metrics{
		HTTPRequestsByMethod: make(map[string]int64),
		startTime:            time.Now().Add(-time.Second),
	}
	m.RecordHTTPRequest("POST", 250*time.Millisecond)
	m.RecordPlayerSession()
	m.RecordPlayerError()
	m.RecordUPnPAction()
	m.RecordUPnPError()

	text := m.RenderText()
	for _, want := range []string{
		"rcast_http_requests_total 1",
		`rcast_http_requests_by_method{method="POST"} 1`,
		"rcast_http_request_duration_seconds_total 0.250000",
		"rcast_player_sessions_total 1",
		"rcast_player_errors_total 1",
		"rcast_upnp_actions_total 1",
		"rcast_upnp_errors_total 1",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("metrics output missing %q:\n%s", want, text)
		}
	}
}
