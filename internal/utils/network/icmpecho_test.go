package network

import (
	"bytes"
	"testing"

	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
)

// The hand-built echo must be byte for byte what x/net's marshaller produced,
// checksum included — odd lengths too, and sequence numbers past 16 bits.
func TestTheHandBuiltEchoMatchesTheLibrary(t *testing.T) {
	var tag [xdiTagLen]byte
	copy(tag[:], "0123456789abcdef")
	for _, typ := range []ipv4.ICMPType{ipv4.ICMPTypeEcho, ipv4.ICMPTypeEchoReply} {
		for _, size := range []int{0, 1, 2, 7, 100, 1301, 1400} {
			payload := bytes.Repeat([]byte{0xa5, 0x3c, 0xff}, size)[:size]
			id, seq := uint16(0xbeef), uint32(0x1_2345)

			want, err := (&icmp.Message{Type: typ, Body: &icmp.Echo{
				ID: int(id), Seq: int(seq & 0xffff), Data: encodeXdiPayload(tag, 1, payload),
			}}).Marshal(nil)
			if err != nil {
				t.Fatal(err)
			}
			got := appendEcho(nil, byte(typ), id, uint16(seq))
			got = appendXdiPayload(got, tag, 1, payload)
			setICMPChecksum(got)
			if !bytes.Equal(got, want) {
				t.Fatalf("type %v, %d bytes:\n got % x\nwant % x", typ, size, got[:12], want[:12])
			}
		}
	}
}

// appendXdiPayload used to be called on an empty buffer only; the send path
// now appends it after the echo header, and it must keep what is there.
func TestTheFramingKeepsTheHeaderBeforeIt(t *testing.T) {
	var tag [xdiTagLen]byte
	got := appendXdiPayload(appendEcho(nil, 8, 1, 2), tag, 1, []byte("x"))
	if len(got) != 8+xdiHeaderLen+1 || got[0] != 8 || got[7] != 2 {
		t.Fatalf("the echo header was overwritten: % x", got)
	}
}
