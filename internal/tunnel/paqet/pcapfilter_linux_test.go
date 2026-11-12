//go:build linux && cgo

package paqet

import "github.com/gopacket/gopacket/pcap"

// checkFilter compiles a BPF expression without opening an interface.
func checkFilter(expr string) error {
	_, err := pcap.NewBPF(1 /* LINKTYPE_ETHERNET */, 65535, expr)
	return err
}
