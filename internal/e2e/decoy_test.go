package e2e

import (
	"bytes"
	"crypto/tls"
	"fmt"
	"io"
	"net/http"
	"strings"
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
	if got := resp.Header.Get("Server"); !strings.HasPrefix(got, "nginx") {
		t.Errorf("Server header = %q, want a plausible one", got)
	}
	// The headers a static file carries. Without them the response is a program
	// answering, not a file being served, whatever the body says.
	for _, h := range []string{"ETag", "Last-Modified", "Accept-Ranges", "Content-Length"} {
		if resp.Header.Get(h) == "" {
			t.Errorf("%s is missing — a real web server sends it for index.html", h)
		}
	}
	body, _ := io.ReadAll(resp.Body)
	if !bytes.Contains(body, []byte("Welcome to nginx")) {
		t.Errorf("the response does not look like a website: %.100q", body)
	}
}

// Two servers must not be the same server. The decoy used to answer with
// byte-identical bytes on every install, which meant one scan for that exact
// response enumerated the whole fleet — so the identity is now derived from the
// token and has to actually differ when the token does.
func TestWSSDecoyIdentityDiffersPerInstall(t *testing.T) {
	certPath, keyPath := testCert(t)

	identity := func(token string) string {
		backend := startEchoBackend(t)
		tunnelPort := freePort(t)
		entryPort := freePort(t)

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
		_, _ = io.Copy(io.Discard, resp.Body)

		return strings.Join([]string{
			resp.Header.Get("Server"),
			resp.Header.Get("ETag"),
			resp.Header.Get("Last-Modified"),
			resp.Header.Get("Content-Length"),
		}, "|")
	}

	first := identity("decoy-identity-one-0123456789abc")
	second := identity("decoy-identity-two-0123456789abc")

	if first == second {
		t.Errorf("two installs present the identical decoy identity %q — one scan finds both", first)
	}
}

// A GET to the tunnel's own control path, still without a websocket upgrade,
// must be indistinguishable from a GET to any other path that is not there.
//
// This used to assert a 200 and the welcome page, on the reasoning that the
// path must not stand out. It did stand out: a static site does not serve its
// index for arbitrary paths, so /channel answering 200 was only unremarkable
// next to the equally wrong 200 that /anything-else got. Now both are the
// stock 404, which is what nginx does and what makes the tunnel path boring.
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
	// Fetch a path, retrying until the listener is up.
	get := func(path string) (int, []byte) {
		var resp *http.Response
		var err error
		deadline := time.Now().Add(tunnelReadyTimeout)
		for time.Now().Before(deadline) {
			resp, err = client.Get(fmt.Sprintf("https://127.0.0.1:%d%s", tunnelPort, path))
			if err == nil {
				break
			}
			time.Sleep(300 * time.Millisecond)
		}
		if err != nil {
			t.Fatalf("GET %s never succeeded: %v", path, err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, body
	}

	status, body := get("/channel")
	if status != http.StatusNotFound {
		t.Errorf("GET /channel returned %d, want the 404 a missing path gets", status)
	}
	if !bytes.Contains(body, []byte("404 Not Found")) {
		t.Errorf("the control path did not answer like a web server: %.100q", body)
	}

	// And it must be the *same* 404 an ordinary missing path gets — a tunnel
	// path that differs in any way is the one worth probing.
	otherStatus, otherBody := get("/not-a-real-path")
	if status != otherStatus || !bytes.Equal(body, otherBody) {
		t.Errorf("/channel answers differently from an ordinary missing path:\n %d %.100q\n %d %.100q",
			status, body, otherStatus, otherBody)
	}
}
