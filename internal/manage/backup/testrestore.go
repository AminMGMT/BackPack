package backup

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/backpack/backpack/internal/app"
)

// Proving a backup would restore, without restoring it.
//
// The recovery procedure is written down and it has never been run. That is the
// ordinary state of a disaster-recovery plan and it is the reason they fail:
// the first time anybody exercises it is the day it has to work, on a machine
// that is already gone, with whatever the backup turned out not to contain.
//
// This runs the real staging path — the same tar reader, the same name
// validation, the same size limits, the same sidecar parsing — against a
// throwaway directory, and then throws the result away. Everything the real
// restore would refuse, this refuses. Everything it would silently do without,
// this reports.
//
// It deliberately does not commit. A drill that changes the machine is not a
// drill anybody runs on a working server, and one nobody runs proves nothing.

// RestoreReport is what a backup turned out to contain.
type RestoreReport struct {
	Path string

	Tunnels        []string
	WebUIConfig    bool
	TelegramConfig bool
	Certificates   int
	Files          int

	// FleetSealed is true when the archive carries node credentials that are
	// sealed with a key the archive does not contain — deliberately, so a
	// stolen backup does not carry a fleet with it. Restoring onto a *different*
	// machine therefore returns the server list without the ability to reach
	// any of them, which is the failure this whole drill exists to surface
	// before it matters.
	FleetSealed bool

	// Warnings are things a real restore would carry on past.
	Warnings []string
}

// Summary renders the report the way an operator reads it.
func (r RestoreReport) Summary() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s would restore:\n", r.Path)
	fmt.Fprintf(&b, "  %d files\n", r.Files)
	if len(r.Tunnels) > 0 {
		fmt.Fprintf(&b, "  %d tunnel(s): %s\n", len(r.Tunnels), strings.Join(r.Tunnels, ", "))
	} else {
		b.WriteString("  no tunnels — this backup would restore a machine with none\n")
	}
	if r.Certificates > 0 {
		fmt.Fprintf(&b, "  %d TLS certificate file(s)\n", r.Certificates)
	}
	if r.WebUIConfig {
		b.WriteString("  the web panel's settings and password\n")
	}
	if r.TelegramConfig {
		b.WriteString("  the Telegram bot and its admins\n")
	}
	for _, w := range r.Warnings {
		fmt.Fprintf(&b, "  ! %s\n", w)
	}
	return b.String()
}

// TestRestore stages a backup into a temporary directory and reports what it
// holds. Nothing on the machine is touched.
func TestRestore(path string) (RestoreReport, error) {
	rep := RestoreReport{Path: path}

	f, err := os.Open(path)
	if err != nil {
		return rep, fmt.Errorf("cannot open the backup: %w", err)
	}
	defer f.Close()

	// A real directory, in the system temp area, removed on the way out. The
	// staging code writes files and checks modes, so a fake filesystem would be
	// testing something other than what a restore does.
	stage, err := os.MkdirTemp("", "backpack-restore-drill-")
	if err != nil {
		return rep, fmt.Errorf("cannot make a scratch directory: %w", err)
	}
	defer os.RemoveAll(stage)

	// Seeded from an empty directory rather than the live one: the question is
	// what the *archive* holds, and seeding from the machine would report this
	// machine's tunnels as though the backup contained them.
	empty, err := os.MkdirTemp("", "backpack-restore-empty-")
	if err != nil {
		return rep, fmt.Errorf("cannot make a scratch directory: %w", err)
	}
	defer os.RemoveAll(empty)

	// The raw file: stageRestore decompresses for itself, which is what makes
	// this the same path a real restore takes rather than a similar one.
	contents, err := stageRestore(f, empty, stage)
	if err != nil {
		return rep, fmt.Errorf("this backup would be refused: %w", err)
	}

	rep.Files = contents.Files
	rep.WebUIConfig = contents.WebUIConfig
	rep.TelegramConfig = contents.TelegramConfig
	rep.Warnings = append(rep.Warnings, contents.Warnings...)

	entries, err := os.ReadDir(stage)
	if err != nil {
		return rep, fmt.Errorf("cannot read the staged copy: %w", err)
	}
	for _, e := range entries {
		name := e.Name()
		switch {
		case e.IsDir() && name == "certs":
			if certs, err := os.ReadDir(stage + "/certs"); err == nil {
				rep.Certificates = len(certs)
			}
		case strings.HasSuffix(name, ".toml"):
			rep.Tunnels = append(rep.Tunnels, strings.TrimSuffix(name, ".toml"))
		case name == "nodes.json":
			if b, err := os.ReadFile(stage + "/nodes.json"); err == nil &&
				strings.Contains(string(b), "password_sealed") {
				rep.FleetSealed = true
			}
		}
	}
	sort.Strings(rep.Tunnels)

	if rep.FleetSealed {
		rep.Warnings = append(rep.Warnings,
			"this backup carries managed servers whose passwords are sealed with a key "+
				"that is NOT in the archive. Restoring onto a different machine gives you "+
				"the server list and no way to reach any of them — take the fleet key now, "+
				"from Backup & Restore → Show the fleet key, and keep it somewhere else")
	}
	if !contents.SawTunnelConfig {
		rep.Warnings = append(rep.Warnings,
			"there is no tunnel configuration in this backup at all, so restoring it "+
				"would produce a machine with no tunnels")
	}
	return rep, nil
}

// DefaultBackupDir is where the drill looks when the operator does not name a
// file. Exported so the menu and the panel agree.
const DefaultBackupDir = app.BackupDir
