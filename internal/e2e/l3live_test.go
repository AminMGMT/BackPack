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
// Everything else in this package runs the reverse and direct engines on
// loopback, which needs no privilege. The l3 tunnel cannot be run that way: it
// creates TUN devices, and pck, sni, xdi and spoof write raw packets. Between
// releases it went untested on a real TUN more than once, and the default udp
// carrier's batching and GSO paths changed in that time — so this exists, and
// runs where an unprivileged user and network namespace can be made:
//
//	BP_L3_LIVE=1 go test ./internal/e2e -run TestL3CarriersOverARealTUN -v
//
// On Ubuntu that needs kernel.apparmor_restrict_unprivileged_userns=0 (it
// resets on reboot); without it the namespace is made but has no capabilities,
// and every carrier fails to open. The script it drives is testdata/l3live.sh.
// With BACKPACK_PREV_BINARY set, udp, quic and pck also run across versions.
func TestL3CarriersOverARealTUN(t *testing.T) {
	if os.Getenv("BP_L3_LIVE") == "" {
		t.Skip("set BP_L3_LIVE=1 on a host that allows unprivileged user namespaces to run this")
	}
	for _, tool := range []string{"unshare", "ip", "nc", "bc", "tc", "tcpdump", "iptables"} {
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
		{"udp", "udp", "1", nil},
		{"udp two paths", "udp", "2", nil},
		{"quic", "quic", "1", nil},
		{"pck", "pck", "1", nil},
		{"sni", "sni", "1", nil},
		{"xdi", "xdi", "1", nil},
		{"spoof", "spoof", "1", nil},
		// A listener that dies without a word, and comes back with no memory of
		// the session: the dialler has to notice and handshake again.
		{"udp listener restart", "udp", "1", []string{"RESTART=1"}},
		{"pck listener restart", "pck", "1", []string{"RESTART=1"}},
		// The path stops passing the dialling end's flow, as middleboxes on
		// these routes do to a long-lived one: the tunnel has to leave that flow
		// behind by itself — a restart that came back on the same ports did not.
		{"pck flow blocked on the path", "pck", "1", []string{"BLOCKFLOW=1"}},
		// A path that loses and delays: slower, but every byte arrives.
		{"udp lossy path", "udp", "1", []string{"NETEM=delay 20ms loss 1%"}},
		{"quic lossy path", "quic", "1", []string{"NETEM=delay 20ms loss 1%"}},
	}
	// With the previous release at hand, the two ends are also run as two
	// versions, both ways round: an l3 tunnel's ends are updated one at a time
	// like any other, and its handshake is where a release changes the wire.
	if prev := os.Getenv(prevBinaryEnv); prev != "" {
		for _, carrier := range []string{"udp", "quic", "pck"} {
			cases = append(cases,
				l3Case{carrier + " old listener, new dialler", carrier, "1", []string{"BIN_LISTEN=" + prev}},
				l3Case{carrier + " new listener, old dialler", carrier, "1", []string{"BIN_LISTEN=" + bin, "BIN=" + prev}})
		}
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// Bounded in time, and its output goes to a file: a script that hung
			// once with its output held in this process's memory grew it to
			// 11 GB and the kernel killed the whole session.
			ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
			defer cancel()
			cmd := exec.CommandContext(ctx, "unshare", "--map-auto", "--map-root-user", "--net", "--mount", "--fork", "--",
				script, c.carrier, c.paths)
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
				t.Fatalf("%s paths=%s: %v\n%s", c.carrier, c.paths, err, out)
			}
			t.Log(strings.TrimSpace(strings.TrimPrefix(last, "RESULT OK")))
		})
	}
}

// l3Case is one run of testdata/l3live.sh: a carrier, its path count, and the
// environment that varies the run.
type l3Case struct {
	name, carrier, paths string
	env                  []string
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
