package manage

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/backpack/backpack/internal/app"
)

// What an update forces onto a server that an older version set up.
//
// A fix that changes only what a new install writes reaches nobody. The servers
// that need it most are the ones that have been running longest, and those are
// exactly the ones nothing rewrites: the config was written once at setup, the
// sysctl file was written once by Optimize, and every release after that
// replaces the binary and leaves both precisely as they were.
//
// ip_local_port_range is the case that made this necessary. The engine widened
// it to "1024 65535" on every start, which put every service port on the box
// inside the range the kernel hands out as the source port of an outgoing
// connection — and a port held that way cannot be bound by the service that
// owns it. Removing that line fixes it for nobody who already has it: the value
// is live in a running kernel, and the only thing that ever mentioned it was a
// health check somebody had to think to run.
//
// So an update corrects the machine as well as the binary, and it does so
// without asking. That is deliberate, and it is the whole point — these are
// corrections to values this program itself wrote and later decided were wrong,
// and a setting the operator never chose is not one they should have to choose
// again. It is also the limit. A migration that would overwrite a real decision
// does not belong here, and each one below says in its own words how it tells
// the two apart.
//
// Everything here is idempotent and runs on every update rather than once. A
// marker file recording "already applied" would be wrong in both directions: a
// config restored from an older backup would be skipped because the marker was
// set by a different file, and a value an operator forced back by hand would
// stay forced back. Running every time and writing only when something is
// actually different is simpler and more correct than remembering.
//
// This runs after the new binary is in place and before the tunnels are
// restarted, so they come back up on the corrected settings rather than inherit
// the old ones until something else happens to bounce them.

// configFile is one tunnel's configuration, as a migration sees it. A migration
// may change the body, the mode, or both; the pass writes back only what
// actually changed.
type configFile struct {
	Name string
	Path string
	Body []byte
	Mode os.FileMode
}

// configMigration corrects one thing about a tunnel's configuration file.
type configMigration struct {
	id string
	// apply returns a one-line description of what it changed, or "" when this
	// file was already right. Nothing is written for an empty description, which
	// is what makes running the whole list on every update free.
	apply func(f *configFile) string
}

// systemMigration corrects machine state that no config file holds.
type systemMigration struct {
	id string
	// apply returns a one-line description of what it changed, or "" when there
	// was nothing to do.
	apply func() string
}

// configMigrations run against every tunnel config on the machine, in order.
var configMigrations = []configMigration{
	{
		// A tunnel config holds its token, and on tcp, udp and kcp that token is
		// the whole of what authorises a connection. Every other file this
		// program writes that holds a secret is 0600 — telegram.json,
		// webui.json, the node registry, the TLS key, the backups — and the
		// configs alone were 0644, readable by every account on the box.
		//
		// Forced rather than offered: 0644 is not a choice anybody made, it is
		// what five call sites happened to pass. Nothing needs to read these
		// files but root, which is what the engine, the panel and the monitor
		// all run as.
		id: "config-mode-0600",
		apply: func(f *configFile) string {
			if f.Mode.Perm() == app.TunnelConfigMode {
				return ""
			}
			was := f.Mode.Perm()
			f.Mode = app.TunnelConfigMode
			return fmt.Sprintf("permissions %#o → %#o — it holds this tunnel's token",
				was, app.TunnelConfigMode)
		},
	},
}

// systemMigrations run once each, against the machine.
var systemMigrations = []systemMigration{
	{
		// The other half of removing the widening from the engine. See the note
		// at the top of this file, and the one over ip_local_port_range in
		// internal/optimize/optimize.go for what the wide range costs.
		//
		// Only the exact value the engine wrote is corrected. A machine whose
		// range is wide in some other way was widened by somebody with a reason,
		// and this is not the place to argue with them; a machine reading
		// "1024 65535" is reading back a string this program put there.
		//
		// Live only. Nothing persists this — the engine set it with `sysctl -w`
		// on every start and it did not survive a reboot either, so there is no
		// file to correct. A machine that ran Optimize has the right value in
		// /etc/sysctl.d/99-backpack.conf already, and ApplyUpdate re-applies that
		// file just before this runs.
		id: "ephemeral-port-range",
		apply: func() string {
			const wrote = "1024 65535"
			cur := readSysctl("net.ipv4.ip_local_port_range")
			if strings.Join(strings.Fields(cur), " ") != wrote {
				return ""
			}
			want := fmt.Sprintf("%d %d", ephemeralDefaultLow, ephemeralDefaultHigh)
			if err := writeSysctl("net.ipv4.ip_local_port_range", want); err != nil {
				return "could not restore the ephemeral port range: " + err.Error()
			}
			return "ephemeral port range " + wrote + " → " + want +
				" — older versions widened it on every tunnel start, which let an " +
				"outgoing connection take a port a service needed"
		},
	},
}

// Indirected so a test can drive a migration without reading or writing the
// kernel and the configs of the machine it runs on — the same reason
// optimize.sysctlFile and confHistRoot are vars. app.ConfigDir is a constant,
// and a test that ran this pass against it would rewrite the configs of a real
// server whenever the suite was run on one.
var (
	configRoot  = app.ConfigDir
	readSysctl  = sysctlValue
	writeSysctl = func(key, value string) error {
		return exec.Command("sysctl", "-w", key+"="+value).Run()
	}
)

// MigrateAfterUpdate applies every correction this version forces onto a server
// an older one set up, and reports what it changed through logf.
//
// Best effort item by item. Nothing here may fail an update: by the time this
// runs the new binary is installed and working, and refusing at this point would
// leave a server with a good binary and a failed update over a permission bit.
// What it must not do is fail silently, so everything it changes — and
// everything it tried and could not — is logged.
func MigrateAfterUpdate(logf func(string)) {
	n := migrateConfigsIn(configRoot, logf)
	for _, m := range systemMigrations {
		if note := m.apply(); note != "" {
			logf("  " + note)
			n++
		}
	}
	if n == 0 {
		logf("Nothing to migrate — this server already matches " + app.Version + ".")
	}
}

// migrateConfigsIn runs the config migrations over every tunnel config in dir
// and returns how many files it changed.
//
// The directory is a parameter rather than read from configRoot so the pass can
// be exercised on its own, a file at a time, without the system migrations.
func migrateConfigsIn(dir string, logf func(string)) int {
	paths, _ := filepath.Glob(filepath.Join(dir, "*.toml"))
	sort.Strings(paths)

	changed := 0
	for _, path := range paths {
		st, err := os.Stat(path)
		if err != nil || st.IsDir() {
			continue
		}
		body, err := os.ReadFile(path)
		if err != nil {
			logf("  could not read " + filepath.Base(path) + ": " + err.Error())
			continue
		}

		f := configFile{
			Name: strings.TrimSuffix(filepath.Base(path), ".toml"),
			Path: path,
			Body: body,
			Mode: st.Mode(),
		}
		var notes []string
		for _, m := range configMigrations {
			if note := m.apply(&f); note != "" {
				notes = append(notes, note)
			}
		}
		if len(notes) == 0 {
			continue
		}

		bodyChanged := !bytes.Equal(body, f.Body)
		if bodyChanged {
			// File what this replaced before replacing it, so the change shows
			// up in the panel's Undo list like any other edit — an update that
			// rewrites a config and leaves no way back is worse than one that
			// does not rewrite it at all.
			recordConfigChange(f.Name, body, "migrated by the update to "+app.Version)
			if err := app.WriteFileAtomic(path, f.Body, f.Mode.Perm()); err != nil {
				logf("  could not migrate " + f.Name + ": " + err.Error())
				continue
			}
		} else if f.Mode.Perm() != st.Mode().Perm() {
			if err := os.Chmod(path, f.Mode.Perm()); err != nil {
				logf("  could not set permissions on " + f.Name + ": " + err.Error())
				continue
			}
		}

		changed++
		for _, note := range notes {
			logf("  " + f.Name + ": " + note)
		}
	}
	return changed
}
