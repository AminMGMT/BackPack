package manage

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Every configuration key must reach code that acts on it.
//
// The configuration surface is 109 keys written in three separate places — the
// struct tag in config/, the renderer that writes the TOML, and the panel form
// that collects it — with nothing until now checking that the three agree. Two
// keys have already been found that did not:
//
//   - spoof_sockbuf was validated, written to the file and read back into the
//     form, while the carrier read a different key entirely.
//   - spoof_dst_ip is documented in detail, validated as an IPv4 address,
//     written out and read back — and reaches no code at all.
//
// This is the most corrosive bug shape in this codebase, because the value
// reads back as applied: an operator who sets it sees it set, and then rules it
// out as a cause when they go looking for why nothing changed.
//
// So the invariant is checked rather than remembered. For every `toml:` key
// declared in config/, the Go field it names must be read somewhere outside
// config/ and outside the management layer that renders and validates it —
// which is to say, by something that actually does the thing.

// engineDirs are the places a setting has to reach to be doing anything. The
// management layer is deliberately excluded: writing a key to a file and
// reading it back into a form is exactly the appearance of working that this
// test exists to see through.
var engineDirs = []string{
	"../../cmd",
	"../server",
	"../client",
	"../tunnel",
	"../utils",
	"../web",
	"../socks",
	"../localproxy",
	"../metrics",
	"../optimize",
	"../debugserver",
}

// configSurfaceExceptions are keys that legitimately do not reach engine code,
// each with the reason. An entry here is a decision, not a silence — which is
// the point of an allow-list over a skip.
var configSurfaceExceptions = map[string]string{
	// A label, by design: the engine reads the values a preset expanded into,
	// never the name it came from. config/l3.go says so on the field.
	"preset": "a label recording which profile the values came from; the engine reads the values",

	// Known gap, deliberately left open for now. See idea.md §2.1: the key is
	// offered, validated, written and read back while reaching no carrier code.
	// It is listed here so the test passes on a known state rather than being
	// disabled — remove this line when the key is either wired or removed.
	"spoof_dst_ip": "KNOWN GAP — wired nowhere; see idea.md §2.1, excluded from the current work",
}

var tomlTagRe = regexp.MustCompile(`^\s*([A-Z][A-Za-z0-9_]*)\s+[^\x60]+\x60toml:"([a-z_0-9]+)"`)

func TestEveryConfigKeyReachesCodeThatActsOnIt(t *testing.T) {
	fields := declaredConfigKeys(t)
	if len(fields) < 80 {
		t.Fatalf("only %d config keys found; the parser has probably stopped matching "+
			"the struct tags and this test is no longer checking anything", len(fields))
	}

	engine := readTree(t, engineDirs)

	var unreached []string
	for key, field := range fields {
		if _, ok := configSurfaceExceptions[key]; ok {
			continue
		}
		if !readsField(engine, field) {
			unreached = append(unreached, key+" (field "+field+")")
		}
	}
	if len(unreached) > 0 {
		t.Errorf("these configuration keys are declared but no engine code reads their field.\n"+
			"Each one is a setting an operator can fill in that does nothing:\n  %s\n"+
			"Wire it, remove it, or — if it genuinely belongs nowhere else — add it to "+
			"configSurfaceExceptions with the reason.",
			strings.Join(unreached, "\n  "))
	}
}

// The exceptions list must not rot: an entry for a key that no longer exists is
// a note nobody will ever read again, and one for a key that has since been
// wired is a hole in the check.
func TestTheConfigSurfaceExceptionsAreStillNeeded(t *testing.T) {
	fields := declaredConfigKeys(t)
	engine := readTree(t, engineDirs)

	for key, why := range configSurfaceExceptions {
		field, ok := fields[key]
		if !ok {
			t.Errorf("configSurfaceExceptions lists %q (%s) but no config key by that "+
				"name exists any more; remove the entry", key, why)
			continue
		}
		if readsField(engine, field) && key != "preset" {
			t.Errorf("configSurfaceExceptions still excuses %q (%s), but engine code reads "+
				"%s now; remove the entry so the key is checked like every other",
				key, why, field)
		}
	}
}

// declaredConfigKeys maps every toml key in config/ to the Go field it names.
func declaredConfigKeys(t *testing.T) map[string]string {
	t.Helper()
	out := map[string]string{}
	paths, err := filepath.Glob("../../config/*.go")
	if err != nil || len(paths) == 0 {
		t.Fatalf("cannot find the config package: %v", err)
	}
	for _, p := range paths {
		if strings.HasSuffix(p, "_test.go") {
			continue
		}
		body, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("cannot read %s: %v", p, err)
		}
		for _, line := range strings.Split(string(body), "\n") {
			if m := tomlTagRe.FindStringSubmatch(line); m != nil {
				out[m[2]] = m[1]
			}
		}
	}
	return out
}

// readTree concatenates the non-test source under each directory, recursively.
func readTree(t *testing.T, dirs []string) string {
	t.Helper()
	var b strings.Builder
	for _, dir := range dirs {
		err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() {
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			body, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			b.Write(body)
			b.WriteByte('\n')
			return nil
		})
		if err != nil {
			t.Fatalf("cannot read %s: %v", dir, err)
		}
	}
	return b.String()
}

// readsField reports whether the source selects that field off something.
// Deliberately textual: the question is "does any of this code mention it",
// which a type-aware walk would answer identically at far greater cost.
func readsField(src, field string) bool {
	return strings.Contains(src, "."+field)
}
