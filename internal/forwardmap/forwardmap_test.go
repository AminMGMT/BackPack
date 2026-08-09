package forwardmap

import "testing"

func TestExpandOffsetRangesAndIPv6(t *testing.T) {
	got, err := Expand([]string{"[::1]:1000-1002=[2001:db8::5]:2000-2002"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0].Listen != "[::1]:1000" || got[2].Target != "[2001:db8::5]:2002" {
		t.Fatalf("unexpected expansion: %#v", got)
	}
}

func TestExpandMultipleBackends(t *testing.T) {
	got, err := Expand([]string{"100-101=127.0.0.1:200-201|[::1]:300-301"})
	if err != nil {
		t.Fatal(err)
	}
	if got[1].Target != "127.0.0.1:201|[::1]:301" {
		t.Fatalf("unexpected target: %q", got[1].Target)
	}
}

func TestExpandRejectsUnequalRangeAndLimit(t *testing.T) {
	if _, err := Expand([]string{"100-102=200-201"}); err == nil {
		t.Fatal("unequal ranges must fail")
	}
	if _, err := Expand([]string{"1-4097"}); err == nil {
		t.Fatal("oversized expansion must fail")
	}
}

func TestExpandRejectsPerMappingExpansionLimit(t *testing.T) {
	if _, err := Expand([]string{"1000-2024=3000-4024"}); err == nil {
		t.Fatal("a mapping expanding beyond 1024 ports must be rejected")
	}
}

func TestExpandKeepsExplicitIPv4AndIPv6FamiliesIndependent(t *testing.T) {
	mappings, err := Expand([]string{"0.0.0.0:443=8443", "[::]:443=8443"})
	if err != nil {
		t.Fatalf("explicit IPv4 and IPv6 listeners should not overlap: %v", err)
	}
	if len(mappings) != 2 {
		t.Fatalf("got %d mappings, want 2", len(mappings))
	}
}
