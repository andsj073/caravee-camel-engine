package camel

import (
	"bufio"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
)

const metricsTTL = 5 * time.Second

type cacheEntry struct {
	value     map[string]float64
	expiresAt time.Time
}

var (
	metricsCacheMu sync.Mutex
	metricsCache   = map[string]cacheEntry{}
)

// ScrapeMetrics fetches /observe/metrics and returns a parsed flat map.
// Cache key controls TTL grouping — use "__engine__" for engine-level,
// "__all_routes__" for per-route (both read the same endpoint).
func (c *Client) ScrapeMetrics(cacheKey string) (map[string]float64, error) {
	metricsCacheMu.Lock()
	if entry, ok := metricsCache[cacheKey]; ok && time.Now().Before(entry.expiresAt) {
		metricsCacheMu.Unlock()
		return entry.value, nil
	}
	metricsCacheMu.Unlock()

	resp, err := c.httpClient.Get(c.baseURL + "/observe/metrics")
	if err != nil {
		return nil, fmt.Errorf("%w", ErrNoSidecar) // treat as no sidecar
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("metrics: HTTP %d", resp.StatusCode)
	}

	result := parsePrometheus(resp.Body)

	metricsCacheMu.Lock()
	metricsCache[cacheKey] = cacheEntry{value: result, expiresAt: time.Now().Add(metricsTTL)}
	metricsCacheMu.Unlock()

	return result, nil
}

// GetEngineMetrics returns engine-level metrics (CPU, mem, uptime).
func (c *Client) GetEngineMetrics() (map[string]float64, error) {
	return c.ScrapeMetrics("__engine__")
}

// GetRouteMetrics returns metrics for an integration, filtered by routeId label.
// Uses prefix match: integration "sales-orders-receiver" matches routes
// "sales-orders-receiver.main", "sales-orders-receiver.secondary", etc.
// Values are summed across all matching routes.
func (c *Client) GetRouteMetrics(integrationID string) (map[string]float64, error) {
	all, err := c.ScrapeMetrics("__routes__")
	if err != nil {
		return nil, err
	}
	// Match exact routeId or routeId starting with integrationID + "."
	exactLabel := fmt.Sprintf(`routeId="%s"`, integrationID)
	prefixLabel := fmt.Sprintf(`routeId="%s.`, integrationID)
	result := map[string]float64{}
	for k, v := range all {
		if strings.Contains(k, exactLabel) || strings.Contains(k, prefixLabel) {
			bare := k
			if lbrace := strings.Index(k, "{"); lbrace != -1 {
				bare = k[:lbrace]
			}
			// Sum across all matching routes (e.g. .main + .secondary)
			result[bare] += v
		}
	}
	return result, nil
}

// parsePrometheus parses Prometheus text format → flat map.
// Keys: "metric_name{labels}" and bare "metric_name" (last seen value).
func parsePrometheus(body interface{ Read([]byte) (int, error) }) map[string]float64 {
	result := map[string]float64{}
	scanner := bufio.NewScanner(body)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		nameAndLabels, rest, ok := strings.Cut(line, " ")
		if !ok {
			continue
		}
		// Strip timestamp
		valueStr := strings.Fields(rest)[0]
		val, err := strconv.ParseFloat(valueStr, 64)
		if err != nil {
			continue
		}
		result[nameAndLabels] = val
		if lbrace := strings.Index(nameAndLabels, "{"); lbrace != -1 {
			result[nameAndLabels[:lbrace]] = val
		}
	}
	return result
}
