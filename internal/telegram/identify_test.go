package telegram

import (
	"net"
	"strings"
	"testing"
)

// The identifier is the whole point of the new diagnosis: it must name what
// answered, because "not a TLS handshake" is true of everything that is wrong.
func TestIdentifyResponder(t *testing.T) {
	cases := []struct {
		name  string
		reply []byte
		want  string
	}{
		{"http server", []byte("HTTP/1.1 400 Bad Request\r\nServer: nginx\r\n\r\n"), "plain HTTP server"},
		{"socks proxy", []byte{0x05, 0x00}, "SOCKS5"},
		{"ssh server", []byte("SSH-2.0-OpenSSH_9.6\r\n"), "SSH server"},
		{"unknown", []byte{0xDE, 0xAD, 0xBE, 0xEF}, "unrecognised"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ln, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer ln.Close()
			go func() {
				c, err := ln.Accept()
				if err != nil {
					return
				}
				defer c.Close()
				buf := make([]byte, 64)
				c.Read(buf)
				c.Write(tc.reply)
			}()

			port := ln.Addr().(*net.TCPAddr).Port
			got := identifyResponder(port)
			if !strings.Contains(got, tc.want) {
				t.Errorf("identifyResponder = %q, want something containing %q", got, tc.want)
			}
			if fix := responderFix(got); fix == "" {
				t.Error("every identification should come with advice")
			}
		})
	}
}

func TestIdentifyResponderOnSilence(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		c.Close() // accept and close, which is what a dead far end looks like
	}()

	got := identifyResponder(ln.Addr().(*net.TCPAddr).Port)
	if !strings.Contains(got, "nothing") {
		t.Errorf("a closing connection should be reported as nothing, got %q", got)
	}
}
