package monitoring

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/tr1v3r/pkg/log"
)

// Metrics tracks basic application metrics
type Metrics struct {
	mu sync.RWMutex

	// HTTP metrics
	HTTPRequestsTotal    int64
	HTTPRequestsByMethod map[string]int64
	HTTPRequestDuration  time.Duration

	// Player metrics
	PlayerSessionsTotal int64
	PlayerErrorsTotal   int64

	// UPnP metrics
	UPnPActionsTotal int64
	UPnPErrorsTotal  int64

	startTime time.Time
}

var (
	globalMetrics *Metrics
	metricsOnce   sync.Once
)

// GetMetrics returns the global metrics instance
func GetMetrics() *Metrics {
	metricsOnce.Do(func() {
		globalMetrics = &Metrics{
			HTTPRequestsByMethod: make(map[string]int64),
			startTime:            time.Now(),
		}
	})
	return globalMetrics
}

// RecordHTTPRequest records an HTTP request
func (m *Metrics) RecordHTTPRequest(method string, duration time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.HTTPRequestsTotal++
	m.HTTPRequestsByMethod[method]++
	m.HTTPRequestDuration += duration
}

// RecordPlayerSession records a player session
func (m *Metrics) RecordPlayerSession() {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.PlayerSessionsTotal++
}

// RecordPlayerError records a player error
func (m *Metrics) RecordPlayerError() {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.PlayerErrorsTotal++
}

// RecordUPnPAction records a UPnP action
func (m *Metrics) RecordUPnPAction() {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.UPnPActionsTotal++
}

// RecordUPnPError records a UPnP error
func (m *Metrics) RecordUPnPError() {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.UPnPErrorsTotal++
}

// GetUptime returns the application uptime
func (m *Metrics) GetUptime() time.Duration {
	m.mu.RLock()
	defer m.mu.RUnlock()

	return time.Since(m.startTime)
}

// LogMetrics logs current metrics
func (m *Metrics) LogMetrics() {
	m.mu.RLock()
	defer m.mu.RUnlock()

	log.Info("Application metrics uptime=%s http_requests_total=%d player_sessions_total=%d player_errors_total=%d upnp_actions_total=%d upnp_errors_total=%d",
		time.Since(m.startTime).String(),
		m.HTTPRequestsTotal,
		m.PlayerSessionsTotal,
		m.PlayerErrorsTotal,
		m.UPnPActionsTotal,
		m.UPnPErrorsTotal)
}

// RenderText renders the metrics in a Prometheus-compatible text exposition
// format, suitable for serving from a /metrics endpoint.
func (m *Metrics) RenderText() string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var b strings.Builder
	uptime := time.Since(m.startTime) // inline; GetUptime would re-lock

	fmt.Fprintf(&b, "# HELP rcast_uptime_seconds Application uptime in seconds\n# TYPE rcast_uptime_seconds gauge\nrcast_uptime_seconds %.0f\n\n", uptime.Seconds())
	fmt.Fprintf(&b, "# HELP rcast_http_requests_total Total HTTP requests received\n# TYPE rcast_http_requests_total counter\nrcast_http_requests_total %d\n\n", m.HTTPRequestsTotal)
	fmt.Fprintf(&b, "# HELP rcast_http_request_duration_seconds_total Cumulative HTTP request duration in seconds\n# TYPE rcast_http_request_duration_seconds_total counter\nrcast_http_request_duration_seconds_total %f\n\n", m.HTTPRequestDuration.Seconds())

	b.WriteString("# HELP rcast_http_requests_by_method HTTP requests broken down by method\n# TYPE rcast_http_requests_by_method counter\n")
	for method, count := range m.HTTPRequestsByMethod {
		fmt.Fprintf(&b, "rcast_http_requests_by_method{method=%q} %d\n", method, count)
	}
	b.WriteString("\n")

	fmt.Fprintf(&b, "# HELP rcast_player_sessions_total Total player sessions created\n# TYPE rcast_player_sessions_total counter\nrcast_player_sessions_total %d\n\n", m.PlayerSessionsTotal)
	fmt.Fprintf(&b, "# HELP rcast_player_errors_total Total player errors\n# TYPE rcast_player_errors_total counter\nrcast_player_errors_total %d\n\n", m.PlayerErrorsTotal)
	fmt.Fprintf(&b, "# HELP rcast_upnp_actions_total Total UPnP actions handled\n# TYPE rcast_upnp_actions_total counter\nrcast_upnp_actions_total %d\n\n", m.UPnPActionsTotal)
	fmt.Fprintf(&b, "# HELP rcast_upnp_errors_total Total UPnP errors returned\n# TYPE rcast_upnp_errors_total counter\nrcast_upnp_errors_total %d\n", m.UPnPErrorsTotal)

	return b.String()
}
