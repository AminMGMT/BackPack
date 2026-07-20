package manage

import (
	"strings"
	"testing"
)

// The certificate choice made during setup has to survive being written to the
// config and read back, or the tunnel starts on the self-signed certificate and
// the user has no idea why.

func tlsSpec(domain, email string) TunnelSpec {
	return TunnelSpec{
		Name:       "acme-test",
		Role:       "server",
		Transport:  "wss",
		BindAddr:   "0.0.0.0:443",
		Token:      "token-0123456789abcdefghijklmno",
		Ports:      []string{"8080"},
		TLSCert:    "/etc/backpack/certs/acme-test.crt",
		TLSKey:     "/etc/backpack/certs/acme-test.key",
		ACMEDomain: domain,
		ACMEEmail:  email,
	}
}

func TestACMEDomainIsWrittenToConfig(t *testing.T) {
	got := tlsSpec("tunnel.example.com", "me@example.com").Render()

	for _, want := range []string{
		`acme_domain = "tunnel.example.com"`,
		`acme_email = "me@example.com"`,
		`tls_cert = "/etc/backpack/certs/acme-test.crt"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("config is missing %s:\n%s", want, got)
		}
	}
}

// A tunnel that never asked for Let's Encrypt must produce exactly the config
// it produced before the feature existed. An upgraded install re-writes its
// config on any edit, and a stray key there is how a working tunnel starts
// behaving differently after an unrelated change.
func TestSelfSignedConfigHasNoACMEKeys(t *testing.T) {
	got := tlsSpec("", "").Render()

	if strings.Contains(got, "acme_") {
		t.Errorf("a self-signed tunnel emitted ACME keys:\n%s", got)
	}
	if !strings.Contains(got, "tls_cert") {
		t.Errorf("the self-signed certificate is missing from the config:\n%s", got)
	}
}

// An email is optional; asking for one and leaving it blank must not write an
// empty key that then reads back as a configured-but-empty address.
func TestACMEEmailOmittedWhenEmpty(t *testing.T) {
	got := tlsSpec("tunnel.example.com", "").Render()

	if !strings.Contains(got, `acme_domain = "tunnel.example.com"`) {
		t.Errorf("the domain is missing:\n%s", got)
	}
	if strings.Contains(got, "acme_email") {
		t.Errorf("an empty email should not be written at all:\n%s", got)
	}
}

// Non-TLS transports have no certificate to configure, so the keys must not
// leak into their configs even if the fields somehow carry a value.
func TestNonTLSTransportNeverEmitsCertKeys(t *testing.T) {
	s := tlsSpec("tunnel.example.com", "me@example.com")
	s.Transport = "tcp"

	got := s.Render()
	for _, unwanted := range []string{"acme_domain", "acme_email", "tls_cert", "tls_key"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("a tcp tunnel emitted %s:\n%s", unwanted, got)
		}
	}
}
