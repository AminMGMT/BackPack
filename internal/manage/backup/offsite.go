package backup

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/backpack/backpack/internal/app"
)

// Getting a backup off the machine it describes.
//
// The backups are thorough and they are written to /var/backups on the server
// they were taken from. The case they exist for — the machine died, the
// provider suspended it, somebody rebuilt it — is precisely the case where they
// are on the machine that is gone. A backup that only survives what it was
// never needed for is not a backup.
//
// # Why a command rather than an S3 client
//
// The obvious design is an S3 uploader, and it is the wrong one here. It means
// credentials on the box, a dependency, and a provider list that is never
// complete — and the operators this is for already have something that works:
// rsync over SSH to a machine they own, rclone to whatever they use, scp to a
// laptop. A command is the interface they already have, and it is the one that
// does not need a release to support a new destination.
//
// So the setting is a command line with one placeholder. Everything else is
// theirs.
//
//	offsite = "rclone copy {} remote:backpack/"
//	offsite = "scp {} backup@10.0.0.9:/srv/backpack/"
//	offsite = "restic backup {}"
//
// # What it deliberately does not do
//
// It does not run as a shell. The command is split on whitespace and executed
// directly, so a destination from a config file cannot become a pipeline, a
// redirect, or `; rm -rf /`. An operator who genuinely wants a shell writes
// `sh -c '...'` and has made that decision visibly.

// offsiteFile holds the command. Beside the other per-machine settings rather
// than in a tunnel's config: it describes this server, not a tunnel.
var offsiteFile = filepath.Join(app.ConfigDir, "offsite")

// offsiteTimeout bounds one upload. Long enough for a large archive over a slow
// link, short enough that a destination which accepts a connection and then
// hangs does not wedge the weekly pass for ever.
const offsiteTimeout = 30 * time.Minute

// offsiteTimeoutFor is the value actually used, so a test can wind it down
// without waiting half an hour to prove the bound exists.
var offsiteTimeoutFor = offsiteTimeout

// OffsiteCommand returns the configured command, or "" when there is none.
func OffsiteCommand() string {
	b, err := os.ReadFile(offsiteFile)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// SetOffsiteCommand records the command. An empty string turns it off.
func SetOffsiteCommand(cmd string) error {
	cmd = strings.TrimSpace(cmd)
	if cmd == "" {
		if err := os.Remove(offsiteFile); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	if _, err := splitOffsite(cmd, "/tmp/example.tar.gz"); err != nil {
		return err
	}
	// 0600: a destination usually names a host and a path, and sometimes a
	// user. Nothing else on the machine needs to read it.
	return os.WriteFile(offsiteFile, []byte(cmd+"\n"), 0o600)
}

// splitOffsite turns the configured line into an argv with the archive path
// substituted, and refuses anything that could not work.
//
// Split on whitespace and run directly — never through a shell. A destination
// read from a file must not be able to become a pipeline or a second command,
// and an operator who wants one writes `sh -c '...'` themselves, which is a
// decision anybody reading the config can see.
func splitOffsite(cmd, archive string) ([]string, error) {
	parts := strings.Fields(cmd)
	if len(parts) == 0 {
		return nil, fmt.Errorf("the offsite command is empty")
	}
	if !strings.Contains(cmd, "{}") {
		return nil, fmt.Errorf("the offsite command has no {} — that is where the " +
			"backup file's path goes, and without it the command would run against " +
			"nothing")
	}
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		out = append(out, strings.ReplaceAll(p, "{}", archive))
	}
	return out, nil
}

// SendOffsite copies one archive to wherever the operator configured.
//
// It reports what happened rather than only whether it worked: a failure here
// is discovered weeks later, when the machine is gone and somebody looks for
// the copy that was supposed to be elsewhere.
func SendOffsite(archive string) error {
	cmd := OffsiteCommand()
	if cmd == "" {
		return nil // not configured, which is not a failure
	}
	argv, err := splitOffsite(cmd, archive)
	if err != nil {
		return err
	}
	if _, err := os.Stat(archive); err != nil {
		return fmt.Errorf("the backup to send does not exist: %w", err)
	}

	// Bounded. A destination that accepts a connection and then hangs — a
	// half-open SSH session, a proxy that never answers — would otherwise wedge
	// the weekly pass for ever, and the weekly pass is a goroutine in the
	// monitor service, so it would take the backups with it silently.
	ctx, cancel := context.WithTimeout(context.Background(), offsiteTimeoutFor)
	defer cancel()

	c := exec.CommandContext(ctx, argv[0], argv[1:]...)
	out, err := c.CombinedOutput()
	if ctx.Err() != nil {
		return fmt.Errorf("%s gave up after %s — the destination accepted the "+
			"connection and then stopped responding", argv[0], offsiteTimeoutFor)
	}
	if err != nil {
		detail := strings.TrimSpace(string(out))
		if detail == "" {
			detail = err.Error()
		}
		return fmt.Errorf("%s: %s", argv[0], lastLine(detail))
	}
	return nil
}

// lastLine is the part of a command's output worth repeating. Tools of this
// kind put the reason on the final line and progress on every line before it.
func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

// NewestBackup returns the most recent backup file on this machine.
func NewestBackup() (string, error) {
	entries, err := os.ReadDir(app.BackupDir)
	if err != nil {
		return "", fmt.Errorf("no backups yet: %w", err)
	}
	var newest string
	var at time.Time
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".tar.gz") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.ModTime().After(at) {
			at, newest = info.ModTime(), filepath.Join(app.BackupDir, e.Name())
		}
	}
	if newest == "" {
		return "", fmt.Errorf("no backups in %s yet", app.BackupDir)
	}
	return newest, nil
}
