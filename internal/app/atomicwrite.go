package app

import (
	"os"
	"path/filepath"
)

// WriteFileAtomic writes data to path by writing a temporary file in the same
// directory and renaming it into place.
//
// Backpack runs as several processes — the CLI, the web panel, the monitor and
// one per tunnel — and they share these config files: the CLI writes them, the
// others read them on a timer. A plain os.WriteFile truncates the file before
// it writes, so a reader landing in that window sees an empty or partial file.
// For a config that means being read as "not configured", which for the monitor
// means an iteration with no bot and no alerts.
//
// Rename is atomic within a filesystem, so a reader sees either the whole old
// file or the whole new one, never a mixture.
func WriteFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+"-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	// Removes the temporary file on any failure; a no-op once renamed.
	defer os.Remove(name)

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	// Flushed to disk before the rename: without this a crash immediately after
	// can leave the file present but empty, which is exactly the state the
	// rename was meant to rule out.
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	// CreateTemp makes the file 0600; set the caller's mode explicitly so a
	// file that is meant to be readable is, and one holding a token is not.
	if err := os.Chmod(name, perm); err != nil {
		return err
	}
	return os.Rename(name, path)
}
