package manage

import (
	"strings"
	"testing"
)

// An uploaded archive has to be this machine's: one for another architecture
// installs a binary that will not execute, and the name is the only place
// that shows before it is too late.
func TestAnUploadedArchiveMustBeThisMachines(t *testing.T) {
	dir := t.TempDir()
	was := localUpdateDirsFn
	localUpdateDirsFn = func() []string { return []string{dir} }
	t.Cleanup(func() { localUpdateDirsFn = was })

	for _, name := range []string{"backpack_linux_other.tar.gz", "evil.sh", "../" + LocalAssetName()} {
		if _, err := SaveLocalUpdate(name, strings.NewReader("x"), nil); err == nil {
			t.Errorf("SaveLocalUpdate(%q) was accepted", name)
		}
	}
	if _, err := SaveLocalUpdate(LocalAssetName(), strings.NewReader(""), nil); err == nil {
		t.Error("an empty archive was accepted")
	}
}
