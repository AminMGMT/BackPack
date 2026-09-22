package manage

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/backpack/backpack/internal/app"
)

// The migration pass exists because a fix that changes only what a new install
// writes reaches nobody. These check the two properties the whole idea rests on:
// it corrects a machine an older version set up, and running it again changes
// nothing.

func writeConfig(t *testing.T, dir, name, body string, mode os.FileMode) string {
	t.Helper()
	p := filepath.Join(dir, name+".toml")
	if err := os.WriteFile(p, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
	// WriteFile respects the umask, so say the mode again — the whole point of
	// this test is the mode the file actually has.
	if err := os.Chmod(p, mode); err != nil {
		t.Fatal(err)
	}
	return p
}

func collect(lines *[]string) func(string) {
	return func(s string) { *lines = append(*lines, s) }
}

// A tunnel config holds the token that authorises a connection to the tunnel.
// Every other secret this program writes is 0600; these alone were 0644, so any
// account on the box could read every token on it. Installs made before the fix
// keep that mode through every update unless something goes and changes it.
func TestAnUpdateTightensConfigsThatAreWorldReadable(t *testing.T) {
	withTempConfigDir(t)
	dir := t.TempDir()
	loose := writeConfig(t, dir, "iran-main", "[server]\ntoken = \"secret\"\n", 0o644)
	already := writeConfig(t, dir, "kharej", "[client]\ntoken = \"secret\"\n", app.TunnelConfigMode)

	var log []string
	if n := migrateConfigsIn(dir, collect(&log)); n != 1 {
		t.Fatalf("migrated %d files, want 1", n)
	}

	if mode := statMode(t, loose); mode != app.TunnelConfigMode {
		t.Errorf("iran-main.toml is %#o after the update, want %#o — every account on "+
			"the machine can still read its token", mode, app.TunnelConfigMode)
	}
	if mode := statMode(t, already); mode != app.TunnelConfigMode {
		t.Errorf("kharej.toml came out %#o", mode)
	}
	if !strings.Contains(strings.Join(log, "\n"), "iran-main") {
		t.Errorf("the change was made without saying so: %q", log)
	}
	// A file that was already right is not mentioned, or every update prints a
	// list of things it did not do.
	if strings.Contains(strings.Join(log, "\n"), "kharej") {
		t.Errorf("a config that was already correct was reported as changed: %q", log)
	}
}

// The contents are left exactly as they are when only the mode is wrong. An
// update that reformatted every config on the machine would be a far bigger
// change than the one it is making.
func TestTighteningTheModeDoesNotTouchTheContents(t *testing.T) {
	withTempConfigDir(t)
	dir := t.TempDir()
	const body = "[server]\n# a comment somebody wrote by hand\ntoken = \"secret\"\nports = [\"80\"]\n"
	p := writeConfig(t, dir, "iran-main", body, 0o644)

	migrateConfigsIn(dir, func(string) {})

	got, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != body {
		t.Errorf("the config was rewritten:\n%s", got)
	}
	// And nothing was filed in the history, because nothing about the
	// configuration changed.
	if h := ConfigHistory("iran-main"); len(h) != 0 {
		t.Errorf("a permission change was filed as a configuration change: %+v", h)
	}
}

// Running on every update is what makes a marker file unnecessary, and it is
// only safe if a second run is free.
func TestRunningTheMigrationsAgainChangesNothing(t *testing.T) {
	withTempConfigDir(t)
	dir := t.TempDir()
	writeConfig(t, dir, "iran-main", "[server]\ntoken = \"secret\"\n", 0o644)

	if n := migrateConfigsIn(dir, func(string) {}); n != 1 {
		t.Fatalf("first pass migrated %d files, want 1", n)
	}
	var log []string
	if n := migrateConfigsIn(dir, collect(&log)); n != 0 {
		t.Fatalf("second pass migrated %d files, want 0 — this runs on every update, "+
			"so a pass that keeps finding work would rewrite configs forever", n)
	}
	if len(log) != 0 {
		t.Errorf("the second pass reported: %q", log)
	}
}

// A migration that rewrites the configuration files what it replaced first, so
// the change is in the panel's Undo list like any other edit. There is no such
// migration in this version, so the path is exercised with one of its own —
// which is also what keeps the plumbing honest until there is.
func TestAMigrationThatRewritesAConfigFilesWhatItReplaced(t *testing.T) {
	withTempConfigDir(t)
	dir := t.TempDir()
	const before = "[server]\nkeepalive_period = 75\n"
	p := writeConfig(t, dir, "iran-main", before, app.TunnelConfigMode)

	saved := configMigrations
	configMigrations = append([]configMigration{{
		id: "test-only",
		apply: func(f *configFile) string {
			if !strings.Contains(string(f.Body), "keepalive_period = 75") {
				return ""
			}
			f.Body = []byte(strings.Replace(string(f.Body),
				"keepalive_period = 75", "keepalive_period = 30", 1))
			return "keepalive_period 75 → 30"
		},
	}}, saved...)
	t.Cleanup(func() { configMigrations = saved })

	if n := migrateConfigsIn(dir, func(string) {}); n != 1 {
		t.Fatalf("migrated %d files, want 1", n)
	}

	got, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "keepalive_period = 30") {
		t.Errorf("the migration did not take:\n%s", got)
	}
	if mode := statMode(t, p); mode != app.TunnelConfigMode {
		t.Errorf("the rewritten config came out %#o, want %#o", mode, app.TunnelConfigMode)
	}

	h := ConfigHistory("iran-main")
	if len(h) != 1 {
		t.Fatalf("the update rewrote a config and left no way back: %+v", h)
	}
	if h[0].Prev != before {
		t.Errorf("the history kept %q, want the configuration as it was", h[0].Prev)
	}
	if !strings.Contains(h[0].Note, app.Version) {
		t.Errorf("the history entry does not say which update made the change: %q", h[0].Note)
	}
}

// Older versions widened the ephemeral port range on every tunnel start, which
// put every service port on the machine inside the range the kernel hands out
// as the source port of an outgoing connection. Not setting it any more fixes
// nothing for a kernel that already has it.
func TestAnUpdateRestoresAnEphemeralRangeAnOlderVersionWidened(t *testing.T) {
	var wrote [][2]string
	cur := "1024 65535"
	restore := stubSysctl(t, func(string) string { return cur }, func(k, v string) error {
		wrote = append(wrote, [2]string{k, v})
		cur = v
		return nil
	})
	defer restore()

	var log []string
	MigrateAfterUpdate(collect(&log))

	if len(wrote) != 1 || wrote[0][0] != "net.ipv4.ip_local_port_range" {
		t.Fatalf("the range was not restored: %v", wrote)
	}
	if want := "32768 60999"; wrote[0][1] != want {
		t.Errorf("set the range to %q, want the kernel's own %q", wrote[0][1], want)
	}
	if !strings.Contains(strings.Join(log, "\n"), "ephemeral port range") {
		t.Errorf("the range was changed without saying so: %q", log)
	}

	// Second run: the value is right now, so there is nothing to do.
	wrote = nil
	MigrateAfterUpdate(func(string) {})
	if len(wrote) != 0 {
		t.Errorf("the second pass set it again: %v", wrote)
	}
}

// A range that is wide in some other way was widened by somebody with a reason.
// This corrects a string this program wrote, and nothing else.
func TestARangeThisProgramDidNotWriteIsLeftAlone(t *testing.T) {
	for _, cur := range []string{
		"2000 65535",  // wide, but not what the engine wrote
		"1024 60999",  // half of it
		"32768 60999", // already correct
		"40000 50000", // deliberately narrow
		"",            // unreadable
	} {
		t.Run(cur, func(t *testing.T) {
			var wrote [][2]string
			restore := stubSysctl(t, func(string) string { return cur },
				func(k, v string) error { wrote = append(wrote, [2]string{k, v}); return nil })
			defer restore()

			MigrateAfterUpdate(func(string) {})
			if len(wrote) != 0 {
				t.Errorf("%q was overwritten with %v", cur, wrote)
			}
		})
	}
}

// A migration that cannot be applied says so rather than passing quietly. By
// the time this runs the binary is installed and working, so it must not fail
// the update — which makes the log the only thing that reports it.
func TestAMigrationThatFailsIsReported(t *testing.T) {
	restore := stubSysctl(t, func(string) string { return "1024 65535" },
		func(string, string) error { return os.ErrPermission })
	defer restore()

	var log []string
	MigrateAfterUpdate(collect(&log))
	if !strings.Contains(strings.Join(log, "\n"), "could not restore") {
		t.Errorf("a migration failed without saying so: %q", log)
	}
}

// stubSysctl points the kernel reads and writes at the test, and the config
// pass at an empty directory. The second half matters as much as the first:
// without it, running this suite on a server would migrate that server's own
// tunnel configs.
func stubSysctl(t *testing.T, read func(string) string, write func(string, string) error) func() {
	t.Helper()
	oldRead, oldWrite, oldRoot := readSysctl, writeSysctl, configRoot
	readSysctl, writeSysctl, configRoot = read, write, t.TempDir()
	return func() { readSysctl, writeSysctl, configRoot = oldRead, oldWrite, oldRoot }
}

func statMode(t *testing.T, path string) os.FileMode {
	t.Helper()
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return st.Mode().Perm()
}

// Removing a key from files that already carry it.
//
// spoof_dst_ip described something that could not exist — an IPv4 destination
// has one home, the IP header, and that has to hold the address the packet is
// routed to. It was offered, validated, written and read into a field nothing
// downstream looked at.
//
// The migration strips it. What it must not do is reformat the file around it:
// these are edited by hand, and rewriting one wholesale to delete a line is a
// worse intrusion than the line.

func TestDroppingAKeyLeavesTheRestOfTheFileAlone(t *testing.T) {
	body := []byte(`[l3]
# The carrier this tunnel uses. Do not change without changing the other end.
carrier = "spoof"

spoof_src_ip   = "203.0.113.10"
spoof_dst_ip   = "192.0.2.9"
spoof_peer_ip  = "198.51.100.7"
`)
	out, removed := dropTOMLKey(body, "spoof_dst_ip")
	if !removed {
		t.Fatal("the key was not removed")
	}
	got := string(out)
	if strings.Contains(got, "spoof_dst_ip") {
		t.Fatalf("the key survived:\n%s", got)
	}
	// Everything else, exactly as it was — the comment, the spacing, the order.
	for _, want := range []string{
		"# The carrier this tunnel uses. Do not change without changing the other end.",
		`carrier = "spoof"`,
		`spoof_src_ip   = "203.0.113.10"`,
		`spoof_peer_ip  = "198.51.100.7"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the migration disturbed the rest of the file; %q is gone:\n%s", want, got)
		}
	}
}

func TestDroppingAKeyThatIsNotThereChangesNothing(t *testing.T) {
	body := []byte("[l3]\ncarrier = \"udp\"\n")
	out, removed := dropTOMLKey(body, "spoof_dst_ip")
	if removed {
		t.Error("reported removing a key that was not there")
	}
	if string(out) != string(body) {
		t.Error("the file changed anyway")
	}
}

// A key whose name is a prefix of another must not take the other with it.
func TestDroppingAKeyDoesNotMatchALongerName(t *testing.T) {
	body := []byte("spoof_dst_ip_extra = \"keep me\"\nspoof_dst_ip = \"go\"\n")
	out, removed := dropTOMLKey(body, "spoof_dst_ip")
	if !removed {
		t.Fatal("the key was not removed")
	}
	if !strings.Contains(string(out), "spoof_dst_ip_extra") {
		t.Errorf("a longer key that merely starts the same way was removed too:\n%s", out)
	}
}
