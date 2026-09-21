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

	// Attribution is the line NOTICE requires a modified version to keep.
	//
	// AGPL-3.0 gives everybody the right to fork this and publish the fork.
	// Section 7(b) of the same licence lets the author require that the
	// attribution be preserved when they do, and NOTICE exercises that. This
	// is that line, in one place, so the surfaces that must show it cannot
	// drift apart from the one that defines it.
	//
	// It is deliberately a fact rather than a restriction: a fork is welcome,
	// and it has to say what it came from. See TRADEMARK.md for the separate
	// question of the name, which is not licensed with the code at all.
	Attribution = "Based on BackPack by Amin Mohammadi (AminMGMT)"

	// AttributionURL accompanies it wherever there is room for a link.
	AttributionURL = "https://github.com/" + RepoOwner + "/" + RepoName

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

// ReleasePublicKey is the Ed25519 key that release signatures are checked
// against, base64 of the raw 32 bytes.
//
// A release is published with a SHA256SUMS file and the updater verifies
// every archive against it, which is what stops a mirror handing over a
// different binary. What it does not stop is a mirror handing over a
// different SHA256SUMS as well: the list travels the same channel as the
// thing it describes, over the third-party proxies these machines are
// obliged to use. Signing the list closes that, because the signature is
// checked against a key that travelled with the binary already running.
//
// Empty meant this build checked checksums and nothing more, which is what
// every build before v1.8.2 did. It is no longer empty, and that changes the
// updater's behaviour in a way worth being exact about: **from a build
// carrying this key, a release without a valid signature is refused rather
// than warned about.** So the RELEASE_SIGNING_KEY secret has to be in place
// before the next tag is pushed, or that release will not install anywhere.
//
// Rotating is the same operation and is not free: a machine running an older
// binary trusts the old key and will refuse a release signed with a new one
// until it has been updated by some other route. Publish one release carrying
// the new public key while still signing with the old private key, then
// switch.
//
// A var rather than a const so a test can pin a key of its own — the same
// reason node.StorePath and optimize.sysctlFile are vars. Nothing at runtime
// writes it.
var ReleasePublicKey = "uDVeC9NUFceAuhg53ZWbBNTVSaMC8tEvtwWmcDZmV9Q="

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
