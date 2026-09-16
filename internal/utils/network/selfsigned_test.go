package network

import (
	"strings"
	"testing"
)

// A wss tunnel that was not told where its certificate is gets one, rather than
// refusing with a sentence that names nothing.
//
// It went through fileTLSConfig with two empty filenames and failed at startup
// with `open : no such file or directory`. The direct engine has always
// generated a certificate in exactly this case, said so in the log, and worked
// — so the same configuration produced a working tunnel on one engine and a
// refusal on the other, with a message that does not say what is missing.
//
// Generating is right because nothing here authenticates a peer with it: the
// token is the credential, and TLS is on the wire to look like TLS. A
// certificate nobody validates serves that whoever signed it.
func TestAWssListenerWithNoCertificateGetsOne(t *testing.T) {
	var said []string
	cfg, err := ServerTLSConfig(TLSSettings{}, func(f string, a ...any) {
		said = append(said, f)
	})
	if err != nil {
		t.Fatalf("a tunnel with no certificate configured could not start: %v", err)
	}
	if cfg == nil || len(cfg.Certificates) != 1 {
		t.Fatal("no certificate was produced")
	}
	// And it must say so, or an operator who meant to supply one never finds out
	// they did not.
	if !strings.Contains(strings.Join(said, " "), "generated") {
		t.Errorf("a generated certificate was served without saying so: %q", said)
	}
	// The websocket upgrade is HTTP/1.1, so h2 must still not be negotiable.
	for _, p := range cfg.NextProtos {
		if p == "h2" {
			t.Error("the generated config offers h2, which leaves a websocket upgrade nowhere to go")
		}
	}
}

// A certificate that was configured is still the one that is used; the fallback
// must not quietly replace a real one.
func TestAConfiguredCertificateIsStillRequired(t *testing.T) {
	_, err := ServerTLSConfig(TLSSettings{
		CertFile: "/nonexistent/cert.pem",
		KeyFile:  "/nonexistent/key.pem",
	}, func(string, ...any) {})
	if err == nil {
		t.Error("a configured certificate that cannot be read was silently replaced " +
			"with a generated one — an operator who supplied a path is entitled to " +
			"hear that it is wrong")
	}
}

// The name in a generated certificate is cosmetic, and it still has to be a
// certificate that parses and names something.
func TestAGeneratedCertificateNamesItsHost(t *testing.T) {
	for _, host := range []string{"", "localhost", "203.0.113.9", "panel.example.ir"} {
		cert, err := GenerateSelfSigned(host)
		if err != nil {
			t.Fatalf("host %q: %v", host, err)
		}
		if cert.Leaf == nil {
			t.Fatalf("host %q produced a certificate with no leaf", host)
		}
		want := host
		if want == "" {
			want = "localhost"
		}
		if cert.Leaf.Subject.CommonName != want {
			t.Errorf("host %q produced a certificate for %q", host, cert.Leaf.Subject.CommonName)
		}
	}
}
