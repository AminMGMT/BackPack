package manage

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/backpack/backpack/internal/app"
)

// Restoring is all-or-nothing.
//
// It used to extract straight into the live config directory, one entry at a
// time, as the archive was read. Every way an archive can turn out to be bad
// is found part-way through reading it — a truncated stream, a gzip checksum
// that only fails at the very end, a path that tries to escape, a read error
// on the upload — and by then some of the tunnels had been overwritten and
// some had not. The installation was left in a state that came from neither
// the backup nor from what was there before, with nothing to put it back.
//
// So the whole post-restore tree is now built in a staging directory first:
// the current configuration copied in, the archive laid over the top, every
// entry checked before it is written, and the compressed stream read through
// to its checksum. Only then does anything in the live directory change, and
// the change is one rename. If any part of that fails, the live directory has
// not been touched at all.

const (
	// Bounds on what an archive may expand to. A backup of this system is
	// tunnel configs, a little JSON and some certificates — kilobytes, and a
	// few megabytes at the very outside. These are far above any real backup
	// and exist so that a hostile or corrupt archive cannot fill the disk
	// before anyone finds out what it contains.
	maxRestoreEntries   = 10_000
	maxRestoreFileBytes = 64 << 20
	maxRestoreBytes     = 256 << 20
)

// restoreContents is what an archive turned out to hold, gathered while it is
// staged so that nothing has to be re-read after the commit.
type restoreContents struct {
	Files            int
	WebUIConfig      bool
	TelegramConfig   bool
	SawTunnelConfig  bool
	AutoRefreshHours int
}

// stageRestore builds the complete post-restore tree in stage: the current
// contents of configDir first, then everything the archive holds over the top.
//
// Seeding from the live directory is what keeps the merge behaviour restores
// have always had. A backup taken by an older version does not know about
// files a newer one added, and the commit replaces the directory wholesale —
// so without the seed, restoring an old archive would silently delete
// everything that came after it.
func stageRestore(r io.Reader, configDir, stage string) (restoreContents, error) {
	var contents restoreContents

	if err := seedStage(configDir, stage); err != nil {
		return contents, err
	}

	gz, err := gzip.NewReader(r)
	if err != nil {
		return contents, fmt.Errorf("not a valid backup archive: %w", err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	seen := make(map[string]bool)
	var total int64

	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return contents, fmt.Errorf("reading archive: %w", err)
		}

		// Sidecar: parse for out-of-tree settings, never write it to disk.
		if hdr.Name == backupMetaName {
			var m backupMeta
			if data, err := io.ReadAll(io.LimitReader(tr, maxRestoreFileBytes)); err == nil {
				_ = json.Unmarshal(data, &m)
				contents.AutoRefreshHours = m.AutoRefreshHours
			}
			continue
		}

		name, err := safeRestoreName(hdr.Name)
		if err != nil {
			return contents, err
		}
		if seen[name] {
			// The same path twice is either a broken archive or an attempt to
			// have the second write land somewhere the first one's checks
			// already passed. Neither is worth being clever about.
			return contents, fmt.Errorf("archive names %q more than once", hdr.Name)
		}
		seen[name] = true
		if len(seen) > maxRestoreEntries {
			return contents, fmt.Errorf("archive holds more than %d entries", maxRestoreEntries)
		}

		// install_path records where install.sh cloned the repo on THIS host.
		// The seed already carries the local one; skipping the archived entry
		// is what keeps a restore onto a new server from pointing the updater
		// at a directory that is not there.
		if installPath := filepath.Base(app.InstallPathFile); name == installPath && fileExists(filepath.Join(configDir, installPath)) {
			continue
		}

		target := filepath.Join(stage, name)
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0755); err != nil {
				return contents, err
			}

		case tar.TypeReg:
			if hdr.Size > maxRestoreFileBytes {
				return contents, fmt.Errorf("%s is %d bytes, over the %d-byte limit for one file", name, hdr.Size, int64(maxRestoreFileBytes))
			}
			total += hdr.Size
			if total > maxRestoreBytes {
				return contents, fmt.Errorf("archive expands to more than %d bytes", int64(maxRestoreBytes))
			}
			if err := writeStagedFile(target, tr, hdr); err != nil {
				return contents, err
			}
			contents.Files++
			noteRestoredFile(&contents, name)

		default:
			// Symlinks and hard links are the classic way an archive reaches
			// outside the directory it is being unpacked into: the link is
			// written first, a later entry writes "through" it, and the write
			// lands wherever the link points. Backups of this system contain
			// neither, so there is nothing to weigh here.
			return contents, fmt.Errorf("refusing %q: a backup holds only files and directories", hdr.Name)
		}
	}

	// tar stops at its own end-of-archive marker, which can come before the end
	// of the compressed stream. Reading the rest is what makes gzip check the
	// CRC and the length it recorded — the difference between an archive that
	// parsed and an archive that is intact. Doing it here, before anything is
	// committed, is the whole point: a truncated upload used to be discovered
	// only after it had been written over the live configuration.
	if _, err := io.Copy(io.Discard, gz); err != nil {
		return contents, fmt.Errorf("the archive is corrupt or truncated: %w", err)
	}

	return contents, nil
}

// writeStagedFile writes one archive entry into the staging tree.
func writeStagedFile(target string, r io.Reader, hdr *tar.Header) error {
	if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
		return err
	}
	mode := os.FileMode(hdr.Mode).Perm()
	if mode == 0 {
		mode = 0600
	}
	f, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	// Bounded even though the header was checked: the header states a size,
	// and a hostile archive is exactly the thing that would state one size and
	// deliver another.
	if _, err := io.Copy(f, io.LimitReader(r, maxRestoreFileBytes+1)); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	// OpenFile respects the umask, so say the mode again — a 0600 token file
	// that comes back 0644 is a restore that widens access to the tunnel.
	return os.Chmod(target, mode)
}

// noteRestoredFile records which of the files the caller asks about were in
// the archive.
func noteRestoredFile(contents *restoreContents, name string) {
	switch base := filepath.Base(name); {
	case base == filepath.Base(app.WebUIConfig):
		contents.WebUIConfig = true
	case base == filepath.Base(app.TelegramConfig):
		contents.TelegramConfig = true
	case strings.HasSuffix(base, ".toml"):
		contents.SawTunnelConfig = true
	}
}

// safeRestoreName turns an archive entry name into a path relative to the
// directory being restored into, or refuses it.
//
// The check is on the name itself rather than on where it happens to land,
// because the answer must not depend on what is already on disk. A backslash
// is refused outright: it is not a separator here, but it is one where these
// archives can be written and opened, and "a\..\..\etc" is a traversal that
// filepath.Clean on Linux happily leaves as one harmless-looking element.
func safeRestoreName(name string) (string, error) {
	refuse := func() (string, error) {
		return "", fmt.Errorf("refusing unsafe path in archive: %q", name)
	}
	if name == "" || strings.ContainsRune(name, '\\') || strings.ContainsRune(name, 0) {
		return refuse()
	}
	if filepath.IsAbs(name) || strings.HasPrefix(name, "/") || filepath.VolumeName(name) != "" {
		return refuse()
	}

	clean := filepath.Clean(filepath.FromSlash(name))
	if clean == "." || clean == ".." || filepath.IsAbs(clean) {
		return refuse()
	}
	for _, element := range strings.Split(clean, string(filepath.Separator)) {
		if element == ".." {
			return refuse()
		}
	}
	return clean, nil
}

// seedStage copies configDir into stage so the staging tree starts out as the
// installation that is there now.
func seedStage(configDir, stage string) error {
	return filepath.Walk(configDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(configDir, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		target := filepath.Join(stage, rel)

		switch {
		case info.IsDir():
			return os.MkdirAll(target, info.Mode().Perm())
		case info.Mode().IsRegular():
			return copyFileTo(path, target, info.Mode().Perm())
		default:
			// The commit replaces the directory, so anything not copied here
			// is gone afterwards. Rather than silently drop something the
			// backup side already refuses to archive, stop and name it while
			// the live directory is still untouched.
			return fmt.Errorf("cannot restore over %s: it is not a regular file (%s)", rel, info.Mode().Type())
		}
	})
}

// copyFileTo copies one file, mode and all.
func copyFileTo(source, target string, mode os.FileMode) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()

	if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
		return err
	}
	out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Chmod(target, mode)
}

// commitRestore puts the staged tree in place of the live one.
//
// The previous directory is moved aside rather than deleted, so that the
// second rename failing leaves something to put back. Two renames is as close
// to atomic as a directory swap gets: the window in which the config directory
// does not exist is between them, and the engine's reload watcher treats a
// path it cannot stat as "not changed yet" and waits, which is the behaviour
// this relies on.
func commitRestore(configDir, stage string) error {
	// The staging directory was created private; the live one is world-readable
	// and has to stay that way for anything that reads a config as a user.
	mode := os.FileMode(0755)
	if info, err := os.Stat(configDir); err == nil {
		mode = info.Mode().Perm()
	}
	if err := os.Chmod(stage, mode); err != nil {
		return err
	}

	previous := configDir + ".restore-previous"
	os.RemoveAll(previous)

	if err := os.Rename(configDir, previous); err != nil {
		return fmt.Errorf("could not move the current configuration aside, so nothing was changed: %w", err)
	}
	if err := os.Rename(stage, configDir); err != nil {
		if back := os.Rename(previous, configDir); back != nil {
			return fmt.Errorf("could not put the restored configuration in place (%v) and could not put the previous one back either; it is in %s: %w", err, previous, back)
		}
		return fmt.Errorf("could not put the restored configuration in place, so the previous one was left as it was: %w", err)
	}

	syncDir(filepath.Dir(configDir))
	os.RemoveAll(previous)
	return nil
}
