package manage

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"encoding/pem"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/backpack/backpack/config"
)

func TestHTTPSPreflightVerifiesTrustHostnameAndHTTP2(t *testing.T) {
	for _, h2 := range []bool{true, false} {
		server := httptest.NewUnstartedServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		server.EnableHTTP2 = h2
		server.StartTLS()
		defer server.Close()
		ca := filepath.Join(t.TempDir(), "ca.pem")
		if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0600); err != nil {
			t.Fatal(err)
		}
		x := config.XrayClientConfig{Mode: "xhttp", Server: server.Listener.Addr().String(), ServerName: server.Certificate().DNSNames[0], CAFile: ca}
		want := "tls-failed"
		if h2 {
			want = "tls-ok"
		}
		if got := ctProbeHTTPS(context.Background(), TunnelSpec{XrayClient: x}); got != want {
			t.Fatalf("HTTP2=%v: %s", h2, got)
		}
		if h2 {
			// The real Naive proxy URL uses an IP SAN rather than the XHTTP SNI.
			n := config.NaiveClientConfig{Server: x.Server, CAFile: ca}
			if got := ctProbeHTTPS(context.Background(), TunnelSpec{NaiveClient: n}); got != "tls-ok" {
				t.Fatalf("IP trust probe: %s", got)
			}
		}
		x.ServerName = "wrong.backpack.invalid"
		if got := ctProbeHTTPS(context.Background(), TunnelSpec{XrayClient: x}); got != "tls-failed" {
			t.Fatalf("wrong hostname accepted: %s", got)
		}
		x.CAFile = filepath.Join(t.TempDir(), "missing.pem")
		if got := ctProbeHTTPS(context.Background(), TunnelSpec{XrayClient: x}); got != "tls-failed" {
			t.Fatalf("missing trust accepted: %s", got)
		}
	}
}

func TestHTTPSPreflightCancellationClosesSilentPeer(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		c, err := l.Accept()
		if err == nil {
			defer c.Close()
			_, _ = io.Copy(io.Discard, c)
		}
	}()
	// A valid trust file forces the probe to enter the silent TLS handshake.
	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer server.Close()
	ca := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	x := config.XrayClientConfig{Mode: "xhttp", Server: l.Addr().String(), ServerName: "example.com", CAFile: ca}
	start := time.Now()
	if got := ctProbeHTTPS(ctx, TunnelSpec{XrayClient: x}); got != "tls-failed" {
		t.Fatalf("silent peer: %s", got)
	}
	if time.Since(start) > time.Second {
		t.Fatal("probe ignored cancellation")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("cancelled probe left the connection open")
	}
	l.Close()
	x.Server = l.Addr().String()
	if got := ctProbeHTTPS(context.Background(), TunnelSpec{XrayClient: x}); got != "tcp-failed" {
		t.Fatalf("closed public port: %s", got)
	}
}

func TestHTTPSProbeCoordinatorAuthenticatesBoundsAndFreezesReports(t *testing.T) {
	c := &ctCoordinator{tok: "secret"}
	for _, request := range []string{"https-probe wrong xhttp:tls-ok", "https-probe secret tcp:tls-ok", "https-probe secret reality:tls-ok", "https-probe secret naive:tcp-ok", "https-probe secret xhttp:untrusted-text", "https-probe secret xhttp:tls-ok extra"} {
		if c.answer(request, "127.0.0.1") != "" || len(c.httpsProbes) != 0 {
			t.Fatalf("invalid report accepted: %s", request)
		}
	}
	for _, report := range []string{"xhttp:tls-ok", "naive:tls-failed", "reality:tcp-ok"} {
		if c.answer("https-probe secret "+report, "127.0.0.1") != "ok" {
			t.Fatalf("valid report rejected: %s", report)
		}
	}
	c.peer = "127.0.0.1"
	if c.answer("https-probe secret xhttp:tcp-failed", c.peer) != "" || c.httpsProbes["xhttp"] != "tls-ok" {
		t.Fatal("late report changed the measurement")
	}
}

func TestHTTPSProbeRetriesTemporaryHelperPortReservation(t *testing.T) {
	identity := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer identity.Close()
	ca := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: identity.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		// The reservation accepts no TLS and closes when the helper starts.
		c, err := l.Accept()
		if err != nil {
			return
		}
		c.Close()
		c, err = l.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		server := tls.Server(c, &tls.Config{Certificates: identity.TLS.Certificates, NextProtos: []string{"h2"}})
		_ = server.Handshake()
	}()
	x := config.XrayClientConfig{Mode: "xhttp", Server: l.Addr().String(), ServerName: identity.Certificate().DNSNames[0], CAFile: ca}
	if got := ctProbeHTTPS(context.Background(), TunnelSpec{XrayClient: x}); got != "tls-ok" {
		t.Fatalf("temporary reservation was diagnosed as a path failure: %s", got)
	}
	<-done
}

func TestTLSPreflightCannotTurnFailedTunnelIntoSuccess(t *testing.T) {
	c := &connTestCase{kind: "reverse", tr: "xhttp", httpsProbe: "tls-ok", link: ShareLink{Port: "8443"}}
	r := ctHTTPSResult(ConnTestResult{Kind: c.kind, Transport: c.tr, Status: ctDown, Detail: "never carried traffic"}, c)
	raw, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	var peer ConnTestResult
	if json.Unmarshal(raw, &peer) != nil || peer.Status != ctDown || peer.OK != 0 || peer.HTTPSProbe != "tls-ok" || peer.TestPort != 8443 {
		t.Fatalf("preflight changed verdict or was lost on the wire: %+v", peer)
	}
	text := ConnTestTable([]ConnTestResult{peer})
	for _, want := range []string{"DOWN", "never carried traffic", ":8443", "separate connections", "not a tunnel traffic check"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in %s", want, text)
		}
	}
	if strings.Contains(ConnTestTable([]ConnTestResult{{Kind: "reverse", Transport: "tcp", Status: ctDown}}), "HTTPS path checks") {
		t.Fatal("ordinary transports received HTTPS notes")
	}
}
