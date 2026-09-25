package network

import (
	"strings"
	"testing"
)

// The rule matches the kernel's copies of the client's requests — the tag and
// the client's direction byte — and not the server's own replies.
func TestTheXdiGuardMatchesOnlyTheKernelsCopies(t *testing.T) {
	tag := xdiTag("a-token")
	rule := strings.Join(xdiEchoRule(tag), " ")
	word := strings.ToLower(strings.TrimPrefix(strings.Fields(strings.Split(rule, "@8=")[1])[0], "0x"))
	want := strings.ToLower(strings.Join([]string{
		hex2(tag[0]), hex2(tag[1]), hex2(tag[2]), hex2(tag[3])}, ""))
	if !strings.HasPrefix(word, want) {
		t.Fatalf("rule matches tag %q, want %q: %s", word, want, rule)
	}
	if !strings.Contains(rule, "@12>>24=0x43") { // 'C', the client's marker
		t.Fatalf("rule does not require the client's direction byte: %s", rule)
	}
	if strings.Contains(rule, "0x53") { // 'S' would drop the server's own replies
		t.Fatalf("rule would drop the server's replies: %s", rule)
	}
	if !strings.Contains(rule, "echo-reply") || !strings.HasPrefix(rule, "OUTPUT ") {
		t.Fatalf("rule is not on outbound echo replies: %s", rule)
	}
}

// Two tunnels with different tokens get different rules, so removing one
// leaves the other.
func TestEachXdiTunnelHasItsOwnRule(t *testing.T) {
	a := strings.Join(xdiEchoRule(xdiTag("one")), " ")
	b := strings.Join(xdiEchoRule(xdiTag("two")), " ")
	if a == b {
		t.Fatal("two tokens produced the same rule")
	}
}

func hex2(b byte) string {
	const digits = "0123456789abcdef"
	return string([]byte{digits[b>>4], digits[b&15]})
}
