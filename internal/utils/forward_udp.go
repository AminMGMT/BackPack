package utils

import (
	"encoding/binary"
	"fmt"
)

// EncodeForwardUDP builds the first datagram of a direct UDP flow. Lengths are
// explicit because both the token and target are user-controlled strings and
// no separator byte is safe. The packet stays well below the UDP maximum.
func EncodeForwardUDP(token, target string) ([]byte, error) {
	if len(token) > 65535 || len(target) > 65535 || len(token)+len(target)+5 > 65507 {
		return nil, fmt.Errorf("forward UDP announcement is too large")
	}
	b := make([]byte, 5+len(token)+len(target))
	b[0] = SG_ForwardUDP
	binary.BigEndian.PutUint16(b[1:3], uint16(len(token)))
	binary.BigEndian.PutUint16(b[3:5], uint16(len(target)))
	copy(b[5:], token)
	copy(b[5+len(token):], target)
	return b, nil
}

func DecodeForwardUDP(b []byte) (token, target string, err error) {
	if len(b) < 5 || b[0] != SG_ForwardUDP {
		return "", "", fmt.Errorf("not a forward UDP announcement")
	}
	tokenLen := int(binary.BigEndian.Uint16(b[1:3]))
	targetLen := int(binary.BigEndian.Uint16(b[3:5]))
	if tokenLen == 0 || targetLen == 0 || 5+tokenLen+targetLen != len(b) {
		return "", "", fmt.Errorf("malformed forward UDP announcement")
	}
	return string(b[5 : 5+tokenLen]), string(b[5+tokenLen:]), nil
}
