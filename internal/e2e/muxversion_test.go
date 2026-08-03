package e2e

import (
	"fmt"
	"testing"
)

// The two ends of a mux tunnel must never disagree about the smux version.
//
// smux has no negotiation of its own: every frame carries a version number, and
// the first frame whose number is not what the reader expects tears the session
// down with "invalid protocol". Because the control channel is not muxed, a
// disagreement does not look like a failure — the tunnel connects, reports
// itself up, and then carries nothing at all.
//
// So the server settles it on the control channel and the client uses what it
// is told, whatever its own file says. These cases cover the combinations that
// used to be able to disagree: each side pinned differently, and each side
// left to negotiate. Carrying traffic end to end is the assertion, because a
// mismatch is precisely what stops traffic while leaving everything else
// looking healthy.
func TestMuxVersionNeverDisagrees(t *testing.T) {
	cases := []struct {
		name           string
		server, client int // 0 = leave it out of the config, i.e. negotiate
	}{
		{"both negotiate", 0, 0},
		{"server negotiates, client pinned to 1", 0, 1},
		{"server negotiates, client pinned to 2", 0, 2},
		{"server pinned to 1, client negotiates", 1, 0},
		{"server pinned to 2, client negotiates", 2, 0},
		// The case that broke before: each side pinned to a different number.
		// The server's answer wins, so this now works rather than silently
		// carrying nothing.
		{"server pinned to 2, client pinned to 1", 2, 1},
		{"server pinned to 1, client pinned to 2", 1, 2},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			backend := startEchoBackend(t)
			tunnelPort := freePort(t)
			entryPort := freePort(t)
			token := "muxver-token-0123456789abcdefg"

			srvCfg := baseServerConfig("tcpmux", tunnelPort, entryPort, backend.addr, token)
			srvCfg.MuxVersion = tc.server
			cliCfg := baseClientConfig("tcpmux", fmt.Sprintf("127.0.0.1:%d", tunnelPort), token, nil)
			cliCfg.MuxVersion = tc.client

			tun := runPair(t, srvCfg, cliCfg, entryPort, tunnelPort)
			if err := tun.waitReady(tunnelReadyTimeout); err != nil {
				t.Fatalf("server=%d client=%d never carried traffic: %v", tc.server, tc.client, err)
			}
		})
	}
}
