package health

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

)

// Poller checks Camel's MicroProfile Health endpoints.
type Poller struct {
	healthURL  string
	httpClient *http.Client
}

// RouteStatus represents the status of a single route.
type RouteStatus struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

// HealthData holds parsed health check results.
type HealthData struct {
	Status string
	Routes []RouteStatus
}

// MicroProfile Health response structure
type mpHealthResponse struct {
	Status string    `json:"status"`
	Checks []mpCheck `json:"checks"`
}

type mpCheck struct {
	Name   string            `json:"name"`
	Status string            `json:"status"`
	Data   map[string]string `json:"data,omitempty"`
}

// NewPoller creates a health poller for the given Camel health URL.
func NewPoller(healthURL string) *Poller {
	return &Poller{
		healthURL: healthURL,
		httpClient: &http.Client{
			Timeout: 5 * time.Second,
		},
	}
}

// GetHealth returns current health status from Camel.
func (p *Poller) GetHealth() HealthData {
	result := HealthData{Status: "UNKNOWN"}

	resp, err := p.httpClient.Get(p.healthURL + "/ready")
	if err != nil {
		slog.Debug("Health check failed", "error", err)
		result.Status = "DOWN"
		return result
	}
	defer resp.Body.Close()

	var health mpHealthResponse
	if err := json.NewDecoder(resp.Body).Decode(&health); err != nil {
		slog.Debug("Failed to parse health response", "error", err)
		result.Status = "DOWN"
		return result
	}

	result.Status = health.Status

	// Extract route statuses from camel-routes check
	for _, check := range health.Checks {
		if check.Name == "camel-routes" || strings.Contains(check.Name, "route") {
			for key, value := range check.Data {
				if strings.HasPrefix(key, "route.") && key != "route.count" {
					routeID := strings.TrimPrefix(key, "route.")
					result.Routes = append(result.Routes, RouteStatus{
						ID:     routeID,
						Status: value,
					})
				}
			}
		}
	}

	return result
}

// CheckRoutes waits for specific routes to appear in health checks.
// Returns a map of routeID → status after timeout.
func (p *Poller) CheckRoutes(routeIDs []string) map[string]string {
	result := make(map[string]string)
	for _, id := range routeIDs {
		result[id] = "Unknown"
	}

	// Poll for up to 30 seconds
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		health := p.GetHealth()
		allFound := true
		for _, id := range routeIDs {
			found := false
			for _, rs := range health.Routes {
				if rs.ID == id {
					result[id] = rs.Status
					found = true
					break
				}
			}
			if !found {
				allFound = false
			}
		}
		if allFound {
			return result
		}
		time.Sleep(1 * time.Second)
	}

	return result
}

// WaitForCamel blocks until Camel's liveness endpoint responds.
func (p *Poller) WaitForCamel(timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := p.httpClient.Get(p.healthURL + "/live")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				slog.Info("Camel runtime is ready")
				return nil
			}
		}
		time.Sleep(1 * time.Second)
	}
	return fmt.Errorf("camel not ready after %s", timeout)
}
