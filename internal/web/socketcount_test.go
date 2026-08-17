package web

import "testing"

func TestParseSocketCount(t *testing.T) {
	sockstat := []byte("sockets: used 317\nTCP: inuse 12 orphan 0 tw 3 alloc 15 mem 1\nUDP: inuse 2 mem 1\n")

	count, err := parseSocketCount(sockstat)
	if err != nil {
		t.Fatalf("parseSocketCount returned an error: %v", err)
	}
	if count != 317 {
		t.Fatalf("parseSocketCount = %d, want 317", count)
	}
}

func TestParseSocketCountRejectsInvalidInput(t *testing.T) {
	for _, input := range []string{
		"TCP: inuse 12\n",
		"sockets: used nope\n",
		"sockets: used -1\n",
	} {
		if _, err := parseSocketCount([]byte(input)); err == nil {
			t.Fatalf("parseSocketCount(%q) unexpectedly succeeded", input)
		}
	}
}
