package manage

import (
	"os"
	"strings"
	"testing"
)

// The report this exists for: "the health check still says Open file limit
// 1024 — low for many connections, run Optimize then reboot — even though
// Optimize has been run and the server rebooted."
//
// Everything about that was wrong except the number.
//
//   - It measured this process. `ulimit -n` in a shell started by the panel
//     reports the panel's own ceiling, and the limit that matters is the one
//     the tunnels run under.
//   - Optimize writes /etc/security/limits.conf, which belongs to PAM and
//     applies to login sessions. A systemd service never reads it. So the
//     advice could not change the number it was complaining about, on that
//     reboot or any other.
//   - And the units for the panel, the monitor and the proxy asked for no
//     limit at all, so they really were on systemd's default of 1024.
//
// These guard the three of them.

// The limit is a property of the unit, so every unit backpack installs has to
// ask for one.
func TestEveryServiceUnitAsksForItsOpenFileLimit(t *testing.T) {
	for _, c := range []struct{ file, what string }{
		{"systemd.go", "the tunnel"},
		{"monitorservice.go", "the monitor"},
		{"proxyservice.go", "the proxy"},
		{"../webui/config.go", "the web panel"},
	} {
		b, err := os.ReadFile(c.file)
		if err != nil {
			t.Fatalf("cannot read %s: %v", c.file, err)
		}
		src := string(b)
		if !strings.Contains(src, "[Service]") {
			t.Fatalf("%s no longer holds a unit — this guard needs updating", c.file)
		}
		if !strings.Contains(src, "LimitNOFILE=") {
			t.Errorf("%s installs %s with no LimitNOFILE, so it runs on systemd's "+
				"default of 1024 — and nothing an operator can do from the menu "+
				"or from Optimize will raise it", c.file, c.what)
		}
	}
}

// And the check must ask the tunnels, not itself.
func TestTheOpenFileCheckMeasuresTheTunnels(t *testing.T) {
	b, err := os.ReadFile("diagnose.go")
	if err != nil {
		t.Fatalf("cannot read diagnose.go: %v", err)
	}
	src := string(b)

	if strings.Contains(src, `"ulimit -n"`) {
		t.Error("the open-file check reads this process's own limit again; that is the " +
			"panel's ceiling, not the one a tunnel runs under")
	}
	if !strings.Contains(src, "/proc/%d/limits") {
		t.Error("the check no longer reads a running tunnel's real limit from /proc, " +
			"which is the only ground truth for what the kernel is enforcing")
	}
	// The advice has to name something that can actually change the number.
	i := strings.Index(src, `Name: "Open file limit"`)
	if i < 0 {
		t.Fatal("the open-file check is gone — this guard needs updating")
	}
	near := src[i:min(len(src), i+900)]
	if strings.Contains(near, "run Optimize, then reboot") {
		t.Error("the fix still tells the operator to run Optimize and reboot, which " +
			"writes limits.conf — a file no systemd service reads, so the number " +
			"they are looking at cannot change")
	}
}

// A unit written by an older version has to be brought up to date on its own,
// or the raised ceiling never reaches the tunnels that predate it.
//
// EnsureUnits is not called here on purpose: app.ServiceDir is a constant, so
// it would write into the real /etc/systemd/system of whatever machine the
// tests run on — including an operator's. What is checked instead is the
// invariant it depends on.
func TestTheUnitItComparesAgainstIsTheOneItWould(t *testing.T) {
	want := unitFor("nl-ws")
	if !strings.Contains(want, "LimitNOFILE=") {
		t.Error("the tunnel unit no longer asks for an open-file limit")
	}
	if !strings.Contains(want, "Backpack Tunnel (nl-ws)") {
		t.Error("unitFor no longer produces the tunnel's own unit")
	}

	src, err := os.ReadFile("systemd.go")
	if err != nil {
		t.Fatalf("cannot read systemd.go: %v", err)
	}
	body := string(src)

	// Both sides must come from unitFor. A second copy of the template is a
	// copy that will drift, and the moment it does EnsureUnits rewrites every
	// unit on every start and never settles.
	for _, fn := range []string{"func writeUnit(", "func EnsureUnits("} {
		i := strings.Index(body, fn)
		if i < 0 {
			t.Fatalf("%s is gone — this guard needs updating", fn)
		}
		f := body[i:]
		if end := strings.Index(f, "\n}\n"); end > 0 {
			f = f[:end]
		}
		if !strings.Contains(f, "unitFor(") {
			t.Errorf("%s builds its own unit text instead of using unitFor, so the two "+
				"will disagree and every start will rewrite every unit", fn)
		}
	}

	// And it has to be called somewhere, or a stale unit is never noticed.
	menu, err := os.ReadFile("../menu/menu.go")
	if err != nil {
		t.Fatalf("cannot read menu.go: %v", err)
	}
	if !strings.Contains(string(menu), "EnsureUnits()") {
		t.Error("nothing brings stale tunnel units up to date, so a tunnel created by " +
			"an older version keeps that version's open-file ceiling for ever")
	}
}
