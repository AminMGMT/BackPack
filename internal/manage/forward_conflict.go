package manage

import (
	"fmt"
	"net"
	"os"
	"strings"

	"github.com/backpack/backpack/config"
	"github.com/backpack/backpack/internal/app"
	"github.com/backpack/backpack/internal/forwardmap"
)

type listenClaim struct {
	network string // tcp or udp
	addr    string
	purpose string
}

func transportNetwork(transport string) string {
	if isDatagram(transport) {
		return "udp"
	}
	return "tcp"
}

func appForwardClaims(s TunnelSpec) ([]listenClaim, error) {
	var claims []listenClaim
	if s.Role == "server" { // geographic Iran edge owns user ingress
		mappings, err := forwardmap.Expand(s.Ports)
		if err != nil {
			return nil, err
		}
		network := "tcp"
		if s.Transport == "udp" {
			network = "udp"
		}
		for _, mapping := range mappings {
			claims = append(claims, listenClaim{network: network, addr: mapping.Listen, purpose: "Direct ingress"})
		}
	}
	if s.operationalServer() && !isRawDatagram(s.Transport) {
		claims = append(claims, listenClaim{network: transportNetwork(s.Transport), addr: s.BindAddr, purpose: "tunnel listener"})
	}
	return claims, nil
}

func tunnelClaims(t Tunnel) []listenClaim {
	var claims []listenClaim
	if t.Role == "server" && !isRawDatagram(t.Transport) && strings.TrimSpace(t.Addr) != "" {
		claims = append(claims, listenClaim{network: transportNetwork(t.Transport), addr: t.Addr, purpose: t.Name + " tunnel listener"})
	}
	if len(t.Ports) > 0 {
		if mappings, err := forwardmap.Expand(t.Ports); err == nil {
			network := "tcp"
			if t.Transport == "udp" {
				network = "udp"
			}
			for _, mapping := range mappings {
				claims = append(claims, listenClaim{network: network, addr: mapping.Listen, purpose: t.Name + " exposed port"})
			}
		}
	}
	for _, mapping := range t.Mappings { // advanced iptables engine
		lr, _, err := mapping.Ranges()
		if err != nil {
			continue
		}
		for _, proto := range mapping.Protocols {
			for p := int(lr.Start); p <= int(lr.End); p++ {
				claims = append(claims, listenClaim{network: strings.ToLower(proto), addr: net.JoinHostPort(mapping.ListenAddress, fmt.Sprint(p)), purpose: t.Name + " iptables mapping"})
			}
		}
	}
	return claims
}

func splitClaim(addr string) (host, port string, ok bool) {
	host, port, err := net.SplitHostPort(addr)
	return host, port, err == nil
}

func wildcardHost(host string) bool {
	switch strings.Trim(strings.TrimSpace(host), "[]") {
	case "", "0.0.0.0", "::", "*":
		return true
	}
	return false
}

func claimFamily(host string) int {
	host = strings.Trim(strings.TrimSpace(host), "[]")
	if host == "" || host == "*" {
		return 0
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return 0
	}
	if ip.To4() != nil {
		return 4
	}
	return 6
}

func claimsOverlap(a, b listenClaim) bool {
	if a.network != b.network {
		return false
	}
	ah, ap, aok := splitClaim(a.addr)
	bh, bp, bok := splitClaim(b.addr)
	if !aok || !bok || ap != bp {
		return false
	}
	af, bf := claimFamily(ah), claimFamily(bh)
	if af != 0 && bf != 0 && af != bf {
		return false
	}
	return wildcardHost(ah) || wildcardHost(bh) || strings.EqualFold(ah, bh)
}

// validateForwardConflicts rejects mistakes before config replacement or
// optimization. Existing local sockets are probed for new instances; edits
// rely on config claims so the instance being replaced is not mistaken for a
// second owner of its own ports.
func validateForwardConflicts(s TunnelSpec) error {
	if s.Engine != config.EngineForward {
		return nil
	}
	claims, err := appForwardClaims(s)
	if err != nil {
		return err
	}
	for i := range claims {
		for j := 0; j < i; j++ {
			if claimsOverlap(claims[i], claims[j]) {
				return fmt.Errorf("%s %s overlaps %s %s", claims[i].network, claims[i].addr, claims[j].purpose, claims[j].addr)
			}
		}
	}
	for _, other := range List() {
		if other.Name == s.Name {
			continue
		}
		for _, existing := range tunnelClaims(other) {
			for _, candidate := range claims {
				if claimsOverlap(candidate, existing) {
					return fmt.Errorf("%s %s conflicts with %s (%s)", candidate.network, candidate.addr, existing.purpose, existing.addr)
				}
			}
		}
	}
	var oldClaims []listenClaim
	if _, err := os.Stat(app.ConfigPath(s.Name)); err == nil {
		if current, ok := Find(s.Name); ok {
			oldClaims = tunnelClaims(current)
		}
	}
	for _, claim := range claims {
		ownedByCurrent := false
		for _, old := range oldClaims {
			if claimsOverlap(claim, old) {
				ownedByCurrent = true
				break
			}
		}
		if ownedByCurrent {
			continue
		}
		if claim.network == "udp" {
			pc, err := net.ListenPacket("udp", claim.addr)
			if err != nil {
				return fmt.Errorf("%s %s is already in use: %w", claim.purpose, claim.addr, err)
			}
			pc.Close()
		} else {
			ln, err := net.Listen("tcp", claim.addr)
			if err != nil {
				return fmt.Errorf("%s %s is already in use: %w", claim.purpose, claim.addr, err)
			}
			ln.Close()
		}
	}
	return nil
}
