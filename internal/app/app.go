// Package app holds shared constants and paths used across the backpack
// management layer (menu, manage, telegram, schedule, optimize).
package app

import (
	"crypto/sha256"
	"encoding/binary"
)

const (
	// Version of the backpack engine.
	Version = "v1.5.0"

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

	// MonitorService is the systemd unit that watches the tunnels and runs the
	// Telegram bot and alerts. It is deliberately separate from the web panel:
	// monitoring must not stop just because the panel is stopped.
	MonitorService = "backpack-monitor.service"

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

// SocksPortForToken derives the loopback port a tunnel's SOCKS relay uses.
//
// It used to be the fixed SocksInternalPort (1080) on every install, which has
// two problems. 1080 is the well-known SOCKS port, so it is often already taken
// on a server that runs any other proxy — and when it is, the relay simply
// never binds. And being identical everywhere makes it trivially guessable.
//
// Deriving it from the tunnel token solves the coordination problem without any
// coordination: the two ends of a tunnel both know the token, so both compute
// the same port without having to agree on anything, and a different tunnel on
// a different machine lands somewhere else.
func SocksPortForToken(token string) int {
	if token == "" {
		return SocksInternalPort
	}
	sum := sha256.Sum256([]byte("backpack-socks-v1:" + token))
	// A 20000-wide window above the usual service range and below the
	// ephemeral range, so it neither collides with a well-known port nor gets
	// handed out to an outgoing connection.
	return 20000 + int(binary.BigEndian.Uint32(sum[:4])%20000)
}
