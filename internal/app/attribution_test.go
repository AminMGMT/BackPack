package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The attribution line lives in the licence documents, not in the product.
//
// AGPL-3.0 lets anybody fork this and publish the fork. Section 7(b) of the
// same licence lets the author require that the attribution be preserved when
// they do, and NOTICE exercises that: a modified version keeps the line in its
// NOTICE and its README. The program itself — the TUI menu, the web panel, the
// version output — does not show it.
const attribution = "Based on BackPack by Amin Mohammadi (AminMGMT)"

// repoRoot is two levels up from internal/app.
func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("cannot resolve the repository root: %v", err)
	}
	return root
}

func read(t *testing.T, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(repoRoot(t), rel))
	if err != nil {
		t.Fatalf("cannot read %s: %v", rel, err)
	}
	return string(b)
}

// NOTICE states the requirement and the READMEs tell a fork where the line goes.
func TestTheAttributionIsInTheLicenceDocuments(t *testing.T) {
	for _, f := range []string{"NOTICE", "README.md", "README_FA.md", "TRADEMARK.md"} {
		if !strings.Contains(read(t, f), attribution) {
			t.Errorf("%s does not carry the attribution %q", f, attribution)
		}
	}
}

// And nowhere a user of the program looks.
func TestTheProductDoesNotShowTheAttribution(t *testing.T) {
	for _, f := range []string{
		"main.go",
		"internal/tui/tui.go",
		"internal/cli/cli.go",
		"internal/webui/assets/login.html",
		"internal/webui/assets/twofactor.html",
		"internal/webui/panel/views/support.html",
	} {
		if strings.Contains(read(t, f), "Based on BackPack") {
			t.Errorf("%s shows the attribution line; it belongs in NOTICE and the README only", f)
		}
	}
}

// The trademark policy is a separate document because it grants nothing and
// restricts nothing about the code — it says the name is not part of what the
// licence hands over. NOTICE points at it, so it has to be there.
func TestTheTrademarkPolicyExistsAndIsPointedAt(t *testing.T) {
	policy := read(t, "TRADEMARK.md")
	for _, want := range []string{"AminMGMT", "AGPL-3.0", "7(e)"} {
		if !strings.Contains(policy, want) {
			t.Errorf("TRADEMARK.md does not mention %q", want)
		}
	}
	notice := read(t, "NOTICE")
	if !strings.Contains(notice, "TRADEMARK.md") {
		t.Error("NOTICE no longer points at TRADEMARK.md, so the trademark term has " +
			"nowhere to be read in full")
	}
	for _, want := range []string{"Section 7", "7(b)", "7(e)"} {
		if !strings.Contains(notice, want) {
			t.Errorf("NOTICE no longer cites %q; the additional terms are only "+
				"permitted because that section allows them, and saying so is what "+
				"distinguishes them from a further restriction section 7 forbids", want)
		}
	}
}

// The licence itself must stay exactly AGPL-3.0. The additional terms live in
// NOTICE precisely so that LICENSE is the unmodified text — editing it would
// make this something other than AGPL-3.0, which is not ours to do: part of the
// data plane derives from prior AGPL/GPL work.
func TestTheLicenceTextIsUnmodifiedAGPL(t *testing.T) {
	l := read(t, "LICENSE")
	if !strings.Contains(l, "GNU AFFERO GENERAL PUBLIC LICENSE") ||
		!strings.Contains(l, "Version 3, 19 November 2007") {
		t.Fatal("LICENSE is not the AGPL-3.0 text")
	}
	// A sanity check on length: the real text is ~34 KB. A truncated or edited
	// licence is a licence nobody can rely on.
	if len(l) < 30000 {
		t.Errorf("LICENSE is %d bytes; the AGPL-3.0 text is around 34,000", len(l))
	}
	if strings.Contains(l, "Based on BackPack") {
		t.Error("the attribution term has been written into LICENSE. It belongs in " +
			"NOTICE: section 7 permits additional terms alongside the licence, not " +
			"edits to it, and editing the text would make this a different licence")
	}
}
