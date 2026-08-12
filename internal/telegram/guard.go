package telegram

import (
	"strconv"
	"strings"
	"sync"
	"time"
)

// Two guards stand between a button and something irreversible.
//
// A rate limit, because a phone in a pocket taps buttons, and because Restart
// pressed eight times in four seconds is eight tunnel restarts — the surest way
// to turn a moment of impatience into a real outage.
//
// A time limit on confirmations, because an inline keyboard never expires by
// itself. Without one, the "Yes, stop it" button in a message from three days
// ago is still live, and scrolling back through the chat is enough to fire it.
// Only the confirmed step is guarded: pressing Stop on an old screen merely
// re-asks the question, which costs nothing and is the natural place for the
// clock to start.

// confirmTTL is how long a confirmation stays valid.
const confirmTTL = 2 * time.Minute

// confirmToken stamps a confirmation with the time it was offered. Base 36
// keeps it short: callback data has 64 bytes for everything.
func confirmToken(now time.Time) string {
	return strconv.FormatInt(now.Unix(), 36)
}

// confirmFresh reports whether a token is still within its window. An
// unparseable token is stale rather than valid — the failure has to fall on the
// safe side.
func confirmFresh(token string, now time.Time) bool {
	sec, err := strconv.ParseInt(strings.TrimSpace(token), 36, 64)
	if err != nil {
		return false
	}
	age := now.Sub(time.Unix(sec, 0))
	return age >= -time.Minute && age <= confirmTTL
}

// Rate limiting.
//
// Two limits, because they catch different mistakes. The per-target cooldown
// stops the same tunnel being restarted twice in a row; the per-user budget
// stops a script — or a stuck client redelivering updates — from working
// through every tunnel at once.
const (
	actionCooldown = 5 * time.Second
	actionBudget   = 15
	budgetWindow   = time.Minute
)

type limiter struct {
	mu     sync.Mutex
	last   map[string]time.Time // user+target -> when it last ran
	recent map[string][]time.Time
}

var actionLimiter = &limiter{
	last:   map[string]time.Time{},
	recent: map[string][]time.Time{},
}

// allow reports whether an action may run now, and how long to wait if not.
func (l *limiter) allow(user, target string, now time.Time) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()

	key := user + "\x00" + target
	if last, ok := l.last[key]; ok {
		if wait := actionCooldown - now.Sub(last); wait > 0 {
			return false, wait
		}
	}

	// Drop everything outside the window before counting, which also keeps the
	// map from growing for a bot that runs for months.
	kept := l.recent[user][:0]
	for _, t := range l.recent[user] {
		if now.Sub(t) < budgetWindow {
			kept = append(kept, t)
		}
	}
	l.recent[user] = kept
	if len(kept) >= actionBudget {
		return false, budgetWindow - now.Sub(kept[0])
	}

	l.last[key] = now
	l.recent[user] = append(kept, now)
	return true, 0
}

// roundSeconds renders a wait as whole seconds, never zero — "wait 0s" reads
// like a bug.
func roundSeconds(d time.Duration) int {
	s := int(d.Seconds() + 0.5)
	if s < 1 {
		s = 1
	}
	return s
}
