package manage

import (
	"github.com/backpack/backpack/internal/manage/core"
	"github.com/backpack/backpack/internal/manage/tunnelspec"
)

// What a tunnel's configuration says and how a change to it is made safely now
// live in internal/manage/tunnelspec: the spec, rendering it, loading it back,
// applying a change with revert-if-it-will-not-start, the presets, and the
// history of superseded configurations — for the reverse tunnel's spec and the
// direct and layer-3 ones alike. See that package's doc for why it was
// lifted out.
//
// As with core_alias.go and spec_alias.go, the names are re-declared here
// exactly as they were, so no caller in this package or outside it changed.

// TunnelSpec is the full description of a tunnel used to render a TOML config.
type TunnelSpec = tunnelspec.Spec

// PresetOption is one performance profile for the panel's preset menu.
type PresetOption = tunnelspec.PresetOption

// ConfigChange is one superseded configuration.
type ConfigChange = tunnelspec.ConfigChange

// The direct and layer-3 tunnels' own specs, and which machine one is for.
type (
	directSpec = tunnelspec.DirectSpec
	l3Spec     = tunnelspec.L3Spec
	directSide = tunnelspec.DirectSide
)

const (
	PresetBalance    = tunnelspec.PresetBalance
	PresetTurbo      = tunnelspec.PresetTurbo
	PresetAggressive = tunnelspec.PresetAggressive
	PresetThroughput = tunnelspec.PresetThroughput

	// TelegramHost is the Telegram API endpoint the bot relay forwards to.
	TelegramHost       = tunnelspec.TelegramHost
	telegramPortSuffix = "=" + TelegramHost

	sideIran   = tunnelspec.SideIran
	sideKharej = tunnelspec.SideKharej
)

var (
	LoadSpec           = tunnelspec.Load
	loadServerSpec     = tunnelspec.LoadServer
	VisiblePorts       = tunnelspec.VisiblePorts
	isBotRelayPort     = tunnelspec.IsBotRelayPort
	isTelegramPort     = tunnelspec.IsTelegramPort
	applySpec          = tunnelspec.Apply
	revertSpec         = tunnelspec.Revert
	lastLogLine        = tunnelspec.LastLogLine
	recordConfigChange = tunnelspec.RecordChange

	ConfigHistory     = tunnelspec.ConfigHistory
	ConfigChangeTimes = tunnelspec.ConfigChangeTimes
	RestoreConfigFrom = tunnelspec.RestoreConfigFrom

	presetOptions        = tunnelspec.PresetOptions
	validPreset          = tunnelspec.ValidPreset
	presetSuitsTransport = tunnelspec.PresetSuitsTransport
	presetOptionsFor     = tunnelspec.PresetOptionsFor
	presetLabel          = tunnelspec.PresetValueLabel
	ApplyPreset          = tunnelspec.ApplyPreset
	applyKCPPreset       = tunnelspec.ApplyKCPPreset
	PresetLabel          = tunnelspec.PresetLabel
	PresetValueLabel     = tunnelspec.PresetValueLabel

	HoldsPorts      = tunnelspec.HoldsPorts
	DialsOut        = tunnelspec.DialsOut
	IsDirectKind    = tunnelspec.IsDirectKind
	TunnelDirection = tunnelspec.TunnelDirection
	TunnelCarrier   = tunnelspec.TunnelCarrier
	l3EncapLabel    = tunnelspec.L3EncapLabel
	limitsLabel     = tunnelspec.LimitsLabel

	WaitServiceActive = core.WaitServiceActive
)

// IsDatagram reports whether a transport carries datagrams (UDP/KCP), for
// callers outside the package — a TCP probe against one is meaningless.
func IsDatagram(t string) bool { return isDatagram(t) }
