//go:build linux

package engine

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/backpack/backpack/config"
)

// TestDirectNetNSAcceptance is opt-in because it needs root, network
// namespaces and working iptables targets. The child roles run this same test
// binary inside the isolated namespaces created by the parent.
func TestDirectNetNSAcceptance(t *testing.T) {
	switch os.Getenv("BP_NETNS_ROLE") {
	case "target":
		netNSTarget(t)
		return
	case "engine":
		netNSEngine(t)
		return
	case "client", "client-fail":
		netNSClient(t, os.Getenv("BP_NETNS_ROLE") == "client-fail")
		return
	}
	if os.Getenv("BACKPACK_NETNS_TEST") != "1" {
		t.Skip("set BACKPACK_NETNS_TEST=1 to run the root network-namespace acceptance test")
	}
	if os.Geteuid() != 0 {
		t.Skip("network-namespace acceptance test requires root")
	}
	for _, binary := range []string{"ip", "iptables", "iptables-save", "iptables-restore"} {
		if _, err := exec.LookPath(binary); err != nil {
			t.Skipf("%s is unavailable", binary)
		}
	}

	suffix := fmt.Sprintf("%d", os.Getpid())
	clientNS, ingressNS, targetNS := "bpc"+suffix, "bpi"+suffix, "bpt"+suffix
	for _, ns := range []string{clientNS, ingressNS, targetNS} {
		if out, err := exec.Command("ip", "netns", "add", ns).CombinedOutput(); err != nil {
			t.Fatalf("create namespace %s: %v: %s", ns, err, out)
		}
	}
	t.Cleanup(func() {
		for _, ns := range []string{clientNS, ingressNS, targetNS} {
			_ = exec.Command("ip", "netns", "del", ns).Run()
		}
	})

	commands := [][]string{
		{"link", "add", "bpc0", "type", "veth", "peer", "name", "bpi0"},
		{"link", "set", "bpc0", "netns", clientNS}, {"link", "set", "bpi0", "netns", ingressNS},
		{"link", "add", "bpi1", "type", "veth", "peer", "name", "bpt0"},
		{"link", "set", "bpi1", "netns", ingressNS}, {"link", "set", "bpt0", "netns", targetNS},
	}
	for _, args := range commands {
		netNSIP(t, args...)
	}
	for _, setup := range []struct {
		ns   string
		args [][]string
	}{
		{clientNS, [][]string{{"link", "set", "lo", "up"}, {"addr", "add", "10.210.1.2/24", "dev", "bpc0"}, {"-6", "addr", "add", "fd42:210:1::2/64", "dev", "bpc0", "nodad"}, {"link", "set", "bpc0", "up"}, {"route", "add", "default", "via", "10.210.1.1"}, {"-6", "route", "add", "default", "via", "fd42:210:1::1"}}},
		{ingressNS, [][]string{{"link", "set", "lo", "up"}, {"addr", "add", "10.210.1.1/24", "dev", "bpi0"}, {"-6", "addr", "add", "fd42:210:1::1/64", "dev", "bpi0", "nodad"}, {"addr", "add", "10.210.2.1/24", "dev", "bpi1"}, {"-6", "addr", "add", "fd42:210:2::1/64", "dev", "bpi1", "nodad"}, {"link", "set", "bpi0", "up"}, {"link", "set", "bpi1", "up"}}},
		{targetNS, [][]string{{"link", "set", "lo", "up"}, {"addr", "add", "10.210.2.2/24", "dev", "bpt0"}, {"-6", "addr", "add", "fd42:210:2::2/64", "dev", "bpt0", "nodad"}, {"link", "set", "bpt0", "up"}, {"route", "add", "default", "via", "10.210.2.1"}, {"-6", "route", "add", "default", "via", "fd42:210:2::1"}}},
	} {
		for _, args := range setup.args {
			netNSIP(t, append([]string{"-n", setup.ns}, args...)...)
		}
	}

	tmp := t.TempDir()
	targetReady, engineReady := filepath.Join(tmp, "target.ready"), filepath.Join(tmp, "engine.ready")
	configPath := filepath.Join(tmp, "direct.toml")
	body := `engine = "iptables"
[forward]
[[forward.mappings]]
listen_address = "10.210.1.1"
listen_ports = "10000-10001"
target_address = "10.210.2.2"
target_ports = "20000-20001"
protocols = ["tcp", "udp"]
[[forward.mappings]]
listen_address = "10.210.1.1"
listen_ports = "10010"
target_address = "10.210.2.2"
target_ports = "20010"
protocols = ["tcp", "udp"]
[[forward.mappings]]
listen_address = "fd42:210:1::1"
listen_ports = "10000-10001"
target_address = "fd42:210:2::2"
target_ports = "20000-20001"
protocols = ["tcp", "udp"]
[[forward.mappings]]
listen_address = "fd42:210:1::1"
listen_ports = "10010"
target_address = "fd42:210:2::2"
target_ports = "20010"
protocols = ["tcp", "udp"]
`
	if err := os.WriteFile(configPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	target := netNSChild(targetNS, exe, "target", "BP_NETNS_READY="+targetReady)
	if err := target.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { terminateChild(target) })
	waitReady(t, targetReady)

	startEngine := func() *exec.Cmd {
		_ = os.Remove(engineReady)
		cmd := netNSChild(ingressNS, exe, "engine", "BP_NETNS_READY="+engineReady, "BP_NETNS_CONFIG="+configPath)
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { terminateChild(cmd) })
		waitReady(t, engineReady)
		return cmd
	}
	engineCmd := startEngine()
	runNetNSClient(t, clientNS, exe, "client")
	terminateChild(engineCmd)
	runNetNSClient(t, clientNS, exe, "client-fail")
	engineCmd = startEngine()
	runNetNSClient(t, clientNS, exe, "client")
}

func netNSIP(t *testing.T, args ...string) {
	t.Helper()
	if out, err := exec.Command("ip", args...).CombinedOutput(); err != nil {
		t.Fatalf("ip %s: %v: %s", strings.Join(args, " "), err, out)
	}
}

func netNSChild(ns, exe, role string, extra ...string) *exec.Cmd {
	cmd := exec.Command("ip", "netns", "exec", ns, exe, "-test.run=^TestDirectNetNSAcceptance$", "-test.v")
	cmd.Env = append(os.Environ(), append([]string{"BP_NETNS_ROLE=" + role}, extra...)...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return cmd
}

func waitReady(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", path)
}

func terminateChild(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(8 * time.Second):
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		<-done
	}
}

func netNSTarget(t *testing.T) {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()
	for _, address := range []string{"10.210.2.2", "fd42:210:2::2"} {
		for _, port := range []int{20000, 20001, 20010} {
			listen := net.JoinHostPort(address, fmt.Sprint(port))
			ln, err := net.Listen("tcp", listen)
			if err != nil {
				t.Fatal(err)
			}
			defer ln.Close()
			go func() {
				for {
					conn, err := ln.Accept()
					if err != nil {
						return
					}
					_, _ = conn.Write([]byte(conn.RemoteAddr().String()))
					_ = conn.Close()
				}
			}()
			pc, err := net.ListenPacket("udp", listen)
			if err != nil {
				t.Fatal(err)
			}
			defer pc.Close()
			go func() {
				buf := make([]byte, 64)
				for {
					_, peer, err := pc.ReadFrom(buf)
					if err != nil {
						return
					}
					_, _ = pc.WriteTo([]byte(peer.String()), peer)
				}
			}()
		}
	}
	_ = os.WriteFile(os.Getenv("BP_NETNS_READY"), []byte("ready"), 0o600)
	<-ctx.Done()
}

func netNSEngine(t *testing.T) {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()
	cfg, err := config.LoadFile(os.Getenv("BP_NETNS_CONFIG"))
	if err != nil {
		t.Fatal(err)
	}
	provider, err := Resolve(cfg)
	if err != nil {
		t.Fatal(err)
	}
	errCh := make(chan error, 1)
	request := Request{ConfigPath: os.Getenv("BP_NETNS_CONFIG"), Config: cfg}
	go func() { errCh <- provider.Run(ctx, request) }()
	deadline := time.Now().Add(12 * time.Second)
	ready := false
	for time.Now().Before(deadline) {
		health, _ := provider.Health(context.Background(), request)
		if health.Ready {
			_ = os.WriteFile(os.Getenv("BP_NETNS_READY"), []byte("ready"), 0o600)
			ready = true
			break
		}
		select {
		case err := <-errCh:
			t.Fatalf("engine startup: %v", err)
		default:
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !ready {
		cancel()
		t.Fatalf("engine did not reach desired-state readiness")
	}
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		if err := <-errCh; err != nil {
			t.Fatal(err)
		}
	}
}

func netNSClient(t *testing.T, wantFailure bool) {
	for _, tc := range []struct{ network, host, expected string }{
		{"tcp", "10.210.1.1", "10.210.2.1"}, {"udp", "10.210.1.1", "10.210.2.1"},
		{"tcp6", "fd42:210:1::1", "fd42:210:2::1"}, {"udp6", "fd42:210:1::1", "fd42:210:2::1"},
	} {
		for _, port := range []int{10000, 10001, 10010} {
			conn, err := net.DialTimeout(tc.network, net.JoinHostPort(tc.host, fmt.Sprint(port)), 800*time.Millisecond)
			if err != nil {
				if wantFailure {
					continue
				}
				t.Fatalf("%s connect: %v", tc.network, err)
			}
			_ = conn.SetDeadline(time.Now().Add(800 * time.Millisecond))
			if strings.HasPrefix(tc.network, "udp") {
				_, _ = conn.Write([]byte("probe"))
			}
			buf := make([]byte, 128)
			n, readErr := conn.Read(buf)
			_ = conn.Close()
			if wantFailure {
				if readErr == nil {
					t.Fatalf("%s port %d still forwarded after stop", tc.network, port)
				}
				continue
			}
			if readErr != nil || !strings.Contains(string(buf[:n]), tc.expected) {
				t.Fatalf("%s port %d did not preserve offset/MASQUERADE source: %q, %v", tc.network, port, buf[:n], readErr)
			}
		}
	}
}

func runNetNSClient(t *testing.T, ns, exe, role string) {
	t.Helper()
	cmd := netNSChild(ns, exe, role)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s: %v\n%s", role, err, out)
	}
}
