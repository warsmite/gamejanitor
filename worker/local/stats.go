package sandbox

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
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
		if memMax > 0 && memMax < 1<<50 {
			stats.MemoryLimitMB = int(memMax / (1024 * 1024))
		}

		stats.CPUPercent = readCgroupCPU(cgroupPath)
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

// readCgroupCPU reads CPU usage from cgroup v2 cpu.stat and computes percentage
// using delta between two samples. Covers all processes in the cgroup scope.
var (
	cgroupCPUMu   sync.Mutex
	lastCgroupCPU = make(map[string]cgroupCPUSample)
)

type cgroupCPUSample struct {
	usageUsec int64
	checked   time.Time
}

func readCgroupCPU(cgroupPath string) float64 {
	data, err := os.ReadFile(filepath.Join(cgroupPath, "cpu.stat"))
	if err != nil {
		return 0
	}

	var usageUsec int64
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "usage_usec ") {
			usageUsec, _ = strconv.ParseInt(strings.TrimPrefix(line, "usage_usec "), 10, 64)
			break
		}
	}

	now := time.Now()
	current := cgroupCPUSample{usageUsec: usageUsec, checked: now}

	cgroupCPUMu.Lock()
	prev, ok := lastCgroupCPU[cgroupPath]
	lastCgroupCPU[cgroupPath] = current
	cgroupCPUMu.Unlock()

	if !ok || time.Since(prev.checked) < time.Second {
		return 0
	}

	usageDelta := float64(current.usageUsec - prev.usageUsec)
	timeDelta := now.Sub(prev.checked).Microseconds()

	if timeDelta <= 0 {
		return 0
	}

	// usageDelta / timeDelta = fraction of one CPU core. Multiply by 100 for percent.
	return (usageDelta / float64(timeDelta)) * 100
}

// readNetDevBytes reads rx/tx bytes for an interface from /proc/<pid>/net/dev.
// Uses the namespace holder's PID to read stats from inside the network namespace.
func readNetDevBytes(nsPID int, iface string) (rxBytes, txBytes int64) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/net/dev", nsPID))
	if err != nil {
		return 0, 0
	}
	prefix := iface + ":"
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, prefix) {
			continue
		}
		fields := strings.Fields(line[len(prefix):])
		if len(fields) < 9 {
			return 0, 0
		}
		rx, _ := strconv.ParseInt(fields[0], 10, 64)
		tx, _ := strconv.ParseInt(fields[8], 10, 64)
		return rx, tx
	}
	return 0, 0
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
var (
	cpuMu        sync.Mutex
	lastCPUCheck = make(map[int]cpuSample)
)

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

	cpuMu.Lock()
	prev, ok := lastCPUCheck[pid]
	lastCPUCheck[pid] = current
	cpuMu.Unlock()

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
