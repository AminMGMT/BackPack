package webui

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strings"
	"testing"

	"github.com/backpack/backpack/internal/manage"
)

// The setup and edit forms and the structs they post to are two halves of one
// contract, and the request decoder rejects unknown fields — so a field renamed
// on one side and not the other does not degrade, it fails the whole form. The
// tests here read the page's own field tables and check every key against the
// struct it ends up in.

var fieldKeyRe = regexp.MustCompile(`\{[^{}]*\bk:'([A-Za-z0-9_]+)'`)

// fieldKeys pulls the keys out of one of the page's field tables.
func fieldKeys(t *testing.T, name string) []string {
	t.Helper()
	body := string(dashboardHTML)
	start := strings.Index(body, "const "+name+"=[")
	if start < 0 {
		t.Fatalf("the page has no %s table", name)
	}
	end := strings.Index(body[start:], "\n];")
	if end < 0 {
		t.Fatalf("%s is not terminated", name)
	}
	var keys []string
	for _, m := range fieldKeyRe.FindAllStringSubmatch(body[start:start+end], -1) {
		keys = append(keys, m[1])
	}
	if len(keys) == 0 {
		t.Fatalf("%s has no fields", name)
	}
	return keys
}

// decodesInto posts the keys as a JSON object and checks the struct takes every
// one of them, the same way the handler does.
func decodesInto(t *testing.T, keys []string, v any, skip ...string) {
	t.Helper()
	drop := map[string]bool{}
	for _, s := range skip {
		drop[s] = true
	}
	obj := map[string]any{}
	for _, k := range keys {
		if !drop[k] {
			obj[k] = nil // the value does not matter; the name does
		}
	}
	raw, _ := json.Marshal(obj)
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		t.Errorf("the form posts a field the server would refuse: %v", err)
	}
}

// Fine Tune has been the two halves of this contract for longer than the rest,
// and it is the one drawer whose every field is a number the tunnel runs on.
func TestFineTuneFieldsReachTheServer(t *testing.T) {
	decodesInto(t, fieldKeys(t, "FT_FIELDS"), &manage.FineTune{})
}

// The spoof drawer is the CLI's askSpoof plus the obfuscation knobs that used
// to need the config file edited by hand.
func TestSpoofDrawerFieldsReachTheServer(t *testing.T) {
	decodesInto(t, fieldKeys(t, "SPOOF_FIELDS"), &manage.SpoofTune{})
}

func TestPacketCarrierFieldsReachTheServer(t *testing.T) {
	decodesInto(t, fieldKeys(t, "PCK_FIELDS"), &manage.PckTune{})
}

// The connection drawer posts as two objects: everything about reaching the
// other end, and the two caps. The page splits them on LIMIT_KEYS, so the split
// is what is checked here.
func TestConnectionDrawerFieldsReachTheServer(t *testing.T) {
	keys := fieldKeys(t, "CONN_FIELDS")
	limits := limitKeys(t)
	decodesInto(t, keys, &manage.ConnTune{}, limits...)

	var inLimits []string
	for _, k := range keys {
		for _, l := range limits {
			if k == l {
				inLimits = append(inLimits, k)
			}
		}
	}
	if len(inLimits) != len(limits) {
		t.Errorf("the page splits off %v, but the drawer only has %v", limits, inLimits)
	}
	decodesInto(t, inLimits, &manage.TunnelLimits{})
}

func limitKeys(t *testing.T) []string {
	t.Helper()
	body := string(dashboardHTML)
	m := regexp.MustCompile(`const LIMIT_KEYS=\[([^\]]*)\]`).FindStringSubmatch(body)
	if m == nil {
		t.Fatal("the page does not say which fields are limits")
	}
	var out []string
	for _, part := range strings.Split(m[1], ",") {
		if k := strings.Trim(strings.TrimSpace(part), "'\""); k != "" {
			out = append(out, k)
		}
	}
	return out
}

// The two sides have opposite jobs with the token: the Iran server invents it
// and hands it over, the kharej client is given it. A Copy button on the kharej
// form copies the empty box it was supposed to help fill, which is the bug this
// pins shut.
func TestTokenButtonPastesOnTheKharejSide(t *testing.T) {
	body := string(dashboardHTML)
	if !strings.Contains(body, `id="af-tokenbtn" onclick="tokenAction(this)"`) {
		t.Error("the token button is not wired to the side-aware handler")
	}
	pick := between(body, "async function pickSide(role){", "\n}")
	if pick == "" {
		t.Fatal("pickSide could not be found")
	}
	if !strings.Contains(pick, `role==='server'?'Copy':'Paste'`) {
		t.Error("pickSide does not turn the token button into Paste on the kharej side")
	}
	for _, fn := range []string{"function pasteField(", "function tokenAction("} {
		if !strings.Contains(body, fn) {
			t.Errorf("the page has no %s", fn)
		}
	}
}

// Both forms have to carry all three drawers, or a setting can be chosen when a
// tunnel is built and never changed again — which is what sent people back to
// the CLI.
func TestBothFormsCarryEveryDrawer(t *testing.T) {
	body := string(dashboardHTML)
	for _, p := range []string{"a", "e"} {
		for _, d := range []string{"sp", "pk", "cn"} {
			for _, tmpl := range []string{`id="acc-%s%s"`, `id="accb-%s%s"`, `id="acch-%s%s"`, `id="accnote-%s%s"`} {
				want := strings.Replace(strings.Replace(tmpl, "%s", p, 1), "%s", d, 1)
				if !strings.Contains(body, want) {
					t.Errorf("missing %s", want)
				}
			}
		}
	}
	for _, box := range []string{`id="af-spoof"`, `id="af-pck"`, `id="af-conn"`,
		`id="ef-spoof"`, `id="ef-pck"`, `id="ef-conn"`} {
		if !strings.Contains(body, box) {
			t.Errorf("missing drawer body %s", box)
		}
	}
	// Real client IP is the CLI's Edit entry for the PROXY protocol; the panel
	// could only set it when the tunnel was created.
	if !strings.Contains(body, `id="ef-pp"`) {
		t.Error("the edit form cannot turn the PROXY protocol on or off")
	}
}
