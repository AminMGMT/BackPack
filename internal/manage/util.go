package manage

import "os"

// fileExists reports whether a path exists.
func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
