package manage

import (
	"bytes"
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/backpack/backpack/config"
)

// Vision runs stealth or stealth+ inside its helper: the Iran end's setup
// link carries the mode, the kharej built from it matches, and the tunnel
// carries traffic through the real Xray helpers. A kharej that did not match
// would never come up (TestEncryptionMustAgree in e2e shows the engine side).
func TestVisionCarriesEachEncryptionInsideIt(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and runs the engine; skipped under -short")
	}
	root := os.Getenv("BP_CONNTEST_HELPERS_ROOT")
	if os.Getenv("BP_CONNTEST_HELPERS") != "1" && root == "" {
		t.Skip("needs the pinned Xray helper: set BP_CONNTEST_HELPERS=1 or BP_CONNTEST_HELPERS_ROOT")
	}
	bin := filepath.Join(t.TempDir(), "backpack")
	build := exec.Command("go", "build", "-o", bin, "../..")
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		t.Fatalf("building the engine: %v", err)
	}
	defer func(prev func() (string, error), soak int, reverse, direct []string, enc string) {
		connTestBinary, connTestSoak, connTestReverse, connTestDirect, ctVisionEncryption = prev, soak, reverse, direct, enc
	}(connTestBinary, connTestSoak, connTestReverse, connTestDirect, ctVisionEncryption)
	connTestBinary = func() (string, error) { return bin, nil }
	if root != "" {
		previous := connTestHelperBinary
		connTestHelperBinary = func(tool string) (string, error) {
			path := filepath.Join(root, tool)
			if _, err := os.Stat(path); err != nil {
				return "", err
			}
			return path, nil
		}
		defer func() { connTestHelperBinary = previous }()
	}
	connTestReverse, connTestDirect, connTestSoak = []string{"vision"}, nil, 4
	speedStagesUpTo16(t)

	cover := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	cover.EnableHTTP2 = true
	cover.TLS = &tls.Config{MinVersion: tls.VersionTLS13}
	cover.StartTLS()
	defer cover.Close()
	_, coverPort, _ := net.SplitHostPort(cover.Listener.Addr().String())

	for _, mode := range []string{config.EncStealth, config.EncStealthPlus} {
		t.Run(mode, func(t *testing.T) {
			ctVisionEncryption = mode
			iran, link, err := StartConnTestIran(ConnTestOptions{Host: "127.0.0.1", VisionTarget: "localhost:" + coverPort})
			if err != nil {
				t.Fatal(err)
			}
			defer iran.Close()
			for _, c := range iran.cases {
				if c.skip != "" {
					t.Fatalf("vision skipped: %s", c.skip)
				}
				var cfg config.Config
				if _, err := toml.DecodeFile(filepath.Join(iran.dir, c.name+".toml"), &cfg); err != nil {
					t.Fatal(err)
				}
				if got := cfg.Server.ManagedEncryption(); got != mode {
					t.Fatalf("the Iran end runs %q inside Vision, want %q", got, mode)
				}
				if c.link.Enc != mode {
					t.Fatalf("the setup link carries %q, want %q", c.link.Enc, mode)
				}
			}

			ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
			defer cancel()
			var out bytes.Buffer
			var kharejErr error
			var wg sync.WaitGroup
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, _, kharejErr = RunConnTestKharej(ctx, link, &out, nil)
			}()
			select {
			case <-iran.Joined():
			case <-ctx.Done():
				t.Fatal("the kharej never checked in")
			}
			results := iran.Run(ctx, nil)
			wg.Wait()
			if kharejErr != nil {
				t.Fatalf("the kharej side: %v\n%s", kharejErr, out.String())
			}
			if len(results) != 1 || results[0].Status != ctOK {
				t.Fatalf("Vision with %s: %+v\n%s", mode, results, out.String())
			}
		})
	}
}
