package manage

import "testing"

// A point release has to be newer than the version it follows.
//
// Versions were compared on three components, and "1.7.7.5" was split three
// ways — so the fourth part landed inside the last field and was dropped with
// everything after the first non-digit. It parsed as [1 7 7], the same as
// 1.7.7, and nothing anywhere could tell them apart: not the CLI's update
// check, not the panel's banner, and not the "Upgrade to" button on a managed
// server's card, because all three ask newerVersion.
func TestAPointReleaseIsNewerThanTheVersionItFollows(t *testing.T) {
	for _, tc := range []struct {
		remote, local string
		want          bool
	}{
		{"v1.7.7.5", "v1.7.7", true},
		{"v1.7.7", "v1.7.7.5", false},
		{"v1.7.7.5", "v1.7.7.4", true},
		{"v1.7.7.4", "v1.7.7.5", false},
		{"v1.7.7.5", "v1.7.7.5", false},

		// A fourth part of zero is the same version written longer.
		{"v1.7.7.0", "v1.7.7", false},
		{"v1.7.7", "v1.7.7.0", false},

		// The first three still decide when they differ.
		{"v1.8.0", "v1.7.7.5", true},
		{"v1.7.7.5", "v1.8.0", false},
		{"v2.0.0", "v1.9.9.9", true},

		// A pre-release tag on the same numbers is not newer.
		{"v1.7.7.5-beta.1", "v1.7.7.5", false},
	} {
		if got := newerVersion(tc.remote, tc.local); got != tc.want {
			t.Errorf("newerVersion(%q, %q) = %v, want %v", tc.remote, tc.local, got, tc.want)
		}
	}
}

// And the parse keeps all four components rather than folding the tail into
// the third.
func TestEveryComponentOfAVersionIsRead(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want [verParts]int
	}{
		{"v1.7.7.5", [verParts]int{1, 7, 7, 5}},
		{"1.7.7", [verParts]int{1, 7, 7, 0}},
		{"v1.7.7.5-beta.2", [verParts]int{1, 7, 7, 5}},
		{"v2", [verParts]int{2, 0, 0, 0}},
		{"", [verParts]int{0, 0, 0, 0}},
	} {
		if got := parseVer(tc.in); got != tc.want {
			t.Errorf("parseVer(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}
