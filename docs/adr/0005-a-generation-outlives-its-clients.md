# ADR 0005 — A generation outlives its clients

**Status:** accepted (v1.8.5 cycle, 2026-09-28). Amends ADR 0001.

## Context

ADR 0001 made a reverse transport run in generations: the tunnel port, the
forwarded ports, the pairing workers and the client's control channel were one
unit, started together and ended together. Any trouble with the client ended
the unit — a second control claim, a failed read or write on the control
channel, the client's goodbye — and the next generation bound every port again.

On the paths these tunnels run over that was the common case, not the rare
one. The link between an Iran server and a kharej drops for a second at a time;
the kharej notices first (it keeps a read deadline), re-dials, and its claim
reached an Iran side that still held the old channel — a restart. Every such
blip closed the forwarded ports and cut every user on them, and the log filled
with `restarting server...`. With two kharej holding one token it never
stopped. Reported from several servers on v1.8.4; a fix that circulated
swapped the channel in place on a re-claim, for the stream transports only, and
not on a failed channel.

## Decision

A generation is the tunnel port, the forwarded ports and the pairing workers.
The client — its control channel, the loop serving it, its pool nonce, and the
pool connections and mux sessions it opened — sits in the generation's seat
(`internal/server/transport/seat.go`) and is replaced in place:

- A claim that proves the token while the generation is serving takes the
  seat. The previous client's loop says goodbye on its own channel and ends,
  its queued pool connections are closed, and its mux sessions — which run on
  the client's context, not the generation's — end with it.
- A control channel that fails empties the seat. The generation keeps
  running; users who arrive meanwhile are accepted and wait (up to the pairing
  timeout) for the next client, whose claim is seated the same way.
- Every change of seat happens under one lock, so a channel that fails just as
  its replacement arrives cannot clear the new client.
- Two clients taking the seat from each other are named in the log, once per
  five minutes, since adoption makes that quiet rather than fixing it.

Only a failure of the generation itself — its listener — rebuilds it.

## Consequences

- A kharej re-dialing costs its own flows and nothing else: no port is
  re-bound, no other user is cut, and there is no restart in the log.
  `TestAKharejReconnectingDoesNotRestartTheIranSide` runs it on the real binary
  for all seven transports; against v1.8.4 every one of them fails.
- Nothing changes on the wire. An older client's claim is answered exactly as
  before; it is only no longer followed by a restart. The kcp claim that used
  to be told `RefusedRestarting` is now simply answered.
- Anything a transport keeps per client has to be undone in its `vacate`, and
  anything a client opens has to run on the seat's client context, because the
  generation's end no longer cleans up after a client. The churn test
  (`TestAServerOutlivesItsClientsWithoutLeaking`) is what holds that.
