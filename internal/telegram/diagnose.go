package telegram

import (
	"bytes"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/backpack/backpack/internal/manage"
)

// Relay diagnosis.
//
// When the bot fails, the error it surfaces is whatever the HTTP client saw —
// usually a bare "EOF" — and that error names the wrong machine. The chain has
// five links across two servers, and knowing which one broke is the entire
// difficulty. Guessing at it from the far end, one exchange at a time, is not a
// method.
//
// This walks the chain in order and stops at the first thing that is actually
// wrong, so the answer is a specific hop rather than a symptom.

// RelayStep is one link in the chain.
type RelayStep struct {
	Name   string
	OK     bool
	Detail string
	Fix    string
}

// DiagnoseRelay checks the relay path hop by hop.
func DiagnoseRelay() []RelayStep {
	var out []RelayStep
	c := Load()

	if c.Token == "" || c.AdminID == "" {
		return append(out, RelayStep{
			Name: "Bot configured", Detail: "no token or admin id saved",
			Fix: "Telegram Bot → Configure",
		})
	}
	out = append(out, RelayStep{Name: "Bot configured", OK: true, Detail: "token and admin id are set"})

	// 1) Is a relay chosen at all?
	name, port, err := resolveRelay(c)
	if err != nil {
		return append(out, RelayStep{
			Name: "Relay tunnel", Detail: err.Error(),
			Fix: "bring up a server tunnel, or set the relay to a specific one",
		})
	}
	if name == "" {
		out = append(out, RelayStep{Name: "Relay tunnel", OK: true, Detail: "direct — no relay in use"})
		return append(out, checkTelegramDirect())
	}
	out = append(out, RelayStep{Name: "Relay tunnel", OK: true,
		Detail: fmt.Sprintf("%s, local port %d", name, port)})

	// 2) Is that tunnel actually up?
	h := manage.AllHealth()[name]
	if h.State != "online" {
		return append(out, RelayStep{
			Name: "Tunnel state", Detail: fmt.Sprintf("%s is %s — %s", name, h.State, h.Detail),
			Fix: "start it, or switch the relay to Automatic so it picks a live one",
		})
	}
	out = append(out, RelayStep{Name: "Tunnel state", OK: true, Detail: name + " is online"})

	// 3) Is the relay port open on this machine?
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		return append(out, RelayStep{
			Name: "Relay port", Detail: fmt.Sprintf("cannot reach %s: %v", addr, err),
			Fix: "the tunnel is not exposing its relay port — reconfigure the bot to re-add it",
		})
	}
	conn.Close()
	out = append(out, RelayStep{Name: "Relay port", OK: true, Detail: addr + " is accepting connections"})

	// 4) Does the far end actually reach Telegram? The forward carries a TLS
	//    stream to api.telegram.org, so a successful handshake proves the whole
	//    chain: tunnel, forward, and the peer's own internet access.
	if err := probeTelegramThrough(port); err != nil {
		// A failed TLS handshake says nothing about what actually answered, and
		// "not a TLS handshake" can mean anything from a proxy greeting to a
		// block page. Read the raw reply and say what it was — that identifies
		// the wrong thing on the other end in one step instead of several.
		step := RelayStep{
			Name:   "Peer's internet",
			Detail: "the tunnel carried the connection, but Telegram was not reached: " + err.Error(),
			Fix: "the OTHER server is what dials api.telegram.org — check that it has\n" +
				"      working internet, and that its tunnel is connected",
		}
		if what := identifyResponder(port); what != "" {
			step.Detail += "\n                   what answered instead: " + what
			step.Fix = responderFix(what)
		}
		return append(out, step)
	}
	out = append(out, RelayStep{Name: "Peer's internet", OK: true, Detail: "api.telegram.org answered through the tunnel"})

	// 5) And the bot's own client, end to end.
	client, err := botClient(c, 20*time.Second)
	if err != nil {
		return append(out, RelayStep{Name: "Telegram", Detail: err.Error()})
	}
	resp, err := client.Get("https://api.telegram.org/")
	if err != nil {
		return append(out, RelayStep{Name: "Telegram", Detail: err.Error()})
	}
	resp.Body.Close()
	return append(out, RelayStep{Name: "Telegram", OK: true, Detail: "reachable through the relay"})
}

// probeTelegramThrough opens a TLS connection to the API through the forwarded
// port, verifying the certificate as the real client would.
//
// Separating this from the full request distinguishes "the far end cannot reach
// Telegram" from "Telegram answered but the request was wrong" — two very
// different problems that the combined failure never told apart.
func probeTelegramThrough(port int) error {
	d := &net.Dialer{Timeout: 10 * time.Second}
	conn, err := tls.DialWithDialer(d, "tcp",
		net.JoinHostPort("127.0.0.1", strconv.Itoa(port)),
		&tls.Config{ServerName: "api.telegram.org"})
	if err != nil {
		return err
	}
	conn.Close()
	return nil
}

// checkTelegramDirect tests reaching Telegram without a relay.
func checkTelegramDirect() RelayStep {
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Get("https://api.telegram.org/")
	if err != nil {
		return RelayStep{
			Name: "Telegram", Detail: err.Error(),
			Fix: "this server cannot reach Telegram directly, which is normal from Iran —\n" +
				"      set the relay to Automatic so it goes out through a peer",
		}
	}
	resp.Body.Close()
	return RelayStep{Name: "Telegram", OK: true, Detail: "reachable directly"}
}

// identifyResponder connects to the relay port and describes whatever replies,
// so a failed handshake names the thing that answered rather than the symptom.
func identifyResponder(port int) string {
	conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), 8*time.Second)
	if err != nil {
		return ""
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(8 * time.Second))

	// A TLS ClientHello. Anything that is not a TLS server will either answer
	// in its own protocol or close.
	hello := []byte{0x16, 0x03, 0x01, 0x00, 0x01, 0x00}
	if _, err := conn.Write(hello); err != nil {
		return ""
	}

	buf := make([]byte, 256)
	n, err := conn.Read(buf)
	if n == 0 {
		if err != nil {
			return "nothing — the connection closed (" + err.Error() + ")"
		}
		return "nothing"
	}
	reply := buf[:n]

	switch {
	case reply[0] == 0x16:
		return "" // it is TLS after all; the earlier failure was elsewhere

	case reply[0] == 0x05:
		return "a SOCKS5 proxy (the old relay mechanism)"

	case bytes.HasPrefix(reply, []byte("HTTP/")):
		line := string(reply)
		if i := strings.IndexAny(line, "\r\n"); i > 0 {
			line = line[:i]
		}
		return "a plain HTTP server: " + strings.TrimSpace(line)

	case bytes.HasPrefix(reply, []byte("SSH-")):
		line := string(reply)
		if i := strings.IndexAny(line, "\r\n"); i > 0 {
			line = line[:i]
		}
		return "an SSH server: " + strings.TrimSpace(line)

	default:
		return fmt.Sprintf("something unrecognised (first bytes: % x)", reply[:minInt(12, len(reply))])
	}
}

// responderFix maps what answered to what to do about it.
func responderFix(what string) string {
	switch {
	case strings.Contains(what, "SOCKS5"):
		return "this tunnel still forwards its relay port to the old proxy.\n" +
			"      Fix it with: sudo backpack → Telegram Bot → Configure"
	case strings.Contains(what, "HTTP server"), strings.Contains(what, "SSH server"):
		return "that port is already taken on THIS server by another service, so the\n" +
			"      tunnel never got it. Reconfigure the bot to pick a different port:\n" +
			"      sudo backpack → Telegram Bot → Configure"
	case strings.Contains(what, "nothing"):
		return "the far end accepted and closed without replying — its side of the\n" +
			"      tunnel is up but it could not reach api.telegram.org"
	default:
		return "the far end answered with something other than Telegram — check what\n" +
			"      that port is forwarded to on the OTHER server"
	}
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
