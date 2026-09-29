# Backpack — domain language

The words this codebase uses for its own concepts, each with the one meaning it
has here. Code, comments, docs and reviews use them in this sense; where two
words compete for one idea, the one listed wins and the other is noted under
_Avoid_. The map of packages is [docs/architecture.md](docs/architecture.md);
decisions with lasting consequences are in [docs/adr](docs/adr).

## Places

**Iran side** — the machine users connect to. In the reverse tunnel it is the
**server**: it listens, and exposes the forwarded ports.

**Kharej side** — the machine abroad whose connectivity the tunnel borrows. In
the reverse tunnel it is the **client**: it dials out to the Iran side, and
dials the real service locally for each user.

_Avoid_: "local"/"remote" for the two sides — both words are relative to
whoever is reading.

## The tunnel

**Tunnel** — one configuration file, one systemd unit, one process. It runs
exactly one engine.

**Engine** — the code a tunnel runs, chosen by which table its file has, in this
order: **layer-3** (`[l3]`, a TUN device and a Noise session), **direct**
(`[direct]`, layer 4, dials out), **reverse** (`[server]`/`[client]`). The three
share no configuration key and no code.

**Transport** — how the reverse engine's connections travel: `tcp`, `tcpmux`,
`stealth`, `ws`, `wss`, `wsmux`, `wssmux`, `kcp`, `quic`, `udp`, `xdi`, `pck`.
Several are one implementation with a layer added (stealth is tcp under Noise,
wss is ws under TLS, xdi and pck are KCP over another socket).

**Carrier** — how the layer-3 and direct engines' packets travel: `udp`, `pck`,
`sni`, `xdi`, `spoof`, `quic`. A carrier is not a transport; they belong to
different engines.

**Forwarded port** — a port the Iran side listens on for users, mapped to a
target on the kharej side (`443=127.0.0.1:2096`). _Avoid_: "inbound".

## The reverse engine at run time

**Control channel** — the one long-lived connection between the two sides of a
reverse tunnel. It carries **signals**, never user data.

**Signal** — one byte on the control channel (`utils.SG_*`): **heartbeat**,
**request** (for one more pool connection), **RTT probe**, **goodbye**
(`SG_Closed`). See `internal/controlwire`.

**Pool connection** — a tunnel connection the client opens ahead of demand, so
a user is paired at once. The **pool** is sized by `poolMaintainer`.

**Pairing** — matching a queued user connection with a pool connection on the
Iran side. See `pairing.go`.

**Generation** — the tunnel listener, the forwarded ports and the pairing
workers, sharing one context. Only a failure of the listener ends it. See
[ADR 0001](docs/adr/0001-reverse-transport-generations.md).
_Avoid_: "session" (a KCP/smux/QUIC term here), "run" in new code.

**Client** (server side) — the kharej a generation serves: its control
channel, the control loop, its pool nonce and the pool connections and mux
sessions it opened. It sits in the generation's **seat** and is replaced in
place when a new one proves the token or its channel fails; the generation goes
on. See [ADR 0005](docs/adr/0005-a-generation-outlives-its-clients.md).

**Peer** — the address of the other side's control channel, as the engine
reports it. A tunnel with a peer has a control channel up.

## Watching and managing

**Snapshot** — the JSON file an engine writes every thirty seconds (traffic,
peer, pool, resources). The only way anything outside the engine learns what it
is doing. See `internal/metrics`.

**Watchdog** — the part of the monitor service that reads snapshots and
restarts a tunnel that has stopped working.

**Spec** — the editable description of a tunnel that setup and edit work on,
rendered to a configuration file. _Avoid_: "profile".

**Preset** — a named set of tuning values applied to a spec.

**Fleet** — the servers a panel manages over SSH; each is a **node**.

**Backup snapshot** — a copy of the binary and the configurations taken before
an update, so it can be rolled back (`backup.Snapshot`). Always written in full,
to keep it apart from the metrics snapshot.
