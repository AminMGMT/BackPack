package manage

import (
	"testing"
)

// Matching a tunnel to its other end on a managed server.
//
// This is what makes a tunnel that already exists joinable to the fleet. Every
// reach-across the panel has — carrying an edit, starting both halves, reading
// the far journal, standing the speed test's receiver up over there — is gated
// on a record of where the other end lives, and until now that record could
// only be written at the moment the panel built both ends at once. A tunnel
// made any other way was permanently outside the fleet, on a server the panel
// was otherwise managing.
//
// The matching has to be demonstrated rather than guessed, because a pairing is
// a decision about where the operator's next edit is sent.

// A client over there aimed at this machine, on this tunnel's port, is this
// tunnel's other end — whatever either of them is called.
// iranEnd is this machine's half: a reverse server bound to 3454.
var iranEnd = TunnelSettings{Name: "nuremberg", Role: "server", TunnelPort: "3454"}

func TestTheOtherEndIsRecognisedByWhereTheEndsMeet(t *testing.T) {
	far := []FarEnd{
		{Name: "totally-different-name", Role: "client", TunnelPort: "3454", ServerHost: "94.139.180.179"},
		{Name: "nuremberg", Role: "client", TunnelPort: "9999", ServerHost: "94.139.180.179"},
		{Name: "someone-elses", Role: "client", TunnelPort: "3454", ServerHost: "203.0.113.9"},
		{Name: "not-a-client", Role: "server", TunnelPort: "3454", ServerHost: ""},
	}
	got := MatchFarEnds(iranEnd, far, []string{"94.139.180.179"})

	if len(got) != 1 {
		t.Fatalf("matched %d candidates, want exactly the one that meets this tunnel: %+v", len(got), got)
	}
	if got[0].Name != "totally-different-name" {
		t.Errorf("matched %q — the name is not what ties two ends together", got[0].Name)
	}
	if !got[0].Certain {
		t.Error("a client dialling this machine on this tunnel's port is a demonstrated " +
			"match, not a guess")
	}
}

// A client aimed at a different server is never a candidate, however well the
// port lines up. Two operators can use port 3454.
func TestAClientAimedElsewhereIsNotOffered(t *testing.T) {
	got := MatchFarEnds(iranEnd,
		[]FarEnd{{Name: "theirs", Role: "client", TunnelPort: "3454", ServerHost: "203.0.113.9"}},
		[]string{"94.139.180.179"})
	if len(got) != 0 {
		t.Errorf("offered a tunnel that dials somebody else's server: %+v", got)
	}
}

// Two servers, or two clients, are not two ends of one tunnel.
func TestTwoOfTheSameRoleAreNeverAPair(t *testing.T) {
	got := MatchFarEnds(iranEnd,
		[]FarEnd{{Name: "also-a-server", Role: "server", TunnelPort: "3454"}},
		[]string{"94.139.180.179"})
	if len(got) != 0 {
		t.Errorf("offered another server as this server's client end: %+v", got)
	}
}

// With nothing to compare the address against, a shared port is offered but
// never claimed as demonstrated.
func TestWithoutAnAddressAMatchIsOfferedNotClaimed(t *testing.T) {
	got := MatchFarEnds(iranEnd,
		[]FarEnd{{Name: "maybe", Role: "client", TunnelPort: "3454"}},
		nil)
	if len(got) != 1 {
		t.Fatalf("a shared port is worth offering: %+v", got)
	}
	if got[0].Certain {
		t.Error("a shared port alone was reported as demonstrated — two operators " +
			"can both use 3454, and a wrong pairing sends the next edit to a stranger")
	}
}
