package network

import (
	"crypto/tls"
	"strings"
	"testing"
)

// The regression test for "the script cannot get a certificate", reported from
// two servers by two people who both concluded Let's Encrypt was down.
//
// autocert identifies the certificate to serve by the name in the ClientHello
// and refuses outright when there is none. A tunnel's remote address is
// normally the server's IP; a client dialling an address literal sends no SNI;
// so every handshake was refused with "missing server name" before autocert
// ever attempted an issuance. Nothing was wrong with Let's Encrypt, and nothing
// in the log said so.
func TestAnSNIlessHandshakeIsAnsweredWithTheConfiguredDomain(t *testing.T) {
	cfg, err := acmeTLSConfig(TLSSettings{
		ACMEDomain:   "tunnel.example.com",
		ACMECacheDir: t.TempDir(),
	}, func(string, ...any) {})
	if err != nil {
		t.Fatalf("building the ACME config: %v", err)
	}

	// No SNI, exactly as a client dialling an IP sends.
	_, err = cfg.GetCertificate(&tls.ClientHelloInfo{
		ServerName:   "",
		CipherSuites: []uint16{tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256},
	})

	// Issuance cannot succeed in a test — there is no ACME server to talk to —
	// so what matters is which wall it hits. "missing server name" means the
	// name was never substituted and autocert refused before trying anything.
	if err != nil && strings.Contains(err.Error(), "missing server name") {
		t.Fatal("an SNI-less handshake was still refused outright: a client dialling " +
			"the server by IP can never cause a certificate to be obtained")
	}
}

// A handshake that does name a host must be left alone, or a listener would
// answer for names it was never configured to serve.
func TestAnExplicitServerNameIsNotRewritten(t *testing.T) {
	cfg, err := acmeTLSConfig(TLSSettings{
		ACMEDomain:   "tunnel.example.com",
		ACMECacheDir: t.TempDir(),
	}, func(string, ...any) {})
	if err != nil {
		t.Fatalf("building the ACME config: %v", err)
	}

	_, err = cfg.GetCertificate(&tls.ClientHelloInfo{
		ServerName:   "somewhere.else.example",
		CipherSuites: []uint16{tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256},
	})
	if err == nil {
		t.Fatal("a name outside the configured domain was accepted")
	}
	// The host policy is what must have refused it, not anything else.
	if !strings.Contains(err.Error(), "not configured") && !strings.Contains(err.Error(), "host") {
		t.Errorf("refused for the wrong reason, so the whitelist may not be in play: %v", err)
	}
}

// The primed hello has to ask for the same kind of key a real client will, or
// the startup fetch and the first handshake miss each other in the cache and
// everything is issued twice — which is how a host reaches a rate limit.
func TestThePrimedHelloAsksForTheSameKeyAsARealClient(t *testing.T) {
	primed := &tls.ClientHelloInfo{
		CipherSuites: []uint16{
			tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256,
			tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,
		},
		SupportedCurves:  []tls.CurveID{tls.CurveP256},
		SignatureSchemes: []tls.SignatureScheme{tls.ECDSAWithP256AndSHA256, tls.PSSWithSHA256},
	}
	if !helloSupportsECDSA(primed) {
		t.Error("the primed hello reads as an RSA-only client, so startup would fetch an " +
			"RSA certificate that the first real handshake then misses")
	}

	bare := &tls.ClientHelloInfo{}
	if helloSupportsECDSA(bare) {
		t.Error("a bare hello should not read as ECDSA-capable; if it does, this test " +
			"is not checking what it thinks it is")
	}
}

// helloSupportsECDSA mirrors autocert's own choice between an RSA and an ECDSA
// certificate. It is duplicated here because that function is unexported, and
// the point of the test is that our hello lands on the same side of it.
func helloSupportsECDSA(hello *tls.ClientHelloInfo) bool {
	if hello.SignatureSchemes != nil {
		ok := false
		for _, s := range hello.SignatureSchemes {
			switch s {
			case tls.ECDSAWithP256AndSHA256, tls.ECDSAWithP384AndSHA384, tls.ECDSAWithP521AndSHA512:
				ok = true
			}
		}
		if !ok {
			return false
		}
	}
	if hello.SupportedCurves != nil {
		ok := false
		for _, c := range hello.SupportedCurves {
			if c == tls.CurveP256 {
				ok = true
			}
		}
		if !ok {
			return false
		}
	}
	for _, suite := range hello.CipherSuites {
		switch suite {
		case tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256,
			tls.TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384,
			tls.TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305:
			return true
		}
	}
	return false
}
