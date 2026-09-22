package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Documentation that says when it was last checked.
//
// Several documents carry figures — a default, a port, a menu path — and a
// figure with no date is read as current no matter how old it is. The stamp is
// the cheapest thing that makes a stale document look stale.
//
// This is in internal/app because that is where the version lives, which is the
// thing the stamp has to agree with.

func TestEveryDocumentSaysWhenItWasVerified(t *testing.T) {
	docs, err := filepath.Glob(filepath.Join("..", "..", "docs", "*.md"))
	if err != nil {
		t.Fatalf("looking for the documents: %v", err)
	}
	if len(docs) < 20 {
		t.Fatalf("found %d documents — this test is looking in the wrong place", len(docs))
	}

	want := "Last verified against Backpack " + Version
	for _, path := range docs {
		b, err := os.ReadFile(path)
		if err != nil {
			t.Errorf("%s: %v", path, err)
			continue
		}
		s := string(b)
		if !strings.Contains(s, "Last verified against Backpack") {
			t.Errorf("%s has no verification stamp. Add one, or a figure in it will be "+
				"read as current however old it is.", filepath.Base(path))
			continue
		}
		if !strings.Contains(s, want) {
			t.Errorf("%s was last verified against an older version than %s. Either "+
				"re-read it and move the stamp, or leave the stamp where it is — "+
				"but this failing means nobody has decided which.", filepath.Base(path), Version)
		}
	}
}

// Every document carries a Persian summary.
//
// The people who run this are Iranian. A page that exists only in English is a
// page a good part of its audience reads with a dictionary open, at the moment
// they are least able to — troubleshooting is read when something is already
// broken.
//
// The summaries are summaries and not translations, deliberately: a full
// translation is a second copy to keep in step, and a second copy of a
// configuration reference that has fallen behind is worse than none. What each
// one carries is the argument of the page — what the thing is, when you want
// it, and what will go wrong — in the language the reader thinks in.
//
// This test is what stops the set going back to being partial. It was 24 of 38
// and the missing 14 were not an oversight anybody could see, because nothing
// listed them.
func TestEveryDocumentHasAPersianSummary(t *testing.T) {
	docs, err := filepath.Glob(filepath.Join("..", "..", "docs", "*.md"))
	if err != nil {
		t.Fatalf("looking for the documents: %v", err)
	}
	if len(docs) < 20 {
		t.Fatalf("found %d documents — this test is looking in the wrong place", len(docs))
	}

	for _, path := range docs {
		b, err := os.ReadFile(path)
		if err != nil {
			t.Errorf("%s: %v", path, err)
			continue
		}
		s := string(b)
		if !strings.Contains(s, "خلاصهٔ فارسی") {
			t.Errorf("%s has no Persian summary. Add one — the people who run this "+
				"read Persian, and a page only they cannot read is a page that does "+
				"not exist for them.", filepath.Base(path))
			continue
		}
		// The block has to be marked right-to-left or it renders as a wall of
		// left-aligned text with the punctuation in the wrong places.
		if !strings.Contains(s, `<div dir="rtl">`) {
			t.Errorf("%s has a Persian summary that is not wrapped in <div dir=\"rtl\">",
				filepath.Base(path))
		}
	}
}
