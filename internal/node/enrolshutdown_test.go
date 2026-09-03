package node

import (
	"os"
	"strings"
	"testing"
)

// The report this exists for, seen first as a CI failure that would not
// reproduce on a laptop:
//
//	--- FAIL: TestRemovingANodeRevokesIt
//	    load: open .../node-agent.json: no such file or directory
//
// It was read as flakiness. It was not. The panel spends the setup token when
// it answers an enrolment, and marks the node online at that moment — before
// the node has read the reply, let alone written the key to disk. A shutdown
// arriving in that window closed the connection under the node, so the reply
// was never read and the credential never saved. The token was spent, the key
// was gone, and the node could never enrol again: bricked by being stopped at
// the wrong instant, which on a loaded machine is not a rare instant at all.
//
// The fix is that shutdown waits for identify to finish. This guards the
// ordering, because the failure it prevents is invisible until somebody's node
// is already locked out.
func TestShutdownDoesNotInterruptEnrolment(t *testing.T) {
	src, err := os.ReadFile("agent.go")
	if err != nil {
		t.Skipf("cannot read agent.go: %v", err)
	}
	body := funcBody(t, string(src), "session")

	cancel := strings.Index(body, "case <-ctx.Done():")
	if cancel < 0 {
		t.Fatal("session no longer closes the connection when the context is cancelled")
	}
	closeMux := strings.Index(body[cancel:], "mux.Close()")
	if closeMux < 0 {
		t.Fatal("the cancellation branch no longer closes the session")
	}
	if !strings.Contains(body[cancel:cancel+closeMux], "<-identified") {
		t.Error("shutdown tears the connection down without waiting for identify: a " +
			"stop arriving between the panel spending the setup token and SaveAgent " +
			"writing the key leaves the node with no credential and no way to enrol again")
	}

	// And the wait has to be released whatever identify does, or a refused
	// enrolment would hang the shutdown until the auth deadline.
	ident := strings.Index(body, "a.identify(mux)")
	if ident < 0 {
		t.Fatal("session no longer calls identify")
	}
	after := body[ident:]
	release := strings.Index(after, "close(identified)")
	fail := strings.Index(after, "return err")
	if release < 0 || (fail >= 0 && fail < release) {
		t.Error("identify's completion is not signalled before session can return, so a " +
			"failed handshake would leave shutdown waiting on it")
	}
}

// funcBody returns the source of one function.
func funcBody(t *testing.T, src, fn string) string {
	t.Helper()
	start := strings.Index(src, "func "+fn+"(")
	if start < 0 {
		start = strings.Index(src, ") "+fn+"(")
		if start < 0 {
			t.Fatalf("%s is not in agent.go any more — this guard needs updating", fn)
		}
	}
	body := src[start:]
	if end := strings.Index(body, "\n}\n"); end > 0 {
		body = body[:end]
	}
	return body
}
