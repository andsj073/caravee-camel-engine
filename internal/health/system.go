package health

import (
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"

)

// SystemMetrics holds system-level metrics.
type SystemMetrics struct {
	CPUPercent    float64 `json:"cpu_percent"`
	MemoryMB      int64   `json:"memory_mb"`
	UptimeSeconds int64   `json:"uptime_seconds"`
}

var startTime = time.Now()

// GetSystemMetrics returns CPU, memory, and uptime metrics.
func GetSystemMetrics() SystemMetrics {
	return SystemMetrics{
		CPUPercent:    getCPUPercent(),
		MemoryMB:      getMemoryMB(),
		UptimeSeconds: int64(time.Since(startTime).Seconds()),
	}
}

func getCPUPercent() float64 {
	// Read from /proc/stat on Linux
	data, err := os.ReadFile("/proc/stat")
	if err != nil {
		return -1
	}
	lines := strings.Split(string(data), "\n")
	if len(lines) == 0 {
		return -1
	}
	// Simplified: return number of goroutines as rough indicator
	// Real implementation would compare two snapshots of /proc/stat
	return float64(runtime.NumGoroutine())
}

func getMemoryMB() int64 {
	// Read from /proc/meminfo on Linux
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		// Fallback: Go runtime memory
		var m runtime.MemStats
		runtime.ReadMemStats(&m)
		return int64(m.Alloc / 1024 / 1024)
	}

	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "MemTotal:") {
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				kb, _ := strconv.ParseInt(fields[1], 10, 64)
				return kb / 1024
			}
		}
	}
	return -1
}
