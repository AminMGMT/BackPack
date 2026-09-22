package manage

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"strings"
	"testing"
	"unicode/utf8"
)

// The setup link, fuzzed.
//
// This one is different from the wire parsers in a way that makes it worth more
// attention, not less: it is the only parser in the product whose input a
// *person* pastes in. It arrives over Telegram, over a chat, out of a
// screenshot — from wherever the other end of the tunnel sent it — and it goes
// through base64, then gzip, then JSON before anything checks what it says.
//
// A decompressor fed something pasted in is the classic shape for a zip bomb,
// and the read is bounded for exactly that reason. This holds that bound still,
// along with the property that matters most: a link that is not accepted must
// not leave a half-filled ShareLink behind for a caller to act on.
func FuzzDecodeShareLink(f *testing.F) {
	f.Add("")
	f.Add("   ")
	f.Add("not a link")
	f.Add(shareScheme)
	f.Add(shareScheme + "1")
	f.Add(shareScheme + "1.")
	f.Add(shareScheme + "1.@@@not-base64@@@")
	f.Add(shareScheme + "9.aGVsbG8")

	// A real link, so the fuzzer has a valid shape to mutate from.
	if good, err := (ShareLink{
		Kind: "reverse", From: "iran", Tok: "secret", Tr: "tcp", Port: "443",
	}).Encode(); err == nil {
		f.Add(good)
	}

	// Valid base64 of valid gzip of something that is not JSON, which is the
	// case that gets furthest in before being rejected.
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	zw.Write([]byte("this is not json"))
	zw.Close()
	f.Add(shareScheme + shareVersion + "." + base64.RawURLEncoding.EncodeToString(buf.Bytes()))

	f.Fuzz(func(t *testing.T, s string) {
		link, err := DecodeShareLink(s)
		if err != nil {
			// A refusal must hand back nothing usable. A half-filled link is
			// worse than no link: the caller builds a tunnel from it.
			if link.Tok != "" || link.Tr != "" || link.Kind != "" {
				t.Fatalf("rejected a link and still returned %+v", link)
			}
			// And every refusal has to say something an operator can act on,
			// because this is the one parser a person is standing in front of.
			if strings.TrimSpace(err.Error()) == "" {
				t.Fatal("a refusal with no explanation")
			}
			return
		}

		// Accepting means the three fields the callers rely on are all there.
		if link.Tok == "" || link.Tr == "" {
			t.Fatalf("accepted a link with no token or transport: %+v", link)
		}
		switch link.Kind {
		case "reverse", "direct":
		default:
			t.Fatalf("accepted a link of unknown kind %q", link.Kind)
		}
	})
}

// A link this build produced must survive the round trip **exactly**, or be
// refused. The encoder and the decoder are the two ends of a format two
// different machines speak, so a disagreement between them is a tunnel that
// cannot be set up — and a field that comes back *changed* is worse than one
// that fails to come back, because the tunnel is then built from it.
//
// This is the property that found the UTF-8 corruption: JSON silently rewrites
// bytes it cannot represent, and it was doing it to the token.
func FuzzShareLinkRoundTrip(f *testing.F) {
	f.Add("reverse", "iran", "tok", "tcp", "443", "1.2.3.4")
	f.Add("direct", "kharej", "t", "udp", "8443", "")
	f.Add("reverse", "", "\x00\x01", "ws", "80", "[::1]")

	f.Fuzz(func(t *testing.T, kind, from, tok, tr, port, host string) {
		switch kind {
		case "reverse", "direct":
		default:
			t.Skip() // only the two kinds the format has
		}
		if tok == "" || tr == "" {
			t.Skip() // the encoder is not asked to carry a link with no secret
		}

		in := ShareLink{Kind: kind, From: from, Tok: tok, Tr: tr, Port: port, Host: host}
		encoded, err := in.Encode()
		if err != nil {
			// The one thing it is allowed to refuse is a field it could not
			// carry without changing it. Refusing is the correct answer there
			// — see Encode — but it must be the *only* reason it refuses, and
			// every field this test supplies is checked rather than the four
			// the first version of this guard happened to name.
			allValid := true
			for _, v := range []string{kind, from, tok, tr, port, host} {
				if !utf8.ValidString(v) {
					allValid = false
				}
			}
			if allValid {
				t.Fatalf("Encode refused a link whose fields are all valid: %v", err)
			}
			return
		}
		out, err := DecodeShareLink(encoded)
		if err != nil {
			t.Fatalf("a link this build encoded could not be decoded by it: %v", err)
		}
		if out.Tok != in.Tok || out.Tr != in.Tr || out.Kind != in.Kind ||
			out.Port != in.Port || out.Host != in.Host || out.From != in.From {
			t.Fatalf("the round trip changed the link:\n in: %+v\nout: %+v", in, out)
		}
	})
}
