package transport

import (
	"context"
	"errors"
	"net"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"testing"
	"time"
)

// Every retry in the control-channel dialer has to pause.
//
// The loop dials, and on any failure starts again. Two of its error paths —
// the token write and the read deadline that follows it — returned to the top
// with no wait at all. They sit after a successful dial, so they are reached
// exactly when the far end accepts a connection and then drops it: a route
// being filtered mid-handshake, a stateful firewall closing a half-open
// connection, or the server's own tunnel service restarting.
//
// In that state the loop spun as fast as the kernel would open sockets. The
// backoff exists to stop precisely that; its own doc note says the fixed
// drumbeat it replaced "also looks like a scan to anything watching", and these
// paths were faster than the drumbeat.
//
// A behavioural test would need a server that accepts and resets on cue for
// each of six transports. The invariant is structural and reads directly off
// the source, so it is checked there.
func TestEveryDialerRetryBacksOff(t *testing.T) {
	// The loop body, from the backoff being created to the end of the function.
	dialer := regexp.MustCompile(`(?s)bo := newBackoff\(.*?\n}`)

	for _, name := range []string{"tcp.go", "tcpmux.go", "kcp.go", "udp.go", "ws.go", "wsmux.go"} {
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("cannot read %s: %v", name, err)
		}
		body := dialer.FindString(string(src))
		if body == "" {
			t.Errorf("%s: no backoff-driven dialer loop found", name)
			continue
		}

		lines := strings.Split(body, "\n")
		for i, line := range lines {
			if strings.TrimSpace(line) != "continue" {
				continue
			}
			// Look back over the few statements that make up this error path.
			var preceding string
			for j := i - 1; j >= 0 && j > i-8; j-- {
				preceding = lines[j] + "\n" + preceding
				if strings.Contains(lines[j], "if ") || strings.Contains(lines[j], "} else") {
					break
				}
			}
			if !strings.Contains(preceding, "bo.Wait(") {
				t.Errorf("%s: a retry near %q returns to the top without backing off — "+
					"if the far end accepts and drops, this loop spins",
					name, strings.TrimSpace(lines[max(0, i-3)]))
			}
		}
	}
}

func TestKCPClientDNSStopsWithItsDialAttempt(t *testing.T) {
	if mode := os.Getenv("BACKPACK_KCP_DNS_AUDIT"); mode != "" {
		parts := strings.Split(mode, ":")
		started, release := make(chan struct{}, 2), make(chan struct{})
		defer close(release)
		net.DefaultResolver = &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
			select {
			case started <- struct{}{}:
			default:
			}
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-release:
				return nil, errors.New("resolver released")
			}
		}}
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		cfg := &KcpConfig{Token: "dns-cancellation", DialTimeOut: 3 * time.Second,
			MTU: 1350, Interval: 20, SndWnd: 128, RcvWnd: 128}
		cfg.UsePck, cfg.UseICMP = parts[0] == "pck", parts[0] == "icmp"
		if parts[1] == "timeout" {
			cfg.DialTimeOut = 80 * time.Millisecond
		}
		client := NewKcpClient(ctx, cfg, silentLogger())
		done := make(chan error, 1)
		go func() {
			conn, err := client.dial("stalled-kcp-dns.invalid:443")
			if conn != nil {
				conn.Close()
			}
			done <- err
		}()
		select {
		case <-started:
		case err := <-done:
			t.Fatalf("dial exited before DNS lookup: %v", err)
		case <-time.After(time.Second):
			t.Fatal("DNS lookup never started")
		}
		if parts[1] == "cancel" {
			cancel()
		}
		select {
		case err := <-done:
			want := context.Canceled
			if parts[1] == "timeout" {
				want = context.DeadlineExceeded
			}
			if !errors.Is(err, want) {
				t.Fatalf("DNS returned %v, want %v", err, want)
			}
		case <-time.After(400 * time.Millisecond):
			// The subprocess exits immediately after this failure; the parent
			// never changes its own resolver or retains a stalled dial worker.
			t.Fatal("KCP DNS ignored client cancellation or configured dial timeout")
		}
		return
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, carrier := range []string{"udp", "pck", "icmp"} {
		for _, kind := range []string{"cancel", "timeout"} {
			t.Run(carrier+"/"+kind, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
				defer cancel()
				cmd := exec.CommandContext(ctx, executable, "-test.run=^TestKCPClientDNSStopsWithItsDialAttempt$", "-test.timeout=6s")
				cmd.Env = append(os.Environ(), "BACKPACK_KCP_DNS_AUDIT="+carrier+":"+kind)
				if out, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("isolated %s %s: %v\n%s", carrier, kind, err, out)
				}
			})
		}
	}
}
