package webui

import (
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"
)

// Edit on a direct tunnel used to open the reverse form and post its fields at
// the top of the request, where the direct edit reads nothing — so it showed
// the wrong dialog and could not save. The direct form posts under "direct",
// and every key it can send has to be one the server reads: one unknown key
// fails the whole save.
func TestTheDirectEditFormPostsWhatTheServerReads(t *testing.T) {
	src, err := os.ReadFile("panel/js/views/edit.js")
	if err != nil {
		t.Fatal(err)
	}
	js := string(src)
	if !strings.Contains(js, "direct: readDirect(root)") {
		t.Fatal("the direct form no longer posts under \"direct\"")
	}
	start := strings.Index(js, "function directMarkup(")
	end := strings.Index(js, "const DIRECT_NUMBERS")
	if start < 0 || end < start {
		t.Fatal("edit.js no longer has the direct form")
	}
	markup := js[start:end]
	var keys []string
	for _, m := range regexp.MustCompile(`(?:field|sw)\('([A-Za-z]+)'`).FindAllStringSubmatch(markup, -1) {
		keys = append(keys, m[1])
	}
	keys = append(keys, "preset") // the menu
	if len(keys) < 8 {
		t.Fatalf("found only %v in the direct form", keys)
	}

	var parts []string
	for _, k := range keys {
		v := `1`
		switch k {
		case "ports", "preset":
			v = `"x"`
		case "acceptUdp", "autoMtu", "fec", "stealth":
			v = `true`
		}
		parts = append(parts, `"`+k+`":`+v)
	}
	body := `{"name":"t","direct":{` + strings.Join(parts, ",") + `}}`
	var req tunnelEditRequest
	r := httptest.NewRequest("POST", "/api/tunnel/edit", strings.NewReader(body))
	if err := decodeJSON(httptest.NewRecorder(), r, &req); err != nil {
		t.Fatalf("the server refuses what the direct form sends: %v\n%s", err, body)
	}
	if req.Direct.MTU == nil || req.Direct.Ports == nil || req.Direct.FEC == nil {
		t.Errorf("the direct fields did not arrive: %+v", req.Direct)
	}
}
