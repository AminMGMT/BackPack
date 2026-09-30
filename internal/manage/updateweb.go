package manage

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// Install From A File, fed from the browser.
//
// The menu finds an archive the operator scp'd into /root. The panel's
// operator has the file on their own computer instead, so it is uploaded to
// the same place under the same name, and from there it is the menu's path
// exactly: FindLocalUpdate, then ApplyLocalUpdate.

// maxUpdateUpload bounds an uploaded release archive. A release is a few tens
// of megabytes; this leaves room for growth and refuses anything absurd.
const maxUpdateUpload = 256 << 20

// SaveLocalUpdate writes an uploaded archive where FindLocalUpdate looks, under
// the one name it accepts, and an optional SHA256SUMS beside it. name is what
// the file was called on the operator's computer: an archive for another
// architecture is refused here, because installed it would not execute.
func SaveLocalUpdate(name string, archive io.Reader, sums io.Reader) (LocalUpdate, error) {
	want := LocalAssetName()
	if name != want {
		return LocalUpdate{}, fmt.Errorf("this server needs %s — %q is for another architecture or is not a release", want, filepath.Base(name))
	}
	var dir string
	for _, d := range localUpdateDirs() {
		if trustedUpdateDir(d) {
			dir = d
			break
		}
	}
	if dir == "" {
		return LocalUpdate{}, fmt.Errorf("none of %v is a directory only root can write to", localUpdateDirs())
	}
	if err := writeBounded(filepath.Join(dir, want), archive, maxUpdateUpload); err != nil {
		return LocalUpdate{}, err
	}
	sumsPath := filepath.Join(dir, "SHA256SUMS")
	if sums != nil {
		if err := writeBounded(sumsPath, sums, 1<<20); err != nil {
			return LocalUpdate{}, err
		}
	} else {
		// A list left from an earlier upload describes some other file and
		// would fail this one as corrupted.
		_ = os.Remove(sumsPath)
	}
	u, ok := FindLocalUpdate()
	if !ok {
		return LocalUpdate{}, fmt.Errorf("the archive was saved but could not be read back")
	}
	return u, nil
}

func writeBounded(path string, r io.Reader, max int64) error {
	tmp := path + ".part"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	n, err := io.Copy(f, io.LimitReader(r, max+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil && n > max {
		err = fmt.Errorf("the file is larger than %d MB", max>>20)
	}
	if err == nil && n == 0 {
		err = fmt.Errorf("the file is empty")
	}
	if err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}
