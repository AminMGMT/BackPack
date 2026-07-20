package manage

import "testing"

func TestIsPrerelease(t *testing.T) {
	cases := map[string]bool{
		"v1.5.0":         false,
		"1.5.0":          false,
		"v1.5.0-beta.1":  true,
		"v1.6.0-rc1":     true,
		"v2.0.0-alpha":   true,
		"v1.5.0+build.7": true,
		" v1.5.0 ":       false,
	}
	for tag, want := range cases {
		if got := isPrerelease(tag); got != want {
			t.Errorf("isPrerelease(%q) = %v, want %v", tag, got, want)
		}
	}
}

// releaseList is the shape of a GitHub /releases response, newest first.
const releaseList = `[
  {"tag_name": "v1.6.0-beta.2", "prerelease": true},
  {"tag_name": "v1.5.0", "prerelease": false},
  {"tag_name": "v1.4.0", "prerelease": false}
]`

func TestPickTagStableIgnoresPrereleases(t *testing.T) {
	// The stable channel asks /releases/latest, which returns a single stable
	// release. If it ever returns a pre-release anyway, it must be refused
	// rather than installed on someone who chose stable.
	if got := pickTag(`{"tag_name": "v1.5.0"}`, false); got != "v1.5.0" {
		t.Fatalf("pickTag stable = %q, want v1.5.0", got)
	}
	if got := pickTag(`{"tag_name": "v1.6.0-beta.2"}`, false); got != "" {
		t.Fatalf("pickTag stable returned the pre-release %q — it must refuse it", got)
	}
}

func TestPickTagBetaTakesTheNewest(t *testing.T) {
	got := pickTag(releaseList, true)
	if got != "v1.6.0-beta.2" {
		t.Fatalf("pickTag beta = %q, want v1.6.0-beta.2", got)
	}
}

func TestPickTagBetaDoesNotGoBackwards(t *testing.T) {
	// An old pre-release sitting in the list must not beat a newer stable one.
	list := `[
	  {"tag_name": "v1.6.0"},
	  {"tag_name": "v1.5.0-beta.1"}
	]`
	if got := pickTag(list, true); got != "v1.6.0" {
		t.Fatalf("pickTag beta = %q, want v1.6.0 — a stale pre-release must not win", got)
	}
}

func TestPickTagEmptyBody(t *testing.T) {
	if got := pickTag("{}", false); got != "" {
		t.Fatalf("pickTag on a body with no tags = %q, want empty", got)
	}
	if got := pickTag("{}", true); got != "" {
		t.Fatalf("pickTag beta on a body with no tags = %q, want empty", got)
	}
}
