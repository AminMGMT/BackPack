package e2e

import (
	"bytes"
	"crypto/tls"
	"fmt"
	"io"
	"net/http"
	"testing"
	"time"
)

// A wss tunnel on 443 has to look like a website to anything that is not a
// genuine tunnel connection — a browser, a scanner, an active probe. If it
// answered those with 401 or a blank close, it would be trivially
// distinguishable from real HTTPS and easy to filter. So a non-tunnel request
// gets a plausible page and a normal 200.
func TestWSSDecoyLooksLikeAWebsite(t *testing.T) {
	certPath, keyPath := testCert(t)
	backend := startEchoBackend(t)

	tunnelPort := freePort(t)
	entryPort := freePort(t)
	token := "decoy-token-0123456789abcdefghij"

	srvCfg := baseServerConfig("wss", tunnelPort, entryPort, backend.addr, token)
	srvCfg.TLSCertFile = certPath
	srvCfg.TLSKeyFile = keyPath
	cliCfg := baseClientConfig("wss", fmt.Sprintf("127.0.0.1:%d", tunnelPort), token, nil)

	tun := runPair(t, srvCfg, cliCfg, entryPort, tunnelPort)
	_ = tun.waitReady(tunnelReadyTimeout) // make sure the listener is up

	client := &http.Client{
		Timeout:   10 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}},
	}

	// A plain browser-style GET to the root, with no websocket upgrade and no
	// credential — exactly what a probe would send.
	var resp *http.Response
	var err error
	deadline := time.Now().Add(tunnelReadyTimeout)
	for time.Now().Before(deadline) {
		resp, err = client.Get(fmt.Sprintf("https://127.0.0.1:%d/", tunnelPort))
		if err == nil {
			break
		}
		time.Sleep(300 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("GET / never succeeded: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("decoy returned %d, want 200 — a probe would notice", resp.StatusCode)
	}
	if got := resp.Header.Get("Server"); got != "nginx" {
		t.Errorf("Server header = %q, want a plausible one", got)
	}
	body, _ := io.ReadAll(resp.Body)
	if !bytes.Contains(body, []byte("Welcome to nginx")) {
		t.Errorf("the response does not look like a website: %.100q", body)
	}
}

// A GET to the tunnel's own control path, still without a websocket upgrade,
// must also look like the website — the path existing is not a tell.
func TestWSSDecoyOnTunnelPath(t *testing.T) {
	certPath, keyPath := testCert(t)
	backend := startEchoBackend(t)

	tunnelPort := freePort(t)
	entryPort := freePort(t)
	token := "decoy-path-token-0123456789abcd"

	srvCfg := baseServerConfig("wss", tunnelPort, entryPort, backend.addr, token)
	srvCfg.TLSCertFile = certPath
	srvCfg.TLSKeyFile = keyPath
	cliCfg := baseClientConfig("wss", fmt.Sprintf("127.0.0.1:%d", tunnelPort), token, nil)

	tun := runPair(t, srvCfg, cliCfg, entryPort, tunnelPort)
	_ = tun.waitReady(tunnelReadyTimeout)

	client := &http.Client{
		Timeout:   10 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}},
	}
	var resp *http.Response
	var err error
	deadline := time.Now().Add(tunnelReadyTimeout)
	for time.Now().Before(deadline) {
		resp, err = client.Get(fmt.Sprintf("https://127.0.0.1:%d/channel", tunnelPort))
		if err == nil {
			break
		}
		time.Sleep(300 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("GET /channel never succeeded: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("GET /channel returned %d, want the decoy 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !bytes.Contains(body, []byte("Welcome to nginx")) {
		t.Errorf("the control path revealed it is not a website: %.100q", body)
	}
}
