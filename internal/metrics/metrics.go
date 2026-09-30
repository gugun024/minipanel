// Package metrics membaca metrik sistem dari /proc (Linux)
// tanpa dependensi eksternal.
package metrics

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"syscall"
)

// Snapshot adalah satu set metrik sistem.
type Snapshot struct {
	CPUPercent float64 `json:"cpu_percent"`
	MemTotalKB uint64  `json:"mem_total_kb"`
	MemUsedKB  uint64  `json:"mem_used_kb"`
	MemAvailKB uint64  `json:"mem_avail_kb"`
	MemPercent float64 `json:"mem_percent"`
	DiskPath   string  `json:"disk_path"`
	DiskTotal  uint64  `json:"disk_total"`
	DiskUsed   uint64  `json:"disk_used"`
	DiskFree   uint64  `json:"disk_free"`
	DiskPct    float64 `json:"disk_percent"`
	UptimeSec  float64 `json:"uptime_seconds"`
	Load1      float64 `json:"load1"`
	Load5      float64 `json:"load5"`
	Load15     float64 `json:"load15"`
}

var (
	mu       sync.Mutex
	prevIdle uint64
	prevTot  uint64
	prevSet  bool
)

// cpuPercent menghitung penggunaan CPU dari delta /proc/stat.
func cpuPercent() float64 {
	f, err := os.Open("/proc/stat")
	if err != nil {
		return 0
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	if !sc.Scan() {
		return 0
	}
	fields := strings.Fields(sc.Text())
	if len(fields) < 9 || fields[0] != "cpu" {
		return 0
	}
	var vals [8]uint64
	for i := 0; i < 8; i++ {
		v, err := strconv.ParseUint(fields[i+1], 10, 64)
		if err != nil {
			return 0
		}
		vals[i] = v
	}
	idle := vals[3] + vals[4] // idle + iowait
	var tot uint64
	for _, v := range vals {
		tot += v
	}

	mu.Lock()
	defer mu.Unlock()
	if !prevSet {
		prevIdle, prevTot, prevSet = idle, tot, true
		return 0
	}
	dIdle := float64(idle - prevIdle)
	dTot := float64(tot - prevTot)
	prevIdle, prevTot = idle, tot
	if dTot <= 0 {
		return 0
	}
	p := (1 - dIdle/dTot) * 100
	if p < 0 {
		p = 0
	}
	if p > 100 {
		p = 100
	}
	return p
}

// memInfo membaca MemTotal & MemAvailable dari /proc/meminfo (dalam kB).
func memInfo() (total, avail uint64) {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0, 0
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "MemTotal:"):
			total = parseKB(line)
		case strings.HasPrefix(line, "MemAvailable:"):
			avail = parseKB(line)
		}
		if total > 0 && avail > 0 {
			break
		}
	}
	return total, avail
}

func parseKB(line string) uint64 {
	f := strings.Fields(line)
	if len(f) < 2 {
		return 0
	}
	v, _ := strconv.ParseUint(f[1], 10, 64)
	return v
}

// diskInfo membaca penggunaan disk untuk path tertentu via statfs.
func diskInfo(path string) (total, free uint64, err error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, 0, err
	}
	bs := uint64(st.Bsize)
	total = st.Blocks * bs
	free = st.Bavail * bs
	return total, free, nil
}

// uptime membaca uptime dari /proc/uptime (detik).
func uptime() float64 {
	b, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return 0
	}
	f := strings.Fields(string(b))
	if len(f) == 0 {
		return 0
	}
	v, _ := strconv.ParseFloat(f[0], 64)
	return v
}

// loadAvg membaca load average dari /proc/loadavg.
func loadAvg() (l1, l5, l15 float64) {
	b, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return 0, 0, 0
	}
	f := strings.Fields(string(b))
	if len(f) < 3 {
		return 0, 0, 0
	}
	l1, _ = strconv.ParseFloat(f[0], 64)
	l5, _ = strconv.ParseFloat(f[1], 64)
	l15, _ = strconv.ParseFloat(f[2], 64)
	return l1, l5, l15
}

// Collect mengumpulkan snapshot metrik. diskPath menentukan
// filesystem mana yang dilaporkan untuk disk usage.
func Collect(diskPath string) (Snapshot, error) {
	var s Snapshot

	if _, err := os.Stat("/proc/stat"); err != nil {
		return s, fmt.Errorf("/proc tidak tersedia (bukan Linux?): %w", err)
	}

	s.CPUPercent = cpuPercent()

	total, avail := memInfo()
	s.MemTotalKB = total
	s.MemAvailKB = avail
	if total > 0 {
		s.MemUsedKB = total - avail
		s.MemPercent = float64(total-avail) / float64(total) * 100
	}

	dTotal, dFree, err := diskInfo(diskPath)
	if err != nil {
		return s, fmt.Errorf("gagal membaca disk %s: %w", diskPath, err)
	}
	s.DiskPath = diskPath
	s.DiskTotal = dTotal
	s.DiskFree = dFree
	s.DiskUsed = dTotal - dFree
	if dTotal > 0 {
		s.DiskPct = float64(dTotal-dFree) / float64(dTotal) * 100
	}

	s.UptimeSec = uptime()
	s.Load1, s.Load5, s.Load15 = loadAvg()
	return s, nil
}
