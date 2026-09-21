package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The attribution has to be present, and it has to be the same everywhere.
//
// AGPL-3.0 lets anybody fork this and publish the fork. Section 7(b) of the
// same licence lets the author require that the attribution be preserved when
// they do, and NOTICE exercises that — naming four places a modified version
// must keep it: NOTICE itself, the README, the program's version/About output,
// and the Appropriate Legal Notices the panel shows its users.
//
// A requirement stated in a file and enforced by nothing is a requirement that
// disappears the first time somebody tidies a template. This is what makes
// removing it a failing build rather than a quiet edit — for a fork, and just
// as importantly for us: a refactor that drops the line from the login page
// would otherwise go unnoticed until somebody looked.
//
// It also keeps the wording in step. Four copies of a sentence drift; this
// asserts they are one sentence.

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

// The constant is the single definition; everything else has to match it.
func TestTheAttributionIsOneSentenceEverywhere(t *testing.T) {
	if Attribution == "" {
		t.Fatal("app.Attribution is empty; every surface below derives from it")
	}
	if !strings.Contains(Attribution, "BackPack") ||
		!strings.Contains(Attribution, "AminMGMT") {
		t.Errorf("app.Attribution = %q, which names neither the project nor its author", Attribution)
	}
	if AttributionURL != "https://github.com/"+RepoOwner+"/"+RepoName {
		t.Errorf("AttributionURL = %q, which does not point at this repository", AttributionURL)
	}

	// Each file that NOTICE names, and the exact text it must carry.
	for _, f := range []struct {
		path, why string
	}{
		{"NOTICE", "NOTICE states the requirement; it has to meet it itself"},
		{"README.md", "the README of a distribution is one of the four places NOTICE names"},
		{"README_FA.md", "the Persian README is a distribution README too"},
		{"internal/webui/assets/login.html", "the first page a network user reaches — AGPL-3.0 section 13"},
		{"internal/webui/panel/views/support.html", "the panel's About screen"},
	} {
		body := read(t, f.path)
		if !strings.Contains(body, Attribution) {
			t.Errorf("%s does not carry the attribution %q.\n  %s\n"+
				"NOTICE requires it under AGPL-3.0 section 7(b). If the wording is being "+
				"changed, change app.Attribution and every one of these together.",
				f.path, Attribution, f.why)
		}
	}
}

// The version output is the fourth place NOTICE names, and the one a bug report
// reaches for. main.go prints it; this asserts it still does.
func TestTheVersionOutputCarriesTheAttribution(t *testing.T) {
	body := read(t, "main.go")
	if !strings.Contains(body, "app.Attribution") {
		t.Error("main.go no longer prints app.Attribution with the version. " +
			"NOTICE names the version output as a place a modified version must keep it.")
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
