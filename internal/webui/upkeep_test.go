package webui

import "testing"

// A backup is named from the list, never by path: the handlers that restore,
// test, delete and serve one must not be steerable at another file.
func TestBackupNamesCannotLeaveTheFolder(t *testing.T) {
	for _, name := range []string{"", "../webui.json", "/etc/passwd", "a/b.tar.gz", "x.json", "..", "backups/x.tar.gz"} {
		if _, ok := backupPath(name); ok {
			t.Errorf("backupPath(%q) was accepted", name)
		}
	}
}
