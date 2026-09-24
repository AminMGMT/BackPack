package webui

import "testing"

// What leaves the machine.
//
// The audit file is on the box the panel is root on, so an attacker who reaches
// it can rewrite the record of how they got there — and being trustworthy
// afterwards is the record's whole value. There is no making a local file
// tamper-proof against local root; what there is, is a copy written elsewhere
// as it happens.
//
// The choice of *what* to copy is the whole design. Forwarding everything means
// thousands of messages a day from a panel that polls itself, and a channel
// nobody reads hides the one line that mattered.

func TestEveryRefusalIsForwarded(t *testing.T) {
	// A run of these is somebody trying credentials, and it is the earliest
	// signal there is.
	for _, status := range []int{401, 403, 429, 500} {
		e := auditEntry{Path: "/api/stats", Method: "GET", Status: status}
		if !worthForwarding(e) {
			t.Errorf("a %d was not forwarded", status)
		}
	}
}

func TestCredentialAndFleetChangesAreForwarded(t *testing.T) {
	for _, e := range []auditEntry{
		{Path: "/api/tokens", Method: "POST", Status: 200},
		{Path: "/api/password", Method: "POST", Status: 200},
		{Path: "/api/totp", Method: "POST", Status: 200},
		{Path: "/api/sessions", Method: "POST", Status: 200},
		{Path: "/api/nodes", Method: "POST", Action: "add", Status: 200},
		{Path: "/api/nodes", Method: "POST", Action: "remove", Status: 200},
		{Path: "/api/nodes", Method: "POST", Action: "credentials", Status: 200},
		{Path: "/api/nodes", Method: "POST", Action: "upgradeall", Status: 200},
	} {
		if !worthForwarding(e) {
			t.Errorf("%s %s (%s) was not forwarded, and it changes who can do what "+
				"or what the fleet is", e.Method, e.Path, e.Action)
		}
	}
}

// The other half, and the half that makes the channel readable: the ordinary
// traffic of somebody using the panel does not leave the machine.
func TestOrdinaryUseIsNotForwarded(t *testing.T) {
	for _, e := range []auditEntry{
		{Path: "/api/tunnel/create", Method: "POST", Status: 200},
		{Path: "/api/logs", Method: "POST", Status: 200},
		{Path: "/api/nodes", Method: "POST", Action: "refresh", Status: 200},
		{Path: "/api/nodes", Method: "POST", Action: "rolloutstatus", Status: 200},
		{Path: "/api/autobackup", Method: "POST", Status: 200},
	} {
		if worthForwarding(e) {
			t.Errorf("%s %s (%s) was forwarded; a channel that carries the ordinary "+
				"traffic of somebody using the panel is one nobody reads, which hides "+
				"the line that mattered", e.Method, e.Path, e.Action)
		}
	}
}

// A successful read is the most common entry there is and is not recorded at
// all, let alone forwarded.
func TestReadsAreNeitherRecordedNorForwarded(t *testing.T) {
	e := auditEntry{Path: "/metrics", Method: "GET", Status: 200}
	if worthForwarding(e) {
		t.Error("a successful read was forwarded")
	}
}
