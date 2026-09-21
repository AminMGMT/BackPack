package manage

import (
	"github.com/backpack/backpack/internal/manage/backup"
	"github.com/backpack/backpack/internal/manage/core"
)

// The lowest layer of this package now lives in internal/manage/core: the
// tunnel listing, the systemd operations and the few helpers both rest on.
//
// It was lifted out because everything here sits on it and it sits on nothing,
// which makes it the one seam in a 28,000-line package that can be cut without
// untangling anything first. What it buys is not tidiness: a change to how a
// unit is written stops being a change to the package the panel, the CLI and
// the monitor all import wholesale.
//
// The names are re-exported here on purpose. `manage.List`, `manage.Tunnel`
// and `manage.RestartService` are called from six packages and from the CLI,
// and a split that moved them would be a split that touched every caller — so
// the public surface is exactly what it was, and the seam is internal.

// Tunnel is a discovered tunnel derived from a config file on disk.
type Tunnel = core.Tunnel

var (
	List             = core.List
	Find             = core.Find
	LoadTunnelConfig = core.LoadTunnelConfig
	Delete           = core.Delete
	RestartAll       = core.RestartAll
	TunnelCountry    = core.TunnelCountry

	DaemonReload   = core.DaemonReload
	IsActive       = core.IsActive
	IsEnabled      = core.IsEnabled
	StartService   = core.StartService
	StopService    = core.StopService
	RestartService = core.RestartService
	DisableService = core.DisableService
	EnsureUnits    = core.EnsureUnits
	FollowLog      = core.FollowLog
)

// The two services this product installs alongside the tunnels. They are in
// core because they are systemd units and nothing else: they sit on the same
// helpers the tunnel units do and on nothing above them.
var (
	MonitorRunning        = core.MonitorRunning
	RestartMonitorService = core.RestartMonitorService
	EnsureMonitorService  = core.EnsureMonitorService
	DisableMonitorService = core.DisableMonitorService

	ProxyRunning        = core.ProxyRunning
	EnableProxyService  = core.EnableProxyService
	DisableProxyService = core.DisableProxyService
)

// The unexported helpers the rest of this package still uses. Aliased rather
// than re-implemented, so there is one definition of each.
var (
	fileExists = core.FileExists
	orDefault  = core.OrDefault
	systemctl  = core.Systemctl
	writeUnit  = core.WriteUnit
	validName  = core.ValidName
	errBadName = core.ErrBadName
	checkName  = core.CheckName
	directRole = core.DirectRole
	l3Role     = core.L3Role
)

// Backup, restore and the update snapshots now live in
// internal/manage/backup. It is a leaf: it sits on core and on nothing else
// here, which is what made it the second seam worth cutting.
//
// Re-exported for the same reason as the rest — `manage.CreateBackup` is called
// from the menu, the panel and the node RPC, and a split that moved the names
// would be a split that touched every caller.
type (
	Snapshot     = backup.Snapshot
	SnapshotMeta = backup.SnapshotMeta
)

var (
	TakeSnapshot    = backup.TakeSnapshot
	RestoreSnapshot = backup.RestoreSnapshot
	ListSnapshots   = backup.ListSnapshots
	SnapshotRoot    = backup.Root

	WriteBackup       = backup.WriteBackup
	BackupToFile      = backup.BackupToFile
	Restore           = backup.Restore
	AutoBackupEnabled = backup.AutoBackupEnabled
	SetAutoBackup     = backup.SetAutoBackup
	RunAutoBackup     = backup.RunAutoBackup
)
