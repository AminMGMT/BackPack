package manage

import (
	"fmt"
	"strings"
)

// The setup link, as the web panel shows it.
//
// The menu prints a tunnel's link with the two lines that build the other end
// (printShareLink); the panel needs the same facts as data. This is that, and
// nothing else: which side the link is for, the link, and the commands.

// SetupLinkInfo is one tunnel's setup link and how to use it.
type SetupLinkInfo struct {
	Name string `json:"name"`
	Link string `json:"link"`
	// For is the side the link builds: "kharej" or "iran".
	For  string `json:"for"`
	Kind string `json:"kind"` // reverse | direct
	// Apply and Install are the two commands for a kharej: with Backpack
	// already on it, and without. Empty when the link is for the Iran side,
	// which is built from its menu.
	Apply   string `json:"apply,omitempty"`
	Install string `json:"install,omitempty"`
	// Where is the menu path on the other server that takes the link.
	Where string `json:"where"`
	// Host is the address of this server the link names; empty when none was
	// known, and then NeedsAddress says the other side will ask for it.
	Host         string `json:"host,omitempty"`
	NeedsAddress bool   `json:"needsAddress,omitempty"`
}

// SetupLinkFor builds a tunnel's setup link. host overrides the address the
// link names; empty uses this server's public address, as the menu does.
func SetupLinkFor(name, host string) (SetupLinkInfo, error) {
	host = strings.Trim(strings.TrimSpace(host), "[]")
	if host == "" {
		host = linkHost()
	}
	raw, err := shareLinkFor(name, host)
	if err != nil {
		return SetupLinkInfo{}, err
	}
	l, err := DecodeShareLink(raw)
	if err != nil {
		return SetupLinkInfo{}, fmt.Errorf("the link does not read back: %w", err)
	}
	info := SetupLinkInfo{Name: name, Link: raw, For: l.PeerSide(), Kind: l.Kind, Host: host,
		NeedsAddress: host == "" && l.PeerNeedsAddress()}
	if info.For == "kharej" {
		info.Apply = "sudo backpack link apply '" + raw + "'"
		info.Install = InstallCommand(raw)
		info.Where = "sudo backpack → Setup Kharej → Setup Link"
		if managedTransport(l.Tr) {
			info.Where = "sudo backpack → Setup Kharej → Reverse → HTTPS → Same Protocol → Setup Link"
		}
	} else {
		info.Where = "sudo backpack → Manage → Set Up From A Link"
	}
	return info, nil
}
