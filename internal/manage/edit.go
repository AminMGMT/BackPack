package manage

import (
	"fmt"
	"net"
	"strings"

	"github.com/backpack/backpack/internal/utils/network"
)

// addrHost returns the host part of a host:port address (brackets stripped for
// IPv6), or fallback when it can't be parsed.

// EditTunnel changes a tunnel's ports/address in one shot and restarts it so
// the change takes effect. Empty values leave a setting unchanged:
//
//   - tunnelPort — the tunnel (control-channel) port: the local bind port on a
//     server, the remote server port on a client
//   - host — the server address (client tunnels only)
//   - ports — the full new forwarded-ports list (server tunnels only); the
//     hidden bot-relay mapping, if present, is preserved automatically
func EditTunnel(name, host, tunnelPort string, ports []string) error {
	s, err := LoadSpec(name)
	if err != nil {
		return err
	}

	changed := false
	if tunnelPort != "" || host != "" {
		var bind tunnelBind
		if tunnelPort != "" {
			var err error
			if bind, err = parseTunnelBind(tunnelPort); err != nil {
				return fmt.Errorf("invalid tunnel port %q: %w", tunnelPort, err)
			}
		}
		if s.Role == "server" {
			if host != "" {
				return fmt.Errorf("the address can only be changed on client tunnels")
			}
			// An address in the new value pins the tunnel to it; a bare port
			// keeps whatever this tunnel already binds, which is what changing
			// only the port has always done. That is also how a pinned tunnel
			// is widened again: say 0.0.0.0:443 rather than 443.
			if bind.HasHost() {
				s.BindAddr = bind.Addr(false)
			} else {
				s.BindAddr = net.JoinHostPort(addrHost(s.BindAddr, "0.0.0.0"), bind.Port)
			}
		} else {
			if bind.HasHost() {
				// This field is the port on the server for a client, so an
				// address here is aimed at the wrong setting.
				return fmt.Errorf("a client binds nothing — its tunnel port is the port on the " +
					"server, so it takes a port alone. Change where it dials with the server address instead")
			}
			h := addrHost(s.RemoteAddr, "")
			p := addrPort(s.RemoteAddr)
			if host != "" {
				h = strings.Trim(strings.TrimSpace(host), "[]")
			}
			if bind.Port != "" {
				p = bind.Port
			}
			if h == "" || !validPort(p) {
				return fmt.Errorf("invalid server address or port")
			}
			s.RemoteAddr = net.JoinHostPort(h, p)
		}
		changed = true
	}

	if len(ports) > 0 {
		if s.Role != "server" {
			return fmt.Errorf("forwarded ports exist only on server tunnels")
		}
		var clean []string
		for _, p := range ports {
			if p = strings.TrimSpace(p); p != "" && !isBotRelayPort(p, s.Token) {
				clean = append(clean, p)
			}
		}
		if len(clean) == 0 {
			return fmt.Errorf("at least one forwarded port is required")
		}
		if err := validatePortSpecs(clean); err != nil {
			return err
		}
		// Keep the hidden Telegram/SOCKS relay mapping the user never sees.
		for _, p := range s.Ports {
			if isBotRelayPort(p, s.Token) {
				clean = append(clean, p)
			}
		}
		s.Ports = clean
		changed = true
	}

	if !changed {
		return fmt.Errorf("nothing to change")
	}
	return applySpec(s)
}

// SetFallbackAddrs replaces the list of backup server addresses on a client
// tunnel. Each entry is a full "host:port" (or "host" — the primary port is
// assumed). When the primary address stops answering, the client walks this
// list until one connects, which keeps the tunnel alive after a server IP gets
// filtered, a port gets blocked, or when you want to fail over to a CDN edge.
//
// Passing an empty list clears the fallbacks.
func SetFallbackAddrs(name string, addrs []string) error {
	s, err := LoadSpec(name)
	if err != nil {
		return err
	}
	if s.Role != "client" {
		return fmt.Errorf("fallback addresses apply to client tunnels only")
	}

	primaryPort := addrPort(s.RemoteAddr)
	var clean []string
	seen := map[string]bool{strings.TrimSpace(s.RemoteAddr): true}
	for _, a := range addrs {
		a = strings.TrimSpace(a)
		if a == "" {
			continue
		}
		// Accept a bare host/IP by reusing the primary's port.
		host, port := a, ""
		if h, p, err := net.SplitHostPort(a); err == nil {
			host, port = h, p
		} else if strings.Count(a, ":") > 1 && !strings.HasPrefix(a, "[") {
			host, port = a, "" // bare IPv6 literal
		}
		if port == "" {
			if primaryPort == "" {
				return fmt.Errorf("%q has no port and the primary address has none either", a)
			}
			port = primaryPort
		}
		if !validPort(port) {
			return fmt.Errorf("invalid port in %q", a)
		}
		if host == "" {
			return fmt.Errorf("invalid address %q", a)
		}
		if strings.Contains(host, ":") && !strings.HasPrefix(host, "[") {
			host = "[" + host + "]" // IPv6 literal
		}
		full := net.JoinHostPort(strings.Trim(host, "[]"), port)
		if seen[full] {
			continue
		}
		seen[full] = true
		clean = append(clean, full)
	}

	s.FallbackAddrs = clean
	return applySpec(s)
}

// ChangeTransport switches an existing tunnel to a different carrier (tcp,
// tcpmux, udp, ws, wss, wsmux, wssmux) without touching its name, token or
// forwarded ports — so the peer keeps the same credentials and services. A
// wss/wssmux server gets a self-signed certificate generated automatically if
// it doesn't have one yet. The change is verified and auto-reverted on failure.
//
// Both ends must use the same transport, so the peer has to be switched too.
func ChangeTransport(name, transport string) error {
	transport = strings.ToLower(strings.TrimSpace(transport))
	s, err := LoadSpec(name)
	if err != nil {
		return err
	}
	if s.Transport == transport {
		return fmt.Errorf("this tunnel already uses %s", transport)
	}
	if err := switchTransport(&s, transport); err != nil {
		return err
	}
	return applySpec(s)
}

// switchTransport moves a spec onto a different carrier, filling in whatever
// the new one needs and cannot inherit. It does not write anything: the caller
// decides when the spec is complete enough to save, which is what lets the
// panel change the transport and the ports in a single restart.
func switchTransport(s *TunnelSpec, transport string) error {
	if !validTransport(transport) {
		return fmt.Errorf("unknown transport %q", transport)
	}
	s.Transport = transport

	// Mux transports need their SMUX knobs populated; a tunnel created as plain
	// TCP has them at zero, which the engine would reject or run badly.
	if isMux(transport) && s.MuxCon <= 0 {
		s.MuxCon = 8
		s.MuxVersion = 2
		s.MuxFrameSize = 32768
		s.MuxRecvBuffer = 4194304
		s.MuxStreamBuffer = 65536
	}
	// TLS transports need a certificate on the server side.
	if s.Role == "server" && needsTLS(transport) && (s.TLSCert == "" || !fileExists(s.TLSCert)) {
		cert, key, err := EnsureSelfSignedCert(s.Name, "")
		if err != nil {
			return fmt.Errorf("could not generate a TLS certificate: %w", err)
		}
		s.TLSCert, s.TLSKey = cert, key
	}
	// A tunnel that was never on KCP has all its KCP knobs at zero. Fill them
	// from the tunnel's own preset so switching to KCP produces a working
	// session rather than one with a zero window and no tick interval.
	if isKCP(transport) && s.KCPInterval <= 0 {
		preset := s.Preset
		if !validPreset(preset) {
			preset = PresetTurbo
		}
		applyKCPPreset(s, preset)
	}
	return nil
}

// SetLoadBalance turns load balancing across the configured server addresses
// on or off for a client tunnel.
func SetLoadBalance(name string, on bool) error {
	s, err := LoadSpec(name)
	if err != nil {
		return err
	}
	if s.Role != "client" {
		return fmt.Errorf("load balancing is a client-side setting")
	}
	if on && len(s.FallbackAddrs) == 0 {
		return fmt.Errorf("add at least one backup server address first — there is nothing to balance across")
	}
	if s.LoadBalance == on {
		return fmt.Errorf("load balancing is already %s", map[bool]string{true: "on", false: "off"}[on])
	}
	s.LoadBalance = on
	// Steering to one best exit and spreading across all of them are opposite
	// intentions; only one can be in force, so turning balancing on retires
	// failover steering.
	if on {
		s.HealthFailover = false
	}
	return applySpec(s)
}

// SetProxyProtocol turns the real-client-IP header on or off for a server
// tunnel.
func SetProxyProtocol(name string, on bool) error {
	s, err := LoadSpec(name)
	if err != nil {
		return err
	}
	if s.Role != "server" {
		return fmt.Errorf("this is a server-side setting")
	}
	if !supportsProxyProtocol(s.Transport) {
		return fmt.Errorf("the %s transport cannot carry a PROXY protocol header", s.Transport)
	}
	if s.ProxyProtocol == on {
		return fmt.Errorf("it is already %s", map[bool]string{true: "on", false: "off"}[on])
	}
	s.ProxyProtocol = on
	return applySpec(s)
}

// SetLimits applies per-tunnel caps. Zero means unlimited for either value.
func SetLimits(name string, maxConns, bandwidthMbps int) error {
	s, err := LoadSpec(name)
	if err != nil {
		return err
	}
	if s.Role != "server" {
		return fmt.Errorf("limits are a server-side setting — they apply where connections arrive")
	}
	if maxConns < 0 || bandwidthMbps < 0 {
		return fmt.Errorf("a limit cannot be negative")
	}
	s.MaxConnections = maxConns
	s.BandwidthMbps = bandwidthMbps
	return applySpec(s)
}

// mssLabel renders an MSS clamp the way the menus and the panel read it.
func mssLabel(mss int) string {
	if mss <= 0 {
		return "automatic"
	}
	return fmt.Sprintf("%d bytes", mss)
}

// SetAcceptUDP turns UDP forwarding on the exposed ports on or off. It is a
// server-side setting — the forwarded ports live there — and off is the default
// a plain web or proxy tunnel should keep. See config.ServerConfig.ForwardsUDP.
func SetAcceptUDP(name string, on bool) error {
	s, err := LoadSpec(name)
	if err != nil {
		return err
	}
	if s.Role != "server" {
		return fmt.Errorf("UDP forwarding is a server-side setting — the exposed ports live there")
	}
	if s.AcceptUDP == on {
		return fmt.Errorf("UDP forwarding is already %s", map[bool]string{true: "on", false: "off"}[on])
	}
	s.AcceptUDP = on
	return applySpec(s)
}

// SetMSS clamps the largest TCP payload the tunnel puts in one packet. Zero —
// the default — hands the decision back to the kernel.
//
// This is the one knob the path-MTU check names outright, and until now there
// was nowhere to turn it. Where a path carries less than a full-sized packet and
// drops the oversized ones without an ICMP reply, nothing on either machine
// learns: the handshake and the heartbeats are small enough to arrive, so the
// tunnel comes up and stays up while every real transfer stalls on the first
// full segment. Clamping the segment size is the whole fix, and it has to be
// done at both ends — each end clamps only what it sends.
//
// No preset sets it, and a preset change leaves it alone: it describes the path
// the tunnel crosses, not how hard the tunnel is being pushed.
func SetMSS(name string, mss int) error {
	s, err := LoadSpec(name)
	if err != nil {
		return err
	}
	if isDatagram(s.Transport) {
		return fmt.Errorf("%s carries datagrams, not TCP segments — size its packets with the KCP MTU instead", s.Transport)
	}
	if mss != 0 && (mss < minMSS || mss > maxMSS) {
		return fmt.Errorf("an MSS clamp must be between %d and %d bytes, or 0 to let the kernel choose", minMSS, maxMSS)
	}
	if s.MSS == mss {
		return fmt.Errorf("the MSS clamp is already %s", mssLabel(mss))
	}
	s.MSS = mss
	return applySpec(s)
}

// pckFlagSummary renders the flag cycle for the Edit header.
func pckFlagSummary(flags []string) string {
	if len(flags) == 0 {
		return strings.Join(network.DefaultTCPFlagList(), ", ") + " (default)"
	}
	return strings.Join(flags, ", ")
}

// SetPckFlags replaces the TCP flag cycle the packet carrier stamps on what it
// sends. An empty list restores the default.
//
// It is a one-sided setting: each end decides only what its own packets look
// like, so the two need not match and changing it here does not strand the
// peer. That is the whole reason it is safe to offer as a live edit.
func SetPckFlags(name string, flags []string) error {
	s, err := LoadSpec(name)
	if err != nil {
		return err
	}
	if s.Transport != "pck" {
		return fmt.Errorf("TCP packet flags apply to the pck transport only")
	}
	var clean []string
	for _, f := range flags {
		if f = strings.TrimSpace(strings.ToUpper(f)); f != "" {
			clean = append(clean, f)
		}
	}
	if _, err := network.ParseTCPFlagList(clean); err != nil {
		return err
	}
	s.PckFlags = clean
	return applySpec(s)
}

// ChangePreset re-applies a whole performance profile to an existing tunnel.
// Every tuning field is rewritten from the preset; the identity of the tunnel
// (name, token, ports, addresses, certificates) is untouched.
func ChangePreset(name, preset string) error {
	if !validPreset(preset) {
		return fmt.Errorf("unknown preset %q", preset)
	}
	s, err := LoadSpec(name)
	if err != nil {
		return err
	}
	if !presetSuitsTransport(preset, s.Transport) {
		return fmt.Errorf("the %s preset applies to the udp+kcp+fec transport only, not %q",
			presetLabel(preset), s.Transport)
	}
	ApplyPreset(&s, preset)
	return applySpec(s)
}

// SetCertificate switches a TLS tunnel between its self-signed certificate and
// a Let's Encrypt one. An empty domain means self-signed.
//
// The self-signed pair is left on disk either way. It costs nothing to keep,
// and it means switching back is instant rather than requiring regeneration —
// which matters when the reason for switching back is that ACME just failed.
func SetCertificate(name, domain, email string) error {
	s, err := LoadSpec(name)
	if err != nil {
		return err
	}
	if s.Role != "server" {
		return fmt.Errorf("the certificate is a server-side setting — the client does not present one")
	}
	if !needsTLS(s.Transport) {
		return fmt.Errorf("transport %s does not use TLS", s.Transport)
	}

	s.ACMEDomain = domain
	s.ACMEEmail = email

	// Even on the ACME path the self-signed pair must exist: it is the fallback
	// the config still points at, and regenerating it later would need the
	// tunnel to be down.
	if s.TLSCert == "" || !fileExists(s.TLSCert) {
		cert, key, err := EnsureSelfSignedCert(s.Name, domain)
		if err != nil {
			return fmt.Errorf("could not prepare the self-signed certificate: %w", err)
		}
		s.TLSCert, s.TLSKey = cert, key
	}
	return applySpec(s)
}
