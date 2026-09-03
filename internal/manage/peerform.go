package manage

import "strings"

// Turning the far end's form back into a setup form.
//
// MirrorForPeer works out what the other side of a tunnel has to be told; this
// is what turns that answer into the two structures that actually build a
// tunnel. In the panel the step is invisible, because PeerForm's field names
// and the setup form's field names are the same strings, and the browser fills
// one from the other by name.
//
// A managed node has no browser in the middle, so the same mapping has to exist
// here. It is written out field by field rather than done with reflection: the
// three places a name differs — the side, the address, the peer address — are
// exactly the places a silent mismatch would produce a tunnel that builds
// cleanly and never comes up.

// ToNewTunnel turns a peer's form into a reverse tunnel setup form.
func (f PeerForm) ToNewTunnel() NewTunnel {
	role := "client"
	if strings.EqualFold(f.Side, "iran") {
		role = "server"
	}
	n := NewTunnel{
		Role:       role,
		Transport:  f.Transport,
		Name:       f.Name,
		TunnelPort: f.TunnelPort,
		Token:      f.Token,
		Ports:      f.Ports,
		Preset:     f.Preset,
	}
	if role == "client" {
		n.ServerAddr = f.ServerAddr
	}
	// The two settings that belong to the path rather than to a profile travel
	// in the drawer, and the drawer is sent only when one of them is set — an
	// empty drawer is not the same as no drawer, because the setup path reads
	// "the operator opened Fine Tune" as "these values replace the preset's".
	if f.AcceptUDP || f.MSS > 0 {
		n.Tune = &FineTune{AcceptUDP: f.AcceptUDP, MSS: f.MSS}
	}
	return n
}

// ToNewDirectTunnel turns a peer's form into a direct tunnel setup form.
func (f PeerForm) ToNewDirectTunnel() NewDirectTunnel {
	side := strings.ToLower(strings.TrimSpace(f.Side))
	if side != "iran" {
		side = "kharej"
	}
	return NewDirectTunnel{
		Side:        side,
		Carrier:     f.Carrier,
		Name:        f.Name,
		Token:       f.Token,
		PeerAddr:    f.ServerAddr,
		TunnelPort:  f.TunnelPort,
		Ports:       f.Ports,
		AcceptUDP:   f.AcceptUDP,
		LocalIP:     f.LocalIP,
		PeerIP:      f.PeerIP,
		Preset:      f.Preset,
		Spoof:       f.Spoof,
		Stealth:     f.Stealth,
		Paths:       f.Paths,
		FEC:         f.FEC,
		SpoofPeerIP: f.SpoofPeerIP,
	}
}
