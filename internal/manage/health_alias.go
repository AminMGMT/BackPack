package manage

import "github.com/backpack/backpack/internal/manage/health"

// Whether the tunnels are working — the per-tunnel health, the watchdog that
// acts on it, the monitor's heartbeat and the full Health Check — now lives in
// internal/manage/health. See that package's doc.
//
// As with core_alias.go, the names are re-declared here exactly as they were,
// so no caller in this package or outside it changed.

type (
	// Health is one tunnel's state as the panel, the bot and the watchdog see it.
	Health = health.Health
	// ServiceDownDetail says which forwarded service is refusing, and since when.
	ServiceDownDetail = health.ServiceDownDetail

	// Check is one line of the Health Check.
	Check = health.Check
	// CheckLevel is how serious a Check is.
	CheckLevel = health.CheckLevel
	// Location is where a Health Check line points the operator.
	Location = health.Location
)

const (
	CheckOK   = health.CheckOK
	CheckInfo = health.CheckInfo
	CheckWarn = health.CheckWarn
	CheckFail = health.CheckFail
)

var (
	TunnelHealth = health.TunnelHealth
	AllHealth    = health.AllHealth
	RunWatchdog  = health.RunWatchdog

	RecordMonitorHeartbeat = health.RecordMonitorHeartbeat
	MonitorHeartbeat       = health.MonitorHeartbeat
	MonitorSilent          = health.MonitorSilent

	Diagnose     = health.Diagnose
	CountByLevel = health.CountByLevel
	Locations    = health.Locations

	minMSS      = health.MinMSS
	maxMSS      = health.MaxMSS
	sysctlValue = health.SysctlValue
)
