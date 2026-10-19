package tunnelspec

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
	"github.com/backpack/backpack/config"
	"github.com/backpack/backpack/internal/app"
)

func TestOrdinaryEditPreservesSkipOptzForBothRoles(t *testing.T) {
	if err := os.MkdirAll(app.ConfigDir, 0755); err != nil {
		t.Fatal(err)
	}
	for _, role := range []string{"server", "client"} {
		for _, skip := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/%t", role, skip), func(t *testing.T) {
				file, err := os.CreateTemp(app.ConfigDir, "skip-optz-edit-*.toml")
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { os.Remove(file.Name()) })
				addrKey := "bind_addr"
				if role == "client" {
					addrKey = "remote_addr"
				}
				_, err = fmt.Fprintf(file, "[%s]\n%s = %q\ntransport = %q\nskip_optz = %t\n", role, addrKey, "127.0.0.1:62001", "tcp", skip)
				file.Close()
				if err != nil {
					t.Fatal(err)
				}
				name := strings.TrimSuffix(filepath.Base(file.Name()), ".toml")
				s, err := Load(name)
				if err != nil {
					t.Fatal(err)
				}
				s.KeepAlive = 15
				var cfg config.Config
				if _, err := toml.Decode(s.Render(), &cfg); err != nil {
					t.Fatal(err)
				}
				got := cfg.Server.SkipOptz
				if role == "client" {
					got = cfg.Client.SkipOptz
				}
				if got != skip {
					t.Fatalf("ordinary edit changed skip_optz from %t to %t", skip, got)
				}
				if !skip && strings.Contains(s.Render(), "skip_optz") {
					t.Fatal("an unset option added a redundant config key")
				}
			})
		}
	}
}
