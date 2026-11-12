//go:build linux

package e2e

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// The l3 carriers on a real TUN, between two network namespaces.
//
// Everything else in this package runs the reverse engine on loopback, which
// needs no privilege. The l3 tunnel cannot be run that way: it creates TUN
// devices, and pck, sni, xdi and spoof write raw packets. Between releases it
// went untested on a real TUN more than once — so this exists, and runs where
// an unprivileged user and network namespace can be made:
//
//	BP_L3_LIVE=1 go test ./internal/e2e -run TestL3CarriersOverARealTUN -v
//
// On Ubuntu that needs kernel.apparmor_restrict_unprivileged_userns=0 (it
// resets on reboot); without it the namespace is made but has no capabilities,
// and every carrier fails to open. The script it drives is testdata/l3live.sh.
// With BACKPACK_PREV_BINARY set, xdi also runs across versions, and pck is run
// against the previous release to check that the two cannot talk and say so.
//
// pck is paqet's engine since v2.0.0 and needs a binary linked with libpcap
// (see CONTRIBUTING.md): run this with CGO_ENABLED=1, CGO_CFLAGS and CGO_LDFLAGS
// set, as CI does. It has no TUN, so its cases check forwarded ports — TCP, UDP
// and a 5 MB copy — instead of ping, and every file carries the TUN keys an old
// pck file had, because a migrated tunnel's file does.
func TestL3CarriersOverARealTUN(t *testing.T) {
	if os.Getenv("BP_L3_LIVE") == "" {
		t.Skip("set BP_L3_LIVE=1 on a host that allows unprivileged user namespaces to run this")
	}
	for _, tool := range []string{"unshare", "ip", "nc", "bc", "tc", "tcpdump", "iptables", "python3"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Fatalf("%s is needed and not installed", tool)
		}
	}
	bin := currentBinary(t)
	script, err := filepath.Abs(filepath.Join("testdata", "l3live.sh"))
	if err != nil {
		t.Fatal(err)
	}

	cases := []l3Case{
		{"pck", "pck", nil},
		{"pck preset balance", "pck", []string{"PRESET=balance"}},
		{"pck preset aggressive", "pck", []string{"PRESET=aggressive"}},
		{"sni", "sni", nil},
		{"xdi", "xdi", nil},
		{"spoof", "spoof", nil},
		// Kernel GRE: no TUN and no carrier socket — the kernel's own link, over
		// the path's IPv4 and its IPv6.
		{"gre", "gre", nil},
		{"ip6gre", "ip6gre", nil},
		// The other end killed and started again: a GRE link has no session to
		// lose, so traffic is back the moment its link is.
		{"gre listener restart", "gre", []string{"RESTART=1"}},
		// A listener that dies without a word, and comes back with no memory of
		// the session: the dialler has to notice and handshake again.
		{"pck listener restart", "pck", []string{"RESTART=1"}},
		{"xdi listener restart", "xdi", []string{"RESTART=1"}},
		// The path stops passing the dialling end's flow, as middleboxes on
		// these routes do to a long-lived one: the tunnel has to leave that flow
		// behind by itself — a restart that came back on the same ports did not.
		{"pck flow blocked on the path", "pck", []string{"BLOCKFLOW=1", "PCKCONNS=1"}},
		// A path that loses and delays: slower, but every byte arrives.
		{"pck lossy path", "pck", []string{"NETEM=delay 20ms loss 1%"}},
		{"sni lossy path", "sni", []string{"NETEM=delay 20ms loss 1%"}},
		{"xdi lossy path", "xdi", []string{"NETEM=delay 20ms loss 1%"}},
	}
	// With the previous release at hand, the two ends are also run as two
	// versions, both ways round: an l3 tunnel's ends are updated one at a time
	// like any other, and its handshake is where a release changes the wire.
	if prev := os.Getenv(prevBinaryEnv); prev != "" {
		for _, carrier := range []string{"xdi"} {
			cases = append(cases,
				l3Case{carrier + " old listener, new dialler", carrier, []string{"BIN_LISTEN=" + prev}},
				l3Case{carrier + " new listener, old dialler", carrier, []string{"BIN_LISTEN=" + bin, "BIN=" + prev}})
		}
		// The new PCK is a different wire from the old one, so these must NOT
		// connect — and must say why instead of sitting silent.
		cases = append(cases,
			l3Case{"pck old listener, new dialler", "pck", []string{"BIN_LISTEN=" + prev, "EXPECT_OLD_PEER=1"}},
			l3Case{"pck new listener, old dialler", "pck", []string{"BIN_LISTEN=" + bin, "BIN=" + prev, "EXPECT_OLD_PEER=1"}})
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// The kernel loads ip_gre and ip6_gre for the first link of their
			// type, but may refuse to from inside an unprivileged namespace.
			// Said as a skip with the reason, not reported as the engine failing.
			if module := greModuleFor(c.carrier); module != "" && !moduleLoaded(module) {
				t.Skipf("the %s module is not loaded on this host, and a user namespace may not "+
					"load it: load it as root (modprobe %s) to test %s", module, module, c.carrier)
			}
			// Bounded in time, and its output goes to a file: a script that hung
			// once with its output held in this process's memory grew it to
			// 11 GB and the kernel killed the whole session.
			ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
			defer cancel()
			cmd := exec.CommandContext(ctx, "unshare", "--map-auto", "--map-root-user", "--net", "--mount", "--fork", "--",
				script, c.carrier)
			cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
			cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
			dir := t.TempDir()
			outFile, err := os.Create(filepath.Join(dir, "script.out"))
			if err != nil {
				t.Fatal(err)
			}
			cmd.Stdout, cmd.Stderr = outFile, outFile
			// c.env comes last, so a case can put another build at either end.
			cmd.Env = append(os.Environ(), append([]string{"BIN=" + bin, "W=" + filepath.Join(dir, "run")}, c.env...)...)
			err = cmd.Run()
			outFile.Close()
			out := tailOf(filepath.Join(dir, "script.out"), 16<<10)
			last := string(out)
			if i := strings.LastIndex(last, "RESULT "); i >= 0 {
				last = last[i:]
			}
			if err != nil || !strings.HasPrefix(last, "RESULT OK") {
				t.Fatalf("%s: %v\n%s", c.carrier, err, out)
			}
			t.Log(strings.TrimSpace(strings.TrimPrefix(last, "RESULT OK")))
		})
	}
}

// l3Case is one run of testdata/l3live.sh: a carrier, its path count, and the
// environment that varies the run.
type l3Case struct {
	name, carrier string
	env           []string
}

// tailOf is at most the last n bytes of a file.
func tailOf(path string, n int64) []byte {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	if fi, err := f.Stat(); err == nil && fi.Size() > n {
		_, _ = f.Seek(-n, io.SeekEnd)
	}
	b, _ := io.ReadAll(io.LimitReader(f, n))
	return b
}

// greModuleFor is the kernel module a GRE carrier's link needs, or "".
func greModuleFor(carrier string) string {
	switch carrier {
	case "gre":
		return "ip_gre"
	case "ip6gre":
		return "ip6_gre"
	}
	return ""
}

// moduleLoaded reports whether a kernel module is loaded, or built in.
//
// A module the kernel would load on demand is tried first, from a namespace of
// our own, exactly as the engine would: the kernel may well load it there, and
// a skip for a module it would have loaded is a test that never runs.
func moduleLoaded(module string) bool {
	if _, err := os.Stat("/sys/module/" + module); err == nil {
		return true
	}
	kind := map[string]string{"ip_gre": "gre", "ip6_gre": "ip6gre"}[module]
	local, remote := "198.51.100.1", "198.51.100.2"
	if kind == "ip6gre" {
		local, remote = "2001:db8::1", "2001:db8::2"
	}
	_ = exec.Command("unshare", "--map-root-user", "--net", "--",
		"ip", "link", "add", "bpprobe", "type", kind, "local", local, "remote", remote).Run()
	_, err := os.Stat("/sys/module/" + module)
	return err == nil
}
