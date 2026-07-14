// Package app holds shared constants and paths used across the backpack
// management layer (menu, manage, telegram, schedule, optimize).
package app

const (
	// Version of the backpack engine.
	Version = "v1.3.0"

	// RepoOwner/RepoName identify the GitHub repository used by the installer
	// and the release-based updater.
	RepoOwner = "AminMGMT"
	RepoName  = "BackPack"

	// InstallDir is where the release bundle lives on the VPS.
	InstallDir = "/root/BackPack"

	// BackupDir is the default folder for configuration backups.
	BackupDir = InstallDir + "/backups"

	// ConfigDir is where per-tunnel TOML configs and runtime state live.
	ConfigDir = "/etc/backpack"

	// ServiceDir is the systemd unit directory.
	ServiceDir = "/etc/systemd/system"

	// ServicePrefix is prepended to every tunnel systemd unit.
	ServicePrefix = "backpack-"

	// BinPath is where the backpack binary is installed.
	BinPath = "/usr/local/bin/backpack"

	// TelegramConfig stores the telegram bot settings (JSON).
	TelegramConfig = ConfigDir + "/telegram.json"

	// AutoRefreshMarker is the cron comment tag for the global auto-refresh job.
	AutoRefreshMarker = "backpack-auto-refresh"

	// WebUIConfig stores the web panel settings (JSON).
	WebUIConfig = ConfigDir + "/webui.json"

	// WebUIService is the systemd unit that runs the web panel.
	WebUIService = "backpack-webui.service"

	// WebUIPort is the default port the web panel listens on.
	WebUIPort = 7777

	// SocksInternalPort is the localhost port the built-in SOCKS5 proxy listens
	// on. It is reachable from a peer only when exposed over a tunnel.
	SocksInternalPort = 1080

	// InstallPathFile records where the source repo was cloned, so the updater
	// and uninstaller can find it.
	InstallPathFile = ConfigDir + "/install_path"
)

// ServiceName returns the systemd unit name for a tunnel by its short name.
func ServiceName(name string) string {
	return ServicePrefix + name + ".service"
}

// ConfigPath returns the on-disk TOML path for a tunnel by its short name.
func ConfigPath(name string) string {
	return ConfigDir + "/" + name + ".toml"
}
