// Package app holds shared constants and paths used across the backpack
// management layer (menu, manage, telegram, schedule, optimize).
package app

import (
	"crypto/sha256"
	"encoding/binary"
	"runtime"
)

const (
	// Version of the backpack engine.
	Version = "v1.8.2"

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

	// ProxyService is the systemd unit for the optional built-in SOCKS5/HTTP
	// proxy, so a node can be its own backend instead of running a separate one.
	ProxyService = "backpack-proxy.service"

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

// TunnelConfigMode is the permission a tunnel's TOML config is written with.
//
// A tunnel config holds its token, and on tcp, udp and kcp that token is the
// whole of what authorises a connection to it. Every other file this program
// writes that holds a secret is 0600 — telegram.json, webui.json, the node
// registry, the TLS key, the backups — and the tunnel configs alone were 0644,
// which made every tunnel's token readable by any account on the machine.
//
// A constant here rather than a literal at each call site because there are
// nine of them, spread over five files, and one of them being missed is exactly
// how they came to disagree in the first place. Nothing but root reads these:
// the engine, the panel and the monitor all run as root.
const TunnelConfigMode = 0o600

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

// The architecture a release asset is named for.
//
// runtime.GOARCH is not enough on ARM. Every 32-bit ARM build reports "arm"
// whatever it was compiled for, and the three variants are not interchangeable:
// a v7 binary on a v5 board is an illegal instruction, not a slow one. So the
// releases name them apart — armv5, armv6, armv7 — and a binary has to know
// which of the three it is to ask for its own successor.
//
// GOARM is stamped in at link time by the release build (see the Makefile). It
// is empty for every other architecture, and empty on a plain `go build`, where
// falling back to "arm" is right: that build was not made by the release
// pipeline and has no published asset of its own.
var GOARM = ""

// AssetArch is the architecture part of this build's release asset name.
func AssetArch() string {
	if runtime.GOARCH == "arm" && GOARM != "" {
		return "armv" + GOARM
	}
	return runtime.GOARCH
}

// AssetName is the release archive this build would update itself from.
func AssetName() string { return "backpack_linux_" + AssetArch() + ".tar.gz" }
