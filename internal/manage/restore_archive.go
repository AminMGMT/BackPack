package manage

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
)

const (
	restoreMetadataName = ".backpack-backup.json"
	maxRestoreEntries   = 10_000
	maxRestoreFileSize  = 64 << 20
	maxRestoreTotalSize = 512 << 20
	maxRestoreMetaSize  = 64 << 10
)

type restoreMetadata struct {
	Version          string `json:"version"`
	Created          string `json:"created"`
	AutoRefreshHours int    `json:"auto_refresh_hours"`
}

type restoreArchiveResult struct {
	Files            int
	WebUIConfig      bool
	TelegramConfig   bool
	AutoRefreshHours int
	HasMetadata      bool
	SawConfig        bool
}

func restoreArchive(r io.Reader, configDir, installPathName, webUIName, telegramName string) (restoreArchiveResult, error) {
	var result restoreArchiveResult
	gz, err := gzip.NewReader(r)
	if err != nil {
		return result, fmt.Errorf("not a valid backup archive: %w", err)
	}
	gz.Multistream(false)

	configDir, err = filepath.Abs(configDir)
	if err != nil {
		_ = gz.Close()
		return result, err
	}
	parent := filepath.Dir(configDir)
	if err := os.MkdirAll(parent, 0755); err != nil {
		_ = gz.Close()
		return result, err
	}
	stage, err := os.MkdirTemp(parent, ".backpack-restore-stage-*")
	if err != nil {
		_ = gz.Close()
		return result, err
	}
	keepStage := true
	defer func() {
		if keepStage {
			_ = os.RemoveAll(stage)
		}
	}()

	if err := seedRestoreStage(configDir, stage); err != nil {
		_ = gz.Close()
		return result, fmt.Errorf("prepare restore transaction: %w", err)
	}
	preserveInstallPath := fileExistsAt(filepath.Join(configDir, installPathName))
	tr := tar.NewReader(gz)
	seen := make(map[string]struct{})
	entries := 0
	var totalSize int64

	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			_ = gz.Close()
			return result, fmt.Errorf("reading archive: %w", err)
		}
		entries++
		if entries > maxRestoreEntries {
			_ = gz.Close()
			return result, fmt.Errorf("backup contains more than %d entries", maxRestoreEntries)
		}
		clean, err := safeRestorePath(hdr.Name)
		if err != nil {
			_ = gz.Close()
			return result, err
		}
		key := filepath.ToSlash(clean)
		if _, exists := seen[key]; exists {
			_ = gz.Close()
			return result, fmt.Errorf("duplicate path in backup: %q", hdr.Name)
		}
		seen[key] = struct{}{}

		if hdr.Size < 0 {
			_ = gz.Close()
			return result, fmt.Errorf("negative size for backup entry %q", hdr.Name)
		}
		if key == restoreMetadataName {
			if hdr.Typeflag != tar.TypeReg && hdr.Typeflag != tar.TypeRegA {
				_ = gz.Close()
				return result, fmt.Errorf("backup metadata is not a regular file")
			}
			if hdr.Size > maxRestoreMetaSize {
				_ = gz.Close()
				return result, fmt.Errorf("backup metadata is too large")
			}
			data, err := io.ReadAll(tr)
			if err != nil {
				_ = gz.Close()
				return result, fmt.Errorf("read backup metadata: %w", err)
			}
			var meta restoreMetadata
			if err := json.Unmarshal(data, &meta); err != nil {
				_ = gz.Close()
				return result, fmt.Errorf("invalid backup metadata: %w", err)
			}
			if meta.AutoRefreshHours < 0 || meta.AutoRefreshHours > 24*365 {
				_ = gz.Close()
				return result, fmt.Errorf("invalid auto-refresh interval in backup: %d", meta.AutoRefreshHours)
			}
			result.AutoRefreshHours = meta.AutoRefreshHours
			result.HasMetadata = true
			continue
		}

		switch hdr.Typeflag {
		case tar.TypeDir:
			if hdr.Size != 0 {
				_ = gz.Close()
				return result, fmt.Errorf("directory entry %q has file contents", hdr.Name)
			}
			target := filepath.Join(stage, clean)
			if err := replaceWithDirectory(target, archiveMode(hdr.Mode, 0755)); err != nil {
				_ = gz.Close()
				return result, fmt.Errorf("restore directory %q: %w", hdr.Name, err)
			}
		case tar.TypeReg, tar.TypeRegA:
			if hdr.Size > maxRestoreFileSize || totalSize > maxRestoreTotalSize-hdr.Size {
				_ = gz.Close()
				return result, fmt.Errorf("backup contents exceed restore size limits")
			}
			totalSize += hdr.Size
			if key == filepath.ToSlash(installPathName) && preserveInstallPath {
				continue
			}
			target := filepath.Join(stage, clean)
			if err := writeRestoredFile(target, tr, hdr.Size, archiveMode(hdr.Mode, 0600)); err != nil {
				_ = gz.Close()
				return result, fmt.Errorf("restore file %q: %w", hdr.Name, err)
			}
			result.Files++
			base := filepath.Base(clean)
			switch {
			case base == webUIName:
				result.WebUIConfig = true
			case base == telegramName:
				result.TelegramConfig = true
			case strings.HasSuffix(base, ".toml"):
				result.SawConfig = true
			}
		default:
			_ = gz.Close()
			return result, fmt.Errorf("unsupported entry type %d for %q", hdr.Typeflag, hdr.Name)
		}
	}

	// tar reaches its own end marker before gzip reaches its checksum. Draining
	// the gzip reader ensures a truncated or corrupted upload cannot commit.
	if _, err := io.Copy(io.Discard, gz); err != nil {
		_ = gz.Close()
		return result, fmt.Errorf("verify backup checksum: %w", err)
	}
	if err := gz.Close(); err != nil {
		return result, fmt.Errorf("close backup archive: %w", err)
	}
	if err := commitRestoreStage(stage, configDir); err != nil {
		return result, err
	}
	keepStage = false
	return result, nil
}

func safeRestorePath(name string) (string, error) {
	if name == "" || strings.Contains(name, "\\") {
		return "", fmt.Errorf("refusing unsafe path in backup: %q", name)
	}
	clean := path.Clean(name)
	if clean == "." || clean == ".." || path.IsAbs(clean) || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("refusing unsafe path in backup: %q", name)
	}
	native := filepath.FromSlash(clean)
	if filepath.IsAbs(native) || filepath.VolumeName(native) != "" {
		return "", fmt.Errorf("refusing unsafe path in backup: %q", name)
	}
	return native, nil
}

func archiveMode(raw int64, fallback os.FileMode) os.FileMode {
	mode := os.FileMode(raw).Perm()
	if mode == 0 {
		return fallback
	}
	return mode
}

func replaceWithDirectory(target string, mode os.FileMode) error {
	if info, err := os.Lstat(target); err == nil && !info.IsDir() {
		if err := os.Remove(target); err != nil {
			return err
		}
	} else if err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := os.MkdirAll(target, mode); err != nil {
		return err
	}
	return os.Chmod(target, mode)
}

func writeRestoredFile(target string, src io.Reader, size int64, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
		return err
	}
	if info, err := os.Lstat(target); err == nil && !info.Mode().IsRegular() {
		if info.IsDir() {
			if err := os.RemoveAll(target); err != nil {
				return err
			}
		} else if err := os.Remove(target); err != nil {
			return err
		}
	} else if err != nil && !os.IsNotExist(err) {
		return err
	}
	f, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	if err := f.Chmod(mode); err != nil {
		_ = f.Close()
		return err
	}
	written, copyErr := io.Copy(f, src)
	if copyErr == nil && written != size {
		copyErr = io.ErrUnexpectedEOF
	}
	if copyErr == nil {
		copyErr = f.Sync()
	}
	closeErr := f.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}

func seedRestoreStage(configDir, stage string) error {
	info, err := os.Stat(configDir)
	if os.IsNotExist(err) {
		return os.Chmod(stage, 0755)
	}
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("configuration path is not a directory")
	}
	if err := os.Chmod(stage, info.Mode().Perm()); err != nil {
		return err
	}
	return filepath.Walk(configDir, func(source string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(configDir, source)
		if err != nil || rel == "." {
			return err
		}
		target := filepath.Join(stage, rel)
		switch {
		case info.IsDir():
			if err := os.MkdirAll(target, info.Mode().Perm()); err != nil {
				return err
			}
			return os.Chmod(target, info.Mode().Perm())
		case info.Mode().IsRegular():
			return copyRestoreSeedFile(source, target, info.Mode().Perm())
		default:
			return fmt.Errorf("unsupported existing config file type: %s", source)
		}
	})
}

func copyRestoreSeedFile(source, target string, mode os.FileMode) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
		_ = in.Close()
		return err
	}
	out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		_ = in.Close()
		return err
	}
	_, copyErr := io.Copy(out, in)
	if copyErr == nil {
		copyErr = out.Sync()
	}
	outCloseErr := out.Close()
	inCloseErr := in.Close()
	if copyErr != nil {
		return copyErr
	}
	if outCloseErr != nil {
		return outCloseErr
	}
	return inCloseErr
}

func commitRestoreStage(stage, configDir string) error {
	parent := filepath.Dir(configDir)
	oldMarker, err := os.CreateTemp(parent, ".backpack-restore-previous-*")
	if err != nil {
		return fmt.Errorf("prepare restore rollback: %w", err)
	}
	old := oldMarker.Name()
	if err := oldMarker.Close(); err != nil {
		_ = os.Remove(old)
		return fmt.Errorf("prepare restore rollback: %w", err)
	}
	if err := os.Remove(old); err != nil {
		return fmt.Errorf("prepare restore rollback: %w", err)
	}

	hadPrevious := false
	if _, err := os.Stat(configDir); err == nil {
		if err := os.Rename(configDir, old); err != nil {
			return fmt.Errorf("move current configuration aside: %w", err)
		}
		hadPrevious = true
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.Rename(stage, configDir); err != nil {
		if hadPrevious {
			if rollbackErr := os.Rename(old, configDir); rollbackErr != nil {
				return fmt.Errorf("commit restore: %v (rollback also failed: %v)", err, rollbackErr)
			}
		}
		return fmt.Errorf("commit restore: %w", err)
	}
	if hadPrevious {
		if err := os.RemoveAll(old); err != nil {
			return fmt.Errorf("restore committed but old configuration cleanup failed: %w", err)
		}
	}
	return nil
}

func fileExistsAt(name string) bool {
	_, err := os.Stat(name)
	return err == nil
}
