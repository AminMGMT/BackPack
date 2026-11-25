package e2e

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The encryption a stream tunnel may wrap its connections in (encryption =
// ...). Each mode is checked the way an operator would meet it: the tunnel
// carries traffic with both ends set alike, it does not with one end left
// out, and the tunnel port shows a probe nothing of the plain handshake.

// encryptionCases is every transport × mode the engine offers.
var encryptionCases = []struct{ transport, mode string }{
	{"tcp", "stealth"},
	{"tcpmux", "stealth"},
	{"ws", "stealth"},
	{"wss", "stealth"},
	{"wsmux", "stealth"},
	{"wssmux", "stealth"},
	{"tcp", "stealth+"},
	{"tcpmux", "stealth+"},
	{"ws", "stealth+"},
	{"wss", "stealth+"},
	{"wsmux", "stealth+"},
	{"wssmux", "stealth+"},
	{"tcp", "scrap"},
	{"tcpmux", "scrap"},
}

func TestEncryptedTunnelsCarryTraffic(t *testing.T) {
	bin := currentBinary(t)
	cases := append([]struct{ transport, mode string }{}, encryptionCases...)
	// Splash, with an encryption and — on the TLS transports — without.
	for _, tr := range []string{"tcp", "tcpmux", "ws", "wsmux"} {
		cases = append(cases, struct{ transport, mode string }{tr, "stealth+splash"})
	}
	for _, tr := range []string{"wss", "wssmux"} {
		cases = append(cases, struct{ transport, mode string }{tr, "none+splash"})
	}
	for _, tc := range cases {
		t.Run(tc.transport+"/"+tc.mode, func(t *testing.T) {
			t.Parallel()
			enc := encLines(tc.mode)
			entry, _ := startEncryptedPair(t, bin, tc.transport, enc, enc)
			if err := awaitEcho(entry, 25*time.Second); err != nil {
				t.Fatalf("an encrypted tunnel never carried traffic: %v", err)
			}
			if err := concurrentEchoes(entry, 32, 64<<10); err != nil {
				t.Fatalf("an encrypted tunnel failed under load: %v", err)
			}
		})
	}
}

// One end without the setting is a tunnel that never comes up, rather than
// one that quietly runs in the clear.
func TestEncryptionMustAgree(t *testing.T) {
	bin := currentBinary(t)
	for _, tc := range encryptionCases {
		t.Run(tc.transport+"/"+tc.mode, func(t *testing.T) {
			t.Parallel()
			enc := encLines(tc.mode)
			entry, _ := startEncryptedPair(t, bin, tc.transport, enc, "")
			if err := awaitEcho(entry, 6*time.Second); err == nil {
				t.Fatal("an encrypted server carried traffic for a client without encryption")
			}
		})
	}
}

// A probe that speaks the plain handshake, or anything else, to an encrypted
// tunnel port is answered with nothing.
func TestEncryptedPortAnswersAProbeWithNothing(t *testing.T) {
	bin := currentBinary(t)
	for _, tc := range encryptionCases {
		if tc.transport != "tcp" && tc.transport != "tcpmux" {
			continue // a websocket port is a web server; see the next test
		}
		if tc.mode == "scrap" {
			continue // scrap answers as nginx; see TestScrapAnswersLikeNginx
		}
		t.Run(tc.transport+"/"+tc.mode, func(t *testing.T) {
			t.Parallel()
			enc := encLines(tc.mode)
			_, tunnel := startEncryptedPair(t, bin, tc.transport, enc, enc)
			time.Sleep(time.Second)
			c, err := net.DialTimeout("tcp", tunnel, 2*time.Second)
			if err != nil {
				t.Fatalf("cannot reach the tunnel port: %v", err)
			}
			defer c.Close()
			// The plain control claim: a length, the token, SG_Chan.
			claim := append([]byte{0, 33}, []byte("encryption-token-0123456789abcd")...)
			claim = append(claim, 1, 1)
			_, _ = c.Write(claim)
			_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
			got, _ := io.ReadAll(c)
			if len(got) != 0 {
				t.Fatalf("the encrypted port answered a probe with %d bytes: %q", len(got), got[:min(len(got), 32)])
			}
			if bytes.Contains(got, []byte("encryption-token")) {
				t.Fatal("the token came back in the clear")
			}
		})
	}
}

// A message-encrypted websocket hands its data on in pieces the size a
// streamed one would be. Handed on whole, each 64 KiB message became one write
// to the kharej's loopback target, whose send buffer the plain engine sets to 32 KiB,
// and every such write waited out a delayed ACK: 4 MB/s, on any machine.
func TestEncryptedWebsocketKeepsItsSpeedToALoopbackTarget(t *testing.T) {
	bin := currentBinary(t)
	for _, mode := range []string{"stealth", "stealth+", "none+splash"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			tr := "ws"
			if mode == "none+splash" {
				tr = "wss"
			}
			// A target that only reads, as an upload's is.
			const total = 32 << 20
			sink, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer sink.Close()
			got := make(chan time.Time, 1)
			go func() {
				c, err := sink.Accept()
				if err != nil {
					return
				}
				defer c.Close()
				if _, err := io.CopyN(io.Discard, c, total); err == nil {
					got <- time.Now()
				}
			}()
			enc := encLines(mode)
			entry, _ := startEncryptedPairTo(t, bin, tr, sink.Addr().String(), enc, enc)
			var c net.Conn
			for deadline := time.Now().Add(25 * time.Second); ; {
				if c, err = net.Dial("tcp", entry); err == nil {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("the tunnel never came up: %v", err)
				}
				time.Sleep(300 * time.Millisecond)
			}
			defer c.Close()
			time.Sleep(time.Second) // the pool settles
			start := time.Now()
			go func() {
				buf := make([]byte, 1<<20)
				for sent := 0; sent < total; sent += len(buf) {
					if _, err := c.Write(buf); err != nil {
						return
					}
				}
			}()
			select {
			case end := <-got:
				if d := end.Sub(start); d > 6*time.Second {
					t.Fatalf("32 MB took %s to a loopback target — the stall is back", d)
				}
			case <-time.After(20 * time.Second):
				t.Fatal("32 MB did not reach the loopback target in 20 s — the stall is back")
			}
		})
	}
}

// A scrap port is a web server to anyone without the token: nginx's 404 for
// a request, nginx's 400 for anything that is not HTTP, and a request copied
// from a real one is refused the second time.
func TestScrapAnswersLikeNginx(t *testing.T) {
	bin := currentBinary(t)
	enc := encLines("scrap")
	_, tunnel := startEncryptedPair(t, bin, "tcp", enc, enc)
	time.Sleep(time.Second)
	ask := func(raw string) string {
		c, err := net.DialTimeout("tcp", tunnel, 2*time.Second)
		if err != nil {
			t.Fatalf("cannot reach the tunnel port: %v", err)
		}
		defer c.Close()
		_, _ = c.Write([]byte(raw))
		_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
		b, _ := io.ReadAll(c)
		return string(b)
	}
	if got := ask("GET / HTTP/1.1\r\nHost: example.com\r\n\r\n"); !strings.HasPrefix(got, "HTTP/1.1 404 Not Found") ||
		!strings.Contains(got, "Server: nginx") || !strings.Contains(got, "<center>nginx</center>") {
		t.Fatalf("a GET was answered with %q, want nginx's 404", got)
	}
	if got := ask("POST /upload HTTP/1.1\r\nHost: x\r\nCookie: sid=AAAA\r\nContent-Length: 0\r\n\r\n"); !strings.HasPrefix(got, "HTTP/1.1 404") {
		t.Fatalf("a POST with a wrong cookie was answered with %q, want 404", got)
	}
	if got := ask("\x16\x03\x01\x02\x00garbage\r\n\r\n"); !strings.HasPrefix(got, "HTTP/1.1 400 Bad Request") {
		t.Fatalf("garbage was answered with %q, want nginx's 400", got)
	}
}

// On ws the upgrade's bearer would carry the token in the clear; with an
// encryption it carries a value derived from it, and the token itself is
// refused.
func TestEncryptedWebsocketDoesNotTakeTheTokenAsBearer(t *testing.T) {
	bin := currentBinary(t)
	enc := "encryption = \"stealth\"\n"
	_, tunnel := startEncryptedPair(t, bin, "ws", enc, enc)
	time.Sleep(time.Second)
	req, _ := http.NewRequest("GET", "http://"+tunnel+"/channel", nil)
	req.Header.Set("Authorization", "Bearer encryption-token-0123456789abcd")
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Sec-WebSocket-Version", "13")
	req.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("cannot reach the tunnel port: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("an encrypted ws server answered the raw token with %d, want 401", resp.StatusCode)
	}
}

// encLines is the config lines for a case's mode: "stealth", or a mode with
// "+splash".
func encLines(mode string) string {
	splash := strings.HasSuffix(mode, "+splash")
	mode = strings.TrimSuffix(mode, "+splash")
	out := ""
	if mode != "none" {
		out = fmt.Sprintf("encryption = %q\n", mode)
	}
	if splash {
		out += "splash = true\n"
	}
	return out
}

// startEncryptedPair runs a server and a client with the given extra lines
// and returns the forwarded port and the tunnel port.
func startEncryptedPair(t *testing.T, bin, transport, serverExtra, clientExtra string) (entry, tunnel string) {
	t.Helper()
	return startEncryptedPairTo(t, bin, transport, startEchoBackend(t).addr, serverExtra, clientExtra)
}

// startEncryptedPairTo is startEncryptedPair forwarding to backend.
func startEncryptedPairTo(t *testing.T, bin, transport, backend, serverExtra, clientExtra string) (entry, tunnel string) {
	t.Helper()
	dir := t.TempDir()
	tunnelPort, entryPort := freePort(t), freePort(t)
	token := "encryption-token-0123456789abcd"
	srv := filepath.Join(dir, "srv.toml")
	writeFile(t, srv, fmt.Sprintf(`[server]
bind_addr = "127.0.0.1:%d"
transport = "%s"
token = "%s"
ports = ["%d=%s"]
log_level = "error"
skip_optz = true
%s`, tunnelPort, transport, token, entryPort, backend, serverExtra))
	cli := filepath.Join(dir, "cli.toml")
	writeFile(t, cli, fmt.Sprintf(`[client]
remote_addr = "127.0.0.1:%d"
transport = "%s"
token = "%s"
log_level = "error"
skip_optz = true
retry_interval = 1
%s`, tunnelPort, transport, token, clientExtra))
	startEngine(t, bin, srv)
	time.Sleep(700 * time.Millisecond)
	startEngine(t, bin, cli)
	return net.JoinHostPort("127.0.0.1", fmt.Sprint(entryPort)), net.JoinHostPort("127.0.0.1", fmt.Sprint(tunnelPort))
}
