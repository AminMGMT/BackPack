//go:build !unix

package manage

import "os"

// ownedByRootOrMe has no owner to read off this platform, which runs no
// updates anyway.
func ownedByRootOrMe(os.FileInfo) bool { return false }
