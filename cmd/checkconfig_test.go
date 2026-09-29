package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sirupsen/logrus"
)

// `backpack check` asks CheckConfigFile, so it has to give the answer Run
// would: accept what starts, and refuse what the engine refuses, for the
// engine's reason.
func TestCheckConfigFileGivesTheEnginesAnswer(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}

	good := write("good.toml", `[server]
bind_addr = "0.0.0.0:8443"
transport = "tcp"
token = "a-long-enough-token-for-a-test"
ports = ["443=127.0.0.1:2096"]
`)
	if err := CheckConfigFile(good); err != nil {
		t.Fatalf("a config the engine runs was refused: %v", err)
	}

	unknown := write("unknown.toml", `[server]
bind_addr = "0.0.0.0:8443"
transport = "carrier-pigeon"
token = "a-long-enough-token-for-a-test"
ports = ["443"]
`)
	if err := CheckConfigFile(unknown); err == nil || !strings.Contains(err.Error(), "carrier-pigeon") {
		t.Fatalf("an unknown transport was not refused by name: %v", err)
	}

	if err := CheckConfigFile(write("broken.toml", "[server\n")); err == nil {
		t.Fatal("an unparsable file was accepted")
	}
}

// check --json owns stdout: the engine's warnings during a check go to stderr,
// never ahead of the JSON.
func TestCheckConfigFileKeepsItsLogOffStdout(t *testing.T) {
	var stdout bytes.Buffer
	saved := logger.Out
	logger.SetOutput(&stdout) // stands in for the engine's usual stdout
	defer logger.SetOutput(saved)
	level := logger.GetLevel()
	logger.SetLevel(logrus.InfoLevel) // the suite runs the engine quiet
	defer logger.SetLevel(level)

	p := filepath.Join(t.TempDir(), "notoken.toml")
	// No token: the engine warns that the public default is in use.
	if err := os.WriteFile(p, []byte("[server]\nbind_addr = \"0.0.0.0:8443\"\ntransport = \"tcp\"\nports = [\"443\"]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_ = CheckConfigFile(p)
	if stdout.Len() != 0 {
		t.Fatalf("the check logged to stdout: %q", stdout.String())
	}
	if logger.Out != &stdout {
		t.Fatal("the check did not put the engine's log output back")
	}
}
