// Package camel provides a client for the local Camel sidecar.
// All communication (health, route control) goes through this client.
package camel

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Client talks to the Camel sidecar running on the same host.
type Client struct {
	baseURL    string
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

// New creates a Camel client targeting the given base URL (e.g. http://localhost:8090).
func New(baseURL string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		httpClient: &http.Client{
			Timeout: 5 * time.Second,
		},
	}
}

// --- Health ---

// GetHealth returns current health status from Camel's MicroProfile Health endpoint.
func (c *Client) GetHealth() HealthData {
	result := HealthData{Status: "UNKNOWN"}

	resp, err := c.httpClient.Get(c.baseURL + "/health/ready")
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

	for _, check := range health.Checks {
		if check.Name == "camel-routes" || strings.Contains(check.Name, "route") {
			for key, value := range check.Data {
				if strings.HasPrefix(key, "route.") && key != "route.count" {
					result.Routes = append(result.Routes, RouteStatus{
						ID:     strings.TrimPrefix(key, "route."),
						Status: value,
					})
				}
			}
		}
	}

	return result
}

// WaitForCamel blocks until Camel's liveness endpoint responds or timeout.
func (c *Client) WaitForCamel(timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := c.httpClient.Get(c.baseURL + "/health/live")
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

// CheckRoutes polls until specific routes appear in health checks (max 30s).
func (c *Client) CheckRoutes(routeIDs []string) map[string]string {
	result := make(map[string]string)
	for _, id := range routeIDs {
		result[id] = "Unknown"
	}

	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		health := c.GetHealth()
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

// --- Route control ---

// SuspendRoute suspends a running route (stops consuming new messages, drains in-flight).
func (c *Client) SuspendRoute(routeID string) error {
	return c.routeCommand(routeID, "suspend")
}

// ResumeRoute resumes a suspended route.
func (c *Client) ResumeRoute(routeID string) error {
	return c.routeCommand(routeID, "resume")
}

// RouteStatus returns the current status of a route (Started/Suspended/Stopped).
func (c *Client) RouteStatus(routeID string) (string, error) {
	resp, err := c.httpClient.Get(fmt.Sprintf("%s/camel/routes/%s/status", c.baseURL, routeID))
	if err != nil {
		return "", fmt.Errorf("route status: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == 404 {
		return "NotFound", nil
	}
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("route status: HTTP %d", resp.StatusCode)
	}

	var result struct {
		Status string `json:"status"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("route status decode: %w", err)
	}
	return result.Status, nil
}

// PlatformHTTPPath describes an active platform-http route exposed by the Camel runtime.
type PlatformHTTPPath struct {
	Path          string   `json:"path"`
	Methods       []string `json:"methods"`
	IntegrationID string   `json:"integration_id"`
}

// GetPlatformHTTPPaths queries the Camel routes API and returns all routes that use
// platform-http as a source, including their paths, HTTP methods, and route IDs.
func (c *Client) GetPlatformHTTPPaths() ([]PlatformHTTPPath, error) {
	resp, err := c.httpClient.Get(c.baseURL + "/camel/routes")
	if err != nil {
		return nil, ErrNoSidecar
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("camel routes: HTTP %d", resp.StatusCode)
	}

	var body struct {
		Value []struct {
			RouteID string `json:"routeId"`
			URI     string `json:"uri"`
			State   string `json:"state"`
		} `json:"value"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("camel routes decode: %w", err)
	}

	var paths []PlatformHTTPPath
	for _, r := range body.Value {
		if !strings.HasPrefix(r.URI, "platform-http:") {
			continue
		}
		// Parse URI: platform-http:///path?httpMethodRestrict=POST,GET
		parsed, err := url.Parse(r.URI)
		if err != nil {
			slog.Warn("Could not parse platform-http URI", "uri", r.URI, "error", err)
			continue
		}
		path := parsed.Path
		if path == "" {
			path = "/"
		}
		var methods []string
		if restrict := parsed.Query().Get("httpMethodRestrict"); restrict != "" {
			for _, m := range strings.Split(restrict, ",") {
				if m = strings.TrimSpace(m); m != "" {
					methods = append(methods, m)
				}
			}
		}
		paths = append(paths, PlatformHTTPPath{
			Path:          path,
			Methods:       methods,
			IntegrationID: r.RouteID,
		})
	}
	return paths, nil
}

// GetCamelVersion tries to fetch the Camel version from /q/info (Quarkus info endpoint).
// Requires quarkus-info extension in the Camel runtime.
func (c *Client) GetCamelVersion() string {
	resp, err := c.httpClient.Get(c.baseURL + "/q/info")
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return ""
	}
	var info map[string]interface{}
	if json.NewDecoder(resp.Body).Decode(&info) != nil {
		return ""
	}
	// Flat key
	if v, ok := info["camel.version"]; ok {
		return fmt.Sprintf("%v", v)
	}
	// Nested: {"camel": {"version": "x.y.z"}}
	if camelMap, ok := info["camel"].(map[string]interface{}); ok {
		if v, ok := camelMap["version"]; ok {
			return fmt.Sprintf("%v", v)
		}
	}
	return ""
}

// ErrNoSidecar is returned when the Camel sidecar is not reachable.
var ErrNoSidecar = fmt.Errorf("camel sidecar not available")

func (c *Client) routeCommand(routeID, command string) error {
	url := fmt.Sprintf("%s/camel/routes/%s/%s", c.baseURL, routeID, command)
	req, _ := http.NewRequest(http.MethodPost, url, nil)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		// Connection refused = no Camel sidecar running
		slog.Warn("Camel sidecar unreachable", "command", command, "route", routeID, "error", err)
		return ErrNoSidecar
	}
	defer resp.Body.Close()
	if resp.StatusCode == 404 {
		// Camel running but route not found — treat as no-op (route may not be loaded yet)
		slog.Warn("Camel route not found (may not be loaded yet)", "route", routeID, "command", command)
		return nil
	}
	if resp.StatusCode != 200 {
		return fmt.Errorf("camel %s route %s: HTTP %d", command, routeID, resp.StatusCode)
	}
	slog.Info("Route command ok", "route", routeID, "command", command)
	return nil
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

