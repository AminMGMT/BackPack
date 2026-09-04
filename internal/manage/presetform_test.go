package manage

import (
	"os"
	"strings"
	"testing"
)

// A preset the build does not know is a refusal on every path that takes one.
//
// ApplyPreset falls back to Turbo for anything it does not recognise, and that
// is right where it is used: a config already on disk naming a preset from a
// later version has to keep loading. It is wrong for a form. Asking for
// "balanced" — one letter off the real name — and being given Turbo without a
// word is a substitution nobody sees until they wonder why a tunnel behaves
// like a profile they did not choose.
//
// The edit path always refused it. Creating did not, and they are the same
// question asked twice.
func TestAFormRefusesAPresetThisBuildDoesNotKnow(t *testing.T) {
	src, err := os.ReadFile("webapi.go")
	if err != nil {
		t.Fatalf("cannot read webapi.go: %v", err)
	}
	body := string(src)

	for _, fn := range []string{"specFromNew", "applyEditTo"} {
		i := strings.Index(body, "func "+fn+"(")
		if i < 0 {
			continue
		}
		f := body[i:]
		if end := strings.Index(f, "\n}\n"); end > 0 {
			f = f[:end]
		}
		if strings.Contains(f, "ApplyPreset(") && !strings.Contains(f, "validPreset(") {
			t.Errorf("%s applies a preset without checking it is one, so an unknown "+
				"name is silently turned into Turbo", fn)
		}
	}

	// And the fallback itself stays, because a stored config must keep loading.
	ps, err := os.ReadFile("preset.go")
	if err != nil {
		t.Fatalf("cannot read preset.go: %v", err)
	}
	if !strings.Contains(string(ps), "preset = PresetTurbo") {
		t.Error("ApplyPreset no longer has a fallback; a config naming a preset this " +
			"build does not know would now apply nothing at all")
	}
}

// The three names a form may use, and the one it may not.
func TestTheKnownPresets(t *testing.T) {
	for _, ok := range []string{PresetBalance, PresetTurbo, PresetAggressive, PresetThroughput} {
		if !validPreset(ok) {
			t.Errorf("%q is not accepted", ok)
		}
	}
	for _, bad := range []string{"balanced", "Balance", "nonsense", "fast"} {
		if validPreset(bad) {
			t.Errorf("%q was accepted as a preset", bad)
		}
	}
}
