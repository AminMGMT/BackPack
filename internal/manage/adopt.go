package manage

import "strings"

// Recognising a tunnel's other end on a server the panel manages.
//
// A tunnel built through the panel's fleet has its two ends recorded as a pair,
// and everything that reaches across — carrying an edit, starting both halves,
// reading the far journal, standing up the speed test's receiver — is gated on
// that record existing. A tunnel built any other way has no record, so none of
// it works, and there was no way to add one: the pairing was written in exactly
// one place, when the panel created both ends at once.
//
// That leaves every tunnel that predates the fleet permanently outside it, on a
// server the panel is otherwise managing. The fix is to be able to say "this
// tunnel's other end is that one over there" — but not by guessing, because a
// wrong pairing sends the next edit to a stranger.
//
// So it is matched on the thing the two ends genuinely share.

// PairCandidate is one tunnel on a managed server that could be the other end
// of a local one.
type PairCandidate struct {
	// Name is what the tunnel is called on that server.
	Name string `json:"name"`

	// Certain is set when the two ends demonstrably meet.
	//
	// A reverse client dials its server's host and port, and that port is the
	// one the server binds — so a client over there aimed at this machine's
	// address, on this tunnel's port, is this tunnel's other end. Nothing about
	// the names has to agree, and nothing about them is trusted.
	Certain bool `json:"certain"`

	// Why says what the match rests on, so the operator confirming it can see
	// whether it was demonstrated or merely plausible.
	Why string `json:"why"`
}

// FarEnd is the little the matcher needs to know about a tunnel on the other
// server. It mirrors node.TunnelState without importing it — manage is below
// node, and one direction of that dependency is all this needs.
type FarEnd struct {
	Name       string
	Role       string
	TunnelPort string
	ServerHost string
}

// MatchFarEnds ranks the tunnels on one managed server against a local one,
// returning only those that could be its other end.
//
// mine is this end's own settings and far is what the server reported; neither
// is read from disk here. The matching is the whole of what this decides, and
// keeping the reading out of it is what lets the decision be tested against
// configurations rather than against a machine.
//
// hostsOfThisServer are the addresses this machine is reachable at, which is
// what a client on the far side would have been pointed at. An empty list
// weakens a match to plausible rather than certain — it does not invent one.
func MatchFarEnds(mine TunnelSettings, far []FarEnd, hostsOfThisServer []string) []PairCandidate {
	want := oppositeRole(mine.Role)

	var out []PairCandidate
	for _, f := range far {
		if f.Role != want {
			continue // two servers, or two clients, are not two ends
		}
		if f.TunnelPort == "" || mine.TunnelPort == "" || f.TunnelPort != mine.TunnelPort {
			continue // they do not meet anywhere
		}

		// Which side names the other decides what can be demonstrated. When the
		// far end is the client, it holds the address it dials and that address
		// is this machine or it is not. When this end is the client, it is our
		// own configuration that names them, and the far server's own address is
		// what the fleet already knows — so the port is all this has.
		c := PairCandidate{Name: f.Name}
		switch {
		case f.Role == "client" && f.ServerHost != "":
			if matchesAny(f.ServerHost, hostsOfThisServer) {
				c.Certain = true
				c.Why = "it dials " + f.ServerHost + ":" + f.TunnelPort + ", which is this tunnel"
			} else {
				// A client aimed somewhere else entirely is not a candidate at
				// all, however well the port lines up.
				continue
			}
		case mine.Role == "client" && mine.ServerHost != "":
			c.Certain = true
			c.Why = "this end dials " + mine.ServerHost + ":" + mine.TunnelPort + ", which is that server"
		default:
			c.Why = "both ends use port " + f.TunnelPort
		}
		out = append(out, c)
	}
	return out
}

func oppositeRole(r string) string {
	if r == "server" {
		return "client"
	}
	return "server"
}

// matchesAny reports whether host is one of the addresses this server answers
// at. Compared case-insensitively and without a port, because the far side
// records what the operator typed.
func matchesAny(host string, hosts []string) bool {
	h := strings.ToLower(strings.TrimSpace(host))
	if h == "" {
		return false
	}
	for _, x := range hosts {
		if strings.EqualFold(strings.TrimSpace(x), h) {
			return true
		}
	}
	return false
}

// MatchFarEndsFor is MatchFarEnds for a tunnel this machine holds, reading its
// settings from the same place the edit form does.
func MatchFarEndsFor(name string, far []FarEnd, hostsOfThisServer []string) []PairCandidate {
	mine, err := TunnelSettingsOf(name)
	if err != nil {
		return nil
	}
	return MatchFarEnds(mine, far, hostsOfThisServer)
}
