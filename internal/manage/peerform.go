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
	// The paired settings travel in the Fine Tune drawer, marked as sent key
	// by key. A drawer built in code without that mark counts every field as
	// answered, so its zeros replaced the preset's: heartbeat off, Nagle back
	// on, kcp's error correction off — a kharej that could not keep a session
	// with its server.
	tune := FineTune{AcceptUDP: f.AcceptUDP, MSS: f.MSS, MuxVersion: f.MuxVersion,
		KCPDataShards: f.FECData, KCPParityShards: f.FECParity, sent: map[string]bool{}}
	if f.AcceptUDP {
		tune.sent["acceptUDP"] = true
	}
	if f.MSS > 0 {
		tune.sent["mss"] = true
	}
	if f.MuxVersion > 0 {
		tune.sent["muxVersion"] = true
	}
	if f.Transport == "kcp" {
		// Zero on both is "off", and has to be carried as an answer.
		tune.sent["kcpDataShards"], tune.sent["kcpParityShards"] = true, true
	}
	if len(tune.sent) > 0 {
		n.Tune = &tune
	}
	if f.SimpleAuth {
		n.Conn = &ConnTune{SimpleAuth: true}
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
		FECData:     f.FECData,
		FECParity:   f.FECParity,
		SpoofPeerIP: f.SpoofPeerIP,
		SNIDomain:   f.SNIDomain,
		GREKey:      f.GREKey,
		MTU:         f.MTU,
		MSSClamp:    f.MSSClamp,
		AutoMTU:     f.AutoMTU,
	}
}
