package manage

import (
	"errors"
	"testing"
)

// A link is pasted from wherever it arrived: a Telegram message around it,
// quotes, a line break the chat app put in the middle of it.
func TestASetupLinkIsFoundInWhatWasPasted(t *testing.T) {
	iran := TunnelSpec{Role: "server", Transport: "tcp", Name: "server-443", BindAddr: ":443",
		Ports: []string{"8443=127.0.0.1:2096"}, Token: randomToken(64)}
	ApplyPreset(&iran, PresetTurbo)
	link := pendingReverseLink(iran, "203.0.113.7", linkExtras{})
	half := len(link) / 2
	for _, pasted := range []string{
		link,
		"  " + link + "\n",
		"Setup link for kharej:\n'" + link + "'\nthanks",
		link[:half] + "\n" + link[half:],
	} {
		got := FindSetupLink(pasted)
		if _, err := DecodeShareLink(got); err != nil {
			t.Errorf("not found in %q: %v", pasted, err)
		}
	}
}

// A link without the Iran server's address builds nothing until one is given,
// and says how to give it.
func TestAKharejFromAnAddresslessLinkNeedsHost(t *testing.T) {
	iran := TunnelSpec{Role: "server", Transport: "tcp", Name: "server-443", BindAddr: ":443",
		Ports: []string{"8443=127.0.0.1:2096"}, Token: randomToken(64)}
	ApplyPreset(&iran, PresetTurbo)
	link, err := DecodeShareLink(pendingReverseLink(iran, "", linkExtras{}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := kharejFromLink(link, LinkApplyOptions{}); !errors.Is(err, ErrLinkNeedsHost) {
		t.Fatalf("got %v, want ErrLinkNeedsHost", err)
	}
	s, err := kharejFromLink(link, LinkApplyOptions{Host: "198.51.100.4"})
	if err != nil || s.RemoteAddr != "198.51.100.4:443" {
		t.Fatalf("with --host: %q, %v", s.RemoteAddr, err)
	}
}

// The link's backup addresses become the kharej's failover list, with the
// tunnel port added where only a host was given.
func TestTheLinksBackupAddressesBecomeTheKharejsFailover(t *testing.T) {
	iran := TunnelSpec{Role: "server", Transport: "tcp", Name: "server-443", BindAddr: ":443",
		Ports: []string{"8443=127.0.0.1:2096"}, Token: randomToken(64)}
	ApplyPreset(&iran, PresetTurbo)
	link, _ := DecodeShareLink(pendingReverseLink(iran, "203.0.113.7",
		linkExtras{hosts: []string{"iran.example.com", "198.51.100.9:8443", "2001:db8::5"}}))
	s, err := kharejFromLink(link, LinkApplyOptions{})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"iran.example.com:443", "198.51.100.9:8443", "[2001:db8::5]:443"}
	if len(s.FallbackAddrs) != len(want) || !s.HealthFailover {
		t.Fatalf("fallbacks %v, failover %v", s.FallbackAddrs, s.HealthFailover)
	}
	for i := range want {
		if s.FallbackAddrs[i] != want[i] {
			t.Errorf("fallback %d = %q, want %q", i, s.FallbackAddrs[i], want[i])
		}
	}
}
