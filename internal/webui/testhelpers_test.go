package webui

// Helpers only the tests use. They lived in the package itself, where nothing
// but the tests called them.

// Save persists a complete config (0600, root only). The panel's own writers
// use UpdateConfig, which keeps fields owned by other callers.
func Save(c Config) error {
	return withConfigLock(func() error { return saveConfig(c) })
}

// password reads the current password from disk, as a login does.
func (s *server) password() string {
	return Load().Password
}
