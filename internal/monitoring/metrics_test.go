package monitoring

import (
	"strings"
	"sync"
	"testing"
	"time"
)

func newTestMetrics() *Metrics {
	return &Metrics{
		HTTPRequestsByMethod: make(map[string]int64),
		startTime:            time.Now(),
	}
}

func TestMetricsRender(t *testing.T) {
	m := newTestMetrics()
	m.startTime = time.Now().Add(-time.Second)
	m.RecordHTTPRequest("POST", 250*time.Millisecond)
	m.RecordPlayerSession()
	m.RecordPlayerError()
	m.RecordUPnPAction()
	m.RecordUPnPError()

	text := m.RenderText()
	for _, want := range []string{
		"rcast_uptime_seconds",
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

func TestRecordHTTPRequestIncrementsCounters(t *testing.T) {
	m := newTestMetrics()
	m.RecordHTTPRequest("GET", 10*time.Millisecond)
	m.RecordHTTPRequest("GET", 20*time.Millisecond)
	m.RecordHTTPRequest("POST", 5*time.Millisecond)

	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.HTTPRequestsTotal != 3 {
		t.Errorf("HTTPRequestsTotal = %d, want 3", m.HTTPRequestsTotal)
	}
	if m.HTTPRequestsByMethod["GET"] != 2 {
		t.Errorf("GET = %d, want 2", m.HTTPRequestsByMethod["GET"])
	}
	if m.HTTPRequestsByMethod["POST"] != 1 {
		t.Errorf("POST = %d, want 1", m.HTTPRequestsByMethod["POST"])
	}
	if m.HTTPRequestDuration != 35*time.Millisecond {
		t.Errorf("HTTPRequestDuration = %v, want 35ms", m.HTTPRequestDuration)
	}
}

func TestRecordPlayerSessionIncrements(t *testing.T) {
	m := newTestMetrics()
	m.RecordPlayerSession()
	m.RecordPlayerSession()
	if m.PlayerSessionsTotal != 2 {
		t.Errorf("PlayerSessionsTotal = %d, want 2", m.PlayerSessionsTotal)
	}
}

func TestRecordPlayerErrorIncrements(t *testing.T) {
	m := newTestMetrics()
	m.RecordPlayerError()
	if m.PlayerErrorsTotal != 1 {
		t.Errorf("PlayerErrorsTotal = %d, want 1", m.PlayerErrorsTotal)
	}
}

func TestRecordUPnPActionAndError(t *testing.T) {
	m := newTestMetrics()
	for i := 0; i < 3; i++ {
		m.RecordUPnPAction()
	}
	m.RecordUPnPError()
	if m.UPnPActionsTotal != 3 {
		t.Errorf("UPnPActionsTotal = %d, want 3", m.UPnPActionsTotal)
	}
	if m.UPnPErrorsTotal != 1 {
		t.Errorf("UPnPErrorsTotal = %d, want 1", m.UPnPErrorsTotal)
	}
}

func TestGetUptimeNonNegative(t *testing.T) {
	m := newTestMetrics()
	if u := m.GetUptime(); u < 0 {
		t.Errorf("GetUptime = %v, want >= 0", u)
	}
}

func TestRenderTextEmptyIsNonEmpty(t *testing.T) {
	m := newTestMetrics()
	text := m.RenderText()
	if text == "" {
		t.Fatal("RenderText returned empty string for fresh metrics")
	}
	// Even with zero state, the metric names must be present.
	for _, want := range []string{
		"rcast_uptime_seconds",
		"rcast_http_requests_total 0",
		"rcast_player_sessions_total 0",
		"rcast_player_errors_total 0",
		"rcast_upnp_actions_total 0",
		"rcast_upnp_errors_total 0",
		"# HELP rcast_uptime_seconds",
		"# TYPE rcast_uptime_seconds gauge",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("empty-state output missing %q", want)
		}
	}
}

// Concurrency stress test — the race-detector anchor for the package. ~50
// goroutines hammer every Record method, then we assert the totals equal the
// expected sums.
func TestMetricsConcurrentRecords(t *testing.T) {
	m := newTestMetrics()
	const goroutines = 50
	const perGoroutine = 100

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < perGoroutine; j++ {
				m.RecordHTTPRequest("GET", time.Millisecond)
				m.RecordPlayerSession()
				m.RecordPlayerError()
				m.RecordUPnPAction()
				m.RecordUPnPError()
			}
		}()
	}
	wg.Wait()

	m.mu.RLock()
	defer m.mu.RUnlock()
	total := int64(goroutines * perGoroutine)
	if m.HTTPRequestsTotal != total {
		t.Errorf("HTTPRequestsTotal = %d, want %d", m.HTTPRequestsTotal, total)
	}
	if m.PlayerSessionsTotal != total {
		t.Errorf("PlayerSessionsTotal = %d, want %d", m.PlayerSessionsTotal, total)
	}
	if m.PlayerErrorsTotal != total {
		t.Errorf("PlayerErrorsTotal = %d, want %d", m.PlayerErrorsTotal, total)
	}
	if m.UPnPActionsTotal != total {
		t.Errorf("UPnPActionsTotal = %d, want %d", m.UPnPActionsTotal, total)
	}
	if m.UPnPErrorsTotal != total {
		t.Errorf("UPnPErrorsTotal = %d, want %d", m.UPnPErrorsTotal, total)
	}
	if m.HTTPRequestsByMethod["GET"] != total {
		t.Errorf("GET = %d, want %d", m.HTTPRequestsByMethod["GET"], total)
	}
}

// GetMetrics is a sync.Once singleton that cannot be reset; verify it via
// before/after delta instead. Do NOT touch the underlying Once.
func TestGetMetricsSingletonAndDelta(t *testing.T) {
	before := GetMetrics()
	before.mu.RLock()
	beforeSessions := before.PlayerSessionsTotal
	before.mu.RUnlock()

	// Must return the same pointer on subsequent calls.
	if after := GetMetrics(); after != before {
		t.Fatal("GetMetrics returned a different pointer")
	}

	// Recording on the singleton advances its counter.
	before.RecordPlayerSession()
	before.mu.RLock()
	afterSessions := before.PlayerSessionsTotal
	before.mu.RUnlock()
	if afterSessions != beforeSessions+1 {
		t.Errorf("singleton PlayerSessionsTotal delta = %d, want +1", afterSessions-beforeSessions)
	}
}
