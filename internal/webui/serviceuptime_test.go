package webui

import (
	"testing"
	"time"
)

// A tunnel's uptime is how long its service has been up, read from systemd.
//
// It used to be the snapshot's own figure: time since the engine's metrics
// collector was made. The engine makes a new collector for every generation —
// every reload of its file, every restart asked for over the control socket —
// so the card's "up 4m" meant "since something last touched this tunnel", not
// "since it was started", and an operator reading it after logging in saw a
// number that had nothing to do with how long the tunnel had been carrying
// traffic.
func TestServiceSinceParsesBothTimestampShapes(t *testing.T) {
	// --timestamp=unix, systemd 248 and later.
	unix := "Id=backpack-a.service\nActiveEnterTimestamp=@1790000000\n\n" +
		"Id=backpack-b.service\nActiveEnterTimestamp=\n\n" +
		"Id=backpack-c.service\nActiveEnterTimestamp=@0\n"
	got := parseActiveSince(unix)
	if want := time.Unix(1790000000, 0); !got["backpack-a.service"].Equal(want) {
		t.Errorf("a = %v, want %v", got["backpack-a.service"], want)
	}
	if _, ok := got["backpack-b.service"]; ok {
		t.Error("a unit that was never active has no start time")
	}
	if _, ok := got["backpack-c.service"]; ok {
		t.Error("@0 is systemd's way of saying never, not 1970")
	}

	// The plain shape older systemd prints, in the machine's own zone.
	when := time.Date(2026, 9, 29, 10, 4, 5, 0, time.Local)
	plain := "Id=backpack-d.service\nActiveEnterTimestamp=" + when.Format("Mon 2006-01-02 15:04:05 MST") + "\n"
	if got := parseActiveSince(plain)["backpack-d.service"]; !got.Equal(when) {
		t.Errorf("d = %v, want %v", got, when)
	}
}

func TestUpForReadsTheWayACardDoes(t *testing.T) {
	for d, want := range map[time.Duration]string{
		40 * time.Second:               "40s",
		12*time.Minute + 5*time.Second: "12m",
		3*time.Hour + 7*time.Minute:    "3h 7m",
		50*time.Hour + 30*time.Minute:  "2d 2h",
		-5 * time.Second:               "0s",
	} {
		if got := upFor(d); got != want {
			t.Errorf("upFor(%v) = %q, want %q", d, got, want)
		}
	}
}
