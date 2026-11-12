package paqet

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"testing/iotest"
)

func TestEveryMessageSurvivesAWriteAndARead(t *testing.T) {
	cases := []proto{
		{Type: pPing},
		{Type: pPong},
		{Type: pHello, Version: engineRevision},
		{Type: pTCP, Addr: Addr{Host: "127.0.0.1", Port: 8443}},
		{Type: pUDP, Addr: Addr{Host: "example.net", Port: 53}},
		{Type: pTCP, Addr: Addr{Host: "::1", Port: 65535}},
		{Type: pTCP, Addr: Addr{Host: "", Port: 0}},
	}
	for _, in := range cases {
		var b bytes.Buffer
		if err := in.write(&b); err != nil {
			t.Fatalf("write %+v: %v", in, err)
		}
		var out proto
		if err := out.read(&b); err != nil {
			t.Fatalf("read %+v: %v", in, err)
		}
		if out != in {
			t.Fatalf("got %+v, want %+v", out, in)
		}
	}
}

func TestAMessageReadFromAOneByteAtATimeStreamIsTheSameMessage(t *testing.T) {
	var b bytes.Buffer
	in := proto{Type: pTCP, Addr: Addr{Host: "10.1.2.3", Port: 443}}
	in.write(&b)
	var out proto
	if err := out.read(iotest.OneByteReader(&b)); err != nil || out != in {
		t.Fatalf("got %+v, %v", out, err)
	}
}

func TestMalformedMessagesAreRefusedWithAReason(t *testing.T) {
	good := func() []byte {
		var b bytes.Buffer
		(&proto{Type: pTCP, Addr: Addr{Host: "h", Port: 1}}).write(&b)
		return b.Bytes()
	}
	mut := func(f func(b []byte) []byte) []byte { return f(good()) }
	cases := map[string]struct {
		in   []byte
		want string
	}{
		"bad magic":                  {mut(func(b []byte) []byte { b[0] = 0; return b }), "magic"},
		"future version":             {mut(func(b []byte) []byte { b[1] = 9; return b }), "unsupported framing version"},
		"unknown type":               {mut(func(b []byte) []byte { b[2] = 0x7f; return b }), "unknown message type"},
		"host longer than it claims": {mut(func(b []byte) []byte { b[5] = 40; return b }), "bad host length"},
		"body too long":              {mut(func(b []byte) []byte { b[3], b[4] = 0xff, 0xff; return b }), "exceeds max"},
		"truncated":                  {good()[:6], "EOF"},
		"empty":                      {nil, "EOF"},
	}
	for name, c := range cases {
		var p proto
		err := p.read(bytes.NewReader(c.in))
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: got %v, want an error mentioning %q", name, err, c.want)
		}
	}
}

func TestAStockPaqetRequestIsNamedNotJustRejected(t *testing.T) {
	// paqet's own framing: magic 0x50, version 0x01.
	stock := []byte{0x50, 0x01, 0x04, 0, 7, 3, 'a', 'b', 'c', 0, 80}
	var p proto
	if err := p.read(bytes.NewReader(stock)); !errors.Is(err, errStockPaqet) {
		t.Fatalf("got %v, want errStockPaqet", err)
	}
}

func TestTheLongestHostAndNothingLongerIsWritten(t *testing.T) {
	var b bytes.Buffer
	ok := proto{Type: pTCP, Addr: Addr{Host: strings.Repeat("a", maxHostLen), Port: 1}}
	if err := ok.write(&b); err != nil {
		t.Fatal(err)
	}
	bad := proto{Type: pTCP, Addr: Addr{Host: strings.Repeat("a", maxHostLen+1), Port: 1}}
	if err := bad.write(&b); err == nil {
		t.Fatal("a host of 254 bytes was accepted")
	}
	if err := (&proto{Type: pTCP, Addr: Addr{Host: "h", Port: 70000}}).write(&b); err == nil {
		t.Fatal("port 70000 was accepted")
	}
	if err := (&proto{Type: pTCPF}).write(&b); err == nil {
		t.Fatal("the reserved flag-negotiation message must not be writable")
	}
}

func TestParseAddrTakesWhatTheForwarderHandsIt(t *testing.T) {
	for in, want := range map[string]Addr{
		"127.0.0.1:80": {"127.0.0.1", 80}, "[::1]:443": {"::1", 443}, "h.example:1": {"h.example", 1},
	} {
		got, err := ParseAddr(in)
		if err != nil || got != want {
			t.Errorf("%q: %+v, %v", in, got, err)
		}
		if got.String() != in {
			t.Errorf("%q renders as %q", in, got.String())
		}
	}
	for _, bad := range []string{"", "nohost", "h:notaport", "h:70000", "h:-1"} {
		if _, err := ParseAddr(bad); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
}

func TestADatagramKeepsItsBoundariesWhenTheStreamDeliversItInPieces(t *testing.T) {
	var wire bytes.Buffer
	sizes := []int{0, 1, 500, 65535}
	var want [][]byte
	for _, n := range sizes {
		d := bytes.Repeat([]byte{byte(n)}, n)
		want = append(want, d)
		if err := writeDatagram(&wire, d); err != nil {
			t.Fatal(err)
		}
	}
	var mu sync.Mutex
	var pending []byte
	r := iotest.OneByteReader(&wire)
	buf := make([]byte, 65535)
	for i, w := range want {
		n, err := readFramed(&mu, &pending, r, buf)
		if err != nil || !bytes.Equal(buf[:n], w) {
			t.Fatalf("datagram %d: %d bytes, %v", i, n, err)
		}
	}
	if _, err := readFramed(&mu, &pending, r, buf); !errors.Is(err, io.EOF) {
		t.Fatalf("after the last datagram: %v, want EOF", err)
	}
}

func TestADatagramLargerThanTheCallersBufferIsDroppedWholeAndTheNextOneStillArrives(t *testing.T) {
	var wire bytes.Buffer
	writeDatagram(&wire, make([]byte, 100))
	writeDatagram(&wire, []byte("next"))
	var mu sync.Mutex
	var pending []byte
	buf := make([]byte, 10)
	n, err := readFramed(&mu, &pending, &wire, buf)
	if err != nil || string(buf[:n]) != "next" {
		t.Fatalf("got %q, %v; want the oversized datagram skipped and %q delivered", buf[:n], err, "next")
	}
}

func TestADatagramThatDoesNotFitAFrameIsRefusedAtTheWriter(t *testing.T) {
	if err := writeDatagram(io.Discard, make([]byte, maxDatagram+1)); err == nil {
		t.Fatal("a 65536-byte datagram was written")
	}
}

func FuzzReadMessage(f *testing.F) {
	var b bytes.Buffer
	(&proto{Type: pTCP, Addr: Addr{Host: "example.net", Port: 443}}).write(&b)
	f.Add(b.Bytes())
	f.Add([]byte{0x50, 0x02, 0x06, 0, 1, 1})
	f.Fuzz(func(t *testing.T, in []byte) {
		var p proto
		if err := p.read(bytes.NewReader(in)); err != nil {
			return
		}
		// Whatever was accepted must write back and read the same.
		var out bytes.Buffer
		if err := p.write(&out); err != nil {
			t.Fatalf("accepted %+v but cannot write it: %v", p, err)
		}
		var q proto
		if err := q.read(&out); err != nil || q != p {
			t.Fatalf("round trip: %+v -> %+v, %v", p, q, err)
		}
	})
}
