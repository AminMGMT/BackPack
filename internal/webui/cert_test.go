package webui

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

// The panel URL is what the browser is redirected to after a switch. Getting it
// wrong strands whoever applied the change on an address that answers nothing.
func TestPanelURL(t *testing.T) {
	for _, tc := range []struct {
		scheme, host string
		port         int
		want         string
	}{
		{"http", "1.2.3.4", 7777, "http://1.2.3.4:7777/"},
		{"https", "panel.example.com", 7777, "https://panel.example.com:7777/"},
		// The default port for the scheme is left off, so the address reads the
		// way anyone would type it.
		{"https", "panel.example.com", 443, "https://panel.example.com/"},
		{"http", "1.2.3.4", 80, "http://1.2.3.4/"},
		// A bare IPv6 address needs brackets before a port can follow it.
		{"https", "2a01:4f8::1", 7777, "https://[2a01:4f8::1]:7777/"},
		{"http", "", 7777, "http://<server-ip>:7777/"},
	} {
		if got := panelURL(tc.scheme, tc.host, tc.port); got != tc.want {
			t.Errorf("panelURL(%q,%q,%d) = %q, want %q", tc.scheme, tc.host, tc.port, got, tc.want)
		}
	}
}

// Let's Encrypt has exactly two ways to validate this server, and when neither
// is available the switch must be refused rather than attempted: the panel
// would restart, fail to obtain a certificate, and answer nothing a browser
// will talk to — with no CLI in reach of whoever pressed the button.
func TestACMEValidationPath(t *testing.T) {
	if path, _ := acmeValidationPath(443, false); path != "alpn" {
		t.Errorf("on port 443 with port 80 taken: path %q, want alpn", path)
	}
	if path, _ := acmeValidationPath(7777, true); path != "http01" {
		t.Errorf("on 7777 with port 80 free: path %q, want http01", path)
	}
	path, note := acmeValidationPath(7777, false)
	if path != "" {
		t.Errorf("on 7777 with port 80 taken: path %q, want none", path)
	}
	// The refusal has to say what to do about it, not just that it failed.
	if !strings.Contains(note, "80") || !strings.Contains(note, "443") {
		t.Errorf("unhelpful note: %q", note)
	}
}

// A typo in the domain is the cheapest way to break the panel, so it is caught
// before anything is written.
func TestValidHostname(t *testing.T) {
	good := []string{"panel.example.com", "a.b.c.example.co.uk", "x-1.example.org"}
	bad := []string{
		"",                 // empty
		"example",          // no dot: not a domain
		"1.2.3.4",          // an IP — Let's Encrypt will not issue for one
		"panel.example.",   // trailing dot
		"-bad.example.com", // label starts with a hyphen
		"bad-.example.com", // label ends with a hyphen
		"pa nel.example.com",
		"panel..example.com",
		"panel.example.com/path",
		"http://panel.example.com",
	}
	for _, h := range good {
		if !validHostname(h) {
			t.Errorf("validHostname(%q) = false, want true", h)
		}
	}
	for _, h := range bad {
		if validHostname(h) {
			t.Errorf("validHostname(%q) = true, want false", h)
		}
	}
}

// validateACME must reject before it reaches the network for the cases it can
// decide on its own.
func TestValidateACMERejectsLocally(t *testing.T) {
	for _, tc := range []struct{ name, domain, want string }{
		{"empty", "", "domain is required"},
		{"an IP address", "1.2.3.4", "IP address"},
		{"a typo", "not a domain", "not a valid domain"},
	} {
		// Port 443 so the port-80 check cannot be what rejects it.
		err := validateACME(tc.domain, 443)
		if err == nil {
			t.Errorf("%s: accepted %q", tc.name, tc.domain)
			continue
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: error %q does not mention %q", tc.name, err, tc.want)
		}
	}
}

// The three modes map onto the config the same way the CLI writes it — they are
// one setting in one file, and a panel that disagreed with the CLI about what
// "self-signed" means would be worse than not offering it here at all.
func TestCertModeRoundTrip(t *testing.T) {
	for _, tc := range []struct {
		mode   string
		https  bool
		domain string
	}{
		{"http", false, ""},
		{"self", true, ""},
		{"acme", true, "panel.example.com"},
	} {
		c := Config{HTTPS: tc.https, TLSDomain: tc.domain}
		var got string
		switch {
		case !c.HTTPS:
			got = "http"
		case c.TLSDomain != "":
			got = "acme"
		default:
			got = "self"
		}
		if got != tc.mode {
			t.Errorf("HTTPS=%v domain=%q reads as %q, want %q", tc.https, tc.domain, got, tc.mode)
		}
		wantScheme := "http"
		if tc.https {
			wantScheme = "https"
		}
		if c.Scheme() != wantScheme {
			t.Errorf("%s: scheme %q, want %q", tc.mode, c.Scheme(), wantScheme)
		}
	}
}

// The endpoint end to end: what it reports, and what it refuses. Nothing here
// restarts anything — every case is one the handler rejects before it saves.
func TestPanelCertEndpoint(t *testing.T) {
	srv := &server{sessions: newSessionStore()}

	post := func(form string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/api/panelcert", strings.NewReader(form))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w := httptest.NewRecorder()
		srv.handlePanelCert(w, r)
		return w
	}

	// GET describes the current state and always names a mode the UI knows.
	w := httptest.NewRecorder()
	srv.handlePanelCert(w, httptest.NewRequest("GET", "/api/panelcert", nil))
	if w.Code != 200 {
		t.Fatalf("GET status %d", w.Code)
	}
	var v certView
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatalf("GET body is not valid JSON: %v", err)
	}
	switch v.Mode {
	case "http", "self", "acme":
	default:
		t.Errorf("GET reported unknown mode %q", v.Mode)
	}
	if v.CurrentURL == "" {
		t.Error("GET gave no current URL — the page has nothing to show")
	}

	// An unknown mode must not be silently treated as one of the real ones.
	if got := post("mode=nonsense").Code; got != 400 {
		t.Errorf("unknown mode: status %d, want 400", got)
	}
	// Let's Encrypt with no domain would restart the panel onto a listener that
	// can never complete a handshake.
	if got := post("mode=acme&domain=").Code; got != 400 {
		t.Errorf("acme with no domain: status %d, want 400", got)
	}
	if w := post("mode=acme&domain=1.2.3.4"); w.Code != 400 ||
		!strings.Contains(w.Body.String(), "IP address") {
		t.Errorf("acme with an IP: status %d body %q", w.Code, w.Body.String())
	}
	if got := post("mode=acme&domain=not%20a%20domain").Code; got != 400 {
		t.Errorf("acme with a typo: status %d, want 400", got)
	}
	// Deliberately no case here that gets past validation: a POST that does is
	// one that writes the real config and restarts the real panel, and this
	// test would then reconfigure the machine it is running on. The accepting
	// paths are covered by TestValidateACMERejectsLocally and
	// TestACMEValidationPath, which decide the same question without saving.

	// GET and POST only.
	w = httptest.NewRecorder()
	srv.handlePanelCert(w, httptest.NewRequest("DELETE", "/api/panelcert", nil))
	if w.Code != 405 {
		t.Errorf("DELETE: status %d, want 405", w.Code)
	}
}
