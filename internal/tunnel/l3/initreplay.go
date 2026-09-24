package l3

import "time"

// Refusing a handshake that has been seen before.
//
// The layer-3 handshake is Noise NNpsk0 and carries no freshness of any kind:
// the initiator's payload holds the encapsulation identifier and nothing else,
// so a recorded typeInit datagram stays valid for ever and the responder cannot
// tell a replay from a first contact. Half the consequence is already closed —
// a replay can no longer take over an established peer — and the other half
// stands: it can still displace a genuine *pending* session, and the genuine
// peer's data packets then find no session and are dropped. One recorded
// packet, reusable indefinitely, keeps a tunnel from establishing.
//
// # And the timestamp
//
// The proper fix is WireGuard's — a timestamp inside the encrypted payload,
// refused unless it advances — and it is protocol version 2, in freshness.go.
// This memory stays: it costs nothing, it covers the legacy handshake an older
// dialler still sends, and it turns a flood of one replayed packet into one
// answer.
//
// # What this does instead, and what it is worth
//
// The initiator picks a random 32-bit session identifier per handshake and it
// already travels in the header. A replay carries the identifier it was
// recorded with. Remembering the ones recently answered therefore refuses a
// replayed handshake with no wire change at all.
//
// It is bounded and it is honest about what it covers. A replay of an init
// older than the window is still accepted, so this is not the timestamp. What
// it does close is the practical attack: an attacker with one recorded packet
// replaying it repeatedly to keep a tunnel down. Under this, the first copy is
// answered and every copy after it inside the window is not — so the flood
// costs the attacker a packet and buys nothing.

const (
	// initMemory is how many recently answered handshakes are remembered.
	//
	// The dialling side rekeys every two minutes, so a handful covers several
	// rekeys with room for the retransmissions each one may produce. It is
	// deliberately small: the identifiers are random 32-bit values and
	// remembering a very large number of them starts to make an accidental
	// collision — a genuine handshake refused as a replay — more likely than
	// the attack being prevented.
	initMemory = 64

	// initMemoryWindow is how long one is remembered for. Longer than the
	// rekey interval by enough that a slow or retried handshake is still
	// covered, short enough that the set stays small on a tunnel that has been
	// up for months.
	initMemoryWindow = 10 * time.Minute
)

// seenInits remembers the handshakes recently answered, so a repeat can be
// told from a first contact.
type seenInits struct {
	ids  []uint32
	when []time.Time
}

// seen reports whether this identifier has been answered inside the window, and
// records it when it has not.
//
// One method rather than a check and a record, because the two must not be
// separable: a caller that checked and then forgot to record would turn this
// into an expensive no-op that looks like it is working.
func (s *seenInits) seen(id uint32, now time.Time) bool {
	s.expire(now)
	for _, known := range s.ids {
		if known == id {
			return true
		}
	}
	s.ids = append(s.ids, id)
	s.when = append(s.when, now)
	// Oldest out first. A ring would avoid the copy; at a handful of entries
	// touched once per rekey, the copy is not worth the index arithmetic.
	if len(s.ids) > initMemory {
		s.ids = s.ids[len(s.ids)-initMemory:]
		s.when = s.when[len(s.when)-initMemory:]
	}
	return false
}

// expire drops entries older than the window.
func (s *seenInits) expire(now time.Time) {
	cut := 0
	for cut < len(s.when) && now.Sub(s.when[cut]) > initMemoryWindow {
		cut++
	}
	if cut > 0 {
		s.ids = s.ids[cut:]
		s.when = s.when[cut:]
	}
}
