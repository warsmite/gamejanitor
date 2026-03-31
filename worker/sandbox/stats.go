package sandbox

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/warsmite/gamejanitor/worker"
)

// readCgroupStats reads memory and CPU stats from cgroups v2.
// Falls back to /proc/<pid> if cgroup data is unavailable.
func readCgroupStats(unitName string, pid int) (*worker.InstanceStats, error) {
	stats := &worker.InstanceStats{}

	// Try cgroups v2 first (via systemd slice path)
	cgroupPath := findCgroupPath(unitName, pid)
	if cgroupPath != "" {
		stats.MemoryUsageMB = int(readCgroupInt(filepath.Join(cgroupPath, "memory.current")) / (1024 * 1024))
		memMax := readCgroupInt(filepath.Join(cgroupPath, "memory.max"))
		if memMax > 0 && memMax < 1<<50 { // "max" reads as a very large number
			stats.MemoryLimitMB = int(memMax / (1024 * 1024))
		}

		// CPU: read cpu.stat for usage_usec, compute % over interval
		// For now, read instantaneous from /proc since cpu.stat needs two samples
		stats.CPUPercent = readProcCPU(pid)
	} else if pid > 0 {
		// Fallback to /proc
		stats.MemoryUsageMB = readProcMemory(pid)
		stats.CPUPercent = readProcCPU(pid)
	}

	return stats, nil
}

// findCgroupPath locates the cgroup v2 directory for a systemd scope unit.
func findCgroupPath(unitName string, pid int) string {
	if unitName == "" {
		return ""
	}

	// systemd scope units live at /sys/fs/cgroup/user.slice/user-<uid>.slice/<unit>.scope
	// Read the process's cgroup to find the exact path
	if pid > 0 {
		data, err := os.ReadFile(fmt.Sprintf("/proc/%d/cgroup", pid))
		if err == nil {
			// cgroups v2: single line "0::/<path>"
			for _, line := range strings.Split(string(data), "\n") {
				if strings.HasPrefix(line, "0::") {
					cgPath := strings.TrimPrefix(line, "0::")
					fullPath := filepath.Join("/sys/fs/cgroup", strings.TrimSpace(cgPath))
					if _, err := os.Stat(fullPath); err == nil {
						return fullPath
					}
				}
			}
		}
	}

	return ""
}

func readCgroupInt(path string) int64 {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	s := strings.TrimSpace(string(data))
	if s == "max" {
		return 1 << 60 // sentinel for "unlimited"
	}
	v, _ := strconv.ParseInt(s, 10, 64)
	return v
}

// readProcMemory reads RSS from /proc/<pid>/status.
func readProcMemory(pid int) int {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "VmRSS:") {
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				kb, _ := strconv.Atoi(fields[1])
				return kb / 1024
			}
		}
	}
	return 0
}

// readProcCPU returns a rough CPU% from /proc/<pid>/stat.
// This is approximate — proper measurement needs two samples over time.
var lastCPUCheck = make(map[int]cpuSample)

type cpuSample struct {
	utime   uint64
	stime   uint64
	uptime  float64
	checked time.Time
}

func readProcCPU(pid int) float64 {
	statData, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0
	}
	uptimeData, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return 0
	}

	fields := strings.Fields(string(statData))
	if len(fields) < 15 {
		return 0
	}
	utime, _ := strconv.ParseUint(fields[13], 10, 64)
	stime, _ := strconv.ParseUint(fields[14], 10, 64)

	uptimeFields := strings.Fields(string(uptimeData))
	uptime, _ := strconv.ParseFloat(uptimeFields[0], 64)

	current := cpuSample{utime: utime, stime: stime, uptime: uptime, checked: time.Now()}

	prev, ok := lastCPUCheck[pid]
	lastCPUCheck[pid] = current

	if !ok || time.Since(prev.checked) < time.Second {
		// First sample or too close together — return 0
		return 0
	}

	totalDelta := float64((current.utime + current.stime) - (prev.utime + prev.stime))
	timeDelta := (current.uptime - prev.uptime) * 100 // clock ticks per second (typically 100)

	if timeDelta <= 0 {
		return 0
	}

	return (totalDelta / timeDelta) * 100
}
