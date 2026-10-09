package manage

import (
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"math/big"
	"net"
	"reflect"
	"strconv"
	"strings"
)

// Setup links, format 2: the same settings as format 1 at half the
// length.
//
// Format 1 is the ShareLink as JSON, gzipped: every field name spelt out, and
// the 64-character token carried as text. It works, and it is a line and a
// half to paste. Format 2 carries exactly the same fields and decodes to the
// identical ShareLink — nothing about what a link builds changes — but writes
// them compactly:
//
//   - each field is a one-byte number (shareFieldIDs) followed by its value;
//     a field at its zero value is left out, as omitempty leaves it out;
//   - a word the links always use — a transport, a side, a preset — is one
//     byte from shareWords, an IPv4 address is four bytes, a port is a number,
//     and a token made of the token alphabet is packed as the base-62 number
//     it is;
//   - the name is left out when it is the one the wizard gives by default,
//     and put back on decoding.
//
// A reader of format 1 is kept: links made by older builds still work. A build
// older than this one cannot read format 2, and says so ("update the older of
// the two machines").

const shareVersion2 = "2"

// shareFieldIDs numbers the ShareLink fields by their JSON names. A number is
// never reused or changed: a link carries them. A field added to ShareLink is
// added at the end here — TestEveryShareLinkFieldHasAnID holds that.
var shareFieldIDs = []string{
	"k", "f", "n", "t", "tr", "e", "sni", "p", "h", "pr", "po", "u", "m", "mt",
	"li", "pi", "fd", "fp", "pa", "gk", "lm", "am", "sp", "su", "sd", "ss", "sl",
	"si", "sa", "mv", "hs", "rh", "rm", "ft", "fw", "ed",
	"hi", "hu", "hp", "hid", "hn", "hpa", "hh", "hk", "hsid", "hca",
}

// shareWords are the values a link repeats, each written as its index. Never
// reordered, only added to.
var shareWords = []string{
	"reverse", "direct", "iran", "kharej",
	"tcp", "tcpmux", "stealth", "pck", "ws", "wss", "wsmux", "wssmux", "kcp", "quic", "udp",
	"xdi", "sni", "spoof", "gre", "balance", "turbo", "aggressive", "throughput",
}

// How a string value is written.
const (
	strRaw    = 0 // length, then the bytes
	strWord   = 1 // an index into shareWords
	strIPv4   = 2 // four bytes
	strNumber = 3 // a decimal number, as a varint
	strBase62 = 4 // length, then the base-62 number the characters spell
)

// encodeShareLinkV2 renders l in format 2.
func encodeShareLinkV2(l ShareLink) (string, error) {
	var b []byte
	v := reflect.ValueOf(l)
	for id, tag := range shareFieldIDs {
		f, ok := shareField(v, tag)
		if !ok {
			return "", fmt.Errorf("setup link: no field %q", tag)
		}
		if tag == "n" {
			switch def := defaultShareName(l); {
			case l.Name == def:
				continue // put back on decoding
			case l.Name == "":
				// No name, where decoding would otherwise supply one.
				b = append(b, byte(id), strRaw, 0)
				continue
			}
		}
		if f.IsZero() {
			continue
		}
		b = append(b, byte(id))
		switch f.Kind() {
		case reflect.String:
			b = appendShareString(b, f.String())
		case reflect.Int:
			b = binary.AppendVarint(b, f.Int())
		case reflect.Uint32:
			b = binary.AppendUvarint(b, f.Uint())
		case reflect.Bool:
			// Present means true.
		case reflect.Pointer: // *bool
			if f.Elem().Bool() {
				b = append(b, 1)
			} else {
				b = append(b, 0)
			}
		case reflect.Slice: // []string
			b = binary.AppendUvarint(b, uint64(f.Len()))
			for i := 0; i < f.Len(); i++ {
				b = appendShareString(b, f.Index(i).String())
			}
		default:
			return "", fmt.Errorf("setup link: field %q has a type format 2 cannot carry", tag)
		}
	}
	// A check, so a link cut short in a paste is refused rather than read as
	// a link with fewer settings: format 1 had gzip's CRC for this.
	sum := crc32.ChecksumIEEE(b)
	b = append(b, byte(sum>>16), byte(sum>>8), byte(sum))
	return shareScheme + shareVersion2 + "." + base64.RawURLEncoding.EncodeToString(b), nil
}

// decodeShareLinkV2 reads a format 2 payload.
func decodeShareLinkV2(payload string) (ShareLink, error) {
	var out ShareLink
	b, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil || len(b) < 4 {
		return out, errShareDamaged
	}
	body, check := b[:len(b)-3], b[len(b)-3:]
	if sum := crc32.ChecksumIEEE(body); check[0] != byte(sum>>16) || check[1] != byte(sum>>8) || check[2] != byte(sum) {
		return out, fmt.Errorf("the setup link is incomplete or damaged — copy the whole of it again")
	}
	b = body
	v := reflect.ValueOf(&out).Elem()
	nameSet := false
	for len(b) > 0 {
		id := int(b[0])
		b = b[1:]
		if id >= len(shareFieldIDs) {
			return out, fmt.Errorf("this setup link was made by a newer version — update this server")
		}
		tag := shareFieldIDs[id]
		f, _ := shareField(v, tag)
		switch f.Kind() {
		case reflect.String:
			var s string
			if s, b, err = readShareString(b); err != nil {
				return out, err
			}
			f.SetString(s)
			if tag == "n" {
				nameSet = true
			}
		case reflect.Int:
			n, k := binary.Varint(b)
			if k <= 0 {
				return out, errShareDamaged
			}
			f.SetInt(n)
			b = b[k:]
		case reflect.Uint32:
			n, k := binary.Uvarint(b)
			if k <= 0 || n > 1<<32-1 {
				return out, errShareDamaged
			}
			f.SetUint(n)
			b = b[k:]
		case reflect.Bool:
			f.SetBool(true)
		case reflect.Pointer:
			if len(b) < 1 {
				return out, errShareDamaged
			}
			val := b[0] == 1
			f.Set(reflect.ValueOf(&val))
			b = b[1:]
		case reflect.Slice:
			n, k := binary.Uvarint(b)
			if k <= 0 || n > 64 {
				return out, errShareDamaged
			}
			b = b[k:]
			list := make([]string, 0, n)
			for i := uint64(0); i < n; i++ {
				var s string
				if s, b, err = readShareString(b); err != nil {
					return out, err
				}
				list = append(list, s)
			}
			f.Set(reflect.ValueOf(list))
		}
	}
	if !nameSet {
		out.Name = defaultShareName(out)
	}
	out.V = 1
	return out, nil
}

var errShareDamaged = errors.New("the setup link is damaged — copy it again, all of it")

// shareField is the field of v whose JSON name is tag.
func shareField(v reflect.Value, tag string) (reflect.Value, bool) {
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		name, _, _ := strings.Cut(t.Field(i).Tag.Get("json"), ",")
		if name == tag {
			return v.Field(i), true
		}
	}
	return reflect.Value{}, false
}

// defaultShareName is the name the wizard gives a tunnel on the side that
// made the link, which format 2 leaves out.
func defaultShareName(l ShareLink) string {
	if l.Port == "" {
		return ""
	}
	iran := strings.EqualFold(l.From, "iran")
	switch {
	case l.Kind == "reverse" && iran:
		return "server-" + l.Port
	case l.Kind == "reverse":
		return "client-" + l.Port
	case l.Kind == "direct" && iran:
		return "l3-iran-" + l.Port
	case l.Kind == "direct":
		return "l3-kharej-" + l.Port
	}
	return ""
}

func appendShareString(b []byte, s string) []byte {
	for i, w := range shareWords {
		if s == w {
			return append(b, strWord, byte(i))
		}
	}
	if ip := net.ParseIP(s).To4(); ip != nil && ip.String() == s {
		return append(append(b, strIPv4), ip...)
	}
	if n, err := strconv.ParseUint(s, 10, 32); err == nil && strconv.FormatUint(n, 10) == s {
		return binary.AppendUvarint(append(b, strNumber), n)
	}
	if packed, ok := packBase62(s); ok && len(packed)+2 < len(s)+1 {
		return append(append(b, strBase62, byte(len(s))), packed...)
	}
	b = binary.AppendUvarint(append(b, strRaw), uint64(len(s)))
	return append(b, s...)
}

func readShareString(b []byte) (string, []byte, error) {
	if len(b) < 1 {
		return "", nil, errShareDamaged
	}
	mode, b := b[0], b[1:]
	switch mode {
	case strWord:
		if len(b) < 1 || int(b[0]) >= len(shareWords) {
			return "", nil, fmt.Errorf("this setup link was made by a newer version — update this server")
		}
		return shareWords[b[0]], b[1:], nil
	case strIPv4:
		if len(b) < 4 {
			return "", nil, errShareDamaged
		}
		return net.IP(b[:4]).String(), b[4:], nil
	case strNumber:
		n, k := binary.Uvarint(b)
		if k <= 0 {
			return "", nil, errShareDamaged
		}
		return strconv.FormatUint(n, 10), b[k:], nil
	case strBase62:
		if len(b) < 1 {
			return "", nil, errShareDamaged
		}
		length := int(b[0])
		width := base62Width(length)
		if len(b) < 1+width {
			return "", nil, errShareDamaged
		}
		s, ok := unpackBase62(b[1:1+width], length)
		if !ok {
			return "", nil, errShareDamaged
		}
		return s, b[1+width:], nil
	case strRaw:
		n, k := binary.Uvarint(b)
		if k <= 0 || uint64(len(b)-k) < n {
			return "", nil, errShareDamaged
		}
		return string(b[k : k+int(n)]), b[k+int(n):], nil
	}
	return "", nil, errShareDamaged
}

// packBase62 is s, when every character is from the token alphabet, as the
// base-62 number it spells, in base62Width(len(s)) bytes.
func packBase62(s string) ([]byte, bool) {
	if len(s) == 0 || len(s) > 255 {
		return nil, false
	}
	n := new(big.Int)
	base := big.NewInt(int64(len(tokenCharset)))
	for i := 0; i < len(s); i++ {
		d := strings.IndexByte(tokenCharset, s[i])
		if d < 0 {
			return nil, false
		}
		n.Mul(n, base).Add(n, big.NewInt(int64(d)))
	}
	return n.FillBytes(make([]byte, base62Width(len(s)))), true
}

func unpackBase62(b []byte, length int) (string, bool) {
	n := new(big.Int).SetBytes(b)
	base := big.NewInt(int64(len(tokenCharset)))
	out := make([]byte, length)
	mod := new(big.Int)
	for i := length - 1; i >= 0; i-- {
		n.DivMod(n, base, mod)
		out[i] = tokenCharset[mod.Int64()]
	}
	if n.Sign() != 0 {
		return "", false // more than length digits: not a link this wrote
	}
	return string(out), true
}

// base62Width is how many bytes hold any length-digit base-62 number.
func base62Width(length int) int {
	if length <= 0 {
		return 0
	}
	max := new(big.Int).Exp(big.NewInt(int64(len(tokenCharset))), big.NewInt(int64(length)), nil)
	return len(max.Sub(max, big.NewInt(1)).Bytes())
}
