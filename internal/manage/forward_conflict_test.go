package manage

import (
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/backpack/backpack/config"
)

func TestForwardConflictRejectsDuplicateWildcardAndSpecific(t *testing.T) {
	s := TunnelSpec{
		Name: "conflict-unit", Role: "server", Engine: config.EngineForward,
		Transport: "tcp", RemoteAddr: "192.0.2.1:443",
		Ports: []string{"0.0.0.0:18080=18080", "127.0.0.1:18080=18081"},
	}
	if err := validateForwardConflicts(s); err == nil {
		t.Fatal("wildcard and specific listeners on the same port must overlap")
	}
}

func TestForwardConflictRejectsExistingLocalListener(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port
	s := TunnelSpec{
		Name: fmt.Sprintf("conflict-%d", time.Now().UnixNano()),
		Role: "server", Engine: config.EngineForward, Transport: "tcp",
		RemoteAddr: "192.0.2.1:443", Ports: []string{fmt.Sprintf("127.0.0.1:%d=18081", port)},
	}
	if err := validateForwardConflicts(s); err == nil {
		t.Fatal("an unrelated local listener must be rejected before config write")
	}
}

func TestForwardConflictTreatsExplicitFamiliesIndependently(t *testing.T) {
	a := listenClaim{network: "tcp", addr: "0.0.0.0:443"}
	b := listenClaim{network: "tcp", addr: "[::]:443"}
	if claimsOverlap(a, b) {
		t.Fatal("explicit IPv4 and IPv6 listeners must be checked independently")
	}
}
