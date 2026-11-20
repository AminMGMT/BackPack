package network

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
)

// Peer authentication for the plain reverse transports: tcp, tcpmux, ws,
// ws mux and udp. See docs/adr/0006-plain-transports-prove-both-ends.md.
//
// Those transports sent the tunnel token as it is and took the server's echo
// of it as proof that the server knew it. On a path someone else controls that
// is two failures at once: the token is read off the wire, and whoever reads it
// can then stand in for the Iran server. The Iran server is what names, for
// every connection, the address the kharej side dials, so standing in for it
// meant the kharej machine would dial wherever it was told (audit F-11).
//
// The handshake here keeps the token off the wire and makes each end prove it:
//
//	client → server  hello    Nc, MAC(K, "c" | label | Nc)
//	server → client  reply    Ns, MAC(K, "s" | label | Nc | Ns)
//	client → server  confirm  MAC(K, "f" | label | Nc | Ns)
//
// K is derived from the token, Nc and Ns are fresh random values, and label
// names the transport so a hello made for one cannot be offered to another.
// The reply is fresh because it covers Nc, so only a holder of the token can
// produce it; the confirm is fresh because it covers Ns, so a recorded hello
// replayed later gets the attacker nowhere. Both ends then share a session key
// S = MAC(K, "k" | label | Nc | Ns), and the server seals every destination it
// sends the kharej with it. A destination that does not open under S is not
// dialled.
//
// What it does not do is encrypt. Plain transports stay plain: anything on the
// path still reads and can still alter what the tunnel carries, exactly as
// before. What it can no longer do is learn the token, pose as either end, or
// steer the kharej side to an address the Iran server never named.

// PeerAuthWire is the version a peer announces when it speaks this handshake.
// It is carried in logs and refusals; the wire itself is told apart by its
// signal, SG_ChanAuth, or for WebSocket by the bearer prefix.
const PeerAuthWire = "bp3"

const (
	peerNonceLen = 16
	peerMACLen   = 16
	peerHexLen   = 2 * (peerNonceLen + peerMACLen)
)

// PeerKey is the long-term key both ends derive from the token.
type PeerKey [32]byte

// PeerKeyFrom derives the key. The token is never used as a key directly, so a
// MAC made here is no use to anything else keyed on the same token.
func PeerKeyFrom(token string) PeerKey {
	m := hmac.New(sha256.New, []byte(token))
	m.Write([]byte("backpack/peer-auth/v1"))
	var k PeerKey
	copy(k[:], m.Sum(nil))
	return k
}

func (k PeerKey) mac(parts ...[]byte) []byte {
	m := hmac.New(sha256.New, k[:])
	for _, p := range parts {
		m.Write(p)
		m.Write([]byte{0})
	}
	return m.Sum(nil)
}

// PeerHello is the client's opening message.
type PeerHello struct {
	Nonce [peerNonceLen]byte
	MAC   [peerMACLen]byte
}

// NewPeerHello makes a fresh hello for label.
func (k PeerKey) NewPeerHello(label string) (PeerHello, error) {
	var h PeerHello
	if _, err := rand.Read(h.Nonce[:]); err != nil {
		return h, err
	}
	copy(h.MAC[:], k.mac([]byte("c"), []byte(label), h.Nonce[:]))
	return h, nil
}

// VerifyHello reports whether a hello was made with this key for label.
func (k PeerKey) VerifyHello(h PeerHello, label string) bool {
	want := k.mac([]byte("c"), []byte(label), h.Nonce[:])
	return subtle.ConstantTimeCompare(h.MAC[:], want[:peerMACLen]) == 1
}

// Encode is the hello as it travels: 64 hex characters.
func (h PeerHello) Encode() string {
	return hex.EncodeToString(h.Nonce[:]) + hex.EncodeToString(h.MAC[:])
}

// DecodePeerHello reads what Encode wrote.
func DecodePeerHello(s string) (PeerHello, bool) {
	var h PeerHello
	ok := decodePair(s, h.Nonce[:], h.MAC[:])
	return h, ok
}

// PeerReply is the server's answer to a hello.
type PeerReply struct {
	Nonce [peerNonceLen]byte
	MAC   [peerMACLen]byte
}

// Encode is the reply as it travels: 64 hex characters.
func (r PeerReply) Encode() string {
	return hex.EncodeToString(r.Nonce[:]) + hex.EncodeToString(r.MAC[:])
}

// DecodePeerReply reads what Encode wrote.
func DecodePeerReply(s string) (PeerReply, bool) {
	var r PeerReply
	ok := decodePair(s, r.Nonce[:], r.MAC[:])
	return r, ok
}

func decodePair(s string, a, b []byte) bool {
	if len(s) != peerHexLen {
		return false
	}
	raw, err := hex.DecodeString(s)
	if err != nil {
		return false
	}
	copy(a, raw[:len(a)])
	copy(b, raw[len(a):])
	return true
}

// PeerSession is what a completed handshake leaves both ends holding.
type PeerSession struct {
	key     [32]byte
	confirm [peerMACLen]byte
}

func (k PeerKey) session(label string, nc, ns []byte) *PeerSession {
	s := &PeerSession{}
	copy(s.key[:], k.mac([]byte("k"), []byte(label), nc, ns))
	copy(s.confirm[:], k.mac([]byte("f"), []byte(label), nc, ns))
	return s
}

// Reply answers a verified hello: the server's half of the handshake. The
// session it returns is not to be trusted until CheckConfirm says so.
func (k PeerKey) Reply(h PeerHello, label string) (PeerReply, *PeerSession, error) {
	var r PeerReply
	if _, err := rand.Read(r.Nonce[:]); err != nil {
		return r, nil, err
	}
	copy(r.MAC[:], k.mac([]byte("s"), []byte(label), h.Nonce[:], r.Nonce[:]))
	return r, k.session(label, h.Nonce[:], r.Nonce[:]), nil
}

// CheckReply is the client checking the server: only a holder of the key can
// answer this hello. It returns the session on success.
func (k PeerKey) CheckReply(h PeerHello, r PeerReply, label string) (*PeerSession, bool) {
	want := k.mac([]byte("s"), []byte(label), h.Nonce[:], r.Nonce[:])
	if subtle.ConstantTimeCompare(r.MAC[:], want[:peerMACLen]) != 1 {
		return nil, false
	}
	return k.session(label, h.Nonce[:], r.Nonce[:]), true
}

// Confirm is the client's last message: 32 hex characters.
func (s *PeerSession) Confirm() string { return hex.EncodeToString(s.confirm[:]) }

// CheckConfirm is the server checking the client's last message.
func (s *PeerSession) CheckConfirm(got string) bool {
	return subtle.ConstantTimeCompare([]byte(got), []byte(s.Confirm())) == 1
}

// peerTargetSep separates a destination from its seal. A NUL cannot appear in
// a host:port, so no destination an older peer sends can be mistaken for a
// sealed one.
const peerTargetSep = "\x00"

// SealTarget is a destination as the server sends it under this session.
// A nil session (a peer on the previous handshake) sends it as it is.
func (s *PeerSession) SealTarget(addr string) string {
	if s == nil {
		return addr
	}
	m := hmac.New(sha256.New, s.key[:])
	m.Write([]byte("t"))
	m.Write([]byte{0})
	m.Write([]byte(addr))
	return addr + peerTargetSep + hex.EncodeToString(m.Sum(nil)[:peerMACLen])
}

// ErrUnsealedTarget is a destination that did not come from the server this
// session was made with.
var ErrUnsealedTarget = errors.New("the destination did not carry the server's seal; " +
	"something on the path may be posing as the Iran server, so it was not dialled")

// OpenTarget checks a destination the client was sent. Under a nil session it
// is taken as it is, which is what an older server sends.
func (s *PeerSession) OpenTarget(sealed string) (string, error) {
	if s == nil {
		return sealed, nil
	}
	addr, seal, ok := strings.Cut(sealed, peerTargetSep)
	if !ok {
		return "", ErrUnsealedTarget
	}
	want := s.SealTarget(addr)[len(addr)+len(peerTargetSep):]
	if subtle.ConstantTimeCompare([]byte(seal), []byte(want)) != 1 {
		return "", ErrUnsealedTarget
	}
	return addr, nil
}

// PoolTag is what a pool connection presents under this session: derived, so
// it never has to cross the wire in the clear.
func (s *PeerSession) PoolTag() string {
	m := hmac.New(sha256.New, s.key[:])
	m.Write([]byte("p"))
	return hex.EncodeToString(m.Sum(nil))
}

// PeerGuard is one tunnel end's policy for the handshake: its key, whether a
// peer on the previous handshake is still accepted, and what it remembers.
//
// The previous handshake stays accepted so a v2 end and a v1.9 end still make
// a tunnel — the user upgrades one server and then the other. It stops being
// accepted once it cannot be needed:
//
//   - peer_auth = "required" in the file, which a v2 Setup Link writes, or
//   - the other end has completed this handshake once. That is remembered in a
//     small file beside the tunnel's config, so it survives restarts and
//     reboots: a peer does not become older, and something on the path that
//     makes the new handshake fail must not be able to push the tunnel back
//     to the old one, where the token crosses the wire.
type PeerGuard struct {
	key      PeerKey
	required bool
	pinPath  string
	token    string

	pinned atomic.Bool
	// misses counts consecutive client attempts a server closed without
	// answering the handshake, which is what an older server does.
	misses   atomic.Int32
	fallback atomic.Bool
	warned   atomic.Bool
	mu       sync.Mutex
}

// PeerAuthRequired is the config value that refuses the previous handshake.
const PeerAuthRequired = "required"

// NewPeerGuard builds the guard. pinPath may be empty (nothing is remembered
// beyond this process).
func NewPeerGuard(token, mode, pinPath string) *PeerGuard {
	g := &PeerGuard{key: PeerKeyFrom(token), required: mode == PeerAuthRequired, pinPath: pinPath, token: token}
	if pinPath != "" {
		if b, err := os.ReadFile(pinPath); err == nil && strings.TrimSpace(string(b)) == g.pinValue() {
			g.pinned.Store(true)
		}
	}
	return g
}

// pinValue ties the remembered proof to this token, so pairing the tunnel
// again with a new token starts over.
func (g *PeerGuard) pinValue() string {
	sum := sha256.Sum256([]byte("backpack/peer-auth/pin:" + g.token))
	return hex.EncodeToString(sum[:8])
}

// Key is the long-term key.
func (g *PeerGuard) Key() PeerKey { return g.key }

// Strict reports whether the previous handshake is refused. A nil guard (a
// transport built by hand in a test) keeps the behaviour before v2.0.0.
func (g *PeerGuard) Strict() bool { return g != nil && (g.required || g.pinned.Load()) }

// Proved records that the other end completed the handshake.
func (g *PeerGuard) Proved() {
	if g == nil {
		return
	}
	g.misses.Store(0)
	g.fallback.Store(false)
	if g.pinned.Swap(true) || g.pinPath == "" {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	_ = os.MkdirAll(filepath.Dir(g.pinPath), 0o755)
	_ = os.WriteFile(g.pinPath, []byte(g.pinValue()+"\n"), 0o600)
}

// PinPath is where the guard remembers the proof, for messages.
func (g *PeerGuard) PinPath() string {
	if g == nil {
		return ""
	}
	return g.pinPath
}

// PeerAuthMissThreshold is how many closed attempts in a row it takes a client
// to conclude the server predates the handshake. One is not enough: a single
// close is far more often the path than an old server.
const PeerAuthMissThreshold = 2

// UseLegacy reports whether a client should offer the previous handshake now.
func (g *PeerGuard) UseLegacy() bool { return g != nil && !g.Strict() && g.fallback.Load() }

// Missed records an attempt the server closed without answering. It reports
// true the first time that tips the client over to the previous handshake, so
// the caller can say why once.
func (g *PeerGuard) Missed() bool {
	if g == nil || g.Strict() || g.misses.Add(1) < PeerAuthMissThreshold {
		return false
	}
	g.fallback.Store(true)
	return !g.warned.Swap(true)
}

// Rearm is called when a tunnel run starts over: the next attempt tries the
// new handshake again, since a fallback drawn from an outage is a guess.
func (g *PeerGuard) Rearm() {
	if g == nil {
		return
	}
	g.misses.Store(0)
	g.fallback.Store(false)
}

// PeerPinPath is where a tunnel's guard remembers its peer, beside its config.
func PeerPinPath(configPath string) string {
	if configPath == "" {
		return ""
	}
	dir, file := filepath.Split(configPath)
	return filepath.Join(dir, "."+strings.TrimSuffix(file, filepath.Ext(file))+".peer")
}
