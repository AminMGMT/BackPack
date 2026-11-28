package manage

import (
	"errors"
	"strings"
	"testing"
)

// No test reaches GitHub or /usr/local/lib by accident: a wizard or panel
// test that finds no helper sees the install fail, as on an offline server.
func init() {
	ensureHelper = func(string, func(string)) (string, error) {
		return "", errors.New("tests do not install helpers")
	}
}

// A kharej applying a setup link gets the helper it runs before the tunnel
// is checked: Xray for Vision and XHTTP, NaiveProxy for Naive.
func TestApplyingAManagedLinkInstallsItsHelper(t *testing.T) {
	prev := ensureHelper
	defer func() { ensureHelper = prev }()
	var asked []string
	ensureHelper = func(tool string, _ func(string)) (string, error) {
		asked = append(asked, tool)
		return "", errors.New("offline")
	}
	for _, tc := range []struct{ tr, tool string }{{"vision", "xray"}, {"xhttp", "xray"}, {"naive", "naive"}} {
		asked = nil
		link := ShareLink{Kind: "reverse", From: "iran", Tok: "secret", Tr: tc.tr, InnerPort: "3080", Port: "443"}
		var s TunnelSpec
		managedClientFromLink(&s, link, "203.0.113.7")
		err := prepareManagedLink(s, link)
		if len(asked) != 1 || asked[0] != tc.tool || err == nil || !strings.Contains(err.Error(), "installing it failed") {
			t.Fatalf("%s: installed %v, %v", tc.tr, asked, err)
		}
	}
}
