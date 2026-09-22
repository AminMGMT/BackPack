package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Errors thrown away where the silence costs something.
//
// Most discarded errors in this codebase are correct and obvious: a goodbye
// byte written to a connection that is closing, a Close on the teardown path, a
// best-effort Telegram message. Nothing is learned by handling those and the
// code reads worse for it.
//
// One pattern is different, and a sweep of all 166 discards found both
// instances of it in code written the same week:
//
//	_ = json.Unmarshal(data, &v)
//
// That turns a *damaged* file into an *empty* one, silently. The two are not
// the same thing and the difference is what an operator needs: an API token
// store read as empty means every token is refused, and the screen then says
// there are none — so the conclusion is "somebody revoked them" rather than
// "the file is corrupt". An audit record read as empty is indistinguishable
// from one nobody wrote to, and being trustworthy after the fact is the entire
// point of keeping it.
//
// Reading a damaged file as empty is usually the right *behaviour*. Doing it
// without a word is not. This guards the pattern rather than the behaviour.
// discardExceptions are decode failures that are legitimately ignored, each
// with the reason. An entry here is a decision, not a silence — which is the
// point of an allow-list over a skip, and the same argument the config-surface
// test makes.
var discardExceptions = map[string]string{
	"internal/telegram/api.go": "an optional description lifted out of an error body; " +
		"every branch below it has a fallback for the empty case, so a failed decode " +
		"produces the generic message rather than losing anything",
	"internal/telegram/diagnose.go": "the same optional description, in the diagnostic " +
		"that explains why the bot cannot reach Telegram",
}

func TestNoSilentlyDiscardedUnmarshal(t *testing.T) {
	root := filepath.Join("..", "..")

	var found []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // an unreadable directory is not this test's business
		}
		if info.IsDir() {
			switch info.Name() {
			case ".git", "dist", "release", "vendor", "node_modules":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		b, rerr := os.ReadFile(path)
		if rerr != nil {
			return nil
		}
		for i, line := range strings.Split(string(b), "\n") {
			trimmed := strings.TrimSpace(line)
			if !strings.HasPrefix(trimmed, "_ = ") && !strings.HasPrefix(trimmed, "_, _ = ") {
				continue
			}
			if !strings.Contains(trimmed, "Unmarshal(") && !strings.Contains(trimmed, "Decode(") {
				continue
			}
			rel, _ := filepath.Rel(root, path)
			rel = filepath.ToSlash(rel)
			if _, allowed := discardExceptions[rel]; allowed {
				continue
			}
			found = append(found, rel+":"+itoa(i+1)+"  "+trimmed)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking the tree: %v", err)
	}

	if len(found) > 0 {
		t.Errorf("a decode failure is being thrown away in %d place(s). That reads a "+
			"damaged file as an empty one, silently — and empty and damaged call for "+
			"completely different things from whoever is looking at it. Handle it, or "+
			"log it and say what the consequence is:\n  %s",
			len(found), strings.Join(found, "\n  "))
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// And the exceptions have to still be needed, or the list becomes a place
// things go to be forgotten.
func TestTheDiscardExceptionsAreStillNeeded(t *testing.T) {
	root := filepath.Join("..", "..")
	for rel, why := range discardExceptions {
		b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			t.Errorf("%s is on the exception list and does not exist: %v", rel, err)
			continue
		}
		if !strings.Contains(string(b), "_ = json.Unmarshal") {
			t.Errorf("%s no longer discards a decode, so its exception can go.\nIt said: %s",
				rel, why)
		}
	}
}
