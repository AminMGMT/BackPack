package core

import "os"

// FileExists reports whether a path exists.
func FileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// OrDefault returns v, or def when v is empty. It reads a config field that is
// allowed to be unset and renders what the engine will actually do with it, so
// a screen never shows a blank where a real default applies.
func OrDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}
