//go:build unix

package manage

import (
	"os"
	"syscall"
)

// ownedByRootOrMe reports whether a directory belongs to root or to this
// process's user — the only two accounts whose directory an archive may be
// taken from — and, when its group may write to it, whether that group is
// root's or this user's.
func ownedByRootOrMe(fi os.FileInfo) bool {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return false
	}
	if st.Uid != 0 && int(st.Uid) != os.Geteuid() {
		return false
	}
	if fi.Mode().Perm()&0o020 != 0 && st.Gid != 0 && int(st.Gid) != os.Getegid() {
		return false
	}
	return true
}
