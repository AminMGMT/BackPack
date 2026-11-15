//go:build linux

package l3

import (
	"os/exec"
	"strings"

	"github.com/sirupsen/logrus"
)

// ifaceAccept is the INPUT rule that lets the tunnel interface's traffic in
// (see ifaceaccept.go). Nil when there is no iptables to ask.
type ifaceAccept struct {
	rule   []string
	active bool
}

// iptablesForAccept is the tool, a variable so a test can point it elsewhere.
var iptablesForAccept = "iptables"

// installIfaceAccept puts the rule in, or adopts one a crashed run left. Where
// it cannot, it says which rule to add by hand.
func installIfaceAccept(iface string, log *logrus.Logger) *ifaceAccept {
	if _, err := exec.LookPath(iptablesForAccept); err != nil || iface == "" {
		return nil
	}
	a := &ifaceAccept{rule: ifaceAcceptRule(iface)}
	if exec.Command(iptablesForAccept, append([]string{"-C"}, a.rule...)...).Run() == nil {
		a.active = true
		return a
	}
	if out, err := exec.Command(iptablesForAccept, append([]string{"-I"}, a.rule...)...).CombinedOutput(); err != nil {
		log.Warnf("l3: could not let what arrives on %s in through the firewall (%v: %s). If this server's "+
			"firewall drops what it does not know, the tunnel comes up and carries nothing — allow it: "+
			"%s -I INPUT -i %s -j ACCEPT", iface, err, strings.TrimSpace(string(out)), iptablesForAccept, iface)
		return a
	}
	a.active = true
	return a
}

// remove takes the rule out. Safe on nil and on a rule never installed.
func (a *ifaceAccept) remove() {
	if a == nil || !a.active {
		return
	}
	a.active = false
	_ = exec.Command(iptablesForAccept, append([]string{"-D"}, a.rule...)...).Run()
}

// CleanupIfaceAccept removes the rule of iface that a killed tunnel could not,
// for when the tunnel is deleted.
func CleanupIfaceAccept(iface string) {
	if iface == "" {
		iface = defaultIfaceName
	}
	rule := ifaceAcceptRule(iface)
	for exec.Command(iptablesForAccept, append([]string{"-D"}, rule...)...).Run() == nil {
		// One a crash adopted and a crash after that inserted again.
	}
}
