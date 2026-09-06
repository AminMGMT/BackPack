package webui

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/backpack/backpack/internal/manage"
	"github.com/backpack/backpack/internal/node"
)

// A name from a managed server must not be able to carry markup into the panel.
//
// The panel lists a managed server's tunnels by reading that server's config
// directory, and those names are filenames. On Linux a filename holds anything
// but "/" and NUL, so the far machine can call a tunnel
// `<img src=x onerror=...>` — and the panel put that name into a confirmation
// dialog that assigned it to innerHTML. It ran, in the operator's session, on
// the origin holding every other server's root password.
//
// Three things stop it now and each is checked on its own, because each has to
// hold without the others: the name is refused at the boundary, it is not
// stored if it somehow arrives, and the sink escapes whatever it is given.

// The far side's answer is filtered on arrival.
func TestAFarSideNameThatCouldNotBeCreatedHereIsRefused(t *testing.T) {
	isolateFleet(t)
	s := newFleetServer()
	t.Cleanup(s.nodes.stop)

	r := newFake()
	r.up["germany"] = true
	r.answers[node.OpList] = []node.TunnelState{
		{Name: `<img src=x onerror=alert(1)>`, Role: "client", TunnelPort: "3454"},
		{Name: "quote\"inside", Role: "client", TunnelPort: "3454"},
		{Name: "ordinary-name", Role: "client", TunnelPort: "3454"},
	}
	s.nodes.run = r

	got, err := farEndsOn(r, "germany")
	if err != nil {
		t.Fatalf("farEndsOn: %v", err)
	}
	if len(got) != 1 || got[0].Name != "ordinary-name" {
		var names []string
		for _, g := range got {
			names = append(names, g.Name)
		}
		t.Errorf("the panel accepted %v from a managed server — a name it would "+
			"refuse to create here is one it must refuse to take from elsewhere", names)
	}
}

// And it is not stored, which matters because the stored one is read back on
// every poll long after the connection it came over is gone.
func TestAPeerNameIsNotStoredUnlessItCouldHaveBeenCreatedHere(t *testing.T) {
	isolateFleet(t)

	if err := manage.NoteNodePair("local", "germany", `<script>alert(1)</script>`); err != nil {
		t.Fatalf("NoteNodePair: %v", err)
	}
	pair, ok := manage.PairFor("local")
	if !ok {
		t.Fatal("the pairing itself was dropped — the tunnel's other end really is " +
			"on that server, and losing the record takes edits, logs and the speed " +
			"test with it")
	}
	if pair.PeerName != "" {
		t.Errorf("stored %q as the far end's name", pair.PeerName)
	}
	if pair.Node != "germany" {
		t.Errorf("the server was recorded as %q", pair.Node)
	}

	// An ordinary name is kept, or this test would pass on a function that
	// dropped everything.
	if err := manage.NoteNodePair("other", "germany", "nuremberg-kharej"); err != nil {
		t.Fatalf("NoteNodePair: %v", err)
	}
	if p, _ := manage.PairFor("other"); p.PeerName != "nuremberg-kharej" {
		t.Errorf("an ordinary far-end name was not kept: %q", p.PeerName)
	}
}

// The sink escapes, whatever reaches it. This is the layer that has to hold
// when somebody adds a third path the two above do not cover.
func TestTheConfirmDialogEscapesTheValuesItIsGiven(t *testing.T) {
	loadPanel()

	src, err := fs.ReadFile(panelRoot, "js/ui/confirm.js")
	if err != nil {
		t.Fatalf("confirm.js: %v", err)
	}
	js := string(src)
	if !strings.Contains(js, "esc(l.text)") {
		t.Error("the confirmation dialog interpolates a line into innerHTML without " +
			"escaping it. Every caller escaped its title and none escaped its lines, " +
			"which is what a convention kept in the callers rather than the sink " +
			"always becomes.")
	}
}

// Nothing may put a bare variable into a confirmation's title either.
//
// That title takes markup on purpose — the callers pass the tags and wrap the
// value — so it is escaped one level down rather than at the sink, and the
// check is that every caller actually remembers. Only confirmBox's title is
// examined: el(tag, {title: v}) is an ordinary attribute set with
// setAttribute, which does not parse HTML and needs no escaping.
func TestEveryDialogTitleEscapesItsVariables(t *testing.T) {
	loadPanel()

	// A confirmBox call, and the title inside it.
	callRe := regexp.MustCompile(`(?s)confirmBox\(\{.*?\}\)`)
	titleRe := regexp.MustCompile("title: `([^`]*)`")
	bare := regexp.MustCompile(`\$\{(?:esc\()?([^}]*)\}`)

	err := fs.WalkDir(panelRoot, "js", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".js") {
			return err
		}
		src, err := fs.ReadFile(panelRoot, p)
		if err != nil {
			return err
		}
		for _, call := range callRe.FindAllString(string(src), -1) {
			for _, m := range titleRe.FindAllStringSubmatch(call, -1) {
				for _, v := range bare.FindAllString(m[1], -1) {
					if !strings.HasPrefix(v, "${esc(") {
						t.Errorf("%s: a confirmation's title interpolates %s unescaped, "+
							"and that title is assigned to innerHTML:\n  title: `%s`", p, v, m[1])
					}
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking the panel: %v", err)
	}
}

// The policy that is supposed to be the last line under a missed escape must
// not permit the thing the escape was there to stop.
func TestTheContentSecurityPolicyDoesNotAllowInlineScript(t *testing.T) {
	rec := httptest.NewRecorder()
	withPanelSecurity(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})).
		ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))

	csp := rec.Header().Get("Content-Security-Policy")
	script := ""
	for _, part := range strings.Split(csp, ";") {
		if strings.HasPrefix(strings.TrimSpace(part), "script-src") {
			script = strings.TrimSpace(part)
		}
	}
	if script == "" {
		t.Fatalf("no script-src in the policy: %q", csp)
	}
	if strings.Contains(script, "'unsafe-inline'") {
		t.Errorf("script-src allows inline script, so an injected onerror= runs and "+
			"the policy protects nothing it is there for: %q", script)
	}
	if !strings.Contains(script, "'nonce-") {
		t.Errorf("script-src carries no nonce, so the panel's own inline script "+
			"cannot run either: %q", script)
	}
}

// The nonce is per-response and reaches the page, or the panel does not work.
func TestTheNonceIsFreshAndReachesThePage(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 3; i++ {
		rec := httptest.NewRecorder()
		var page []byte
		withPanelSecurity(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			page = withNonce([]byte(`<script nonce="`+noncePlaceholder+`">x</script>`), r)
		})).ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))

		csp := rec.Header().Get("Content-Security-Policy")
		m := regexp.MustCompile(`'nonce-([^']+)'`).FindStringSubmatch(csp)
		if m == nil {
			t.Fatalf("no nonce in %q", csp)
		}
		if seen[m[1]] {
			t.Error("the same nonce was served twice — one an attacker can read from " +
				"an earlier response and reuse is a nonce in name only")
		}
		seen[m[1]] = true

		if !strings.Contains(string(page), `nonce="`+m[1]+`"`) {
			t.Errorf("the page does not carry the header's nonce, so its own script "+
				"is blocked:\n  header: %s\n  page:   %s", m[1], page)
		}
		if strings.Contains(string(page), noncePlaceholder) {
			t.Error("the placeholder was served to the browser")
		}
	}
}

// Both pages that ship an inline script have to be stamped, and no template may
// reintroduce an inline handler that the nonce cannot cover.
func TestTheServedPagesCarryTheNoncePlaceholder(t *testing.T) {
	loadPanel()

	index, err := fs.ReadFile(panelRoot, "index.html")
	if err != nil {
		t.Fatalf("index.html: %v", err)
	}
	if strings.Contains(string(index), "<script>") {
		t.Error("the panel shell has an inline script with no nonce on it — under " +
			"this policy it will not run")
	}
	if !strings.Contains(string(index), noncePlaceholder) {
		t.Error("the panel shell carries no nonce placeholder")
	}
	if strings.Contains(string(loginHTML), "<script>") {
		t.Error("the login page has an inline script with no nonce on it")
	}
	// Markup only: the page's own script may mention the word in a comment.
	markup := regexp.MustCompile(`(?s)<script.*?</script>`).ReplaceAllString(string(loginHTML), "")
	if regexp.MustCompile(`\son[a-z]+="`).MatchString(markup) {
		t.Error("the login page has an inline handler, which no nonce can cover")
	}
}
