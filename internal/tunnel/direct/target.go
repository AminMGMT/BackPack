package direct

import (
	"fmt"
	"net"
)

// What the origin refuses to dial.
//
// The kharej side dials whatever address the stream names. That is the design —
// the Iran side decides what is forwarded, and the origin learns each target
// from the stream that wants it — and it is why changing the forwarded ports
// touches only one machine.
//
// It also means a peer holding the token can point this host at any address it
// can reach, including addresses that are not on the internet. Most of those
// are fine and some are the point: forwarding to 10.0.0.5 on the kharej side's
// own network is a documented use, so blocking private ranges wholesale would
// break a feature rather than protect anything.
//
// One class is different. The cloud metadata services answer, without
// authentication, to anything running on the instance, and what they answer
// with is credentials: IAM tokens on AWS, service-account tokens on GCP, the
// instance's whole identity on most providers. A tunnel that will dial them on
// request turns "someone has the token" into "someone has the cloud account",
// which is a different order of problem — and unlike a private backend, nobody
// has ever wanted to forward a port to their own metadata service.
//
// So exactly that class is refused, and nothing else.

// metadataAddrs are the link-local addresses cloud providers answer credentials
// on. They are fixed by the providers and are not configurable, which is what
// makes a list of them workable rather than a guess.
var metadataAddrs = []net.IP{
	// AWS, GCP, Azure, DigitalOcean, Oracle, and everything else that followed
	// the EC2 convention.
	net.ParseIP("169.254.169.254"),
	// Alibaba Cloud.
	net.ParseIP("100.100.100.200"),
	// GCP's alternate name, and AWS IMDSv6.
	net.ParseIP("fd00:ec2::254"),
	// Oracle Cloud's second address.
	net.ParseIP("169.254.169.253"),
}

var errMetadataTarget = fmt.Errorf(
	"direct: refusing to reach a cloud metadata service — it answers credentials to " +
		"anything on this host, and no forwarded port has a reason to go there")

// vetTarget refuses the addresses above and passes everything else.
//
// A hostname is resolved first, and rejected if *any* of its addresses is a
// metadata address: a name that resolves to several is a name that can be made
// to resolve to the wrong one on the next lookup.
func vetTarget(target string) error {
	host, _, err := net.SplitHostPort(target)
	if err != nil {
		return fmt.Errorf("direct: target %q must be host:port: %w", target, err)
	}

	if ip := net.ParseIP(host); ip != nil {
		if isMetadataAddr(ip) {
			return errMetadataTarget
		}
		return nil
	}

	// A name. Resolution failing is not this function's business — the dial
	// below will report it in its own words — so a lookup error passes here.
	ips, err := net.LookupIP(host)
	if err != nil {
		return nil
	}
	for _, ip := range ips {
		if isMetadataAddr(ip) {
			return errMetadataTarget
		}
	}
	return nil
}

func isMetadataAddr(ip net.IP) bool {
	for _, m := range metadataAddrs {
		if m != nil && m.Equal(ip) {
			return true
		}
	}
	return false
}
