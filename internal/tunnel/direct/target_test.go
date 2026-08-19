package direct

import (
	"strings"
	"testing"
)

// The metadata services are refused, and nothing else is.
//
// The narrowness is the point. Blocking every private address would be the
// textbook SSRF rule and would break a documented feature: forwarding a port to
// a backend on the kharej side's own network is exactly what "443=10.0.0.5:8443"
// is for. What has no legitimate use is the metadata address, which hands out
// the instance's cloud credentials to anything that asks.
func TestOnlyTheMetadataServicesAreRefused(t *testing.T) {
	for _, target := range []string{
		"169.254.169.254:80",  // AWS, GCP, Azure, DigitalOcean, Oracle
		"169.254.169.254:443", // the port makes no difference
		"100.100.100.200:80",  // Alibaba
		"169.254.169.253:80",  // Oracle's second
		"[fd00:ec2::254]:80",  // IMDSv6
	} {
		err := vetTarget(target)
		if err == nil {
			t.Errorf("%s was allowed — a peer with the token could read this host's cloud credentials", target)
			continue
		}
		// The message has to say why, because the operator seeing it in a log
		// did not ask for this rule and will not guess at it.
		if !strings.Contains(err.Error(), "credentials") {
			t.Errorf("%s: the refusal does not explain itself: %v", target, err)
		}
	}

	for _, target := range []string{
		"127.0.0.1:443",   // the ordinary case: the kharej box's own service
		"10.0.0.5:8443",   // a private backend, which is documented
		"192.168.1.10:80", // likewise
		"172.16.0.1:80",   // likewise
		"100.64.0.1:80",   // CGNAT — a real provider network, not metadata
		"169.254.1.1:80",  // link-local, but not a metadata address
		"8.8.8.8:53",      // the open internet
		"[::1]:443",       // loopback, v6
		"[fd00::1]:443",   // a private v6 backend
	} {
		if err := vetTarget(target); err != nil {
			t.Errorf("%s was refused, but it is a legitimate forward target: %v", target, err)
		}
	}
}

// A malformed target is reported as malformed rather than quietly allowed.
func TestAMalformedTargetIsRejected(t *testing.T) {
	if err := vetTarget("no-port-here"); err == nil {
		t.Error("a target with no port was accepted")
	}
}

// A name that cannot be resolved is not this check's business — the dial that
// follows reports it in its own words, and failing here would turn a DNS
// hiccup into a confusing security message.
func TestAnUnresolvableNamePassesThrough(t *testing.T) {
	if err := vetTarget("this-name-does-not-exist.invalid:443"); err != nil {
		t.Errorf("an unresolvable name was refused by the target check: %v", err)
	}
}
