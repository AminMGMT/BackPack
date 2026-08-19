package l3

// The replay window.
//
// A stream cipher over a reliable transport needs none of this: records arrive
// once, in order, and the nonce is implied by position. Datagrams give neither
// guarantee. They arrive out of order, they arrive twice when a path duplicates
// them, and an attacker who has recorded one can send it again at any time.
// Authentication alone does not help — a replayed packet is a genuine packet
// with a genuine tag, and it will decrypt perfectly.
//
// So each counter may be used exactly once, and this is what remembers which
// ones have been. It is the sliding-window scheme from RFC 4303 (IPsec), which
// WireGuard also uses, and it is deliberately not an invention: the failure
// modes of anti-replay windows are well explored and there is nothing to gain
// by exploring them again.
//
// The window tracks the highest counter accepted so far and a bitmap of the
// windowBits counters below it. A counter above the high-water mark is new and
// slides the window forward. One inside the window is accepted only if its bit
// is clear. One below the window is too old to judge — it may or may not have
// been seen before the window moved past it — and is refused, which is the
// conservative answer.
//
// # Why accept and commit are separate
//
// A counter must not be recorded until the packet carrying it has
// authenticated. If it were recorded first, anyone could advance the window by
// sending garbage with a high counter, and the genuine packets that followed
// would fall below it and be discarded. That is a denial of service costing
// one forged datagram. Callers therefore ask accept first, decrypt, and commit
// only on success.

// replayWindowBits is how far back the window remembers. 2048 is what
// WireGuard uses: wide enough to absorb the reordering of any real path,
// narrow enough that the bitmap stays 256 bytes.
const replayWindowBits = 2048

const replayWords = replayWindowBits / 64

type replayWindow struct {
	// max is the highest counter committed so far, meaningful only once
	// seeded. Counters are unsigned and start at zero, so a zero max is
	// ambiguous without it.
	max    uint64
	seeded bool
	bitmap [replayWords]uint64
}

// accept reports whether counter is eligible, without recording anything.
func (w *replayWindow) accept(counter uint64) bool {
	if !w.seeded {
		return true
	}
	if counter > w.max {
		return true // ahead of everything seen: new by definition
	}
	// Subtraction rather than counter+replayWindowBits: counter is known to be
	// no greater than max here, so this cannot overflow, whereas the addition
	// could near the top of the range.
	if w.max-counter >= replayWindowBits {
		return false // fallen out of the window
	}
	word, bit := slot(counter)
	return w.bitmap[word]&bit == 0
}

// commit records a counter whose packet has authenticated. Calling it without
// a preceding accept that returned true is a programming error; it stays
// tolerant of one rather than panicking, because the cost of a mistake here is
// a dropped packet and the cost of a panic is the tunnel.
func (w *replayWindow) commit(counter uint64) {
	if !w.seeded {
		w.seeded = true
		w.max = counter
		word, bit := slot(counter)
		w.bitmap[word] |= bit
		return
	}

	if counter > w.max {
		// Everything between the old mark and the new one is unseen, and the
		// bits standing in those slots belong to counters a full window ago.
		// They have to be cleared or those stale bits would reject packets
		// that have never arrived.
		if counter-w.max >= replayWindowBits {
			w.bitmap = [replayWords]uint64{}
		} else {
			for c := w.max + 1; c <= counter; c++ {
				word, bit := slot(c)
				w.bitmap[word] &^= bit
			}
		}
		w.max = counter
	}

	word, bit := slot(counter)
	w.bitmap[word] |= bit
}

// slot maps a counter onto its word and bit in the ring.
func slot(counter uint64) (word int, bit uint64) {
	idx := counter % replayWindowBits
	return int(idx / 64), 1 << (idx % 64)
}
