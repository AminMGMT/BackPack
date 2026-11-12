package webui

import (
	"io/fs"
	"regexp"
	"strings"
	"testing"

	"github.com/backpack/backpack/internal/manage"
)

// The PCK carrier in the Add form.
//
// PCK has no TUN, so the form shows it none of the private network's rows, no
// error correction and no stealth — hidden by data-when, and so not posted —
// and offers a fine-tune drawer for the engine's six pck_* values, which are
// fields of the create request and are sent as numbers or strings to match it.
func TestTheAddFormFitsAPckTunnel(t *testing.T) {
	loadPanel()
	raw, err := fs.ReadFile(panelRoot, "views/add.html")
	if err != nil {
		t.Fatal(err)
	}
	html := string(raw)
	direct := html[strings.Index(html, `class="step3direct"`):]

	// The TUN's rows are marked !pck.
	for _, name := range []string{"localIp", "peerIp", "greKey"} {
		re := regexp.MustCompile(`<div class="f3[^"]*" data-when="([^"]*)">(?:<label>[^<]*</label>)?(?:<div class="hint"[^>]*>[^<]*</div>)?[^"]*?name="` + name + `"`)
		if m := re.FindStringSubmatch(direct); m == nil || m[1] != "!pck" {
			t.Errorf("the %s row is shown on PCK: %q", name, m)
		}
	}
	for _, label := range []string{"Error correction (FEC)", "Stealth"} {
		if !regexp.MustCompile(`data-when="!gre-!pck"><div class="tx"><b>` + regexp.QuoteMeta(label)).MatchString(direct) {
			t.Errorf("%s is offered on PCK, which cannot have it", label)
		}
	}
	// One note says what PCK is and is not.
	if !strings.Contains(direct, `data-when="pck"><div class="fnote">PCK forwards the ports`) {
		t.Error("the form does not say that PCK forwards ports and has no interface")
	}

	// The drawer, with a field for each pck_* key, under the names the request
	// reads.
	drawer := direct[strings.Index(direct, `id="dw-pck"`):]
	drawer = drawer[:strings.Index(drawer, `</div></div></div>`)+len(`</div></div></div>`)]
	for _, name := range []string{"pckConns", "pckKcpSndWnd", "pckKcpRcvWnd", "pckKcpMtu"} {
		if !strings.Contains(drawer, `name="`+name+`"`) {
			t.Errorf("the PCK drawer has no %s field", name)
		}
	}
	for _, name := range []string{"pckKcpMode", "pckKcpBlock"} {
		if !strings.Contains(drawer, `data-name="`+name+`"`) {
			t.Errorf("the PCK drawer has no %s menu", name)
		}
	}

	js, err := fs.ReadFile(panelRoot, "js/views/add.js")
	if err != nil {
		t.Fatal(err)
	}
	src := string(js)
	for _, want := range []string{
		// shown only for PCK, like the spoof drawer is for spoof
		`const pk = drawer('pck')`, `direct && chosen.carrier === 'pck'`,
		// the preset's numbers are the boxes' grey text, and a new preset clears them
		`paintPckDefaults`, `pckPresetShown`,
		// the preset cards describe what they set in the engine
		`['KCP sessions', o.pckConns]`,
		// the menus' choices come from the server
		`directOpts?.pckModes`, `directOpts?.pckBlocks`,
	} {
		if !strings.Contains(src, want) {
			t.Errorf("add.js lacks %q", want)
		}
	}
	num, err := fs.ReadFile(panelRoot, "js/lib/numeric.js")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"pckConns", "pckKcpMtu", "pckKcpSndWnd", "pckKcpRcvWnd"} {
		if !strings.Contains(string(num), "'"+name+"'") {
			t.Errorf("%s is posted as text, which the server's int field refuses", name)
		}
	}
}

// The options the form is built from describe PCK's presets and menus.
func TestThePanelsDirectOptionsDescribePck(t *testing.T) {
	for _, p := range manage.DirectPresets() {
		for _, k := range []string{"pck", "pckConns", "pckMode", "pckSndWnd", "pckRcvWnd", "pckMtu", "pckBuf", "pckCapture"} {
			if p[k] == "" {
				t.Errorf("preset %s has no %s", p["value"], k)
			}
		}
	}
	if len(manage.PckModes()) < 4 || manage.PckBlocks()[0] != "aes" {
		t.Errorf("modes %v, blocks %v", manage.PckModes(), manage.PckBlocks())
	}
	var pck map[string]string
	for _, c := range manage.PanelDirectCarriers() {
		if c["value"] == "pck" {
			pck = c
		}
	}
	if pck == nil || !strings.Contains(pck["desc"], "forwards your ports") {
		t.Errorf("the carrier's description does not say what PCK is now: %v", pck)
	}
}

// Edit: a PCK tunnel gets its own form — no MTU, auto MTU or FEC, and the
// engine's values — and it posts only keys the direct edit reads.
func TestTheEditFormHasAPckPane(t *testing.T) {
	loadPanel()
	js, err := fs.ReadFile(panelRoot, "js/views/edit.js")
	if err != nil {
		t.Fatal(err)
	}
	src := string(js)
	start := strings.Index(src, "function pckMarkup(")
	end := strings.Index(src, "function directMarkup(")
	if start < 0 || end < start {
		t.Fatal("edit.js has no PCK form")
	}
	pane := src[start:end]
	for _, never := range []string{"'mtu'", "'autoMtu'", "'fec'", "'stealth'"} {
		if strings.Contains(pane, never) {
			t.Errorf("the PCK pane offers %s", never)
		}
	}
	for _, want := range []string{"pckConns", "pckKcpMode", "pckKcpSndWnd", "pckKcpRcvWnd", "pckKcpMtu", "pckKcpBlock", "set.pckNotes", "pckEffective"} {
		if !strings.Contains(pane, want) {
			t.Errorf("the PCK pane lacks %s", want)
		}
	}
	for _, want := range []string{"if (set.pck) return pckMarkup(set)", "wirePck(root, settings", "'pckConns', 'pckKcpMtu', 'pckKcpSndWnd', 'pckKcpRcvWnd'"} {
		if !strings.Contains(src, want) {
			t.Errorf("edit.js lacks %q", want)
		}
	}
}
