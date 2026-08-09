package manage

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// archiveMetadata captures settings that live outside ConfigDir. Its synthetic
// entry is parsed on restore but is never written into the configuration tree.
type archiveMetadata struct {
	Version          string `json:"version"`
	Created          string `json:"created"`
	AutoRefreshHours int    `json:"auto_refresh_hours"`
}

const backupMetaName = ".backpack-backup.json"

// writeBackupArchive closes every layer explicitly so a short write while tar
// or gzip is flushing cannot be reported as a successful backup.
func writeBackupArchive(w io.Writer, root string, meta archiveMetadata) error {
	gz := gzip.NewWriter(w)
	tw := tar.NewWriter(gz)

	metaJSON, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		_ = tw.Close()
		_ = gz.Close()
		return fmt.Errorf("encode backup metadata: %w", err)
	}
	if err = tw.WriteHeader(&tar.Header{
		Name: backupMetaName,
		Mode: 0600,
		Size: int64(len(metaJSON)),
	}); err != nil {
		_ = tw.Close()
		_ = gz.Close()
		return fmt.Errorf("write backup metadata header: %w", err)
	}
	if _, err = tw.Write(metaJSON); err != nil {
		_ = tw.Close()
		_ = gz.Close()
		return fmt.Errorf("write backup metadata: %w", err)
	}

	walkErr := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		if !info.IsDir() && !info.Mode().IsRegular() {
			return fmt.Errorf("unsupported file type in config directory: %s", path)
		}
		hdr, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		hdr.Name = filepath.ToSlash(rel)
		if info.IsDir() {
			hdr.Name += "/"
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(tw, f)
		closeErr := f.Close()
		if copyErr != nil {
			return copyErr
		}
		return closeErr
	})
	if err := tw.Close(); walkErr == nil && err != nil {
		walkErr = fmt.Errorf("finalize tar archive: %w", err)
	}
	if err := gz.Close(); walkErr == nil && err != nil {
		walkErr = fmt.Errorf("finalize gzip stream: %w", err)
	}
	return walkErr
}

func publishBackupFile(dir, root string, meta archiveMetadata, retention int) (string, error) {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", err
	}
	f, err := os.CreateTemp(dir, ".backpack-backup-*.tmp")
	if err != nil {
		return "", err
	}
	tempPath := f.Name()
	randomSuffix := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(tempPath), ".backpack-backup-"), ".tmp")
	name := fmt.Sprintf("backpack-backup-%s-%s.tar.gz", time.Now().Format("20060102-150405.000000000"), randomSuffix)
	path := filepath.Join(dir, name)
	keepTemp := true
	defer func() {
		if keepTemp {
			_ = os.Remove(tempPath)
		}
	}()
	if err := f.Chmod(0600); err != nil {
		_ = f.Close()
		return "", err
	}
	if err := writeBackupArchive(f, root, meta); err != nil {
		_ = f.Close()
		return "", err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return "", fmt.Errorf("sync backup archive: %w", err)
	}
	if err := f.Close(); err != nil {
		return "", fmt.Errorf("close backup archive: %w", err)
	}
	if err := os.Rename(tempPath, path); err != nil {
		return "", fmt.Errorf("publish backup archive: %w", err)
	}
	keepTemp = false
	pruneBackupFiles(dir, retention)
	return path, nil
}

func pruneBackupFiles(dir string, retention int) {
	matches, _ := filepath.Glob(filepath.Join(dir, "backpack-backup-*.tar.gz"))
	if len(matches) <= retention {
		return
	}
	sort.Sort(sort.Reverse(sort.StringSlice(matches)))
	for _, old := range matches[retention:] {
		_ = os.Remove(old)
	}
}
