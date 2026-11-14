package api

import (
	"bufio"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

type cpuSample struct {
	total, idle uint64
	at          time.Time
}

var cpuMonitor struct {
	sync.Mutex
	previous cpuSample
}

func parseCPUSample(data string) (cpuSample, error) {
	line := strings.SplitN(data, "\n", 2)[0]
	fields := strings.Fields(line)
	if len(fields) < 5 || fields[0] != "cpu" {
		return cpuSample{}, fmt.Errorf("invalid /proc/stat cpu line")
	}
	var result cpuSample
	for i, field := range fields[1:] {
		n, e := strconv.ParseUint(field, 10, 64)
		if e != nil {
			return cpuSample{}, e
		}
		result.total += n
		if i == 3 || i == 4 {
			result.idle += n
		}
	}
	return result, nil
}
func parseMemory(data string) (total, used uint64, err error) {
	var available uint64
	scanner := bufio.NewScanner(strings.NewReader(data))
	for scanner.Scan() {
		f := strings.Fields(scanner.Text())
		if len(f) < 2 {
			continue
		}
		n, e := strconv.ParseUint(f[1], 10, 64)
		if e != nil {
			continue
		}
		switch strings.TrimSuffix(f[0], ":") {
		case "MemTotal":
			total = n * 1024
		case "MemAvailable":
			available = n * 1024
		}
	}
	if total == 0 {
		return 0, 0, fmt.Errorf("MemTotal unavailable")
	}
	if available > total {
		available = total
	}
	return total, total - available, nil
}
func (s *Server) systemMetrics(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		writeError(w, http.StatusMethodNotAllowed, "GET required")
		return
	}
	if !validLocalReadRequest(r) {
		writeError(w, http.StatusForbidden, "local request required")
		return
	}
	memoryData, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "memory metrics unavailable")
		return
	}
	total, used, err := parseMemory(string(memoryData))
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "memory metrics unavailable")
		return
	}
	response := struct {
		CPUPercent       *float64 `json:"cpu_percent"`
		MemoryPercent    float64  `json:"memory_percent"`
		MemoryUsedBytes  uint64   `json:"memory_used_bytes"`
		MemoryTotalBytes uint64   `json:"memory_total_bytes"`
	}{MemoryPercent: float64(used) * 100 / float64(total), MemoryUsedBytes: used, MemoryTotalBytes: total}
	if cpuData, err := os.ReadFile("/proc/stat"); err == nil {
		if current, e := parseCPUSample(string(cpuData)); e == nil {
			cpuMonitor.Lock()
			previous := cpuMonitor.previous
			current.at = time.Now()
			cpuMonitor.previous = current
			cpuMonitor.Unlock()
			if previous.total > 0 && current.total > previous.total && current.idle >= previous.idle {
				value := 100 * (1 - float64(current.idle-previous.idle)/float64(current.total-previous.total))
				if value < 0 {
					value = 0
				}
				if value > 100 {
					value = 100
				}
				response.CPUPercent = &value
			}
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, response)
}
