package webui

import (
	"io/fs"
	"os"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/backpack/backpack/internal/manage"
)

// The setup form against the structures it posts into.
//
// Every field in the new panel's Add tunnel ends up as a key in NewTunnel,
// NewDirectTunnel or one of their drawers, and the create handlers decode with
// unknown keys refused — so a name the form invents fails the whole submission
// with a decoding error, and a field the form never offers is a setting the
// panel simply cannot reach. Neither shows up as a broken build or a failing
// request in development; the first needs a real create to surface and the
// second needs somebody to notice an absence.
//
// So the two are pinned to each other here. This found real gaps when it was
// written: automatic failover, which the CLI wizard has always asked and the
// panel could not set; the whole spoof drawer, which sat in the reverse section
// gated on a transport that no longer exists and was therefore unreachable; and
// stealth, FEC and multipath on a direct tunnel, which were never offered at
// all.

// formNames is how the panel decides what each control posts.
//
// Three conventions, and the test has to know all three because the markup uses
// all three: a name attribute, an explicit data-name, and — for the controls
// the preview drew as bare divs and inputs — a "<drawer>-<field>" id. See
// wireDrawerControls in js/views/add.js, which is the code this mirrors.
func formNames(t *testing.T) map[string]bool {
	t.Helper()
	loadPanel()
	raw, err := fs.ReadFile(panelRoot, "views/add.html")
	if err != nil {
		t.Fatalf("reading add.html: %v", err)
	}
	html := string(raw)

	group := map[string]string{"ft": "tune.", "sp": "spoof.", "pk": "pck.", "cn": "conn."}
	out := map[string]bool{}

	for _, m := range regexp.MustCompile(`name="([^"]+)"`).FindAllStringSubmatch(html, -1) {
		out[m[1]] = true
	}
	for _, m := range regexp.MustCompile(`data-name="([^"]+)"`).FindAllStringSubmatch(html, -1) {
		out[m[1]] = true
	}
	// Bare divs and unnamed inputs take their name from their id.
	for _, m := range regexp.MustCompile(`<(?:div|input)([^>]*id="([a-z]{2})-([A-Za-z0-9]+)"[^>]*)>`).
		FindAllStringSubmatch(html, -1) {
		if strings.Contains(m[1], "name=") {
			continue
		}
		if pfx, ok := group[m[2]]; ok {
			out[pfx+m[3]] = true
		}
	}
	return out
}

// jsonTags returns the keys a structure accepts, prefixed, skipping the nested
// drawers themselves — a drawer is not a field the form fills, its members are.
func jsonTags(prefix string, v any) map[string]string {
	out := map[string]string{}
	rt := reflect.TypeOf(v)
	for i := 0; i < rt.NumField(); i++ {
		f := rt.Field(i)
		tag := strings.Split(f.Tag.Get("json"), ",")[0]
		if tag == "" || tag == "-" {
			continue
		}
		ft := f.Type
		for ft.Kind() == reflect.Ptr {
			ft = ft.Elem()
		}
		if ft.Kind() == reflect.Struct {
			continue // a drawer; its own fields are checked separately
		}
		out[prefix+tag] = rt.Name() + "." + f.Name
	}
	return out
}

// chosenInJS are answers the operator gives by pressing something rather than
// filling something in, and the view writes them onto the payload itself.
var chosenInJS = map[string]bool{
	"role": true, "transport": true, "side": true, "carrier": true,
	"preset": true, "name": true,
}

func TestTheSetupFormMatchesWhatTheServerAccepts(t *testing.T) {
	names := formNames(t)

	accepts := map[string]string{}
	for _, s := range []struct {
		prefix string
		v      any
	}{
		{"", manage.NewTunnel{}},
		{"", manage.NewDirectTunnel{}},
		{"tune.", manage.FineTune{}},
		{"pck.", manage.PckTune{}},
		{"conn.", manage.ConnTune{}},
		{"limits.", manage.TunnelLimits{}},
		{"spoof.", manage.SpoofTune{}},
	} {
		for k, v := range jsonTags(s.prefix, s.v) {
			accepts[k] = v
		}
	}

	var invented, missing []string
	for n := range names {
		if _, ok := accepts[n]; !ok {
			invented = append(invented, n)
		}
	}
	for n, owner := range accepts {
		if !names[n] && !chosenInJS[n] {
			missing = append(missing, n+" ("+owner+")")
		}
	}
	sort.Strings(invented)
	sort.Strings(missing)

	for _, n := range invented {
		t.Errorf("the form posts %q, which no setup structure has — the create "+
			"handler refuses unknown keys, so this fails the whole submission", n)
	}
	for _, n := range missing {
		t.Errorf("%s is a setting the server accepts and the form never offers — "+
			"it cannot be reached from the panel at all", n)
	}
}

// The edit form against what the edit endpoint accepts.
//
// Same rule as the setup form and for the same reason — the handler decodes
// with unknown keys refused, so a name this form invents fails the whole save
// with a decoding error, and one it never offers is a setting that cannot be
// changed from the panel.
//
// The edit form is the more dangerous of the two: a setup form that will not
// submit is noticed immediately, and a save that quietly does nothing is not.
func TestTheEditFormMatchesWhatTheServerAccepts(t *testing.T) {
	loadPanel()
	raw, err := fs.ReadFile(panelRoot, "views/edit.html")
	if err != nil {
		t.Fatalf("reading edit.html: %v", err)
	}
	html := string(raw)

	names := map[string]bool{}
	// (?:^|\s) so that data-name="x" is not also read as name="x".
	for _, m := range regexp.MustCompile(`(?:^|\s)name="([^"]+)"`).FindAllStringSubmatch(html, -1) {
		names[m[1]] = true
	}
	for _, m := range regexp.MustCompile(`data-name="([^"]+)"`).FindAllStringSubmatch(html, -1) {
		// A control prefixed __ drives the form rather than the payload.
		if !strings.HasPrefix(m[1], "__") {
			names[m[1]] = true
		}
	}

	accepts := map[string]string{}
	for _, s := range []struct {
		prefix string
		v      any
	}{
		{"", manage.TunnelEdit{}},
		{"", manage.DirectEdit{}},
		{"tune.", manage.FineTune{}},
		{"pck.", manage.PckTune{}},
		{"conn.", manage.ConnTune{}},
		{"limits.", manage.TunnelLimits{}},
		{"spoof.", manage.SpoofTune{}},
	} {
		for k, v := range jsonTags(s.prefix, s.v) {
			accepts[k] = v
		}
	}
	// The dialog's own header field, and the name the request carries.
	accepts["name"] = "tunnelEditRequest.Name"

	var invented []string
	for n := range names {
		if _, ok := accepts[n]; !ok {
			invented = append(invented, n)
		}
	}
	sort.Strings(invented)
	for _, n := range invented {
		t.Errorf("the edit form posts %q, which no edit structure has — the handler "+
			"refuses unknown keys, so the save fails entirely", n)
	}
}

// The setup form posts JSON, so every field has to arrive as the type its Go
// field is declared with. The screen decides that from one list, and this is
// what keeps the list honest: a new int on NewTunnel or FineTune that nobody
// adds to it would be posted as a string and rejected, and a string field that
// creeps into it would be posted as a number and rejected — which is exactly
// what a numeric-looking tunnel port did.
func TestTheFormSendsNumbersForTheFieldsGoDeclaresNumeric(t *testing.T) {
	src, err := os.ReadFile("panel/js/lib/numeric.js")
	if err != nil {
		t.Fatalf("read numeric.js: %v", err)
	}
	block := regexp.MustCompile(`(?s)const NUMERIC = new Set\(\[(.*?)\]\)`).FindSubmatch(src)
	if block == nil {
		t.Fatal("lib/numeric.js no longer declares a NUMERIC set — the forms are guessing types again")
	}
	declared := map[string]bool{}
	for _, m := range regexp.MustCompile(`'([^']+)'`).FindAllSubmatch(block[1], -1) {
		declared[string(m[1])] = true
	}

	want := map[string]bool{}
	for prefix, v := range map[string]any{
		"":        manage.NewTunnel{},
		"@direct": manage.NewDirectTunnel{},
		"tune.":   manage.FineTune{},
		"limits.": manage.TunnelLimits{},
	} {
		rt := reflect.TypeOf(v)
		for i := 0; i < rt.NumField(); i++ {
			f := rt.Field(i)
			name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
			if name == "" || name == "-" {
				continue
			}
			switch f.Type.Kind() {
			case reflect.Int, reflect.Int64, reflect.Uint32, reflect.Uint64:
				if prefix == "@direct" {
					want[name] = true // direct fields sit at the top level
				} else {
					want[prefix+name] = true
				}
			}
		}
	}

	for name := range want {
		if !declared[name] {
			t.Errorf("%s is a number in Go but the form posts it as a string", name)
		}
	}
	for name := range declared {
		if !want[name] {
			t.Errorf("the form posts %s as a number, but Go does not declare it one", name)
		}
	}
}
