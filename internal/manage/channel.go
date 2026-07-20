package manage

import (
	"os"
	"strings"

	"github.com/backpack/backpack/internal/app"
	"github.com/backpack/backpack/internal/tui"
)

// Release channels.
//
// Stable installs only finished releases. Beta also installs pre-releases,
// which is how a new version gets tried on one server before everybody else
// receives it — the alternative is finding out from users that a release does
// not work on their route.
const (
	ChannelStable = "stable"
	ChannelBeta   = "beta"
)

// channelFile records the chosen channel. It sits next to the tunnel configs so
// a backup carries it along with everything else.
var channelFile = app.ConfigDir + "/channel"

// channelOptions is the ordered list shown in the update menu.
var channelOptions = []struct {
	label, desc, value string
}{
	{"Stable", "finished releases only — recommended", ChannelStable},
	{"Beta", "also installs pre-releases — for testing before everyone else", ChannelBeta},
}

// Channel returns the configured release channel, defaulting to stable. An
// unreadable or unrecognised file means stable: the safe answer is never to
// silently opt somebody into pre-releases.
func Channel() string {
	b, err := os.ReadFile(channelFile)
	if err != nil {
		return ChannelStable
	}
	if strings.TrimSpace(string(b)) == ChannelBeta {
		return ChannelBeta
	}
	return ChannelStable
}

// SetChannel persists the release channel.
func SetChannel(channel string) error {
	if channel != ChannelBeta {
		channel = ChannelStable
	}
	if err := os.MkdirAll(app.ConfigDir, 0755); err != nil {
		return err
	}
	return os.WriteFile(channelFile, []byte(channel+"\n"), 0644)
}

// ChannelLabel is the display name of the configured channel.
func ChannelLabel() string { return channelLabel(Channel()) }

// ChannelOptions returns the menu entries and their matching values, so the
// menu package does not have to know what a channel is made of.
func ChannelOptions() ([]tui.Option, []string) {
	opts := make([]tui.Option, len(channelOptions))
	values := make([]string, len(channelOptions))
	for i, o := range channelOptions {
		opts[i] = tui.Option{Title: o.label, Desc: o.desc}
		values[i] = o.value
	}
	return opts, values
}

// channelLabel returns the display name of a channel value.
func channelLabel(value string) string {
	for _, o := range channelOptions {
		if o.value == value {
			return o.label
		}
	}
	return value
}

// isPrerelease reports whether a version tag is a pre-release — anything
// carrying a suffix after the version numbers, such as v1.6.0-beta.2 or
// v1.6.0-rc1. Stable installs skip these.
func isPrerelease(tag string) bool {
	v := strings.TrimPrefix(strings.TrimSpace(tag), "v")
	return strings.ContainsAny(v, "-+")
}
