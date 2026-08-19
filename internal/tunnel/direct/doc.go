// Package direct forwards ports with the tunnel dialled the other way round.
//
// The reverse tunnel this tree was built on has the Iran server listen and the
// kharej server dial in. That is the right shape when Iran can accept an
// inbound connection on the tunnel port. Where it is not — a provider that
// filters inbound connections from abroad, a port that is blocked in one
// direction only, a host behind NAT — the tunnel cannot be established at all,
// even though the user-facing ports on Iran work perfectly well.
//
// This package turns the tunnel around. The ports stay exactly where they
// were:
//
//	reverse:  users -> [ Iran: listens ] <- dials -< [ kharej ] -> service
//	direct:   users -> [ Iran: dials  ] >- dials -> [ kharej: listens ] -> service
//
// Iran still exposes the ports and kharej still holds the real service. Only
// who reaches out first has changed, and an outbound connection from Iran to
// abroad is the ordinary direction that a filter is least likely to touch.
//
// # Why this is smaller than the reverse tunnel, not a mirror of it
//
// The reverse tunnel needs a great deal of machinery to work: a control
// channel, so the listening side can ask for another connection; a pool of
// pre-dialled connections waiting to be used; a nonce so a pool connection can
// be told from a stranger's; and a signal protocol between the two.
//
// None of that exists here, because of one property of stream multiplexing:
// once a mux session is up, *either* end can open a stream on it. The side
// that dialled the session is not the only side that can start work over it.
// So the edge dials one session, and every user connection becomes a stream on
// it, opened on demand. There is nothing to pool, nothing to signal, and no
// second channel to keep alive.
//
// # Relationship to the rest of the tree
//
// Nothing here is reachable from the reverse tunnel's code, and nothing here
// calls into it. The two share the utility layer — the Noise record layer, the
// smux settings, the binary framing helpers — and nothing else. A config
// without a [direct] table can never enter this package, and a config with one
// never reaches the reverse engine.
package direct
