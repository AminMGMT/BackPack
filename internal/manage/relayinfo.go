package manage

import (
	"crypto/subtle"
	"path/filepath"

	"github.com/BurntSushi/toml"
	"github.com/backpack/backpack/config"
	"github.com/backpack/backpack/internal/app"
)

// TunnelToken returns the shared secret token of a tunnel by name.
func TunnelToken(name string) string {
	var cfg config.Config
	if _, err := toml.DecodeFile(app.ConfigPath(name), &cfg); err != nil {
		return ""
	}
	if cfg.Server.BindAddr != "" {
		return cfg.Server.Token
	}
	return cfg.Client.Token
}

// TokenMatches reports whether any local tunnel is configured with the given
// token. Used to authenticate the built-in SOCKS5 proxy (the token is a shared
// secret between the two ends of a tunnel).
func TokenMatches(token string) bool {
	if token == "" {
		return false
	}
	matches, _ := filepath.Glob(app.ConfigDir + "/*.toml")
	for _, path := range matches {
		var cfg config.Config
		if _, err := toml.DecodeFile(path, &cfg); err != nil {
			continue
		}
		for _, t := range []string{cfg.Server.Token, cfg.Client.Token} {
			if t != "" && subtle.ConstantTimeCompare([]byte(t), []byte(token)) == 1 {
				return true
			}
		}
	}
	return false
}
