package manage

import (
	"fmt"
	"strings"
	"testing"
)

// The Telegram forward is created automatically and hidden from every port
// listing, so nobody is going to notice where it binds. That makes the bind
// address worth pinning down in a test: a mapping written as a bare port number
// listens on every interface, which would put an unauthenticated path to
// api.telegram.org on this machine's public address.

func TestNewMappingBindsLoopback(t *testing.T) {
	mapping := fmt.Sprintf("%s:%d%s", telegramBindAddr, 34567, telegramPortSuffix)

	if !strings.HasPrefix(mapping, "127.0.0.1:") {
		t.Fatalf("mapping %q does not bind loopback — it would listen on every interface", mapping)
	}
	if !isTelegramPort(mapping) {
		t.Errorf("the loopback form %q is no longer recognised as the Telegram forward", mapping)
	}
	port, ok := telegramMappingPort(mapping)
	if !ok || port != 34567 {
		t.Errorf("telegramMappingPort(%q) = %d, %v; want 34567, true", mapping, port, ok)
	}
}

// Reading the port back has to work for both forms. If the loopback form were
// unreadable, EnsureTelegramPort would fail to recognise its own mapping and
// append another one on every single call.
func TestPortIsReadableFromBothForms(t *testing.T) {
	for _, tc := range []struct {
		mapping string
		want    int
	}{
		{"41234=api.telegram.org:443", 41234},           // the old bare form
		{"127.0.0.1:41234=api.telegram.org:443", 41234}, // the loopback form
		{"  127.0.0.1:41234=api.telegram.org:443  ", 41234},
	} {
		got, ok := telegramMappingPort(tc.mapping)
		if !ok || got != tc.want {
			t.Errorf("telegramMappingPort(%q) = %d, %v; want %d, true", tc.mapping, got, ok, tc.want)
		}
	}

	for _, bad := range []string{"=api.telegram.org:443", "notaport=api.telegram.org:443", "99999=x", ""} {
		if _, ok := telegramMappingPort(bad); ok {
			t.Errorf("telegramMappingPort(%q) accepted a mapping with no usable port", bad)
		}
	}
}

// An install created before the loopback bind carries the wildcard form. It has
// to be rewritten, because the operator cannot see the mapping to fix it.
func TestWildcardMappingIsMigrated(t *testing.T) {
	got := boundMapping("41234=api.telegram.org:443")
	want := "127.0.0.1:41234=api.telegram.org:443"
	if got != want {
		t.Errorf("boundMapping did not move the wildcard form to loopback:\n got %q\nwant %q", got, want)
	}
}

// A mapping already on loopback must report no migration, or EnsureTelegramPort
// would rewrite and restart the tunnel on every call.
func TestLoopbackMappingIsNotMigratedAgain(t *testing.T) {
	if got := boundMapping("127.0.0.1:41234=api.telegram.org:443"); got != "" {
		t.Errorf("boundMapping wanted to migrate an already-loopback mapping to %q", got)
	}
}

// Migration must be a fixed point: running it twice cannot change the result.
func TestMigrationIsIdempotent(t *testing.T) {
	once := boundMapping("41234=api.telegram.org:443")
	if twice := boundMapping(once); twice != "" {
		t.Errorf("migrating twice produced %q — the port list would grow on every call", twice)
	}
	p1, _ := telegramMappingPort("41234=api.telegram.org:443")
	p2, _ := telegramMappingPort(once)
	if p1 != p2 {
		t.Errorf("migration changed the port from %d to %d; the bot would dial the wrong one", p1, p2)
	}
}
