// Package sysstat reads the handful of machine-level numbers that both the web
// panel and the Telegram bot report on: processor, memory, swap, disk, load and
// uptime.
//
// It exists because the panel imports the bot (it starts it), so the bot cannot
// import the panel back. Rather than sampling the same counters twice with two
// sets of rounding, both read them here — an alert that says 86% and a
// dashboard that says 85% for the same instant is a support question nobody
// should have to answer.
package sysstat

import (
	"fmt"
	"strings"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/host"
	"github.com/shirou/gopsutil/v4/load"
	"github.com/shirou/gopsutil/v4/mem"
)

// Snapshot is one reading of the machine. Any field may be zero if the
// underlying counter could not be read; callers should treat zero as "unknown"
// rather than "idle".
type Snapshot struct {
	Hostname string
	OS       string
	Uptime   time.Duration

	CPUPercent float64
	CPUCores   int
	Load1      float64
	Load5      float64
	Load15     float64

	MemUsed    uint64
	MemTotal   uint64
	MemPercent float64

	SwapUsed    uint64
	SwapTotal   uint64
	SwapPercent float64

	DiskUsed    uint64
	DiskTotal   uint64
	DiskPercent float64
}

// Get samples the machine now.
//
// The processor figure is the instantaneous reading (cpu.Percent with a zero
// interval), which is what the panel has always shown. It is cheap enough to
// call on a short timer and does not block for a sampling window.
func Get() Snapshot {
	var s Snapshot

	if info, err := host.Info(); err == nil {
		s.Hostname = info.Hostname
		s.OS = strings.TrimSpace(titleCase(info.Platform) + " " + info.PlatformVersion)
		s.Uptime = time.Duration(info.Uptime) * time.Second
	}

	if pct, err := cpu.Percent(0, false); err == nil && len(pct) > 0 {
		s.CPUPercent = Round1(pct[0])
	}
	s.CPUCores, _ = cpu.Counts(true)

	if l, err := load.Avg(); err == nil {
		s.Load1, s.Load5, s.Load15 = l.Load1, l.Load5, l.Load15
	}

	if vm, err := mem.VirtualMemory(); err == nil {
		s.MemUsed, s.MemTotal = vm.Used, vm.Total
		s.MemPercent = Round1(vm.UsedPercent)
	}
	if sw, err := mem.SwapMemory(); err == nil {
		s.SwapUsed, s.SwapTotal = sw.Used, sw.Total
		s.SwapPercent = Round1(sw.UsedPercent)
	}
	if du, err := disk.Usage("/"); err == nil {
		s.DiskUsed, s.DiskTotal = du.Used, du.Total
		s.DiskPercent = Round1(du.UsedPercent)
	}
	return s
}

// LoadString formats the three load averages the way uptime(1) does.
func (s Snapshot) LoadString() string {
	return fmt.Sprintf("%.2f, %.2f, %.2f", s.Load1, s.Load5, s.Load15)
}

// Round1 rounds to one decimal place, so a reading never renders as 84.99999.
func Round1(f float64) float64 { return float64(int(f*10+0.5)) / 10 }

// HumanBytes renders a byte count in the largest unit that keeps it readable.
func HumanBytes(b uint64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := uint64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(b)/float64(div), "KMGTPE"[exp])
}

// HumanDuration renders a duration as days/hours/minutes, dropping the units
// that would read as zero.
func HumanDuration(d time.Duration) string {
	d = d.Round(time.Minute)
	days := int(d.Hours()) / 24
	hours := int(d.Hours()) % 24
	mins := int(d.Minutes()) % 60
	switch {
	case days > 0:
		return fmt.Sprintf("%dd %dh %dm", days, hours, mins)
	case hours > 0:
		return fmt.Sprintf("%dh %dm", hours, mins)
	default:
		return fmt.Sprintf("%dm", mins)
	}
}

// titleCase upper-cases the first letter only. strings.Title is deprecated and
// would also capitalise inside words, which mangles platform names.
func titleCase(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
