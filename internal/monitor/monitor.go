// Package monitor polls Camel metrics and pushes error events to cloud.
package monitor

import (
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/caravee/engine/internal/camel"
)

const (
	PollInterval         = 30 * time.Second
	FailureMetric        = "camel_exchanges_failed_total"
	InFlightMetric       = "camel_exchanges_inflight"
	ExchangesTotalMetric = "camel_exchanges_total"
)

// RouteErrorEvent is sent to cloud when failures increase.
type RouteErrorEvent struct {
	IntegrationID string  `json:"integration_id"`
	FailureDelta  float64 `json:"failure_delta"` // new failures since last check
	TotalFailures float64 `json:"total_failures"`
	InFlight      float64 `json:"inflight"`
	Timestamp     string  `json:"timestamp"`
}

// Sender is implemented by cloud.Connection.
type Sender interface {
	SendRouteError(evt RouteErrorEvent)
	ListDeployedRoutes() []string
	UpdateRunStats(integrationID string, totalExchanges int64)
	RecordRunFailure(integrationID string, errorSummary string)
}

// Monitor polls Camel metrics and emits error events.
type Monitor struct {
	camel             *camel.Client
	sender            Sender
	baseline          map[string]float64 // last known failure count per route
	exchangesBaseline map[string]float64 // last known total exchanges per route
	mu                sync.Mutex
	stop              chan struct{}
}

// New creates a monitor.
func New(c *camel.Client, s Sender) *Monitor {
	return &Monitor{
		camel:             c,
		sender:            s,
		baseline:          map[string]float64{},
		exchangesBaseline: map[string]float64{},
		stop:              make(chan struct{}),
	}
}

// Start begins background polling. Call Stop() to halt.
func (m *Monitor) Start() {
	go m.loop()
}

// Stop halts the monitor.
func (m *Monitor) Stop() {
	close(m.stop)
}

func (m *Monitor) loop() {
	ticker := time.NewTicker(PollInterval)
	defer ticker.Stop()

	// First tick after a short delay so agent can finish connecting
	time.Sleep(10 * time.Second)
	m.check()

	for {
		select {
		case <-ticker.C:
			m.check()
		case <-m.stop:
			return
		}
	}
}

func (m *Monitor) check() {
	routes := m.sender.ListDeployedRoutes()
	if len(routes) == 0 {
		return
	}

	for _, routeID := range routes {
		metrics, err := m.camel.GetRouteMetrics(routeID)
		if err != nil || len(metrics) == 0 {
			continue // Camel not running or route not loaded yet
		}

		failures := metrics[FailureMetric]
		inflight := metrics[InFlightMetric]
		totalExchanges := metrics[ExchangesTotalMetric]

		m.mu.Lock()
		prevFailures := m.baseline[routeID]
		failureDelta := failures - prevFailures
		if failureDelta > 0 {
			m.baseline[routeID] = failures
		} else if prevFailures == 0 {
			m.baseline[routeID] = failures // initialize baseline
		}

		prevExchanges := m.exchangesBaseline[routeID]
		exchangeDelta := totalExchanges - prevExchanges
		if exchangeDelta > 0 {
			m.exchangesBaseline[routeID] = totalExchanges
		} else if prevExchanges == 0 {
			m.exchangesBaseline[routeID] = totalExchanges
		}
		m.mu.Unlock()

		if failureDelta > 0 {
			slog.Warn("Route failures detected",
				"route", routeID,
				"new_failures", failureDelta,
				"total", failures,
			)
			m.sender.SendRouteError(RouteErrorEvent{
				IntegrationID: routeID,
				FailureDelta:  failureDelta,
				TotalFailures: failures,
				InFlight:      inflight,
				Timestamp:     time.Now().UTC().Format(time.RFC3339),
			})
			m.sender.RecordRunFailure(routeID, fmt.Sprintf("%.0f new exchange failure(s), total: %.0f", failureDelta, failures))
		}

		if exchangeDelta > 0 {
			m.sender.UpdateRunStats(routeID, int64(totalExchanges))
		}
	}
}
