package manage

import (
	"fmt"

	"github.com/backpack/backpack/config"
	"github.com/backpack/backpack/internal/tui"
)

// What a tunnel's forwarded ports carry — TCP, UDP or both — asked once at
// setup and changeable later, in the CLI/TUI and in the panel alike.

// portProtocolOptions is the setup question's answers, in the order shown.
var portProtocolOptions = []struct {
	value string
	opt   tui.Option
}{
	{config.PortsTCP, tui.Option{Title: "TCP", Desc: "web, proxies, Xray over TCP — the usual choice"}},
	{config.PortsUDP, tui.Option{Title: "UDP", Desc: "WireGuard, games, DNS — no TCP on these ports"}},
	{config.PortsBoth, tui.Option{Title: "TCP + UDP", Desc: "Xray/Shadowsocks with UDP, or both kinds of service"}},
}

// portProtocolLabel names a port_protocol value for a summary line.
func portProtocolLabel(v string) string {
	switch v {
	case config.PortsUDP:
		return "UDP"
	case config.PortsBoth:
		return "TCP + UDP"
	}
	return "TCP"
}

// choosePortProtocol asks what the forwarded ports carry, among what the
// transport can forward. It returns false when the operator went back. A
// transport that forwards one kind only is not asked; it is told.
func choosePortProtocol(s *TunnelSpec, current string) bool {
	allowed := s.PortProtocols()
	if len(allowed) == 1 {
		s.SetPortProtocol(allowed[0])
		if allowed[0] == config.PortsTCP {
			tui.Info(fmt.Sprintf("Forwarded ports: TCP (the %s transport carries TCP only; use tcp, kcp or quic for UDP).", s.Transport))
		}
		return true
	}
	v, ok := askPortProtocol(allowed, current)
	if ok {
		s.SetPortProtocol(v)
	}
	return ok
}

// askPortProtocol is the question itself, among allowed, starting on current.
func askPortProtocol(allowed []string, current string) (string, bool) {
	var opts []tui.Option
	var values []string
	def := 0
	for _, o := range portProtocolOptions {
		for _, a := range allowed {
			if a == o.value {
				if o.value == current {
					def = len(values)
				}
				opts = append(opts, o.opt)
				values = append(values, o.value)
			}
		}
	}
	i := tui.ChooseOptDefault("What Do The Forwarded Ports Carry?", opts, def)
	if i < 0 {
		return "", false
	}
	return values[i], true
}

// allPortProtocols is every choice, for a tunnel that can forward both kinds.
var allPortProtocols = []string{config.PortsTCP, config.PortsUDP, config.PortsBoth}

// setL3PortProtocol sets what a direct tunnel's mapped ports carry.
func setL3PortProtocol(l *config.L3Config, v string) {
	l.AcceptUDP = v == config.PortsUDP || v == config.PortsBoth
	l.PortProtocol = v
}

// l3PortProtocol reads it back, for a summary or a default.
func l3PortProtocol(l config.L3Config) string {
	switch {
	case !l.ForwardsUDP():
		return config.PortsTCP
	case !l.ForwardsTCP():
		return config.PortsUDP
	}
	return config.PortsBoth
}

// unionPortProtocol is what a port shared by two tunnels has to carry: what
// either of them asked for.
func unionPortProtocol(a, b string) string {
	tcp := a != config.PortsUDP || b != config.PortsUDP
	udp := a != config.PortsTCP || b != config.PortsTCP
	switch {
	case tcp && udp:
		return config.PortsBoth
	case udp:
		return config.PortsUDP
	}
	return config.PortsTCP
}
