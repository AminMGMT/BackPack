package network

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"sync"
)

// Authorising pool connections by what the peer knows, not where it dials from.
//
// A tunnel is a control channel plus a pool of data connections, and the data
// connections used to prove nothing at all: they were accepted purely because
// their source address matched the control channel's. That works on a client
// with one stable egress address and silently fails everywhere else — behind
// carrier-grade NAT, on a multi-homed host, behind a SNAT pool or a
// load-balanced gateway, the pool dials out from a different address than the
// control channel did and the server discards every one of them. The tunnel
// reports itself connected, because the control channel is fine, and then
// carries no traffic. It is the same shape of failure whichever way it lands:
// something about the *network path* decides whether the tunnel works, with no
// error that names the cause.
//
// So the server hands the client a fresh random nonce when the control channel
// comes up, and each pool connection presents it. The nonce is per run — a new
// one on every control channel — so it cannot be replayed into a later run, and
// it is never written anywhere the token is not already. Source address stops
// being part of the decision, which is what lets a client behind any of the
// above work exactly like one with a single static IP.

// poolNonceBytes is the size of the random value; poolNonceHexLen is its length
// once hex encoded, which is what actually travels and what Decode splits on.
const (
	poolNonceBytes  = 32
	poolNonceHexLen = poolNonceBytes * 2
)

// NewPoolNonce returns a fresh random nonce for one run of a tunnel.
func NewPoolNonce() (string, error) {
	b := make([]byte, poolNonceBytes)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("could not generate a pool nonce: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// EncodeControlAck packs the server's answer to a v2 control handshake: the
// token the client checks the server by, and the nonce its pool connections
// will carry.
//
// The nonce goes first because it is always exactly poolNonceHexLen characters,
// so the two fields split apart on a fixed offset. A separator would have to be
// a byte the token cannot contain, and the token is arbitrary user input.
func EncodeControlAck(token, nonce string) string {
	return nonce + token
}

// DecodeControlAck splits what EncodeControlAck packed. A malformed answer
// yields two empty strings, which fails the caller's token comparison — so a
// server that answers with something else is rejected rather than half-trusted.
func DecodeControlAck(ack string) (token, nonce string) {
	if len(ack) < poolNonceHexLen {
		return "", ""
	}
	return ack[poolNonceHexLen:], ack[:poolNonceHexLen]
}

// PoolNonce holds the nonce of the current run.
//
// It is read by every accepted connection and written when the control channel
// is established or torn down, so it is behind a lock rather than a plain
// string.
type PoolNonce struct {
	mu sync.RWMutex
	v  string
}

// Set publishes the nonce for a newly established control channel. Setting it
// to "" turns pool authorisation off, which is what a legacy control channel
// does.
func (p *PoolNonce) Set(v string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.v = v
}

// Get returns the current nonce, or "" when there is none.
func (p *PoolNonce) Get() string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.v
}

// Clear forgets the nonce, so connections carrying the previous run's value are
// no longer accepted.
func (p *PoolNonce) Clear() { p.Set("") }

// Verify reports whether got is the current nonce. It is false while no nonce
// is set, so clearing it closes the door rather than opening it, and the
// comparison is constant time.
func (p *PoolNonce) Verify(got string) bool {
	want := p.Get()
	if want == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}
