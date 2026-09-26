package l3

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Freshness in the handshake: protocol version 2.
//
// The layer-3 handshake is Noise NNpsk0, and its first message carried nothing
// that changes over time, so a recorded one stayed valid for ever. initreplay.go
// stops the same one being replayed inside ten minutes; this closes the rest.
// It is WireGuard's rule: the dialler puts a timestamp inside the encrypted,
// authenticated first message, and the listener refuses one that is not newer
// than the last it accepted. A recorded handshake is then worthless the moment
// a newer one has been seen, and the one piece of the message that would have
// to change to revive it is the piece nobody without the token can write.
//
// # Why it could not ship before, and how it ships now
//
// A listener before v2 compares the dialler's payload whole against its own
// encapsulation, so a payload with anything after the encapsulation is refused
// by every listener already in the field — and the header, where a version
// could be announced for free, is not authenticated, so nothing that matters
// can live there. What makes it shippable is that the refusal is not silent: a
// listener that holds the token answers a mismatched payload with a reply
// naming its own encapsulation, so that both logs can say what is wrong (see
// respondFresh). That reply is encrypted under the handshake, and only a holder
// of the token can produce it.
//
// So a v2 dialler tries the timestamp first. A v2 listener accepts it. An older
// one refuses it with an authenticated reply that carries no version block, and
// the dialler — on that reply and on nothing weaker — falls back to the legacy
// payload for a while, then tries v2 again in case the listener was upgraded.
// Something on the path cannot cause the fallback: it can drop the timestamped
// attempt, but a dropped attempt is retried, not given up on, and it cannot
// write the reply that is the only thing a fallback is taken on.
//
// A v2 listener that has once accepted a timestamp refuses a legacy payload
// from then on. Without that, the recorded handshakes of the dialler's *old*
// build would stay replayable against the new listener for ever, which is the
// hole this exists to close.
//
// # Clocks
//
// The timestamp is the dialler's wall clock in nanoseconds, forced to rise by at
// least one per handshake, and the listener compares it only with what the
// same dialler sent before. The two machines' clocks never have to agree. The
// one thing that would hurt is the dialler's clock stepping backwards by more
// than the gap between two handshakes; a listener restart forgets what it saw
// and accepts the next timestamp whatever it is, which is what makes that
// recoverable.

// freshSep separates the encapsulation from the timestamp in the payload. The
// same separator the reply's version block uses, so neither can be mistaken for
// part of an encapsulation name.
const freshSep = versionSep + "t"

// legacyRetry is how long a dialler that met an old listener keeps using the
// legacy payload before trying v2 again. Each try costs the old listener one
// line in its log, so not every rekey.
const legacyRetry = 30 * time.Minute

// clockStepGrace is how long a listener with no session goes on refusing a
// dialler's stamps as too old before it takes them as the dialler's clock
// having gone back. See freshJudge.stale. A variable so a test can shorten it.
var clockStepGrace = time.Minute

// errPeerLegacy is a timestamped attempt refused by a listener that predates
// timestamps. The dialler falls back and tries again at once.
var errPeerLegacy = errors.New("l3: the listener does not read handshake timestamps")

// initPayload renders the dialler's payload: the encapsulation alone when fresh
// is zero, as every build before v2 sent it, and the encapsulation and the
// timestamp otherwise.
func initPayload(encap string, fresh uint64) string {
	if fresh == 0 {
		return encap
	}
	return encap + freshSep + strconv.FormatUint(fresh, 10)
}

// parseInitPayload reads the dialler's payload back.
func parseInitPayload(payload string) (encap string, fresh uint64, err error) {
	encap, rest, found := strings.Cut(payload, freshSep)
	if !found {
		return payload, 0, nil
	}
	fresh, err = strconv.ParseUint(rest, 10, 64)
	if err != nil || fresh == 0 {
		return "", 0, fmt.Errorf("l3: the dialler's handshake timestamp is unreadable")
	}
	return encap, fresh, nil
}

// freshClock hands out the dialler's timestamps: its wall clock, never less
// than one more than the last one handed out.
type freshClock struct{ last uint64 }

func (c *freshClock) next(now time.Time) uint64 {
	ts := uint64(now.UnixNano())
	if ts <= c.last {
		ts = c.last + 1
	}
	c.last = ts
	return ts
}

// freshJudge is the listener's memory of what it has accepted.
type freshJudge struct {
	// last is the newest timestamp accepted.
	last uint64
	// required is set by the first timestamp accepted; from then on a legacy
	// payload is refused.
	required bool
	// staleSince is when authenticated stamps started arriving older than
	// last, with no newer one accepted since; zero when none has.
	staleSince time.Time
}

// stale decides an authenticated stamp that admit refused as not newer than
// the last one. It is taken, and becomes the new last, when this end has no
// session left (idle) and such stamps have been refused for clockStepGrace.
//
// That is a dialler whose wall clock went back — NTP correcting a clock that
// ran fast — and which then restarted, so its own memory of the last stamp is
// gone. Refusing it was right against a recorded handshake and wrong for this:
// every stamp it sends is older than the one remembered, so the tunnel stayed
// down until the clock caught up, which can be hours, or until the listener
// restarted. Reported from the field on v1.8.3 as "a direct tunnel stops
// working after a while".
//
// While a session is up nothing changes: a recorded handshake is still
// worthless the moment a newer one has been seen. With none up there is
// nothing for a replay to displace, and it still cannot complete a session
// without the dialler's keys.
func (j *freshJudge) stale(fresh uint64, idle bool, now time.Time) bool {
	if fresh == 0 || fresh > j.last {
		return false
	}
	if j.staleSince.IsZero() {
		j.staleSince = now
	}
	if !idle || now.Sub(j.staleSince) < clockStepGrace {
		return false
	}
	j.last, j.required, j.staleSince = fresh, true, time.Time{}
	return true
}

// admit decides one handshake and records it when it is admitted. fresh is zero
// for a legacy payload.
func (j *freshJudge) admit(fresh uint64) error {
	if fresh == 0 {
		if j.required {
			return errors.New("a handshake without a timestamp, after this dialler has sent timestamped " +
				"ones — a replay of a handshake recorded before the upgrade, or the dialler was " +
				"downgraded (restart this listener to accept an older dialler again)")
		}
		return nil
	}
	if fresh <= j.last {
		return fmt.Errorf("a handshake timestamped %s, not newer than the last one accepted (%s) — a replay",
			time.Unix(0, int64(fresh)).UTC().Format(time.RFC3339Nano),
			time.Unix(0, int64(j.last)).UTC().Format(time.RFC3339Nano))
	}
	j.last, j.required, j.staleSince = fresh, true, time.Time{}
	return nil
}
