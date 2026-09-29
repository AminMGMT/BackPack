package l3

import (
	"net"
	"time"
)

// What the listening side does with what arrives: a handshake to answer, a
// response, a data packet that may confirm a session.

// handleInit is the listening side's half of the handshake.
func (t *Tunnel) handleInit(h header, body []byte, from net.Addr) {
	if t.cfg.Mode != ModeListen {
		return // the dialling side does not answer handshakes
	}

	// A retransmission of a message already answered gets the same answer
	// back. Deriving a second set of keys for the same session would leave the
	// initiator holding keys this end has thrown away.
	t.mu.Lock()
	if h.session == t.lastInitID && t.lastReply != nil {
		reply := t.lastReply
		t.mu.Unlock()
		_, _ = t.carrier.WriteTo(reply, from)
		return
	}
	// A handshake answered earlier and now arriving again with a *different*
	// one in between is not a retransmission — it is a replay, and answering it
	// would displace whatever session is pending. Refused silently: a peer that
	// genuinely needs a new session picks a new identifier, so there is nothing
	// a real caller loses here.
	if t.seenInits.known(h.session, time.Now()) {
		t.mu.Unlock()
		t.stats.dropped.Add(1)
		t.log.Debugf("l3: refusing a handshake from %s: session %08x has been answered "+
			"before, so this is a replay rather than a first contact", from, h.session)
		return
	}
	t.mu.Unlock()

	// The counter on a handshake message is the dialler's announced protocol
	// version; every build before this one sent 0 there and ignored it. See
	// version.go.
	sess, reply, fresh, err := respondFresh(t.cfg.Token, h.session, int(h.counter), body, encapID(t.encap))
	if reply != nil {
		// It authenticated — whatever else is wrong with it — so it is
		// remembered now, and a replay of it is refused above. A handshake
		// that did not authenticate is not remembered: see seenInits.record.
		t.mu.Lock()
		t.seenInits.record(h.session, time.Now())
		t.mu.Unlock()
	}
	if err == nil {
		// Judged only once the handshake has authenticated: a stranger's
		// timestamp is not allowed to move what this end remembers. And
		// refused in silence, like anything else that is not answered — a
		// replay learns nothing, not even that it was recognised.
		t.mu.Lock()
		if why := t.fresh.admit(fresh); why != nil {
			remembered := t.fresh.last
			if t.fresh.stale(fresh, t.current == nil, time.Now()) {
				// Provisional until a data packet confirms the session; see
				// freshJudge.stale.
				if sess != nil {
					sess.adoptFresh, sess.adoptOver = fresh, remembered
				}
				t.mu.Unlock()
				t.log.Warnf("l3: answering a handshake from %s stamped %s earlier than the last one "+
					"accepted: no session is up and it has been refused for %s, so the other "+
					"server's clock has most likely gone back — its stamp is taken once its session "+
					"carries data", from,
					time.Duration(remembered-fresh).Round(time.Second), clockStepGrace)
			} else {
				t.mu.Unlock()
				t.stats.dropped.Add(1)
				t.log.Warnf("l3: refusing a handshake from %s: %v", from, why)
				return
			}
		} else {
			t.mu.Unlock()
		}
	}
	if err != nil {
		// A mismatched encapsulation is a misconfiguration, not an intruder:
		// the peer proved it holds the token, so it is told, loudly, and its
		// reply is still sent so it can say the same thing in its own log.
		// Everything else is met with silence — a peer without the token learns
		// nothing, not even that something is listening.
		if reply != nil {
			t.log.Errorf("l3: refusing the tunnel from %s: %v", from, err)
			_, _ = t.carrier.WriteTo(reply, from)
		} else if n, say := t.badHandshakes.allow(time.Now()); say {
			// The peer still hears nothing. The operator is told, because
			// this is what a token copied wrong looks like from here, and it
			// used to be logged at debug: the dialling side reported "did not
			// answer" for ever and this side said nothing at all. At most once
			// a minute, so a scanner cannot fill the log.
			t.log.Warnf("l3: a handshake from %s did not authenticate (%d so far): "+
				"the token on the two servers is not the same, or it is not a Backpack "+
				"tunnel — check the token with Edit → Show the token on both", from, n)
		} else {
			t.log.Debugf("l3: refusing a handshake from %s: %v", from, err)
		}
		t.stats.dropped.Add(1)
		return
	}

	t.mu.Lock()
	t.pending = sess
	t.lastInitID = h.session
	t.lastReply = reply
	// The address is provisional until a data packet confirms it, which is
	// also what promotes the session.
	//
	// Only when no peer is known at all. A legacy handshake carries no
	// freshness of any kind — NNpsk0 has none, and its payload holds the
	// encapsulation and nothing else — so a recorded one stays valid forever
	// and this end cannot tell its replay from a first contact. It used to be enough
	// that no session was CURRENT, and retireSessions clears current after
	// rejectAfterTime: five idle minutes reopened the window on every tunnel,
	// and one replayed datagram from a forged source then pointed this end's
	// outgoing traffic at an address of the attacker's choosing. It could not
	// be read there — the keys need the initiator's ephemeral, which a replay
	// does not carry — but it was not going to the peer either.
	//
	// Pinning it to "no peer has ever been seen" narrows that to the first
	// handshake after a restart, and the first authenticated packet from the
	// real peer corrects it through notePeer. The residual — a replay
	// accepted as that first handshake — is closed by the timestamp between
	// two v2 builds (freshness.go), and stays open only for a legacy dialler.
	if t.current == nil && t.peer == nil {
		t.peer = from
	}
	t.mu.Unlock()

	// Say that a peer is here, now, rather than waiting for it to send
	// something.
	//
	// Promotion deliberately waits for an authenticated data packet, because
	// installing a replayed handshake as the live session would be a denial of
	// service costing one recorded datagram. That reasoning is about which keys
	// the tunnel seals with. It is not about what the management screens say,
	// and applying it to them had a cost nobody intended: the listening side
	// published no peer until traffic happened to cross, so a tunnel that was
	// up and simply idle read as offline on one machine and online on the
	// other. A completed handshake proves the peer holds the token, which is
	// exactly what "a peer is connected" means.
	t.publishPeer()

	if _, err := t.carrier.WriteTo(reply, from); err != nil {
		t.log.Debugf("l3: answering a handshake to %s: %v", from, err)
	}
}

// handleResp hands a handshake answer to the loop waiting for it.
func (t *Tunnel) handleResp(h header, body []byte) {
	if t.cfg.Mode != ModeDial {
		return
	}
	// The body aliases the read buffer, which the next read overwrites.
	reply := handshakeReply{id: h.session, body: append([]byte(nil), body...)}
	select {
	case t.replies <- reply:
	default:
		// Nobody waiting, or the buffer is full of stale answers. Either way
		// this one is not wanted.
	}
}

// handleData decrypts one packet and writes it into the interface. It returns
// the plaintext buffer so its capacity is carried into the next call.
//
// wbuf is the caller's one-element slice for the write, reused for the same
// reason plain is.
func (t *Tunnel) handleData(plain []byte, h header, body []byte, from net.Addr) ([]byte, []byte) {
	sess, isPending := t.sessionFor(h.session)
	if sess == nil {
		t.stats.dropped.Add(1)
		return plain, nil
	}

	opened, err := sess.open(plain, h, body)
	if err != nil {
		t.stats.dropped.Add(1)
		t.log.Debugf("l3: discarding a datagram from %s: %v", from, err)
		return plain, nil
	}
	// Keep whichever buffer is larger, so the capacity settles rather than
	// being reallocated per packet.
	if cap(opened) > cap(plain) {
		plain = opened[:0]
	}

	// The packet authenticated, so everything it implies can now be trusted:
	// that these keys are live, and that this is where the peer is.
	if isPending {
		t.promote(sess)
	}
	t.notePeer(from)
	t.noteAnswered()

	inner, err := t.encap.Unwrap(opened)
	if err != nil {
		t.stats.dropped.Add(1)
		t.log.Debugf("l3: discarding a malformed inner packet from %s: %v", from, err)
		return plain, nil
	}
	// Counted before the packet is handed on, not after, and bytes before
	// packets. See Stats for why the order is load-bearing.
	//
	// Counting first also matches what the name claims. A packet that arrived,
	// authenticated and decrypted *was* received; if the interface then refuses
	// it, that is a drop, and it is counted as one below. Received-and-dropped
	// is a different fact from never-arrived, and only one of them is true here.
	t.stats.bytesIn.Add(uint64(len(inner)))
	t.stats.packetsIn.Add(1)

	return plain, inner
}

// notePeer follows a peer that has moved, which is safe only because the
// caller has already authenticated the packet the address came from.
func (t *Tunnel) notePeer(from net.Addr) {
	if from == nil {
		return
	}
	t.mu.RLock()
	same := sameAddr(t.peer, from)
	t.mu.RUnlock()
	if same {
		return
	}
	t.mu.Lock()
	previous := t.peer
	t.peer = from
	t.mu.Unlock()
	if previous != nil && !sameHost(previous, from) {
		t.log.Infof("l3: peer moved from %s to %s", previous, from)
	}
}

// sameHost reports whether two addresses name one machine, ports aside. ICMP
// has no ports: xdi reads its peer as a bare IP while the dialler resolved
// addr as host:port, so every xdi tunnel announced "peer moved from
// 1.2.3.4:6999 to 1.2.3.4" on its first packet — the same server, which read
// as a fault. The address is still updated; only the report is kept for a
// real move.
func sameHost(a, b net.Addr) bool {
	ip := func(x net.Addr) net.IP {
		switch v := x.(type) {
		case *net.UDPAddr:
			return v.IP
		case *net.IPAddr:
			return v.IP
		}
		return nil
	}
	x, y := ip(a), ip(b)
	return x != nil && x.Equal(y)
}
