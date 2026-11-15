package webui

import (
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The cases a comment stripper gets wrong: what looks like a comment inside a
// string, a template or a regular expression, a division that looks like a
// regex, and a line break that automatic semicolon insertion depends on.
func TestMinifyJSKeepsWhatIsNotAComment(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"a = 1; // note\nb = 2;", "a = 1;\nb = 2;"},
		{"x = '// not a comment';", "x = '// not a comment';"},
		{"x = \"/* nor */ this\";", "x = \"/* nor */ this\";"},
		{"x = `a // b ${y /* c */ + 1} d /* e */`;", "x = `a // b ${y + 1} d /* e */`;"},
		{"x = `outer ${`inner ${z} // kept`} end`;", "x = `outer ${`inner ${z} // kept`} end`;"},
		{"r = /\\/\\/[/]x/g.test(s);", "r = /\\/\\/[/]x/g.test(s);"},
		{"q = a / b / c;", "q = a / b / c;"},
		{"if (ok) return /x\\/y/.test(s);", "if (ok) return /x\\/y/.test(s);"},
		{"a = b\n/* gone */\n(c)", "a = b\n(c)"},
		{"let v = x /* inline */ + y;", "let v = x + y;"},
		{"i = a - -b; j = a + +b;", "i = a - -b; j = a + +b;"},
		{"const o = { k: `${a}` }; f(o)", "const o = { k: `${a}` }; f(o)"},
	} {
		got := string(minifyJS([]byte(c.in)))
		// Only comments and spaces may differ; line breaks must match.
		if squash(got) != squash(c.want) || strings.Contains(got, "gone") || strings.Contains(got, "inline") || strings.Contains(got, "note") {
			t.Errorf("minifyJS(%q)\n got %q\nwant %q", c.in, got, c.want)
		}
	}
	// A comment that spanned lines must leave the line break behind.
	if got := string(minifyJS([]byte("a = b\n/* x\n y */\n(c)"))); !strings.Contains(got, "\n") {
		t.Errorf("a multi-line comment took the line break with it: %q", got)
	}
}

// squash drops spaces and tabs, so two forms compare equal when they differ
// only in the whitespace the minifier is allowed to take out.
func squash(s string) string { return strings.NewReplacer(" ", "", "\t", "").Replace(s) }

func TestMinifyCSSAndHTML(t *testing.T) {
	css := string(minifyCSS([]byte("/* why */\n.a  .b {\n  color: red; /* note */\n}\n.c:hover{x:y}")))
	if css != ".a .b{color: red;}.c:hover{x:y}" {
		t.Errorf("minifyCSS = %q", css)
	}
	html := string(minifyHTML([]byte("<div>\n<!-- why -->\n\n<b>x</b> <i>y</i></div>")))
	if strings.Contains(html, "why") || !strings.Contains(html, "<b>x</b> <i>y</i>") {
		t.Errorf("minifyHTML = %q", html)
	}
}

// Every file of the panel, as served, still parses, and the panel's own
// JavaScript tests still pass against the served forms. Skipped without node.
func TestTheServedPanelStillParsesAndPasses(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed")
	}
	dir := t.TempDir()
	var saved, total int
	err = filepath.WalkDir("panel", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		raw, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel := strings.TrimPrefix(filepath.ToSlash(p), "panel/")
		out := minifyAsset(rel, raw)
		total += len(raw)
		saved += len(raw) - len(out)
		dst := filepath.Join(dir, p)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(dst, out, 0o644); err != nil {
			return err
		}
		if strings.HasSuffix(p, ".js") && !strings.Contains(p, "/vendor/") {
			if b, err := exec.Command(node, "--check", dst).CombinedOutput(); err != nil {
				t.Errorf("%s does not parse once minified: %s", p, b)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("served panel: %d of %d bytes saved", saved, total)
	tests, _ := filepath.Glob("paneltest/*.test.js")
	if len(tests) == 0 {
		return
	}
	if err := os.MkdirAll(filepath.Join(dir, "paneltest"), 0o755); err != nil {
		t.Fatal(err)
	}
	args := []string{"--test"}
	for _, f := range tests {
		b, _ := os.ReadFile(f)
		dst := filepath.Join(dir, f)
		if err := os.WriteFile(dst, b, 0o644); err != nil {
			t.Fatal(err)
		}
		args = append(args, dst)
	}
	if b, err := exec.Command(node, args...).CombinedOutput(); err != nil {
		t.Errorf("the panel's tests fail against the served files:\n%s", b)
	}
}
