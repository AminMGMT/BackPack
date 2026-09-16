package webui

import (
	"io/fs"
	"regexp"
	"strings"
	"testing"
)

// The Settings rail is five one-line summaries, and it was drawn with five
// invented ones.
//
// The markup is lifted from the approved design preview, so every subtitle
// under it arrived as sample text: "1.7.6 available", "Port 8443 · Let's
// Encrypt", "2FA on · 2 devices" on a panel that has no two-factor at all, and
// "Yesterday 03:00". Those are not placeholders that look like placeholders —
// they are specific, plausible claims about this machine, and an operator has
// no way to tell them from a reading.
//
// They are rewritten from real data now. What matters for this test is the case
// where there is no real data: the write used to be skipped, and skipping it is
// what leaves the invented line on screen. The one moment the panel knows least
// is the moment it was most confident.
func TestTheSettingsRailNeverLeavesTheDesignPreviewsText(t *testing.T) {
	loadPanel()
	src := jsCode(panelSource(t, "js/views/settings.js"))

	// The writer must not be conditional on having something to say.
	if regexp.MustCompile(`if \(line && text\)`).MatchString(src) {
		t.Error("summarise only writes a rail line when it has data, so a failed read " +
			"leaves the design preview's invented text in place")
	}
	if !strings.Contains(src, "could not be read") {
		t.Error("a rail line with no data has nothing honest to fall back to")
	}
}

// The port on that rail is the port the panel is served on, which the stats
// payload does not carry and never did.
func TestTheSettingsRailReadsThePortFromSomethingThatHasOne(t *testing.T) {
	loadPanel()
	src := jsCode(panelSource(t, "js/views/settings.js"))

	if strings.Contains(src, "stats?.panelPort") || strings.Contains(src, "stats.panelPort") {
		t.Error("the rail reads stats.panelPort, and the stats payload has no such " +
			"field — the port half of that line was always undefined")
	}
	if !strings.Contains(src, "api.panelCertRead()") {
		t.Error("the rail no longer reads the certificate endpoint, which is the one " +
			"that knows the port and the certificate mode")
	}
}

// Nothing else on the screen may assert a fact it does not have.
func TestTheSettingsFooterDoesNotDescribeADrawing(t *testing.T) {
	loadPanel()
	js := jsCode(panelSource(t, "js/views/settings.js"))
	if !strings.Contains(js, ".df .note") {
		t.Error("the settings footer note is never written, so it keeps the preview's " +
			"\"the two marked with a dot differ from the default\" — nothing on this " +
			"screen marks anything with a dot")
	}
}

// jsCode strips comments, so a check reads what the file does rather than what
// it says about what it used to do.
func jsCode(src string) string {
	var out []string
	block := false
	for _, line := range strings.Split(src, "\n") {
		t := strings.TrimSpace(line)
		if block {
			if strings.Contains(t, "*/") {
				block = false
			}
			continue
		}
		if strings.HasPrefix(t, "//") {
			continue
		}
		if strings.HasPrefix(t, "/*") {
			if !strings.Contains(t, "*/") {
				block = true
			}
			continue
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

func panelSource(t *testing.T, name string) string {
	t.Helper()
	b, err := fs.ReadFile(panelRoot, name)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return string(b)
}

// The direct form's suggestion has to land in the field, not only be fetched.
//
// /api/direct/defaults was registered and never called at all, which is the
// finding. Calling it is half the fix: this bind rearranges the form into five
// steps after the template loads, so the field that exists when the answer
// arrives is not necessarily the one the operator types into. A lookup tied to
// the container the template put the field in is a lookup that stops matching
// as soon as the field is moved.
func TestTheDirectSuggestionIsNotTiedToWhereTheTemplatePutTheField(t *testing.T) {
	loadPanel()
	src := jsCode(panelSource(t, "js/views/add.js"))

	if !strings.Contains(src, "api.directDefaults(") {
		t.Fatal("the add form never asks for its suggested values")
	}
	if strings.Contains(src, `.step3direct [name=`) {
		t.Error("the suggestion is looked up under .step3direct, which this bind moves " +
			"the fields out of — so it matches before the restructure and not after")
	}
	// Applied on every shape change rather than once on the answer, so it lands
	// whenever the field appears.
	if !strings.Contains(src, "applySuggestion()") {
		t.Error("the suggestion is applied once, at whatever moment the answer arrives")
	}
	// And only into an empty field: overwriting what somebody typed because
	// they clicked something else is worse than not suggesting at all.
	if !strings.Contains(src, "if (!f.value) f.value = value") {
		t.Error("the suggestion can overwrite a value the operator typed")
	}
	// Only the two fields the endpoint exists for. The preset is a row of
	// buttons and the tunnel port exists on both shapes of the form.
	if !strings.Contains(src, "SUGGESTED_FIELDS = ['localIp', 'peerIp']") {
		t.Error("the suggestion reaches past the fields it is for")
	}
}
