package l3

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"github.com/flynn/noise"
	"golang.org/x/crypto/hkdf"
)

// The cryptographic session.
//
// The handshake is Noise NNpsk0 with the pre-shared key derived from the
// tunnel token, which is the same construction the stealth transport uses over
// TCP. Everything below it differs, because a datagram carrier breaks the two
// assumptions a record layer over TCP gets for free: that messages arrive, and
// that they arrive in order.
//
// What follows from that:
//
//   - The nonce is explicit, carried in the header, rather than an implicit
//     counter both ends increment in step. A single lost datagram would
//     desynchronise an implicit counter permanently.
//   - Decryption goes through noise.Cipher rather than noise.CipherState.
//     CipherState owns an internal nonce and insists messages be decrypted in
//     the order they were encrypted; Cipher takes the nonce as an argument.
//     The library documents this as the intended path for "a network protocol
//     that can deliver out-of-order messages", which is exactly this one.
//   - Every accepted counter is recorded in a replay window. See replay.go.
//   - The handshake retransmits, because its messages can be lost like any
//     other datagram.
//
// # Domain separation
//
// The prologue and the key-derivation label below are specific to this
// protocol and differ from the stealth transport's. Two consequences, both
// intended: a handshake captured from a stealth connection cannot be replayed
// into an l3 tunnel or the reverse, and the two protocols derive different
// keys from the same token, so neither can decrypt the other even in
// principle. This is why the handshake is written out again here instead of
// being lifted from internal/utils/network — the duplication is the point.

const (
	// l3Prologue is mixed into the handshake transcript by both ends and never
	// travels on the wire.
	l3Prologue = "backpack-l3-v1"

	// l3PSKLabel separates this protocol's key schedule from every other use
	// of the same token.
	l3PSKLabel = "backpack-l3-psk-v1"
)

// Session lifetime.
//
// The message limits sit far below the 2^64 ceiling the nonce imposes, so
// exhaustion is something the rekey timer prevents rather than something the
// data path has to survive. The time limits are what actually fire in
// practice: a busy tunnel rekeys every two minutes, which bounds how much
// traffic a single compromised key could ever expose.
const (
	// rekeyAfterMessages triggers a new handshake well before the nonce space
	// is anywhere near spent.
	rekeyAfterMessages = uint64(1) << 48

	// rejectAfterMessages is the hard stop. A session that somehow reaches it
	// refuses to encrypt rather than risk reusing a nonce.
	rejectAfterMessages = uint64(1) << 60

	// rekeyAfterTime is how long the dialling side lets a session run before
	// starting its replacement.
	rekeyAfterTime = 2 * time.Minute

	// rejectAfterTime is when a session stops being usable at all. The gap
	// between this and rekeyAfterTime is the window in which a rekey has to
	// complete, and it is generous because a failed rekey costs connectivity.
	rejectAfterTime = 5 * time.Minute
)

var (
	errSessionExhausted = errors.New("l3: session has sent its maximum number of messages")
	errReplayed         = errors.New("l3: counter already used or outside the replay window")
	errHandshakeShort   = errors.New("l3: handshake did not yield transport keys")
)

// Agreeing on the encapsulation, in the handshake.
//
// The two ends must wrap packets the same way, and until this existed nothing
// checked that they did. A tunnel whose ends disagreed came up perfectly: the
// handshake has no encapsulation in it, so the keys matched, the session was
// established, the peer was reported, and the panel went green on both
// machines. Then every single data packet decrypted correctly and was thrown
// away one layer later, because an IPIP sender's payload is an IP packet and a
// GRE receiver reads the first four bytes of it as a GRE header.
//
// The symptom is a tunnel that is up, has a peer, logs no errors above debug,
// and moves nothing at all. It was found in the field, on a pair of servers
// where one end had been set up with IPIP and the other with GRE, and it cost
// an afternoon because every diagnostic said the tunnel was fine.
//
// So the identifier travels in the handshake payload, which Noise encrypts and
// authenticates along with everything else, and a mismatch is refused by name
// on both ends. It is the same reasoning that made the token asymmetric in the
// wizard: two values that must match, and silence when they do not, is a
// combination worth engineering against.

// encapID is what each end announces. The key is part of it because a GRE key
// mismatch fails in exactly the same silent way as an encapsulation mismatch.
func encapID(e Encap) string {
	if g, ok := e.(greEncap); ok && g.keyed {
		return fmt.Sprintf("gre:%d", g.key)
	}
	return e.Name()
}

// errEncapMismatch names both sides, because the message is read on a machine
// that only knows its own half.
func errEncapMismatch(mine, theirs string) error {
	return fmt.Errorf(
		"l3: the two ends wrap packets differently — this end uses %q and the peer uses %q. "+
			"Set the same encap (and gre_key) on both machines; until then every packet is "+
			"decrypted and then discarded, which looks like a tunnel that is up but carries nothing",
		mine, theirs)
}

// l3Suite is fixed, not negotiated: X25519, ChaCha20-Poly1305, BLAKE2s. A
// negotiation would be one more thing on the wire to fingerprint, and there is
// no second choice worth offering.
var l3Suite = noise.NewCipherSuite(noise.DH25519, noise.CipherChaChaPoly, noise.HashBLAKE2s)

// l3PSK turns the tunnel token into this protocol's 32-byte pre-shared key.
func l3PSK(token string) ([]byte, error) {
	psk := make([]byte, 32)
	r := hkdf.New(sha256.New, []byte(token), []byte(l3PSKLabel), nil)
	if _, err := io.ReadFull(r, psk); err != nil {
		return nil, err
	}
	return psk, nil
}

// session is one set of transport keys, identified on the wire by id.
//
// Both directions are covered: send encrypts what this host originates, recv
// decrypts what the peer sends. They are distinct keys, so the two directions
// have independent nonce spaces and a packet cannot be reflected back.
type session struct {
	id      uint32
	created time.Time

	send noise.Cipher
	recv noise.Cipher

	// sendCounter is the next nonce to use. Atomic so a future multi-writer
	// send path cannot hand the same nonce to two packets, which is the one
	// mistake this construction does not survive.
	sendCounter atomic.Uint64

	// recvMu guards the replay window across the whole accept/decrypt/commit
	// sequence. Held over the decryption rather than around it: the three
	// steps have to be atomic with respect to each other, or two datagrams
	// carrying the same counter could both pass accept before either commits.
	recvMu sync.Mutex
	replay replayWindow
}

func newSession(id uint32, sendCS, recvCS *noise.CipherState) (*session, error) {
	if sendCS == nil || recvCS == nil {
		return nil, errHandshakeShort
	}
	return &session{
		id:      id,
		created: time.Now(),
		send:    sendCS.Cipher(),
		recv:    recvCS.Cipher(),
	}, nil
}

// seal encapsulates one already-encapsulated packet as a data message,
// appending the whole datagram to dst[:0] and returning it.
func (s *session) seal(dst, plaintext []byte) ([]byte, error) {
	return s.sealKind(dst, plaintext, typeData)
}

// sealKind is seal for a message that is not user data. The kind is part of the
// authenticated header, so a probe cannot be replayed as a data packet or the
// other way round.
func (s *session) sealKind(dst, plaintext []byte, kind byte) ([]byte, error) {
	// Add returns the new value, so the nonce this call owns is one less. No
	// two calls can ever be handed the same one.
	counter := s.sendCounter.Add(1) - 1
	if counter >= rejectAfterMessages {
		return nil, errSessionExhausted
	}
	h := header{kind: kind, session: s.id, counter: counter}
	ad := h.bytes()
	dst = append(dst[:0], ad[:]...)
	return s.send.Encrypt(dst, counter, ad[:], plaintext), nil
}

// open authenticates and decrypts a data message, appending the plaintext to
// dst[:0]. The header is the additional data, so a peer cannot alter the
// session or counter fields without the tag failing.
func (s *session) open(dst []byte, h header, body []byte) ([]byte, error) {
	s.recvMu.Lock()
	defer s.recvMu.Unlock()

	// Asked before the work, so a flood of replays costs a bitmap lookup
	// rather than a decryption.
	if !s.replay.accept(h.counter) {
		return nil, errReplayed
	}
	ad := h.bytes()
	plain, err := s.recv.Decrypt(dst[:0], h.counter, ad[:], body)
	if err != nil {
		return nil, err
	}
	// Only now, once the packet has proved itself. Committing before this
	// would let a forged datagram with a high counter drag the window past
	// every genuine packet still in flight.
	s.replay.commit(h.counter)
	return plain, nil
}

// dueForRekey reports whether this session should be replaced. Only the
// dialling side acts on it; the listener rekeys when told to.
func (s *session) dueForRekey(now time.Time) bool {
	return s.sendCounter.Load() >= rekeyAfterMessages || now.Sub(s.created) >= rekeyAfterTime
}

// expired reports whether the session is too old to use at all.
func (s *session) expired(now time.Time) bool {
	return now.Sub(s.created) >= rejectAfterTime
}

// newHandshakeState builds the Noise state for one handshake attempt.
func newHandshakeState(token string, initiator bool) (*noise.HandshakeState, error) {
	psk, err := l3PSK(token)
	if err != nil {
		return nil, err
	}
	return noise.NewHandshakeState(noise.Config{
		CipherSuite:  l3Suite,
		Pattern:      noise.HandshakeNN,
		Initiator:    initiator,
		Prologue:     []byte(l3Prologue),
		PresharedKey: psk,
		// Placement 0 is NNpsk0: the key is mixed in before the first message,
		// so a peer without the token cannot produce anything the responder
		// will accept, and the responder answers nothing at all.
		PresharedKeyPlacement: 0,
	})
}

// initiatorHandshake is one in-flight attempt from the dialling side. It holds
// the message so a retransmission resends the identical bytes rather than
// building a new handshake each time — a fresh ephemeral key per retry would
// mean the reply to the first attempt could no longer be read.
type initiatorHandshake struct {
	id    uint32
	state *noise.HandshakeState
	msg   []byte

	// encap is what this end announced, kept so complete can name both sides
	// when the answer disagrees.
	encap string

	// announced is the protocol version this end put in the header, and
	// agreed is what the two ends settled on once the reply arrived. See
	// version.go for why the announcement travels in the header and the
	// agreement is confirmed inside the encrypted reply.
	announced int
	agreed    int

	// fresh is the timestamp this attempt carries in its payload, or zero for
	// the legacy payload an older listener can read. See freshness.go.
	fresh uint64
}

// beginHandshake starts an attempt, returning the state and the message to
// send. avoid is the id of the session being replaced, so a rekey cannot
// accidentally reuse it.
func beginHandshake(token string, avoid uint32, encap string) (*initiatorHandshake, error) {
	return beginHandshakeFresh(token, avoid, encap, 0)
}

// beginHandshakeFresh is beginHandshake with a timestamp in the payload, or
// the legacy payload when fresh is zero.
func beginHandshakeFresh(token string, avoid uint32, encap string, fresh uint64) (*initiatorHandshake, error) {
	state, err := newHandshakeState(token, true)
	if err != nil {
		return nil, fmt.Errorf("l3: building the handshake: %w", err)
	}
	// -> psk, e, and the encapsulation this end will use. NNpsk0 mixes the
	// pre-shared key in before the first message, so the payload is encrypted
	// and authenticated like everything else — including the timestamp, which
	// is why it lives here and not in the header.
	msg, _, _, err := state.WriteMessage(nil, []byte(initPayload(encap, fresh)))
	if err != nil {
		return nil, fmt.Errorf("l3: writing the first handshake message: %w", err)
	}
	id, err := newSessionID(avoid)
	if err != nil {
		return nil, err
	}
	return &initiatorHandshake{
		id: id, state: state, msg: msg, encap: encap,
		announced: versionCurrent,
		agreed:    versionLegacy, // until the reply says otherwise
		fresh:     fresh,
	}, nil
}

// datagram renders the attempt as a message ready for the carrier.
func (h *initiatorHandshake) datagram() []byte {
	// The counter field is unused on a handshake message and unvalidated by
	// every build before this one, which is what makes it the one place a new
	// dialler can announce itself without an old listener noticing. See
	// version.go.
	hdr := header{kind: typeInit, session: h.id, counter: uint64(h.announced)}.bytes()
	return append(hdr[:], h.msg...)
}

// complete consumes the responder's reply and yields the session.
func (h *initiatorHandshake) complete(reply []byte) (*session, error) {
	// <- e, ee. A wrong token, a corrupted reply or an answer from something
	// that is not the peer all fail here.
	payload, cs0, cs1, err := h.state.ReadMessage(nil, reply)
	if err != nil {
		return nil, fmt.Errorf("l3: handshake rejected: %w", err)
	}
	theirEncap, theirVersion, sawMine, err := parseReplyPayload(string(payload))
	if err != nil {
		return nil, err
	}
	// An empty payload is a peer built before this check existed. Its
	// encapsulation cannot be read, so it is not judged — the alternative would
	// be refusing to talk to a working older build.
	if theirEncap != "" && theirEncap != h.encap {
		return nil, errEncapMismatch(h.encap, theirEncap)
	}
	// A peer that answered with a version block saw what this end announced,
	// and says so. If it saw something else, the header was rewritten on the
	// way — which can only ever weaken the terms. See version.go.
	if theirVersion > versionLegacy && sawMine != h.announced {
		return nil, errDowngraded(h.announced, sawMine)
	}
	// A timestamped attempt answered by a listener that does not read
	// timestamps was refused by it: it compared the whole payload with its
	// encapsulation, found them different, and answered only so this end
	// would know. The answer is authenticated — only a holder of the token
	// can write it — so falling back on it cannot be forced by the path.
	if h.fresh != 0 && theirVersion < version2 {
		return nil, errPeerLegacy
	}
	h.agreed = agreedVersion(h.announced, theirVersion)
	// The initiator sends under cs0 and receives under cs1; the responder
	// mirrors it. Getting this pair the wrong way round would produce a
	// tunnel in which nothing decrypts.
	return newSession(h.id, cs0, cs1)
}

// respond is the listening side of the handshake: it consumes the initiator's
// message and produces both the session and the reply to send back.
func respond(token string, id uint32, msg []byte, encap string) (*session, []byte, error) {
	return respondV(token, id, versionLegacy, msg, encap)
}

// respondV is respond with the version the dialler announced in its header.
//
// Split so the old signature keeps working for the tests and callers that do
// not care, and so there is exactly one place that decides what a reply looks
// like.
func respondV(token string, id uint32, peerVersion int, msg []byte, encap string) (*session, []byte, error) {
	sess, reply, _, err := respondFresh(token, id, peerVersion, msg, encap)
	return sess, reply, err
}

// respondFresh is respondV that also reports the timestamp the dialler's
// payload carried, zero for a legacy payload. Judging it is the caller's: it
// is the one that remembers what was seen before.
func respondFresh(token string, id uint32, peerVersion int, msg []byte, encap string) (*session, []byte, uint64, error) {
	state, err := newHandshakeState(token, false)
	if err != nil {
		return nil, nil, 0, fmt.Errorf("l3: building the handshake: %w", err)
	}
	payload, _, _, err := state.ReadMessage(nil, msg)
	if err != nil {
		// Anything that does not hold the token lands here, and gets no reply.
		// A scanner finds a socket that never answers.
		return nil, nil, 0, fmt.Errorf("l3: handshake rejected: %w", err)
	}

	// The dialler's payload is the encapsulation, and from v2 on a timestamp
	// after it (initPayload). An old listener compares the payload whole,
	// which is what tells a v2 dialler it has met one; this one reads both.
	theirs, fresh, err := parseInitPayload(string(payload))
	if err != nil {
		return nil, nil, 0, err
	}

	// The reply is built even when the encapsulation disagrees, and carries
	// this end's own identifier. The peer holds the token — it proved that
	// above — so it is not a stranger to be met with silence; it is the other
	// half of a misconfigured tunnel, and it can only say so in its own log if
	// it is told what this end uses.
	//
	// A timestamped payload is answered with a version block whatever the
	// header claimed. The header is not authenticated: rewritten to version 0,
	// it used to draw a reply with no block — the shape an old listener gives —
	// and the dialler fell back to the replayable handshake on the say-so of a
	// byte flipped in transit. Answering what was really read (v2, saw 0) lets
	// the dialler's downgrade check refuse it instead. The payload is the part
	// that is authenticated, so it is what decides.
	var answer string
	if fresh != 0 {
		answer = replyPayloadBlock(encap, versionCurrent, peerVersion)
	} else {
		answer = replyPayload(encap, versionCurrent, peerVersion)
	}
	reply, cs0, cs1, err := state.WriteMessage(nil, []byte(answer))
	if err != nil {
		return nil, nil, 0, fmt.Errorf("l3: writing the handshake reply: %w", err)
	}
	hdr := header{kind: typeResp, session: id, counter: 0}.bytes()
	datagram := append(hdr[:], reply...)

	if theirs != "" && theirs != encap {
		return nil, datagram, 0, errEncapMismatch(encap, theirs)
	}

	sess, err := newSession(id, cs1, cs0)
	if err != nil {
		return nil, nil, 0, err
	}
	return sess, datagram, fresh, nil
}

// newSessionID draws a random identifier, never zero and never the one being
// replaced. Zero is reserved so that an uninitialised field cannot be mistaken
// for a live session.
func newSessionID(avoid uint32) (uint32, error) {
	var b [4]byte
	for attempt := 0; attempt < 8; attempt++ {
		if _, err := rand.Read(b[:]); err != nil {
			return 0, fmt.Errorf("l3: drawing a session id: %w", err)
		}
		id := binary.BigEndian.Uint32(b[:])
		if id != 0 && id != avoid {
			return id, nil
		}
	}
	return 0, errors.New("l3: could not draw a distinct session id")
}
