//go:build linux

package manage

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// The direct tunnel, built the way an operator builds it now: the Iran side by
// its wizard, the kharej from the setup link it printed — by the same path
// `backpack link apply` takes — and both run for real on a TUN between two
// network namespaces, with bytes required to cross.
//
// It needs what TestL3CarriersOverARealTUN needs:
//
//	BP_L3_LIVE=1 go test ./internal/manage -run TestADirectKharejFromTheSetupLinkCarriesTraffic -v
func TestADirectKharejFromTheSetupLinkCarriesTraffic(t *testing.T) {
	if os.Getenv("BP_L3_LIVE") == "" {
		t.Skip("set BP_L3_LIVE=1 on a host that allows unprivileged user namespaces to run this")
	}
	bin := os.Getenv("BACKPACK_CURRENT_BINARY")
	if bin == "" {
		bin = filepath.Join(t.TempDir(), "backpack")
		build := exec.Command("go", "build", "-o", bin, "../..")
		build.Stderr = os.Stderr
		if err := build.Run(); err != nil {
			t.Fatalf("building the engine: %v", err)
		}
	}
	script, err := filepath.Abs(filepath.Join("..", "e2e", "testdata", "l3live.sh"))
	if err != nil {
		t.Fatal(err)
	}

	for _, carrier := range []string{"udp", "quic", "pck", "sni", "xdi", "spoof"} {
		t.Run(carrier, func(t *testing.T) {
			dir := t.TempDir()
			off := false
			iran := l3Spec{
				Name: "l3-iran-9000", Side: sideIran, Carrier: carrier, Encap: "gre",
				Addr: "10.99.0.2:9000", Token: randomToken(64),
				Iface: "bpi0", LocalIP: "10.200.0.1/30", PeerIP: "10.200.0.2",
				MTU: 1300, AutoMTU: &off,
			}
			findL3Preset("").apply(&iran)
			if carrier == "sni" {
				iran.SNIDomain = "www.example.com"
			}
			if carrier == "spoof" {
				iran.Spoof.SpoofSrcPool = []string{"198.51.100.66"}
			}

			// The link the Iran summary prints, with a restart schedule on it —
			// and, for spoof, this server's real address, as the wizard adds it.
			host := ""
			if carrier == "spoof" {
				host = "10.99.0.1"
			}
			raw := pendingShareLinkFrom(iran, host, linkExtras{restartHours: 6, restartMinute: 17})
			if raw == "" {
				t.Fatal("the Iran side produced no setup link")
			}
			link, err := DecodeShareLink(raw)
			if err != nil {
				t.Fatal(err)
			}
			if link.RestartHours != 6 || link.RestartMinute != 17 {
				t.Errorf("the link carries restart %d:%d, want 6:17", link.RestartHours, link.RestartMinute)
			}

			// The kharej, as ApplySetupLink builds it (applyDirectLink), up to
			// the file it writes.
			form := MirrorForPeer(link)
			if carrier == "spoof" {
				if form.SpoofPeerIP != "10.99.0.1" {
					t.Fatalf("the spoof link did not carry the Iran server's address: %q", form.SpoofPeerIP)
				}
				// The kharej's own answer to askSpoofLocal: it forges too.
				form.Spoof.SrcIPs = "198.51.100.77"
			}
			spec, err := form.ToNewDirectTunnel().spec()
			if err != nil {
				t.Fatalf("the kharej form from the link was refused: %v", err)
			}
			_, kharejBody, err := directBodyFromSpec(spec)
			if err != nil {
				t.Fatal(err)
			}

			iranConf, kharejConf := filepath.Join(dir, "iran.toml"), filepath.Join(dir, "kharej.toml")
			if err := os.WriteFile(iranConf, []byte(iran.Render()), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(kharejConf, []byte(kharejBody), 0o600); err != nil {
				t.Fatal(err)
			}

			ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
			defer cancel()
			cmd := exec.CommandContext(ctx, "unshare", "--map-auto", "--map-root-user", "--net", "--mount", "--fork", "--",
				script, carrier, "1")
			cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
			cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
			outPath := filepath.Join(dir, "script.out")
			out, err := os.Create(outPath)
			if err != nil {
				t.Fatal(err)
			}
			cmd.Stdout, cmd.Stderr = out, out
			cmd.Env = append(os.Environ(), "BIN="+bin, "W="+filepath.Join(dir, "run"),
				"IRAN_CONF="+iranConf, "KHAREJ_CONF="+kharejConf)
			runErr := cmd.Run()
			out.Close()
			b, _ := os.ReadFile(outPath)
			if len(b) > 16<<10 {
				b = b[len(b)-16<<10:]
			}
			last := string(b)
			if i := strings.LastIndex(last, "RESULT "); i >= 0 {
				last = last[i:]
			}
			if runErr != nil || !strings.HasPrefix(last, "RESULT OK") {
				t.Fatalf("%v\n%s\n--- iran config\n%s\n--- kharej config\n%s", runErr, b, iran.Render(), kharejBody)
			}
			t.Log(strings.TrimSpace(strings.TrimPrefix(last, "RESULT OK")))
		})
	}
}
