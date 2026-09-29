# ADR 0001 — A reverse transport runs in generations, and the generation is shared code

**Status:** accepted (v1.8.5 cycle, 2026-09-28)

## Context

The reverse tunnel has seven transports on each side (`tcp`, `tcpmux`, `ws`,
`wsmux`, `kcp`, `quic`, `udp`; `stealth`, `wss`, `wssmux`, `xdi` and `pck` ride
one of them). Each was a near-copy of the others, and each carried its own copy
of the parts that have nothing to do with the transport:

- ending a run when the control channel is lost and starting the next one;
- binding the forwarded ports, admitting users under the limits, queueing them;
- the control channel's loop — heartbeat, requests for pool connections, the
  goodbye;
- how a signal is written on the wire, and how long a write may block.

Copies drift. The 2026-09-27 audit found the same defect in six of them at once
(queued users' limiter slots leaked when a run ended; no header timeout on the
websocket listeners), and the unification below found more that had drifted
silently: two server loops read one byte from the client and stopped listening;
the websocket pair dropped the tunnel on a signal it did not know; every loop
wrote to whatever control channel the transport held at the moment of writing,
which for a goroutine running late is the next run's; two client restarts left
the last run's pool figures on the panel; only one client copy had learned not
to blame the path for a timeout the beat clock had already explained.

## Decision

A transport runs in **generations**. A generation is one control channel and
everything that serves it, sharing one context. The generation is shared code:

- `lifecycle` (each side) owns the run: one restart at a time, the old
  generation's ports waited for rather than slept past, nothing rebuilt once
  the tunnel itself is shutting down, the published status and peer cleared on
  every way out. A transport embeds it and supplies what to close, what to
  reset and how to start.
- `portForwarder` (server) owns the forwarded ports. A transport supplies its
  socket options and what a queued user asks for.
- `controlLoop` (each side) owns the control channel, bound at start to the
  channel its generation was established with. A transport supplies the link
  and, where it needs them, the RTT probe, the goodbye's flush time, and
  whether its server expects heartbeats answered.
- `internal/controlwire` owns the wire: one byte on a stream, one binary
  message on a websocket, and the single write bound both ends use.

What outlives a generation is exactly what is on the transport struct and its
configuration; everything a generation owns is built for it and handed to it.

## Consequences

- A fix to any of these is a fix to fourteen transports, and a test of it is a
  test of all of them: the modules are tested through their interfaces
  (`controlloop_test.go`, `limitrelease_test.go`, `restart_test.go`, the
  `controlwire` tests), and the source-reading parity tests now check that each
  transport delegates rather than that each carries a copy.
- Compatibility is kept deliberately where the unification could have broken
  it. The server sends the RTT probe only to `tcp` and `udp` clients, because
  the others restart on a signal they do not expect; the client answers
  heartbeats only on the websocket transports, because a stream server of an
  earlier release reads one byte and stops, and an answered heartbeat would be
  the only thing it ever heard. The server now ignores a signal it does not
  know instead of dropping the tunnel, so a client newer than the server does
  not take it down.
- The client still restarts on a signal it does not know, as every release
  before it did, and so do the websocket servers of earlier releases. A signal
  added later therefore has to wait, in either direction, for a version the
  far end announces — or it takes the older peers down.
- A new transport starts with all of this and has to write only what is
  genuinely its own.
