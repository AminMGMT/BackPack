package manage

import (
	"io"
	"os"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
	"github.com/backpack/backpack/config"
	"github.com/backpack/backpack/internal/tui"
)

// Reported on v1.8.4: a reverse tunnel set up from the setup link did not
// work, and the same tunnel typed in by hand did. The link under Manage →
// Setup Link was built with no address for the kharej to dial, and Setup from
// a link — the path that link points to — had no way to ask for one: it
// refused with "the server address is required".

func TestALinkFromManageCarriesThisServersAddress(t *testing.T) {
	iran := TunnelSpec{Role: "server", Transport: "tcp", Name: "server-443", BindAddr: ":443",
		Ports: []string{"8443=127.0.0.1:2096"}, Token: randomToken(64)}
	ApplyPreset(&iran, PresetTurbo)
	var cfg config.Config
	if _, err := toml.Decode(iran.Render(), &cfg); err != nil {
		t.Fatal(err)
	}
	prevFor, prevHost := shareLinkFor, linkHost
	shareLinkFor = func(name, host string) (string, error) { return shareLinkOf(name, host, cfg) }
	linkHost = func() string { return "203.0.113.7" }
	t.Cleanup(func() { shareLinkFor, linkHost = prevFor, prevHost })

	out := captureStdout(t, func() {
		if !printShareLink(iran.Name) {
			t.Fatal("no link was printed")
		}
	})
	link, err := DecodeShareLink(FindSetupLink(out))
	if err != nil {
		t.Fatalf("the printed link does not decode: %v\n%s", err, out)
	}
	if link.Host != "203.0.113.7" {
		t.Fatalf("the link carries %q as the address to dial; the kharej would have none", link.Host)
	}
}

func TestAKharejFromALinkWithNoAddressAsksForIt(t *testing.T) {
	iran := TunnelSpec{Role: "server", Transport: "tcp", Name: "server-443", BindAddr: ":443",
		Ports: []string{"8443=127.0.0.1:2096"}, Token: randomToken(64)}
	ApplyPreset(&iran, PresetTurbo)
	link, err := DecodeShareLink(pendingReverseLink(iran, "", linkExtras{}))
	if err != nil {
		t.Fatal(err)
	}

	restore := tui.SetInput(strings.NewReader("198.51.100.4\n"))
	defer restore()
	form := MirrorForPeer(link)
	captureStdout(t, func() {
		if !askMissingAddress(&form) {
			t.Fatal("the address was typed and still refused")
		}
	})
	s, err := specFromNew(form.ToNewTunnel())
	if err != nil {
		t.Fatalf("the kharej was refused: %v", err)
	}
	if s.RemoteAddr != "198.51.100.4:443" {
		t.Fatalf("the kharej dials %q", s.RemoteAddr)
	}
}

// A link that carries the address is not questioned.
func TestALinkWithAnAddressIsNotQuestioned(t *testing.T) {
	iran := TunnelSpec{Role: "server", Transport: "tcp", Name: "server-443", BindAddr: ":443",
		Ports: []string{"8443=127.0.0.1:2096"}, Token: randomToken(64)}
	ApplyPreset(&iran, PresetTurbo)
	link, _ := DecodeShareLink(pendingReverseLink(iran, "203.0.113.7", linkExtras{}))
	restore := tui.SetInput(strings.NewReader(""))
	defer restore()
	form := MirrorForPeer(link)
	if !askMissingAddress(&form) || form.ServerAddr != "203.0.113.7" {
		t.Fatalf("a link with an address was questioned: %+v", form.ServerAddr)
	}
}

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	prev := os.Stdout
	os.Stdout = w
	done := make(chan string)
	go func() { b, _ := io.ReadAll(r); done <- string(b) }()
	defer func() { os.Stdout = prev }()
	fn()
	w.Close()
	os.Stdout = prev
	return <-done
}
