package utils

import (
	"bytes"
	"encoding/json"
	"testing"
)

// The JSON field names are an interface.
//
// A dashboard, an alert rule and a grep in somebody's runbook are all written
// against these names. Renaming one is not a refactor: it silently breaks every
// query written against it, on the update that ships it, with nothing failing
// anywhere. docs/log-schema.md is the contract and this is what holds it.

func logJSON(t *testing.T, msg string) map[string]any {
	t.Helper()
	log := NewLoggerWithFormat("info", "json")
	var buf bytes.Buffer
	log.SetOutput(&buf)
	log.Info(msg)

	var line map[string]any
	if err := json.Unmarshal(buf.Bytes(), &line); err != nil {
		t.Fatalf("the JSON formatter did not produce JSON: %v\n%s", err, buf.String())
	}
	return line
}

func TestAShippedLineSaysWhichTunnelItCameFrom(t *testing.T) {
	t.Cleanup(func() { logIdentity.Store(nil) })
	SetLogIdentity(LogIdentity{
		Tunnel: "fr-relay", Role: "server", Transport: "wss", Host: "iran-1",
	})

	line := logJSON(t, "listening")

	for field, want := range map[string]string{
		FieldTunnel:    "fr-relay",
		FieldRole:      "server",
		FieldTransport: "wss",
		FieldHost:      "iran-1",
	} {
		got, ok := line[field]
		if !ok {
			t.Errorf("a shipped line has no %q field; it carried %v", field, keys(line))
			continue
		}
		if got != want {
			t.Errorf("%s = %v, want %q", field, got, want)
		}
	}

	// The fields logrus already wrote are part of the contract too.
	for _, field := range []string{"time", "level", "msg"} {
		if _, ok := line[field]; !ok {
			t.Errorf("a shipped line has no %q field", field)
		}
	}
}

// The hostname is what tells two servers running a tunnel of the same name
// apart, which is the single most common fleet arrangement here: the same
// tunnel name on the Iran box and on the kharej box.
func TestTheHostIsFilledInWhenItIsNotGiven(t *testing.T) {
	t.Cleanup(func() { logIdentity.Store(nil) })
	SetLogIdentity(LogIdentity{Tunnel: "fr-relay", Role: "client"})

	if CurrentLogIdentity().Host == "" {
		t.Fatal("the identity was stored with no host, so two servers' lines are indistinguishable")
	}
	if _, ok := logJSON(t, "dialling")[FieldHost]; !ok {
		t.Fatal("a shipped line has no host field")
	}
}

// A logger built before anything set the identity must not invent one.
func TestWithNoIdentityNothingIsAdded(t *testing.T) {
	logIdentity.Store(nil)
	line := logJSON(t, "starting")
	for _, field := range []string{FieldTunnel, FieldRole, FieldTransport, FieldHost} {
		if v, ok := line[field]; ok {
			t.Errorf("%s was set to %v with no identity recorded", field, v)
		}
	}
}

// The identity is a default, not an override: a call site that says something
// specific under one of these names means it.
func TestAnExplicitFieldWinsOverTheProcessIdentity(t *testing.T) {
	t.Cleanup(func() { logIdentity.Store(nil) })
	SetLogIdentity(LogIdentity{Tunnel: "fr-relay", Role: "server", Transport: "wss"})

	log := NewLoggerWithFormat("info", "json")
	var buf bytes.Buffer
	log.SetOutput(&buf)
	log.WithField(FieldTransport, "quic").Info("rotated")

	var line map[string]any
	if err := json.Unmarshal(buf.Bytes(), &line); err != nil {
		t.Fatalf("not JSON: %v", err)
	}
	if line[FieldTransport] != "quic" {
		t.Fatalf("transport = %v, want the value the call site gave", line[FieldTransport])
	}
	if line[FieldTunnel] != "fr-relay" {
		t.Fatalf("the rest of the identity was lost: %v", line)
	}
}

// The text format is read by a person on the server they are already logged
// into. Repeating four things they know on every line is noise.
func TestTheTextFormatIsLeftAlone(t *testing.T) {
	t.Cleanup(func() { logIdentity.Store(nil) })
	SetLogIdentity(LogIdentity{Tunnel: "fr-relay", Role: "server", Transport: "wss"})

	log := NewLoggerWithFormat("info", "")
	var buf bytes.Buffer
	log.SetOutput(&buf)
	log.Info("listening")

	if bytes.Contains(buf.Bytes(), []byte("fr-relay")) {
		t.Fatalf("the human format grew the identity fields: %s", buf.String())
	}
}

func keys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
