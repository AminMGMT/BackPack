package cli

import (
	"strings"
	"testing"

	"github.com/backpack/backpack/internal/localproxy"
)

func TestProxyEnableIsTheMenusProxy(t *testing.T) {
	var got localproxy.Config
	prevE, prevR, prevRoot := enableProxy, proxyRunning, isRoot
	defer func() { enableProxy, proxyRunning, isRoot = prevE, prevR, prevRoot }()
	enableProxy = func(c localproxy.Config) error { got = c; return nil }
	proxyRunning = func() bool { return false }
	isRoot = func() bool { return true }

	r := Run([]string{"proxy", "enable", "socks5", "45917", "--user", "u", "--pass=p"})
	if r.Code != CodeOK || got.Type != localproxy.SOCKS5 || got.Port != 45917 || got.Username != "u" || got.Password != "p" {
		t.Fatalf("enable: %+v, config %+v", r, got)
	}
	for _, args := range [][]string{
		{"proxy"}, {"proxy", "enable"}, {"proxy", "enable", "ftp", "1"}, {"proxy", "enable", "http", "0"},
		{"proxy", "enable", "http", "80", "--user", "u"}, {"proxy", "nope"},
	} {
		if r := Run(args); r.Code != CodeUsage {
			t.Errorf("%v: code %d, want usage", args, r.Code)
		}
	}
	isRoot = func() bool { return false }
	if r := Run([]string{"proxy", "enable", "http", "45918"}); r.Code != CodeFailed || !strings.Contains(r.Err, "root") {
		t.Errorf("not root: %+v", r)
	}
	if !IsCommand("proxy") {
		t.Error("main would hand proxy to the flag parser")
	}
}
