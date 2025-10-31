package monitoring

import (
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
			startTime:           time.Now(),
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
		m.GetUptime().String(),
		m.HTTPRequestsTotal,
		m.PlayerSessionsTotal,
		m.PlayerErrorsTotal,
		m.UPnPActionsTotal,
		m.UPnPErrorsTotal)
}