# Changelog

All notable changes to Backpack are documented here.

## Unreleased

### Fixed
- **Server setup asks about UDP forwarding in the main flow, and its firewall
  advice now matches the answer.** v1.7.2 made forwarded UDP opt-in again, for
  the QUIC reason below, but left the setup wizard describing the old default:
  after the exposed ports it still announced "These ports carry UDP as well as
  TCP" and told you to run `ufw allow <port>/udp` — on a tunnel that had just
  been created with `accept_udp = false`. The only place to turn UDP on during
  setup was a question inside "Fine-tune the advanced settings by hand", which
  defaults to *no*, so a fresh install never saw it. The result was reported as
  a TCP Mux bug: a server upgraded from v1.7.1 kept forwarding UDP (its config
  already said `accept_udp = true`, and an upgrade does not rewrite configs)
  while a fresh v1.7.2 install on the same setup carried TCP only, with nothing
  on screen to explain the difference.

  The question is now asked in the main server flow, right after the exposed
  ports it applies to, with the trade named on both sides — yes for
  Xray/Shadowsocks UDP, WireGuard, DNS and games; no for a plain web or proxy
  tunnel, whose browser QUIC would otherwise crowd out the TCP forwards. The
  firewall line that follows then names only the protocol the tunnel actually
  carries, and says where to change it later. The default is unchanged: still
  off. See [docs/forwarded-udp.md](docs/forwarded-udp.md).

## v1.7.2 — 2026-08-14

### Added
- **A Throughput preset, for `udp + kcp + fec` only.** Balance, Turbo and
  Aggressive are all gaming profiles: they differ in how much headroom they buy,
  but every one of them spends bandwidth to hold the ping steady — immediate
  ACKs, a 10 ms tick, and enough parity to repair a loss rather than wait a round
  trip for the retransmit. That is the right trade for a game and the wrong one
  for a download, and no tuning inside those three fixes it, because the cost is
  the point. This profile makes the opposite trade on the same transport: ACKs
  batched, a 20 ms tick, 10:1 parity instead of 10:4, a 4096-packet window and a
  32 MB per-stream buffer, so one stream can fill a long fat path (~210 Mbit/s at
  200 ms round trip, against ~105 on Aggressive). Measured end to end on
  loopback it carries roughly twice what Aggressive does. It is offered on the
  plain `kcp` transport alone — the other KCP carriers (xdi, spoof, pck) build
  their packets by hand and pay a syscall per datagram, so bandwidth is not what
  they are for, and on a TCP transport every knob it changes belongs to the
  kernel rather than to this process. Choosing it elsewhere is refused with a
  message saying so, rather than written to the config and quietly ignored.
  **Not a gaming preset:** with congestion control off, a window that large is a
  queue that deep. Use Turbo or Aggressive for play and this for transfers.
- **Multi-exit failover with health scoring.** A client with more than one server
  address can now steer traffic to the healthiest one on its own. A scoring loop
  measures every exit every few seconds and ranks it by
  `rtt + 2·jitter + 20·loss%` — the weighting that puts a steady 90 ms exit ahead
  of a 60 ms one that stutters — then keeps new connections on the best exit,
  with hysteresis (a challenger must be ≥15% better for three checks running)
  so the choice does not flap. It is the piece that turns a plain fallback list
  into the multi-exit gaming behaviour, and it steps off an exit as it *degrades*
  rather than only when it dies. Opt-in per tunnel (`health_failover`), asked at
  setup when a tunnel has backup addresses, and it overrides load balancing
  (steering to one best exit is the opposite of spreading across all of them).
  **Manage → Exit Health** is the manual companion: it scores and ranks every
  address on demand and offers to pin the healthiest as the primary.
- **Game Latency Test (Manage → Game Latency Test).** Estimates the in-game ping
  a player would feel through this exit without anyone installing a game or a
  config. It pings the nearest edge of popular game publishers (Dota 2, CS2,
  Valorant, PUBG, Fortnite and more) from the abroad server, measures the
  exit-to-game leg, adds the hub-to-exit tunnel leg where a TCP tunnel makes it
  measurable, and rates the result with a typical player last mile folded in.
  Test one game, every game through an exit, or a custom host. The endpoint list
  is bundled but editable (`/etc/backpack/game-endpoints.list`) — publishers move
  addresses, and many game servers filter ICMP, both of which the tool says
  plainly rather than guessing around.
- **FEC recommendation in the Link Test.** After measuring loss, the test now
  names the exact parity ratio to run on a KCP link — the setting that most
  decides how a lossy route feels — sized so the parity always clears the loss
  with burst headroom (10:5 at a few percent, 8:8 in the teens, 4:8 past 20%).
  On the CLI it offers to apply the ratio and, on a switch to KCP, sets it as
  part of the switch; the web panel shows it alongside the transport pick. Both
  ends must run the same ratio, which every screen repeats.

### Changed
- **KCP sessions run in stream mode, which is worth 5-7% on every preset.**
  kcp-go leaves stream mode off by default, and nothing here turned it on. Off,
  every write becomes its own segment and the last one goes out part-empty
  however few bytes it holds — so a tunnel carrying SMUX, whose frames are a
  header plus whatever the application happened to write, spent a share of every
  packet on padding. On, a short write is appended to the segment already queued
  until that segment is full. What rides on these sessions is a byte stream, so
  message boundaries were never meaningful, and `SetWriteDelay(false)` still
  flushes on the next tick — it costs no latency. Measured at 5-7% more
  throughput on Balance, Turbo, Aggressive and Throughput alike.
- **Aggressive is now genuinely maximal in everything that does not cost ping.**
  Socket buffers 16 → 32 MB and the per-stream mux buffer 8 → 16 MB, so flow
  control stops being what limits a transfer and a full window can land in one
  burst without the kernel dropping its tail. The KCP window deliberately stays
  at 2048: with congestion control off the window *is* the queue, and a deeper
  queue is exactly the bufferbloat a gaming preset exists to prevent — wanting a
  bigger one is wanting the new Throughput profile. Worst-case memory is
  unchanged at 16 × 32 MB.
- **UDP + KCP is now "UDP + KCP + FEC", a low-latency gaming transport.** The
  transport was already running gaming-grade ARQ on Turbo and Aggressive; it is
  now tuned that way throughout and named for what it is. Every preset — Balance
  included — runs the same latency-first ARQ (NoDelay, a 10 ms tick, Resend=2,
  KCP's own congestion window off, immediate ACKs) and carries **FEC by
  default**: Balance 10:2, Turbo 10:3, Aggressive 10:4. Send/receive windows are
  pulled back to near the bandwidth-delay product (512 / 1024 / 2048 packets)
  so that, with congestion control off, buffering — and therefore ping — stays
  bounded instead of ballooning under a large window. The trade is deliberate:
  a little peak throughput for a steadier ping, which is what a game needs.
  Existing tunnels keep their on-disk numbers until a preset is re-applied.

### Added
- **TCP + PCK — a TCP transport that does not use the kernel's TCP stack.**
  Setup → TCP → TCP + PCK. Linux, root, and both ends must be on it.

  The problem it is for is the one where a plain TCP tunnel connects and then
  dies, stalls or is throttled, and nothing in any log says why. The cause in
  those cases is usually something acting on the *connection* rather than on the
  packets: connection tracking holding state, a netfilter rule matching the flow,
  a middlebox that recognised the transfer once it got going. Every one of those
  levers is attached to the kernel's TCP stack, so this transport does not use
  it. Receive is a packet socket, which taps the device driver upstream of
  conntrack, of every netfilter chain and of reverse-path filtering; send builds
  the frame and hands it to the same driver, falling back to a raw IP socket on a
  link with no L2 header to build. KCP above supplies the reliability the absent
  stack would have, so the presets, error correction and encryption are the ones
  the `kcp` transport already uses.

  **It forges nothing.** The source address is the machine's real one and the
  ports are real, so replies route normally and none of the proving that
  [IP Spoofing](docs/transports.md) needs applies here. What does not exist is
  the connection — no handshake, no socket, no kernel state on either host —
  while the segments carry what a real one's do: timestamps on every one,
  sequence and acknowledgement numbers that track the bytes actually exchanged, a
  normal window, DSCP marking, and a flag pattern the operator can vary. Those
  are not decoration; a segment with no options, an acknowledgement of zero or a
  source port equal to its destination is classifiable on the header alone, and
  the tunnel would go on working perfectly while being trivially picked out.

  There is nothing to configure. The reference implementation this takes its
  approach from asks for the interface, the local address and the gateway's MAC,
  and spends a page of its README explaining how to find each; all three are in
  the routing and neighbour tables, so they are read. The `pck_interface` and
  `pck_gateway_mac` keys exist only to correct a wrong guess on an unusual host.

  Two firewall rules are installed on start and removed on stop: one dropping the
  kernel's RSTs for the tunnel's port — the kernel is not listening there, so it
  answers every arriving segment with one, and to a stateful device in between
  that RST is the connection ending — and one keeping the pseudo-flows out of
  conntrack. They are tagged `backpack-pck-<port>`. Without `iptables` the tunnel
  runs and is unreliable, and says so at startup. The client's source port is
  derived from the token rather than picked at random, so a reconnect reuses the
  rule instead of adding one. See [docs/tcp-pck.md](docs/tcp-pck.md).

### Changed
- **Forwarded UDP is opt-in again — off unless you turn it on.** v1.7.1 made a
  forwarded port carry UDP as well as TCP by default, to fix Xray/Shadowsocks
  UDP working nowhere. That default caused a worse problem than it solved. A
  browser's QUIC is UDP on port 443, so a plain web tunnel silently began
  carrying every QUIC flow the browser opened — and each forwarded UDP flow
  holds a pooled tunnel connection for as long as it lives. On the
  connection-pooled transports (`ws`, `wss`, and the mux family) a browser's
  many long-lived QUIC connections to a site drained the pool the TCP forwards
  shared, so the site half-loaded (images stalled while audio played) and a
  restart cleared it for a while before it filled again. Instagram, which is
  QUIC-heavy, was the usual casualty; Telegram, which is TCP, was fine.

  So `accept_udp` is off by default once more, as it was before v1.7.1. New
  tunnels carry TCP only; turn UDP on — Edit → Fine Tune → *Forward UDP…*, or
  `accept_udp = true` — for the tunnels that genuinely need it (a VPN, a game,
  an Xray/Shadowsocks inbound). **An existing tunnel created on v1.7.1 has
  `accept_udp = true` written in its config and keeps forwarding UDP until you
  turn it off** — that is the fix for the symptom above if you are hitting it.
  See [docs/forwarded-udp.md](docs/forwarded-udp.md).

- **The spoof transport's ICMP and UDP profiles now match the reference spoof
  transports on the wire — and carry the payload bare.** They used to borrow
  xdi's framing: every datagram went out with a 5-byte tag+direction prefix in
  front of the KCP packet, and the ICMP profile split the two directions into
  Echo Request (client) and Echo Reply (server). The reference transports do
  neither — an ICMP packet is a plain Echo Request in both directions, a UDP
  packet is a plain datagram, and the payload sits directly inside the L4 header
  with nothing added. What authenticates a packet as the tunnel's is the same
  thing that always did: the encryption above it, whose key comes from the
  token, plus the L4 port (UDP/TCP) or echo identifier (ICMP) that the kernel
  filter already matched on. The tag was redundant with the cipher; the
  direction byte's job — discarding the kernel's automatic Echo Reply — is now
  done by keeping only Echo Requests, since both ends send them.

  **This is a breaking wire change: both ends must run this version.** A spoof
  tunnel with one end on the old framing and the other on the new one will not
  pass traffic. The effective MTU also grows by five bytes (the dropped prefix),
  which both ends compute identically. The `tcp` profile is unframed by the same
  change. The xdi transport is untouched — it still carries its own framing,
  which is what lets several xdi tunnels share one host's ICMP socket.

### Fixed
- **"Diagnose relay" failed at the last step even when the relay was working.**
  The final check fetched `https://api.telegram.org/`, which is not an API
  endpoint — it redirects to the documentation site at `core.telegram.org` — and
  Go's HTTP client follows redirects by default. The relay only rewrites the dial
  for `api.telegram.org`, so the redirected request left the Iran server
  directly, where `core.telegram.org` resolves to the filtering blackhole
  (`10.10.34.36`) and times out. The tool then reported
  `dial tcp 10.10.34.36:443: i/o timeout` and blamed the relay for a failure it
  had caused itself — one line after proving the relay carried a TLS connection
  to Telegram. It now calls `getMe`, a real endpoint that does not redirect, with
  redirects refused outright, and reads Telegram's own answer.
- **A bad bot token reported itself as a network fault.** `telegram API returned
  status 401` is the one error that cannot be the tunnel's doing: the request
  reached Telegram and Telegram answered. It now says so — that the token was
  rejected, that the relay is fine, and to get a fresh one from @BotFather —
  and quotes Telegram's own description of any other refusal instead of showing
  a bare status number. "Diagnose relay" checks the token as part of the same
  request, so an invalid one is named at the step it belongs to.
- **The bot token could leak into error output.** Every Bot API URL contains the
  token, and Go copies the full URL into transport errors, so a failed request
  printed the credential to the screen and the journal. It is redacted now.
- **A data race on the logger's level, on every transport.** Both the client and
  the server hand one logger to all of their transports, and a transport's
  `Restart` saved the current level before quieting the log with
  `level := logger.Level` — reading the field directly. logrus does not treat
  that field as plain memory: `SetLevel` stores it with `atomic.StoreUint32` and
  every log call loads it with `atomic.LoadUint32`. A direct read is therefore an
  unsynchronised read racing an atomic write, which is a data race by definition
  and was reported as one by the detector under CI's timing. The write it raced
  with is real and reachable: the shutdown path calls `SetLevel(FatalLevel)` to
  suppress teardown noise, so any restart overlapping a shutdown could hit it.
  All fourteen sites now use `logger.GetLevel()`, which performs the atomic load.
- **TCP + PCK never stayed connected: every pool connection killed the one
  before it.** The carrier derived its source port from the tunnel token, so
  every connection a client opened sent from the *same* port. kcp-go's listener
  demultiplexes sessions purely on the sender's address
  (`l.sessions[addr.String()]`), and its rule for a packet announcing a new
  conversation on an existing entry is to close that entry — so each pool
  connection closed the session before it, the control channel included. The
  tunnel spent its life in a loop: control channel up, pool dials, control
  channel dead, `read from control channel: timeout`, restart. Aggressive made it
  worse by opening sixteen. Each carrier now takes its own port from a
  128-port range derived from the token, so every session reaches the server as
  its own peer and the TCP sequence numbers of one flow stay consistent. The
  kernel-RST rules cover the range in one set and are reference-counted, so a
  pool of sixteen no longer installs sixteen sets of iptables rules.
- **The pck send path allocated 4.2 KB per packet.** It built each frame a layer
  at a time — TCP, then IPv4, then Ethernet — allocating three times and copying
  the payload three times for every datagram. This carrier pays that per packet
  and cannot amortise it the way UDP does, because kcp-go only reaches for its
  batching write path when the socket is a real `*net.UDPConn`. Frames are now
  assembled in place into a pooled buffer: 628 ns → 296 ns and 4224 B → **zero**
  allocations per packet, with a test asserting the result is byte for byte what
  the layered builders produced. The send socket also gets an 8 MB `SO_SNDBUF`;
  only the receive socket had one, so a burst from a full window had its tail
  refused with `EAGAIN` at exactly the busiest moment.
- **The pck receive path allocated 64 KB per packet.** `ReadFrom` took a fresh
  64 KiB buffer on every call, and KCP calls it once per packet — so the garbage
  collector, not the network, was setting the pace. Pooled: 1115 ns → 44 ns.
- **A failing accept loop could pin a CPU core at 100%.** Every transport's
  accept loop retried instantly on error. That is right for the error `Accept`
  normally returns (one connection failed the handshake, take the next) and wrong
  for the two that matter: a closed listener and an exhausted file-descriptor
  table both fail immediately and keep failing, so `continue` spun as fast as the
  CPU allowed with nothing to block on. The context check at the top did not
  help, because the context was not cancelled — only the listener was broken, a
  state a tunnel restart passes through every time. Consecutive failures now back
  off geometrically to 100 ms, reset on the first success, and return at once on
  cancellation. This affected `acceptLocalConn` too, of which one runs **per
  forwarded port**. The per-iteration `listener.Addr().String()` — built on every
  accept regardless of log level — is gone with it.
- **The websocket relay used an unpooled 16 KB buffer.** The TCP relay was moved
  to a pooled 64 KB buffer for two measured reasons (a relay's cost is syscalls,
  and 16 KB took four times as many per gigabyte; and allocating per connection
  put a buffer through the GC for every connection forwarded). The ws/wss path
  was missed. It now shares the same pool.
- **The end-to-end suite could fail with `bind: address already in use` on CI.**
  Test ports were handed out from 34000-60000, which sits entirely inside Linux's
  default ephemeral range (32768-60999). The suite opens hundreds of outgoing
  connections and the kernel draws each one's *source* port from that range, so a
  port the harness had just verified free could be taken before the tunnel bound
  it. macOS starts its ephemeral range at 49152, which is why this failed on CI
  and not on a developer's machine. The harness range moved to 12000-30000,
  below both.
- **A spoof tunnel could not restart: `bind: address already in use`.** In the
  field logs, a spoof server that restarted to adopt a new client died with
  `listen udp4 0.0.0.0:58521: bind: address already in use`, and the client
  looped on the same error. The carrier transports — spoof, xdi, and pck — hand
  kcp-go a socket they open themselves, and kcp-go's `ServeConn`/`NewConn2`
  deliberately do not take ownership of a caller-provided socket: closing the
  KCP listener or session left the raw socket bound. So every restart leaked the
  previous run's receive socket, and two seconds later the new run could not
  rebind the port and exited fatally. Plain UDP never hit it because it dials
  through kcp-go's own `ListenWithOptions`/`DialWithOptions`, which own and close
  the socket. The client now builds its session with ownership set, and the
  server closes the carrier socket alongside the listener; the plain-UDP path is
  unchanged.

- **The MSS clamp the diagnostics ask for can now be set, and now does
  something.** Health Check measures the path MTU and, where the path carries
  less than the tunnel is sending, prints the exact fix: `set mss = 1208 on both
  ends`. There was nowhere to do it. The setting existed in the config format
  and in the engine, but no menu, no wizard question and no panel field ever
  wrote it — so the one fault in this system that reports itself precisely was
  also the one with no way to act on the report short of hand-editing a TOML
  file the CLI would rewrite.

  It is now **Edit → TCP MSS clamp** in the menu, a question in the manual
  tuning step of both setup wizards, and a field in the panel's Fine Tune
  drawer. It is offered only on the transports that carry TCP segments; a
  datagram transport is sized by its KCP MTU instead.

  Setting it was only half the problem. On `ws`, `wss`, `wsmux` and `wssmux` the
  value was accepted, written to the config and then dropped on the floor: those
  transports opened their sockets through `http.ListenAndServe` and a dialer
  that passed a hardcoded zero, so nothing ever reached `TCP_MAXSEG`. A clamp
  applied to a wss tunnel — the transport most of these paths use — changed
  nothing at all, which is the worse failure of the two, because the config then
  agrees with you. All four now build their listener and their dials with the
  tunnel's own options, as the `tcp` and `tcpmux` transports already did.

  Two smaller things came with it. The clamp survives a preset change, in the
  menu and in the panel both — it describes the path, not how hard the tunnel is
  being pushed, and resetting it with the rest of the tuning would have silently
  undone the fix. And the check no longer prints a number the tunnel would
  refuse: on a path below the 576 bytes IPv4 requires, no clamp helps, so it
  says the route is at fault instead of naming a value nothing will accept.

- **Ports 1 and 65535 can now be forwarded.** The config validator accepts the
  whole `1..65535` range and always has, but every transport's mapping parser
  wrote the check as `port > 1 && port < 65535`. A mapping of `1=127.0.0.1:80`
  or `65535=127.0.0.1:80` therefore failed that test, fell through to the branch
  that treats the left-hand side as a complete address, and was handed to the
  listener as the literal string `1` — which does not resolve, so the forward
  never came up while the config it came from was perfectly valid.

  All seven server transports had their own copy of that expression. They now
  share one helper, so the range is stated once rather than seven times, and
  cannot drift apart again.

  *Thanks to [@dr-hoseyn](https://github.com/dr-hoseyn), who raised this in
  [#12](https://github.com/AminMGMT/BackPack/pull/12). We reviewed that pull
  request, finished it and improved on it, and what shipped here is the
  result — the shared helper trims its input and states the bound as an
  inclusive range, so the edge the report was about is the one the code now
  reads as valid.*

- **A forwarded port with an empty backend list took the client down.** A port
  can name several backends separated by a pipe. The pool decided it had a list
  to balance by looking for that pipe in the target — not by looking at what was
  on either side of it — so a target like `||`, which parses to no backends at
  all, still built a group with an empty list. The round-robin then indexed it
  with `next % 0` and the process panicked. The target comes off the tunnel, not
  out of this side's config, so what it contains is not this process's to assume.

  Two other things about that pool were wrong in the same direction. Every
  distinct backend list created a goroutine that probed its backends every ten
  seconds forever and a map entry that was never removed, both of them keyed by
  a string the far end chooses; a tunnel that saw many different lists grew both
  without limit, and the goroutines went on dialling backends nothing was
  routing to long after the generation that created them had stopped. The map
  is now bounded and evicts the list used longest ago, and the health checks run
  from the pick that needs them — at most one pass at a time, at most one per
  interval — so an abandoned list finishes its current pass and then costs
  nothing. A backend that recovers while no traffic is flowing is noticed on the
  next connection instead of within ten seconds, which is the moment the answer
  begins to matter.

  *Thanks to [@dr-hoseyn](https://github.com/dr-hoseyn), who raised this in
  [#26](https://github.com/AminMGMT/BackPack/pull/26). We reviewed that pull
  request, finished it and improved on it, and what shipped here is the result:
  the group is keyed on the parsed list rather than the raw target, so spacing
  and a trailing separator no longer each get a pool of their own, and the
  last-used timestamp is read and written under the group's own lock rather
  than beside it.*

- **A `udp`, `kcp` or `quic` tunnel could fail to come back from a config
  reload: `bind: address already in use`.** Before starting the new run, the
  reload waits for the old one's ports to come free, and the only honest way to
  ask that is to try to bind them. It asked with `net.Listen("tcp", …)` whatever
  the transport was. For a datagram transport that is the wrong question: the
  TCP port of that number was free — nothing had ever bound it — while the
  previous run's UDP socket was still open. The wait returned at once, the new
  listener raced the socket that was actually still there, and lost.

  Each address is now probed on the protocol its transport actually uses. `xdi`,
  `spoof` and `pck` are left out of the wait entirely rather than guessed at:
  they read through a raw or packet socket, so there is no listener to probe,
  and opening one to find out would need the same privilege and could take
  packets from the run still shutting down. Their teardown is the transport's
  own, and the reload waits only for the web port alongside them. While in
  there, each address also got its own settling budget instead of sharing one
  across the list — a slow tunnel port used to spend the web port's wait as
  well, and the web port, the one that shuts down gracefully and genuinely needs
  waiting for, was the one that then got none.

  *Thanks to [@dr-hoseyn](https://github.com/dr-hoseyn), who raised this in
  [#11](https://github.com/AminMGMT/BackPack/pull/11). We reviewed that pull
  request, finished it and improved on it, and what shipped here is the result:
  `pck` did not exist when it was written and is a raw-socket transport too, so
  it joins `xdi` and `spoof` in being left out of the wait instead of spending
  the whole settling timeout on a port it was never going to bind.*

- **Idle forwarded connections survived a shutdown or a reload.** The relay ran
  one direction of the copy in a goroutine and the other on the handler's own
  stack, and only looked at the context once that second copy returned. On a
  connection carrying traffic that is a distinction without a difference. On an
  idle one it is the whole problem: neither Read returns until the peer sends
  something, and on a connection nobody is using, neither ever does. The tunnel
  stopped, the generation was replaced, and the connections from the previous
  one stayed open — with their descriptors, their goroutines and their share of
  the connection quota — for as long as the process ran.

  Both directions now run independently, so cancellation is seen while both are
  blocked, and both ends are closed on the way out, which is the only thing that
  interrupts a blocked Read. The handler also waits for the second copy before
  returning rather than leaving it to finish on its own: the caller releases its
  connection quota at that point, so nothing belonging to that connection may
  still be running. One consequence worth stating plainly — the relay now ends
  as soon as *either* direction does, where it used to wait for both. That is
  what the close is for, and it is what every other relay in this project does,
  but a protocol that half-closes and then expects a reply on the other
  direction will not get one.

  *Thanks to [@dr-hoseyn](https://github.com/dr-hoseyn), who raised this in
  [#18](https://github.com/AminMGMT/BackPack/pull/18). We reviewed that pull
  request, finished it and improved on it, and what shipped here is the result —
  the waiting and closing is one shared routine both handlers call rather than
  the same dozen lines written out twice, so the two relays cannot drift apart
  on the behaviour this fix is about.*

- **The monitor page was served to the whole internet without a password, and
  now defaults to loopback.** `web_port` opened a page that reports the host's
  CPU, memory, disk, swap and network counters, the tunnel's status and total
  traffic, and — with the sniffer on — the usage of every forwarded port. It
  has never had authentication of any kind, and it was bound to every
  interface. On a server with a public address that is a live readout of the
  machine handed to anything that connects, with the port number as the only
  thing in the way.

  It now binds to `127.0.0.1` and is reached the way the profiling endpoint
  already is:

  ```
  ssh -L 2060:127.0.0.1:2060 root@server
  ```

  **This changes where an existing tunnel's monitor page answers.** If you open
  it over the network today, set `web_bind = "0.0.0.0"` to have it back exactly
  as it was — the key is written into every config that has a `web_port`, so it
  is there to find and edit, and unlike a hand-added setting it survives the
  next time the menu or the panel rewrites the file. A single address works too
  (`web_bind = "10.0.0.5"`), for serving it on a private network and nowhere
  else. Whenever the page is bound anywhere but loopback, startup says so.

  The page itself got the boundaries it never had: `GET` and `HEAD` only,
  unknown paths answered as missing instead of rendering the dashboard,
  `no-store` and the usual hardening headers on every response, and header,
  read, write and idle timeouts on the server — without which a handful of
  connections that open and go quiet hold their goroutines for as long as the
  process lives. Two handlers that logged a failure and returned an empty `200`
  now return a `500`, so a monitor that cannot collect stats says so rather
  than looking like one reporting nothing.

  *Thanks to [@dr-hoseyn](https://github.com/dr-hoseyn), who raised this in
  [#24](https://github.com/AminMGMT/BackPack/pull/24). We reviewed that pull
  request, finished it and improved on it, and what shipped here is the result:
  the pull request bound the page to loopback outright, with nothing an
  operator could do about it. Anyone watching that page from another machine
  would have lost it on upgrade with no way back, so it is a default here
  rather than a rule — `web_bind`, carried through the config writer and the
  edit path so the CLI and the panel cannot silently drop it.*

- **Turning profiling off, or reloading a tunnel that has it on, left the old
  listener holding the port.** `pprof = true` started the endpoint with
  `http.ListenAndServe`, which serves the process-wide default mux and returns
  only on failure — so there was no server to shut down and no way to know when
  it had. Disabling profiling in the config did not close the socket that was
  already open, and a reload built the next generation while the previous one
  still had 127.0.0.1:6060; if that generation wanted profiling too, it raced
  the port and lost.

  Profiling is now an explicit server tied to the generation's context, with
  its handlers named one by one rather than inherited from the default mux —
  which matters because that mux is shared with every package in the build that
  registers something in an `init` function, and anything there was reachable
  on the profiling port without a decision being made. `Start` does not return
  until the port is released, so the reload's wait for a free port is waiting
  for something that will actually come.

  *Thanks to [@dr-hoseyn](https://github.com/dr-hoseyn), who raised this in
  [#29](https://github.com/AminMGMT/BackPack/pull/29). We reviewed that pull
  request, finished it and improved on it, and what shipped here is the result:
  the read and write deadlines it put on the profiling server are gone, because
  a CPU profile is a thirty-second response by default and a trace is longer —
  deadlines there would have cut off the one thing the port exists for. The
  header timeout and the header-size cap, which a slow client runs into and a
  profile never does, are what remain.*

- **An interrupted backup left a truncated archive under a name that said it
  was whole.** The archive was written straight to its final path, so a backup
  cut short by a full disk, a reboot or a killed process left a partial file
  called `backpack-backup-….tar.gz` — which the retention policy then counted
  as one of the ten kept, and a restore accepted as far as the point it stopped.

  Worse, the write could report success over a short archive even without a
  crash. The tar writer and the gzip writer were closed by `defer`, and a
  deferred `Close` reports to nobody — but closing them is what writes the
  archive's footer and the compressed trailer, so a failure exactly there was
  discarded and the backup was called good.

  Backups are now written to a temporary file beside the destination, fsynced,
  and renamed into place only once complete, so a file under the real name is
  always a whole archive. Both closes are checked and reported. Every file is
  closed as it is archived rather than at the end of the walk — the close was
  deferred inside the walk callback, which meant every file in the tree stayed
  open until the last one was written, and a large enough config directory ran
  the process out of descriptors. Anything in the tree that is not a directory
  or an ordinary file is now refused by name: a symlink produced a header with
  no target recorded and no content behind it, so the archive looked complete
  and was not.

  *Thanks to [@dr-hoseyn](https://github.com/dr-hoseyn), who raised this in
  [#27](https://github.com/AminMGMT/BackPack/pull/27). We reviewed that pull
  request, finished it and improved on it, and what shipped here is the result:
  the directory entry is fsynced after the rename as well, because syncing only
  the file leaves the rename itself able to reach the disk after a crash and
  the archive to be correctly named and empty; the second backup taken in the
  same second gets a suffix instead of replacing the first; and a partial file
  abandoned by a hard crash is swept up after an hour rather than staying in
  the backup folder for the life of the host. The archive writer and the
  publishing are separate functions now, so both are covered by tests that need
  neither a real `/etc/backpack` nor a real crash.*

- **A bad backup archive used to half-restore, leaving an installation that
  came from neither the backup nor from what was there before.** Restoring
  extracted straight into the live config directory, one entry at a time, as
  the archive was read. Every way an archive turns out to be bad is found
  part-way through reading it — a truncated upload, a gzip checksum that only
  fails at the very end, a path trying to escape the directory, a read error
  half way — and by then some tunnels had been overwritten and some had not,
  with nothing kept to put them back.

  The whole post-restore tree is now built in a staging directory beside the
  live one first: the current configuration copied in, the archive laid over
  the top, every entry checked before it is written, and the compressed stream
  read through to its checksum. Only then does anything in the live directory
  change, and the change is a rename. A restore that fails now leaves the
  installation exactly as it found it, and says why.

  What the archive may contain is bounded too. Paths are checked as names
  rather than by where they happen to land, so the answer does not depend on
  what is already on disk; a backslash is refused outright, since it is a
  separator where these archives can also be opened. Anything that is not a
  file or a directory is refused — a symlink or a hard link is the standard way
  an archive reaches outside the directory it is unpacked into, and a backup of
  this system contains neither. The same path twice is refused. And there are
  ceilings on the number of entries, the size of one file and the total
  expanded size, so a corrupt or hostile archive cannot fill the disk before
  anyone learns what is in it.

  Restores still merge rather than replace: a backup taken by an older version
  does not know about files a newer one added, and this host's `install_path`
  still wins over the archived one.

  *Thanks to [@dr-hoseyn](https://github.com/dr-hoseyn), who raised this in
  [#28](https://github.com/AminMGMT/BackPack/pull/28). We reviewed that pull
  request, finished it and improved on it, and what shipped here is the result:
  it swapped the config tree without first copying the current one in, so
  restoring an older archive would have deleted every file added since it was
  taken. Seeding the staging directory is what keeps the merge behaviour
  restores have always had — and refusing, before the commit, to swap over
  anything in the live tree that cannot be copied is what keeps that swap from
  quietly dropping something on the way through.*

- **The panel's login could be used to grow its memory from outside, and its
  cookies were missing the attribute that keeps them off plain HTTP.** The
  rate limiter remembers one entry per source address and only ever forgot an
  address that came back, so an address that failed once and never returned
  stayed for the life of the process — and the panel sits on a port that gets
  scanned by hosts that rotate their source. The entry table is now bounded.
  Which entry it gives up matters as much as that it gives one up: an address
  is stamped when it fails, so the one that just hit the limit is the least
  recently seen thing in the table, and evicting by age alone would have let a
  flood of fresh addresses lift the block on the attacker producing them. A
  blocked address is never the one dropped.

  Session and pending-login cookies now carry `Secure` when the panel is
  actually serving over TLS, so they are not sent in the clear. They stay
  `HttpOnly` and `SameSite=Lax` as before, and they are all built in one place
  now rather than in three with slightly different flags — including the ones
  that clear them, which a browser ignores unless the attributes match.

  Responses carry the headers the panel had none of: `X-Frame-Options`,
  `X-Content-Type-Options`, `Referrer-Policy` and a content security policy
  that forbids framing, for a page that creates, edits and restarts tunnels.
  The two endpoints that run before any authentication no longer read a request
  body of any size into memory to find two short form fields in it. And the
  listener gained a header timeout, a header-size cap and an idle timeout —
  `ReadTimeout` and `WriteTimeout` were already set; these are what a
  connection that opens and then says nothing runs into.

  *Thanks to [@dr-hoseyn](https://github.com/dr-hoseyn), who raised this in
  [#23](https://github.com/AminMGMT/BackPack/pull/23). We reviewed that pull
  request, finished it and improved on it, and what shipped here is the result:
  `Secure` is set on evidence of TLS — this connection, or a panel configured
  for HTTPS — and never on `X-Forwarded-Proto`, which anyone can send and which
  on a plain-HTTP panel would have logged its owner in and then treated every
  request after that as a stranger's. The origin checks it added to mutating
  endpoints are not here either: `SameSite=Lax` already keeps cookies off
  cross-site POSTs, and an origin check is the thing most likely to break a
  legitimate deployment behind a reverse proxy for a case that is already
  covered.*

- **A single empty WebSocket frame could stop a `ws`, `wss`, `wsmux` or
  `wssmux` tunnel dead.** The control channel carries one-byte signals —
  heartbeat, open a channel, closed — and all four read loops went straight to
  `msg[0]` on whatever arrived. That is correct for every frame either end of
  this tunnel has ever sent, and fatal for one it has not: indexing an empty
  frame panics, and the panic takes the process with it. The control channel is
  reachable by whatever holds the token, so a tunnel that can be stopped by one
  zero-length frame is not one that should be.

  A frame that is not exactly one binary byte is now logged and dropped, and
  the loop reads on.

  *Thanks to [@dr-hoseyn](https://github.com/dr-hoseyn), who raised this in
  [#22](https://github.com/AminMGMT/BackPack/pull/22). We reviewed that pull
  request and took the part of it that fixes the crash. The rest of it changes
  how live tunnels behave — read limits on the tunnel connections, a shorter
  handshake timeout, and replacing the deliberate `IdleTimeout: -1` on the
  WebSocket listeners with a write and idle deadline — and those are throughput
  and stability questions to answer with measurements on a real link, not
  alongside a crash fix. Its response to a malformed frame is also not ours:
  the pull request closes the control channel and restarts the transport, which
  hands anyone who can reach that channel a way to keep a tunnel restarting.
  Dropping the frame leaves a working tunnel exactly where ignoring it always
  left it.*

- **The monitor page collected the host's statistics once per request, and
  could crash the tunnel doing it.** One collection samples the network
  counters, sleeps a whole second so a rate can be worked out, and then walks
  the process table to count connections. The dashboard polls on a timer, so a
  page left open on two screens meant two of those, each holding a goroutine
  asleep for a second and each doing the walk again — and anything scraping the
  endpoint faster multiplied it from there, on the page that exists to report
  how the host is doing.

  A collection is now shared: whoever arrives while one is running waits for it
  and gets its result, and a result stays good for two seconds. The tunnel's
  own status and traffic total are refreshed on the way out, so the two figures
  that describe the tunnel are still live — only the part that was slow to get
  is reused. A failure is not cached, so a host that could not be read a moment
  ago is asked again rather than reported as broken for the next two seconds.

  Two crashes went with it. The CPU sample comes back as an empty slice rather
  than an error when the kernel has nothing to give — briefly at boot, and
  where the counter has not moved — and indexing it took the whole tunnel down
  over a number on a status page; the same was true of the network counters.
  Both are checked now.

  The tunnel's traffic total was written by the save loop and read by the stats
  endpoint with nothing between them, on a plain integer. It is atomic now, and
  published once at the end of a save rather than zeroed and added back a port
  at a time — which is what let a poll land mid-rebuild and report the tunnel
  as having carried nothing. The usage file is serialised too: the save is a
  read-modify-write, it runs on a timer in its own goroutine, and a save slower
  than the tick used to be joined by the next one, with whichever finished last
  discarding the other's totals. Reading is under the same lock, so a page load
  cannot catch the file half-written. And the first read of a fresh install
  creates that file and now closes it.

  *Thanks to [@dr-hoseyn](https://github.com/dr-hoseyn), who raised this in
  [#25](https://github.com/AminMGMT/BackPack/pull/25). We reviewed that pull
  request; the largest thing in it — the shared tunnel-status string, rewritten
  by transports while the stats endpoint read it — had already been fixed here
  since, so what shipped is the rest of it, reviewed and finished. The
  accounting lock is deliberately not the one the cache and the file use:
  traffic is accounted once per read on every forwarded connection, so putting
  disk work or a one-second collection behind that lock would have paid for a
  monitoring page out of the tunnel's throughput.*

- **A tunnel using a Let's Encrypt certificate stopped renewing it after the
  second reload.** The HTTP-01 responder was started with a bare `go` on every
  TLS configuration built, and nothing ever stopped it. One process builds
  another on every reload, so the second reload found port 80 held by the
  responder the first run had left behind: it logged that it could not have the
  port and carried on. What was still answering on 80 was the *old* manager,
  belonging to a generation that had been torn down, with the configuration and
  cache directory it was built with rather than the ones now in force. Renewal
  then depended on TLS-ALPN, which only works when the tunnel is on 443 — off
  443 it simply stopped, silently, and the first anyone would know is a
  certificate expiring ninety days later.

  There is now one responder for the life of the process, started once and
  pointed at whichever manager is current. It holds no state of its own —
  everything a challenge needs lives in the manager — so a reload only has to
  swap the pointer. A port that cannot be taken is still reported and survived
  rather than fatal: a tunnel on 443 validates over TLS-ALPN and needs no
  responder at all.

  *Thanks to [@dr-hoseyn](https://github.com/dr-hoseyn), who raised this in
  [#21](https://github.com/AminMGMT/BackPack/pull/21). We reviewed that pull
  request and took the problem it identified rather than its solution. It
  builds a registry with reference-counted leases, a retry loop and a scheme
  for sharing the challenge port between separate processes — several hundred
  lines, on the path where a mistake means no certificate and where Let's
  Encrypt's rate limits punish finding out the hard way. One process runs one
  tunnel here, so the fix that matters is one listener that survives a reload,
  and that is about forty lines. The responder also gained the header and idle
  timeouts every other listener in this project has.*

## v1.7.1 — 2026-08-10

Two halves. The web panel stops being a window and becomes a way to work: it
creates, edits and drives tunnels itself, so the CLI is a choice rather than the
only door. The other half is the UDP side of the engine: the three transports
that carry their data in datagrams — udp, kcp (and xdi, which is kcp over ICMP) —
plus quic, which joins them.

### Fixed
- **A forwarded port carries UDP now, on every transport.** This is the one
  reported as "UDP does not pass through BackPack" with 3x-ui/Xray and
  Shadowsocks, worked around by building a GRE tunnel underneath and running
  Backpack over that. The workaround was sound and the diagnosis was right: the
  UDP was never reaching Backpack's forwarded port, because nothing was
  listening for it.

  A forwarded port opened a TCP listener and nothing else. Datagrams were
  refused by the kernel; nothing was logged, so the port looked healthy and half
  of it silently was not there. There *was* an `accept_udp` setting, but it was
  off by default, undocumented, and honoured only by the plain `tcp` transport —
  the management layer deleted it from the config whenever the transport was
  anything else. So on `tcpmux`, `ws`, `wss`, `wsmux`, `wssmux`, `kcp` or
  `quic`, which is most tunnels, forwarded UDP could not be turned on at all.

  It now works everywhere, and it is on by default: expose `443` and the tunnel
  listens on 443/tcp and 443/udp both. Each source address becomes a flow that
  is paired, limited, counted and torn down by exactly the code that already
  does it for a TCP connection — a datagram flow is handed to the transport
  wearing the same shape, so there is no second implementation to drift. The
  flow is marked in the target address rather than in a new header byte, which
  is why this needed no change to any transport's framing. Datagrams are
  length-prefixed, so a packet that goes in as one message comes out as one
  message of the same size.

  **Open the port for UDP in your firewall** — `ufw allow 443/udp` — TCP is not
  enough, and it is the first thing to check if this still does not work. Both
  ends must be on v1.7.1: a client that predates it logs a resolve error for the
  UDP flow and carries TCP as before. `accept_udp = false` still turns it off
  per tunnel, and a UDP port that cannot be bound now warns and leaves the
  tunnel running instead of killing the server. See
  [docs/forwarded-udp.md](docs/forwarded-udp.md).

  Three faults in the old path went with it. A source address whose flow could
  not be started was recorded anyway and never removed, so one full channel
  blackholed that peer until the service was restarted — the signature being
  "UDP worked, then stopped, and a restart fixes it". The client leaked a
  goroutine and a file descriptor per flow, forever, so a busy client eventually
  ran out of descriptors and stopped accepting anything. And congestion was
  judged by comparing a timestamp from one machine against the clock of the
  other: two servers do not share a clock, so a kharej host a second ahead made
  every packet look a second late, flagged every flow as congested, and tore
  them down in a loop. That measurement was never sound and is gone rather than
  corrected.

- **The `udp` transport could silence a peer permanently.** The same fault, in
  the one place the rewrite above does not reach. A forwarded flow that waits
  more than three seconds for a tunnel connection is given up on — which happens
  whenever the pool is briefly empty, and always happens when traffic arrives
  before the client has finished connecting. Giving up removed the flow from
  service but left its source address in the table, and nothing else ever
  removed it, so every later datagram from that peer was filed against a payload
  channel no goroutine was reading. The peer went quiet for good; restarting the
  service was the only cure. The flow is now dropped whole, so the next datagram
  from that address starts a fresh one.

  A second, smaller fault went with it: the tunnel-side table was written
  without its lock on one path, which is a data race that can corrupt a Go map
  outright.

  Worth saying plainly, though: the `udp` transport carries datagrams with no
  retransmission, no ordering and no error correction, so on a lossy or
  throttled route it will still do far worse than **UDP + KCP** — and since this
  release you do not need it to forward a UDP service at all. Any transport
  does that now.

### Added
- **The web panel builds and manages tunnels.** It used to say "monitoring
  only" in the corner of the Tunnels heading, and it meant it: every tunnel was
  created, edited and restarted from the CLI over SSH. That corner now holds
  **Add Tunnel** and **Restart all**.

  Add Tunnel asks which side this machine is — Iran (server) or kharej (client) —
  and then asks the setup wizard's own questions, in the wizard's own order:
  transport family, transport, name, tunnel port, forwarded ports, token,
  performance preset, Fine Tune, IPv6 and PROXY protocol. The port fields carry a
  button that suggests a free four-digit port, the token is generated for the
  server side and copyable in one click, and Fine Tune opens on the preset's own
  numbers, marking the tunnel custom only if one of them is actually changed.

  Every card gained an **Edit** button beside Logs and Details, and a row of
  **Start / Stop / Restart / Delete** beneath them. Edit changes the server
  address, the transport, the tunnel port, the forwarded ports, the preset and
  the advanced settings — all of it applied in one write and one restart, and
  reverted to the previous config if the tunnel does not come back up.

  None of this is a second implementation. The transport and preset menus are
  served from the same tables the CLI menu reads, and every form posts to
  `manage`, so a tunnel built in the browser is the same file as one built in the
  terminal. The endpoints sit behind a panel session; the read-only remote token
  still cannot change anything.
- **QUIC is available as a transport.** It carries the tunnel inside QUIC
  streams over UDP: its own TLS 1.3, its own stream multiplexing, congestion
  control and loss recovery, so every byte is encrypted and there is nothing to
  hand-tune. Pick **UDP + QUIC** in Setup, or set `transport = "quic"`.

  It is offered, not recommended. v1.5.0 records QUIC being built, tested on a
  real Iran route, and dropped because it never completed a handshake there while
  KCP on the same link ran at full speed. That finding still stands and nothing
  since has disproved it, so the benchmark's advisor keeps recommending KCP for a
  lossy link and names QUIC only as the other thing to try. Test it on your own
  route before committing to it.

### Fixed
- **A client that restarted on its own left a dead tunnel behind, on every
  datagram transport.** The server accepted exactly one control channel claim per
  run. When the client restarted — a crash, a service restart, an edit — it
  re-dialed with a fresh claim, and the server discarded it as a duplicate,
  because as far as that run was concerned it already had a control channel.

  Nothing corrected it. A datagram transport has no RST to deliver the news: on
  KCP the server's control-channel read simply blocks and a heartbeat write to a
  silent peer is buffered rather than refused, so the dead session stayed
  "established" indefinitely and the tunnel was down until someone restarted the
  server by hand. The server now keeps accepting claims for the whole run, and a
  second one that passes the token check makes it rebuild and adopt the new
  client. The decision sits on the token, never on the bare connection, so a peer
  that does not know the secret cannot force a restart loop. There are end-to-end
  tests for both halves on udp, kcp and quic, each driven by a real client
  restart behind a cut path.

- **UDP sockets kept the kernel's default buffers, whatever `so_rcvbuf` and
  `so_sndbuf` said.** The settings were honoured on the TCP transports and
  ignored on udp, on both ends. The kernel default is small enough that a
  datagram flood — a speed test, a busy game server — overruns it, and the
  packets it cannot hold are dropped before any goroutine reads them, which
  looked like the tunnel stalling under load. Every UDP socket the transport
  opens is now sized to the configured value.

- **One bad forwarding target took down the whole udp client.** A target address
  that would not resolve, or a UDP dial that failed, called `Fatalf` and killed
  the process — every other tunnelled port with it. A failed dial also fell
  through and dereferenced the nil connection one line later. Both now drop the
  one flow and let the rest of the tunnel carry on.

- **The udp control-channel handshake leaked a goroutine on every restart.** Its
  accept loop had no way to be woken, so it sat blocked on the old listener past
  the restart that replaced it. The listener is now closed when the run ends. The
  control connection also gets TCP keepalive, so a peer that dies without closing
  surfaces as a read error instead of a connection that hangs open forever.

- **`proxy`, `local_addr`, `interface` and `so_mark` were accepted on quic and
  silently ignored.** They apply to the TCP dialer, which a quic tunnel does not
  use for anything — not even its control channel, which is a QUIC stream. They
  are now refused at load time on quic, as they already were on udp, kcp and xdi.

## v1.7.0 — 2026-08-04

This one started from a report that the tunnel worked on most servers and not on
some, with no error that said why. Chasing it down found several separate causes
of exactly that symptom, all of them old, and all of them in the part of the code
that decides whether a connection is allowed to exist at all.

**Upgrade the server first, then the clients.** Three things now settle
themselves between the two ends rather than being assumed, and all three fall
back to the old behaviour when one end is older — but a new server with old
clients is the combination that degrades most gracefully.

### Fixed
- **A wss tunnel could not sit behind a TLS-terminating reverse proxy.** The wss
  credential is a proof bound to the client's TLS session — which stops an
  intermediary that terminated the TLS from replaying it, and also stops a
  *legitimate* one, like an NGINX reverse proxy in front of the server, because
  the proxy holds a different session and the bound proof can never match. There
  was no way to run that setup at all. A new `simple_auth` option authorises on
  the raw token instead, the same credential the plain ws transport already
  sends:

  ```toml
  simple_auth = true
  ```

  It is off by default, because without a trusted proxy it hands the token to
  whoever terminates the TLS. Set it on both ends when a proxy is doing so.

- **Backpack squatted the well-known SOCKS port 1080 on every install.** The
  monitor bound `127.0.0.1:1080` unconditionally, as a fallback for tunnels
  written before the relay port was derived from the token. On a machine that
  also runs a panel or an xray SOCKS inbound on 1080, backpack boots first, wins
  the port, and the other service quietly loses it — the panel's nodes drop after
  a reboot, and the only way anyone found to clear it was to uninstall backpack.
  It now binds 1080 only when a tunnel actually still maps to it, and leaves it
  alone otherwise. Nothing that uses the derived port — every current tunnel, and
  every fresh install — touches 1080 at all.

- **A tunnel would connect and then carry nothing, for any client that dials out
  from more than one address.** The server accepted a pool connection only if its
  source address matched the control channel's. Behind carrier-grade NAT, on a
  multi-homed host, behind a SNAT pool or a load-balanced gateway, the pool dials
  from a different address than the control channel did, and every one of those
  connections was discarded. The control channel was fine, so the tunnel reported
  itself connected and simply moved no traffic.

  Pool connections now prove what they know instead of where they came from: the
  server issues a random nonce when the control channel comes up, and each pool
  connection presents it. Source address is out of the decision entirely. A
  client too old to present one still works, on the old rule, with a warning
  saying so.

- **On some kernels the tunnel could not open a single socket.** Every socket —
  outgoing connections included — asked for `SO_REUSEPORT`, and a kernel or
  container that refuses the option failed the dial or the listen outright. It is
  no longer asked for at all.

  Where it *worked* it was worse. `SO_REUSEPORT` is a deliberate request to let
  another process bind the same port, so a leftover process from a crash or an
  upgrade no longer collided with "address already in use" — it quietly took a
  share of the arriving connections, and the client's control channel and its
  pool ended up on different processes.

- **A port scanner could keep the tunnel's own client from connecting.** The
  server read each new connection's token in a single loop, one connection at a
  time, blocking up to two seconds on each. Anything that connected and said
  nothing cost every connection queued behind it the full timeout. Each
  connection is now handled in its own goroutine.

- **Only the first address a server name resolved to was ever tried.** A name
  with several A records — the ordinary way to publish more than one route to
  the same machine — was resolved to a single address and that address dialled
  forever. If it was the filtered one, the tunnel never connected while every
  other record sat there working. Every resolved address is now tried in turn,
  with IPv4 and IPv6 raced as usual.

- **A restart could rebind ports after the tunnel had been told to stop.** The
  restart path waits two seconds before rebuilding, and did not check whether the
  tunnel was still meant to be running. On shutdown it leaked listeners; with the
  new configuration reload it fought the run replacing it for its own ports.

- **The control channel gave up after two seconds.** That has to cover a round
  trip plus whatever the server takes to answer, and on a long or lossy path — the
  ordinary case here, not the exceptional one — a single retransmitted SYN can eat
  most of it. It is fifteen seconds now; failing fast bought nothing, because
  failing means backing off and dialling again.

- **`mux_streambuffer` did nothing on the default mux version.** smux only applies
  a per-stream window on version 2; on version 1 there is no per-stream flow
  control at all. The setting was accepted, shown back by the panel, and ignored.
  It is now either applied or reported as inapplicable.

### Added
- **Reach the tunnel server through a proxy.** A client that cannot open an
  arbitrary outbound connection can be pointed at one:

  ```toml
  proxy = "socks5://127.0.0.1:1080"
  proxy = "http://user:pass@10.0.0.1:8080"
  ```

  Only the connections that reach the server go through it; the dial to the local
  backend never can, by construction rather than by rule. Not available on the
  `udp` and `kcp` transports, whose data is carried in datagrams a TCP proxy
  cannot relay — configuring it there is refused at startup rather than half
  working.

- **Choose which way out of a multi-homed machine the tunnel leaves by.** On a
  server with two uplinks the kernel's routing table decided, and the only way to
  influence it was to change routing for the whole machine. Three ways to say it
  instead, in increasing order of what they need from the system:

  ```toml
  local_addr = "192.0.2.10"   # bind the source address — needs no privilege
  interface  = "eth1"          # pin to a device — Linux, needs CAP_NET_RAW
  so_mark    = 100             # fwmark for `ip rule` — Linux, needs CAP_NET_ADMIN
  ```

  Any of these that is configured and cannot be applied fails the connection,
  rather than being logged and skipped. They were asked for by name, and a tunnel
  that quietly ignored "leave by eth1" would send traffic out the wrong link
  while reporting itself healthy. Like the proxy, none of it touches the dial to
  the local backend, and none of it is available on the datagram transports —
  configuring it there is refused at startup.

- **Editing a tunnel's configuration file now takes effect on its own.** The file
  is watched, and a change stops the tunnel and starts it again from the new
  configuration in the same process. A file that does not parse is ignored and
  reported, so a half-saved file or a typo cannot take a working tunnel down; a
  file that means the same thing is ignored silently, so touching it or editing a
  comment does not drop every connection it is carrying.

- **Stealth records carry random padding.** Encryption settles what is in a
  record and says nothing about how big it is, and record sizes are one of the
  few things left for an observer to work with. Each record now carries a random
  amount of filler, inside the encryption — both the length and the filler are
  under the AEAD, so all that reaches the wire is a record whose length moved.

- **A new experimental transport, `xdi`, tunnels inside ICMP echo.** It is for
  the one network where UDP and TCP are filtered but ICMP is not — the tunnel
  rides in ping packets, which such a network is unwilling to drop because ping
  is how it proves itself reachable. It is the KCP transport with its packets in
  echo requests and replies instead of UDP datagrams; everything above the packet
  layer — the reliability, the error correction, the encryption — is identical,
  so the `aggressive` preset drives it to the same throughput as KCP.

  ICMP has no ports, so a raw ICMP socket receives every ping the host sees.
  Several `xdi` tunnels on one machine stay out of each other's way — and out of
  the way of stray pings and the kernel's own automatic replies — by a session
  tag derived from each tunnel's token: a packet without this tunnel's tag is not
  this tunnel's packet, and is dropped without a second look.

  It is Linux only and needs a raw socket (root, or `cap_net_raw`), refused at
  startup with that said plainly if it does not have one. Slower than the other
  transports and heavier on ICMP rate limits, so it is a last resort, not a
  default.

- **Zero-copy forwarding, off by default.** Where both ends of a forwarded
  connection are plain sockets, the bytes can be moved by the kernel instead of
  being copied out into the process and back. It is the fastest path here and
  the least proven, so it is opt-in per tunnel:

  ```toml
  zero_copy = true
  ```

  Nothing about it reaches the wire, so the two ends need not agree — it can be
  enabled on one side, or on one tunnel, while everything else keeps the path it
  has always used. It applies only to the plain `tcp` transport on Linux, and
  only where the tunnel has no bandwidth limit; anything else silently keeps the
  buffered copy, and the tunnel says which it is actually using in its log every
  five minutes, because "enabled" and "in effect" are not the same thing.

- **Health Check answers the two questions a tunnel could not answer about
  itself.** How much of the pool is really there — a control channel with none
  behind it looks healthy from every other angle while forwarding nothing — and
  whether the path can carry a full-sized packet. The second one is measured
  rather than assumed: where a network drops oversized packets without returning
  an ICMP message, TCP never finds out, so the handshake and the heartbeats
  arrive, the tunnel comes up and stays up, and every real transfer stalls. It
  now reports the measured path MTU against the segment size the sockets are
  actually using, and names the `mss` to set.

### Changed
- **`mux_version` is negotiated rather than assumed.** smux has no version
  negotiation of its own: every frame carries the number, and the first frame
  whose number is not what the reader expects tears the session down. Because the
  control channel is not muxed, a disagreement did not look like a failure — the
  tunnel connected and carried nothing.

  Leaving `mux_version` out (or at `0`) now has the server settle it on the
  control channel and the client use what it is told. An explicit `1` or `2` is
  still honoured on the server side. A client too old to be told anything keeps
  to version 1, as it always did.

- **Forwarded connections copy through 64 KB buffers, taken from a pool.** They
  were 16 KB, freshly allocated per direction per connection. A relay's cost is
  syscalls, and the buffer size sets how many a gigabyte takes.

## v1.6.5 — 2026-08-01

The panel is checked from a phone more often than from a desktop, so it installs
as one now. Getting there needed a certificate, which needed a terminal — that
is a Settings screen too. And the release before this one shipped a broken brace
that quietly emptied the Health Check.

### Fixed
- **Health Check and Alerts came up empty.** Both write their rows and then ask
  the page to re-translate itself, and `applyLang` assigns `textContent` to
  every element marked `data-i18n` — including the containers those rows had
  just been written into. The results were overwritten by the placeholder the
  markup shipped with, in the same tick they arrived. The hostname in the header
  was being reset the same way between polls.
- **The accent picker multiplied.** A merge had joined `function relang(){` to
  the comment that opened the accent section, so the closing brace ended up
  sixty lines further down and the whole block — the palette, the swatch builder
  and a GitHub request — became the body of a function called on every render.
  Six swatches became twelve, then eighteen, depending on how many panels you
  had opened. It also meant none of it ran on page load.
- **Settings toggle labels sat under their switch instead of beside it.**
  `.modal label` sets `display:block` and outranks a bare `.tg`, so the flex row
  never applied inside Settings. Invisible while the control was a small
  checkbox; obvious once it became a 40px switch.
- **Location and ISP were usually blank.** The public address was resolved once,
  behind a `sync.Once`, and the panel starts from systemd at boot — often before
  the network is up. One failed lookup then stuck for the life of the process,
  and the geo lookup that depends on it never had anything to work with. Both
  are now refreshed in the background, retried while incomplete, and never block
  a request.
- **Changing the panel port redirected to `http://`** even when the panel was
  serving HTTPS.

### Added
- **Install the panel as an app.** A service worker, a proper manifest and real
  bitmap icons — including a maskable one for Android and a PNG for iOS, which
  ignores SVG for a home-screen icon. On a phone or tablet the panel offers to
  add itself once: one tap where the browser supports it, the Share-menu steps
  on iOS where it does not. The worker caches nothing on purpose — this is a
  live dashboard behind a login, and a cached reading of a server is a wrong one
  — so it exists for installability and answers a failed page load with an
  offline card.
- **Panel certificate, in the panel.** Settings → Panel access → Certificate,
  with the same three choices as the CLI. Let's Encrypt is only offered when it
  could actually succeed: the panel checks that the domain resolves to this
  server and that a validation route exists (port 443 for TLS-ALPN, or a free
  port 80 for HTTP-01), and refuses with the reason when it does not. Getting
  that wrong restarts the panel onto a listener that can never complete a
  handshake, and whoever pressed the button has a browser and no shell.

### Changed
- **Total traffic is the sum of the tunnels**, not the machine's interface
  counters. It is now exactly the total of what the cards show, rather than a
  larger figure including ssh, apt and the panel itself — and it survives a
  reboot, which the interface counters do not. Up and down speed still come from
  the interface: that answers what the box is doing now.
- **The health pass probes tunnels concurrently.** Sequentially, a client tunnel
  whose server is down cost the full four-second timeout each, and enough of
  them ran past the panel's 30-second write timeout — cutting off the response
  mid-flight.

## v1.6.3 — 2026-07-29

The panel gets a clear-out. The header carried six facts nobody reads twice,
the tile row carried eight figures to answer four questions, and the menu would
not close. All of that is smaller now, the accent colour is yours to pick, and
the panel can serve itself over HTTPS.

### Fixed
- **A server card never showed its own tunnel port.** The field was declared,
  documented, and never once assigned, so every card printed a dash where the
  port belongs — the one number on a card that cannot be found anywhere else on
  the page.
- **The menu would not close when you clicked away from it.** Its backdrop asked
  to cover the viewport and covered only the header, because the header carries
  a backdrop-filter and a filtered element becomes the containing block for
  anything positioned against the viewport inside it. Nothing about the CSS
  looked wrong. The menu and its backdrop live outside the header now, which
  also fixes the full-width sheet on phones — mispositioned by the same trap,
  and not previously noticed.
- **A connection limit leaked a slot on every timeout.** A forwarded connection
  that waited more than three seconds to be paired was closed without releasing
  the slot it took on accept; only the handler frees that, and the handler never
  runs for a connection that timed out. Tunnels with `max_connections` set would
  fill up permanently. Tunnels with no limit were never affected.

### Added
- **Pick the accent colour.** Six of them — Ember (the CLI's own, and the
  default), Pomegranate, Saffron, Pistachio, Turquoise and Frost — in Settings
  beside Language. Every coloured thing in the panel derives from one variable,
  so a theme is that variable and nothing else, and the login screen follows the
  same choice: it is the first thing anyone sees, and a sign-in page in a colour
  the panel does not use reads as a different product.
- **HTTPS for the web panel**, under Web Panel → Certificate in the CLI. A
  self-signed certificate works anywhere, including on the bare IP most of these
  panels live on, and the browser warns once. With a domain that resolves here
  and port 80 reachable, Let's Encrypt issues one browsers trust and renews it
  on its own — the certificate is resolved per handshake, so a reissue lands
  without restarting the panel. Off by default, and it stays off on upgrade:
  turning it on changes the address people have bookmarked, so it is a
  deliberate act rather than something an update does to you.
- **GitHub, with its star count**, in the menu. Fetched by the browser so it
  works from a panel on a server with no internet of its own, cached for six
  hours because GitHub allows sixty anonymous requests an hour, and simply left
  off when it cannot be had.
- A prompt asking for a star, shown at most once every three days, never on a
  first visit, and never again after either answer. This panel also carries the
  alerts, and anything that teaches people to dismiss it without reading costs
  more than a star is worth.

### Changed
- **The header carries the brand and the way in, and nothing else.** Uptime,
  the addresses, OS, location and ISP all moved into the menu: they are read
  when a server is set up and then almost never again, which does not earn a
  permanent strip across the top of every screen.
- **The tile row went from eight to four**: download, upload, one total, and the
  running version. In and out were two tiles answering half a question each —
  "how much has this machine moved" is the question, so it is one figure now.
  Load and the tunnel count were already on screen elsewhere, and the congestion
  algorithm is a setup detail, so it joined the rest of the machine's facts in
  the menu.
- **A tunnel's status is a ring around its whole card**, drawn inside the edge,
  instead of a bar down one side. The same three pixels, now describing the card
  they belong to, breathing on the status dot's rhythm — identical duration and
  identical keyframes, so the two read as one signal rather than two things
  blinking near each other.
- **Traffic on a card is one line** — `↓ 203.5 GiB  ↑ 13.0 GiB  Σ 216.6 GiB` —
  with the glyphs quiet and the figures bright. Dropping the label and the
  pairing slash bought the total, which is what most people were adding up in
  their head.
- Support Backpack left the menu. The heart button already floats in the corner
  of every screen, and asking twice is not asking better.

### Notes
No config, wire protocol, or tunnel behaviour changes. Updating replaces the
binary.

## v1.6.1 — 2026-07-27

A hotfix. Tunnels updated to v1.6.0 came up, held a good ping, and then carried
nothing: sites and applications would not open through them.

### Fixed
- **Forwarded connections stopped carrying data.** v1.6.0 added a zero-copy path
  to the forwarded relay — between two ordinary sockets, the kernel moved the
  bytes itself instead of them passing through this process. It is reverted. The
  relay is byte-for-byte the loop the upstream project uses, which is the
  behaviour proven on real tunnels; the optimisation was worth CPU on the plain
  TCP transport and nothing else, and it was the only place the forwarded data
  path had diverged. The traffic counting that existed only to serve it is gone
  with it, back to counting each read and write.

  Speed is unaffected: the ceiling is set by the mux stream window and the KCP
  window, which this never touched. Every performance preset behaves exactly as
  it did.
- **A connection limit leaked a slot on every timeout.** A forwarded connection
  that waited more than three seconds for a tunnel connection to pair with was
  closed without releasing the slot it took when it was accepted — only the
  handler frees that, and the handler never runs for a connection that timed
  out. On a tunnel with `max_connections` set, the limit filled up permanently
  until it would accept nothing at all. Tunnels with no limit configured were
  never affected, because the limiter does not count at all when it is unlimited.

### Notes
No config, wire protocol, or tunnel behaviour changes. Updating replaces the
binary.

## v1.6.0 — 2026-07-27

A release about being told the truth. The panel now says what it actually
knows — which language you read in, whether BBR is really in effect, what the
connection pool is doing and why — and four things that quietly lied or quietly
span are fixed.

### Fixed
- **A datagram tunnel stayed green after its client was stopped.** Stopping a
  KCP or UDP tunnel from the far end left the Iran panel showing it online,
  while the peer address and the ping both disappeared — the two halves of the
  same card disagreeing. They came from different places: the peer is written
  by the transport, which knew, and the state came from the socket table, which
  cannot know. A UDP listener is one unconnected socket that keeps no record of
  who is talking to it, so the check answered "do not restart this" and the
  panel read it as "a peer is connected". Those are two different questions and
  now have two different answers: the watchdog keeps its own, and the panel
  reads the peer the transport records. Not knowing is still kept distinct from
  knowing nobody is there, so a tunnel that has only just started is not shown
  as down before it has written anything.
- **The reconnect loop could spin without pausing.** Two of the control-channel
  dialer's error paths — the token write and the read deadline after it —
  returned to the top of the loop without waiting. Both sit *after* a
  successful dial, so they were reached exactly when a server accepts a
  connection and then drops it: a route filtered mid-handshake, a stateful
  firewall closing a half-open connection, a tunnel service restarting on the
  other side. In that state the client redialled as fast as the kernel would
  open sockets — pegged CPU, a flood of connections that looks like a scan, and
  a log filling at the same rate. Every retry now backs off, and a test walks
  all six transports to keep it that way.
- **The bot's own relay was listed among your forwarded ports.** The mapping the
  Telegram bot adds for itself appeared in the panel as
  `127.0.0.1:28583=api.telegram.org:443`, reading like something you had
  configured and could tidy away — and tidying it away stopped the bot for a
  reason that looked unrelated to the bot. The panel had its own idea of what a
  relay mapping looks like, and it knew only the oldest of the three shapes.
  There is one definition now, and the relay has its own small section showing
  just the port.
- **Data races in the restart path of every server transport.** Restart rebuilt
  a run's context and channels while goroutines from the previous run were
  still reading those same fields. Each run now carries its own state from the
  moment it starts, so a goroutine that outlives its run keeps what it began
  with instead of reaching into the run that replaced it. The race detector is
  clean across the suite.
- The header menu opened *behind* the tunnel cards. Its z-index was never the
  problem — the header makes its own stacking context, so the number only ever
  ranked things inside it.

### Added
- **Persian, throughout.** The panel and the Telegram bot can both be read in
  Persian, chosen separately: the person reading the bot is not always the
  person reading the panel. Choosing Persian flips the layout right to left,
  and Latin runs — addresses, ports, tokens, log lines — are pinned
  left-to-right inside it, because `127.0.0.1:8080` reordered on screen is
  worse than untranslated text. Anything not yet translated falls back to
  English rather than to a blank. Nothing is downloaded: the panel uses the
  reader's own system fonts, so it looks the same on a server with no internet.
- **The panel says whether BBR actually took effect.** Every socket asks the
  kernel for BBR and ignores the answer, because a kernel without it should not
  cost anyone a connection — but that means the request can quietly do nothing,
  on a tunnel whose presets were tuned expecting it. The answer is read back and
  shown, and says plainly when the kernel does not have it.
- **What the connection pool is doing.** The pool is allowed to grow past the
  size configured for it, which from outside is indistinguishable from a leak.
  The details panel now shows the live count against the configured one, and the
  throughput that grew it.
- **A visible notice when a release is out**, with the version you are on, and
  the fact that updating replaces only the binary — tunnels and configs are
  kept. The Telegram bot announces it too, once per version.
- The built-in proxy appears in the panel when it is enabled, and says so when
  it is enabled but not running — a tunnel forwarding a port to a dead proxy
  looks healthy from every other angle while refusing every connection.

### Changed
- **The header carries three facts instead of six**, and the row of unlabelled
  icons became one labelled menu. OS, location and ISP moved into it: they are
  read once when a server is set up and essentially never again.
- **Settings is five collapsed groups instead of eight flat sections**, one open
  at a time. The panel port and the panel password are one group now — both
  answer "how do I get into this panel" and used to sit at opposite ends of the
  list — and restore points moved under Update, which is what makes them.
- **The panel is responsive.** It had no breakpoints at all; on a phone the menu
  is now a sheet across the top rather than a dropdown opening below the fold.
- **The plain TCP transport relays without copying through user space** where it
  can — between two ordinary sockets the kernel moves the bytes itself. Traffic
  is still counted while it flows rather than at the end, and a bandwidth cap
  keeps the old path, because a capped connection has to be paced.

### Notes
Nothing in this release changes a config file, the wire protocol, or the
behaviour of a tunnel that already exists. Updating replaces the binary.

## v1.5.5 — 2026-07-23

A monitoring release: the web panel grows from a live snapshot into something
that remembers, and two bugs that made a KCP tunnel look broken are fixed.

### Fixed
- **Real client IP over KCP dropped every forwarded connection.** With the
  real-client-IP option on, the server prepends a PROXY protocol v2 header to
  each connection — and to build it, the code cast the *outbound* tunnel
  connection to a TCP address. On the datagram transports that connection is a
  UDP socket, so the cast failed, the header was never written, and the
  connection was closed before a byte moved. The tunnel connected, the control
  channel came up, and then nothing crossed it — the log filled with
  `destination connection address is not a TCP address`. The header now takes
  its destination from the forwarded listener the client actually connected to,
  which is a TCP address on every transport, so KCP (and raw UDP) carry the
  real client IP like the rest. There is an end-to-end test exercising the
  PROXY header over every transport that supports it, which is the coverage
  that was missing when the bug shipped.
- **KCP and UDP client tunnels showed as offline in the panel, with no ping.**
  The panel probed a client tunnel by opening a TCP connection to the server's
  port. A KCP or UDP server listens on a *UDP* port, so that probe always
  failed — and the panel then marked a working tunnel offline and showed no
  latency. The datagram transports are now judged by the same socket check the
  watchdog uses (never by a TCP probe), and their latency comes from a
  best-effort ICMP ping that can be blank without ever implying the tunnel is
  down.

### Added
- **The panel remembers now.** A per-tunnel **sparkline** shows the last few
  minutes of throughput on each card, and **Details** carries the longer view:
  a 24-hour speed chart, per-day totals for the week, and an **uptime
  percentage** for the last day and week. The history is sampled every five
  minutes by the monitoring service and kept for a month, so it survives a
  panel restart — the sparkline answers "what is happening now", this answers
  "what happened this week".
- **Health Check in the panel** (the bell-and-graph button): the same screen as
  the CLI's Health Check — server tuning, the monitor service, the panel, and
  every tunnel — with a ✓ / ! / ✗ and a plain-language fix per item, read-only.
- **Link Test in the panel** (**Details → Link Test**): measures latency, jitter
  and packet loss to the server over TCP and recommends a transport, the same
  as the CLI. It runs on a client tunnel, where there is a server to measure.
- **Alerts view** (the bell): what the monitoring service has fired — the
  conditions active right now and the recent messages, the same source as the
  Telegram alerts. A dot on the bell marks a live alert. Alerts are now recorded
  even when the Telegram bot is not configured, and the watchdog writes a line
  here every time it restarts a dropped tunnel.
- **Fuller tunnel Details.** Traffic in and out, uptime, performance preset,
  per-tunnel limits, the certificate (self-signed or Let's Encrypt, with its
  expiry), PROXY protocol, and the failover/backup addresses — all read from
  the tunnel's own config and metrics.
- **The panel warns when the monitor is down**, and shows a notice when a newer
  release is out — both from the background check, so nothing on the display
  path waits on the network.
- **Prometheus metrics** at `/metrics` (system, per-tunnel traffic and state,
  and the KCP link-quality counters), reachable with a read-only access token
  minted under **Settings → Remote access** — for anyone running Grafana across
  several servers.
- **Weekly automatic backup**, taken by the monitoring service into the standard
  backups folder and pruned like a manual one — **Settings → Backup**.
- **Restore points are listed in the panel** (**Settings**), read-only; a
  rollback stays a CLI decision because it replaces the running binary.
- **Login hardening.** Five failed logins from one address earn a ten-minute
  lockout; an optional **two-factor step** sends a code through the Telegram bot
  after the password; an optional **login alert** messages you on every sign-in
  with the address; and **Settings** lists signed-in devices with a per-device
  revoke and "sign out everywhere".
- **The panel is installable as an app** (a web manifest and icon), so it can be
  added to a phone's home screen — the usual way this dashboard gets checked.
- **Release channel and log tools in the panel**: switch stable/beta under
  **Settings → Update**, and filter the log drawer with ERROR/WARN lines
  highlighted.

## v1.5.0 — 2026-07-18

### Added
- **New transport: TCP + Stealth.** A TCP tunnel wrapped in an encrypted record layer
  (Noise, NNpsk0) that has **no handshake to fingerprint** — on the wire it is
  two short bursts of what looks like random data, followed by an encrypted
  stream that looks the same. There is no TLS ClientHello and no recognisable
  protocol for deep packet inspection to match against, which is the failure
  mode the TLS-based transports are increasingly hitting on filtered routes.

  The pre-shared key is derived from the tunnel token, so the transport needs no
  key of its own, and because that key is mixed in from the very first message,
  a peer without the token cannot produce a message the server will accept: it
  is dropped with no reply, so a probe or a port scan finds a dead port rather
  than a service to fingerprint. It carries TCP like the plain transport — PROXY
  protocol, per-tunnel limits and metrics all apply — with slightly more CPU for
  the encryption. Pick it under **Setup → Stealth**, or switch an existing tunnel
  to it from **Edit**. Reach for it where filtering is heavy; TCP Mux or WSS
  remain the lighter choice on an open route.
- **WSS and WSS Mux now send a browser TLS fingerprint.** A WSS tunnel is meant
  to look like ordinary HTTPS, and at the HTTP layer it already did — a real
  User-Agent, a plausible path. But the TLS ClientHello underneath was Go's, and
  Go's ClientHello has a fingerprint of its own (its cipher list, its curves, the
  order of its extensions) that filtering can pick out even when everything above
  looks right. The handshake now carries the fingerprint of a current Chrome
  build instead, so it blends into ordinary browser traffic. Nothing above TLS
  changes, and trust is unchanged — the certificate is still not verified,
  because the tunnel authenticates with its token. It applies automatically to
  every wss/wssmux tunnel; there is nothing to configure. (Where **Stealth**
  looks like nothing, this looks like a browser.)
- **New transport: UDP + KCP** — a reliable, retransmitting protocol inside UDP
  datagrams, with **forward error correction**: for every 10 packets it sends 3
  (or 4) parity packets, so losses are repaired instantly instead of waiting a
  full round trip for a retransmit. This is the transport to use when the route
  loses packets and TCP keeps backing off. Datagrams are encrypted with a key
  derived from the tunnel token.

  KCP runs over UDP. **If your provider filters UDP it will not help** — test
  before committing to it.
- **Real client IP (PROXY protocol v2).** The service behind the tunnel normally
  sees every connection as coming from the tunnel itself, so a VPN panel counts
  all users as one device and per-user device limits stop working. Turning this
  on prefixes each forwarded connection with a PROXY protocol v2 header carrying
  the user's real IP and port. Available on TCP, TCP Mux, KCP, WS Mux and WSS Mux
  (the plain websocket and raw UDP transports have nowhere to put it). **Off by
  default, and the backend must be set to accept it first** — otherwise it reads
  the header as traffic and every connection breaks.
- **Performance presets: Balance, Turbo and Aggressive**, applied to every
  transport instead of the old yes/no "Best Performance" question.
  - **Balance** — light on CPU and RAM, for a small or shared VPS.
  - **Turbo** — the recommended default. **It is byte-for-byte identical to the
    old Best Performance preset**, so upgrading changes nothing about an
    existing tunnel.
  - **Aggressive** — maximum throughput and noticeably more CPU.

  A tunnel's preset can be changed later from **Edit → Change performance
  preset**. Configs written before this release carry no preset field and are
  left exactly as they are.
- **Link Test** (**Manage → Link Test**): measures latency, jitter and packet
  loss to the far server over TCP (never ICMP — many networks on this route drop
  ping while carrying tunnel traffic fine), then **recommends a transport** and
  explains why: KCP when the link loses packets, TCP Mux when it is jittery or
  clean, WSS when nothing answers at all. It also derives **liveness timers**
  from the measured round trip instead of the fixed 75s/40s defaults, and offers
  to apply them.
- **Load balancing across backup addresses.** Previously the backup addresses
  were only spares. With balancing on, the tunnel's data connections are spread
  over all of them at once, so a single throttled route slows only its own share
  of the traffic. The control channel stays pinned to one address, since it is
  what identifies the peer. **Every address must reach the same server** — a
  second IP of it, another of its ports, or a CDN edge in front of it.
- **Setup menus are grouped by transport family** — TCP, UDP and WebSocket —
  so the choice is made in two short steps instead of one long list.

- **Per-tunnel limits.** A cap on simultaneous forwarded connections and a cap
  on total throughput, for when several services or customers share one link and
  none of them should be able to take all of it. Both off by default —
  **Edit → Limits**.
- **Structured JSON logging** (`log_format = "json"`), for anyone feeding these
  logs to a collector or a script. The default stays human-readable, since the
  usual reader is a person running `journalctl`.
- **You get told when a new version is out.** The CLI shows a line under the
  logo — and marks the Update entry — as soon as a newer release exists, and the
  Telegram bot messages you once per version.

  The check runs in the background and its answer is cached on disk, so nothing
  on the display path ever waits for GitHub: the menu cannot stall on a redraw,
  which matters on a route where the request may fail over through several
  mirrors first. A failed check leaves the previous answer in place rather than
  erasing it. The "already announced" mark is stored on disk too, so restarting
  the panel does not re-announce a version you have already been told about, and
  the notice clears itself once the update is applied. Switch it off under
  **Telegram Bot → Alerts**.
- **Telegram alerts.** The bot no longer only answers when asked — it messages
  you on its own when the processor, memory or disk crosses a threshold, and
  when a tunnel goes down or comes back. Every alert has a matching recovery
  message, because knowing a problem started is only half of it.

  Two things keep it from becoming noise, which is what makes people mute a
  monitoring bot and then miss the outage that mattered. A reading has to fall
  clearly below its threshold before the alert clears, so a value hovering on
  the line produces one message rather than dozens; and a condition that
  persists is repeated at most once per cooldown. The first pass after a restart
  only records tunnel state instead of announcing all of it.

  Defaults: processor 85%, memory 85%, disk 90%, tunnel up/down on, checked
  every 60s, repeated at most every 30 minutes. Existing installs get these on
  upgrade — a bot that never warns you is the thing being fixed — and all of it
  is editable under **Telegram Bot → Alerts**, where 0 turns a threshold off.
  Alerts are watched by the backpack-monitor service (see below), which runs
  independently of the web panel.
- **The Telegram bot reports much more.** Alongside Status it now has **System**
  (processor, memory, disk, swap, load and uptime, with bars), **Tunnels**
  (per-tunnel state, including whether the peer is really connected rather than
  just whether systemd is happy), **Metrics** (traffic, packet loss and FEC
  repairs) and **Alerts**. Everything is reachable both as a button and as a
  command — `/status`, `/system`, `/tunnels`, `/metrics`, `/alerts`, `/webui`,
  `/help` — and the two share one implementation, so they cannot drift apart.
- **Let's Encrypt certificates for wss and wssmux** (**Edit → Certificate**).
  Self-signed stays the default, because it works on a bare IP and most setups
  have no domain.

  The reason to want a real one is not encryption — the client is Backpack's own
  code and does not verify the certificate either way. It is how the connection
  looks from outside: genuine HTTPS on port 443 is never self-signed, so a
  self-signed certificate is a distinguishing mark on a route where being
  distinguishable is the whole problem. A real one removes it, and a CDN in
  front of the tunnel requires one.

  Validation works over the tunnel's own listener when it is on port 443
  (TLS-ALPN), so usually nothing extra needs opening; otherwise an HTTP-01
  responder runs on port 80. Renewal is automatic and needs no restart — the
  listener asks for the current certificate per handshake rather than holding
  the one it started with. The CLI checks that the domain resolves to this
  server before saving, so a typo is caught while the old certificate is still
  in place rather than after a restart.
- **Tunnel Metrics** (**Manage → Tunnel Metrics**): traffic and connection
  counts per tunnel, and for KCP the numbers that actually explain a slow link —
  retransmits, lost and duplicated segments, and **how many packets forward
  error correction repaired**. That last one is the direct answer to "is KCP
  earning its overhead on my route?"
- **Release channels.** The updater can follow **stable** (default) or **beta**,
  so pre-releases can be tested without being pushed to everyone. Switch under
  **Update → Release channel**.
- **Downloaded releases are checksum-verified.** The installer and the updater
  both check the asset's SHA-256 against the published `SHA256SUMS` before
  installing it, and **refuse to install anything they cannot verify** — see
  *Security* below.
- **The Telegram bot picks its own way out, and re-picks it when that breaks.**
  Reaching Telegram from Iran means going out through a tunnel, and choosing
  which one was a question you should never have been asked. The relay is set to
  **Automatic** by default: the bot forwards through whichever tunnel is up, and
  when that tunnel goes down it moves to the next live one on its own. A specific
  tunnel can still be pinned if you want one.
- **Relay diagnosis** (**Telegram Bot → Diagnose**). When the bot cannot reach
  Telegram, the error it surfaces is whatever the HTTP client saw — usually a
  bare `EOF` — and that names the wrong machine. The chain has five links across
  two servers. This walks them in order — bot configured, relay tunnel chosen,
  that tunnel up, relay port open, the peer's own internet, Telegram itself —
  and stops at the first one that is actually wrong. When something other than
  Telegram answers on the relay port, it reads the reply and **says what that
  was** (an HTTP server, an SSH server, a stale SOCKS proxy, or nothing at all)
  instead of reporting a failed handshake.
- **Backup import and export from the web panel** (**Settings**), alongside the
  CLI. Configs can be pulled down and pushed back without SSH.
- **Telegram setup from the web panel** (**Settings**) — token, admin ID, alert
  thresholds and relay choice, all previously CLI-only.
- **Setup checks the address you give it.** Before saving a client tunnel it
  resolves the server address and warns about the two things that silently break
  a tunnel that looks correctly configured: an address that resolves into a
  **CDN** (matched against published IP ranges, not reverse DNS — Cloudflare's
  addresses carry no PTR record naming it), and a domain carrying **both an A
  and an AAAA record**, where the tunnel may connect over IPv6 and fail if IPv6
  does not reach the server or the port is only open for IPv4. That second one
  is the reason a bare IP can work where its own domain does not.

### Changed
- **Monitoring is now its own service, independent of the web panel.** The
  watchdog, the Telegram bot and the alerts used to run inside the panel
  process, which made the panel a dependency of monitoring — backwards. Stopping
  the panel, or the panel crashing, or turning it off because you only wanted
  the CLI, silently stopped dropped tunnels being restarted and stopped every
  alert. Nothing visibly broke; it just quietly stopped watching, which is the
  worst way for a monitor to fail.

  They now run as `backpack-monitor.service`, which depends on nothing but the
  machine being up and restarts itself if it dies. Existing installs pick it up
  automatically — the CLI installs it on launch and the updater installs it as
  part of an update — so there is nothing to do by hand. **Health Check** reports
  on it, and says plainly that dropped tunnels will not be restarted if it is
  down.
- **The web panel now has one fixed theme, matching the CLI.** The accent is the
  same red-orange used by the menu, and the colour picker is gone — the panel and
  the terminal should look like one product rather than two. The CPU, RAM, disk
  and swap gauges follow that accent instead of a green-amber-red scale;
  **green now means exactly one thing, a tunnel that is up**, with amber for one
  that is down. Load is still readable at a glance: a gauge past 85% brightens
  rather than changing colour. An accent saved by an older build is cleared on
  first load, so an upgraded install does not keep a colour the panel no longer
  offers.
- **The panel's tunnel cards were cut back to what you actually read.** State is
  a single dot rather than a word, ports are split into **Tunnel Port** and
  **Forwarded Ports** instead of one undifferentiated list, and the country flag
  is derived from the peer's address rather than being something to configure.
  Sign out moved to the bottom of **Settings**, and Support is pinned to the
  bottom-right corner so it stays put while the page scrolls.
- **The Telegram bot's messages were rewritten.** **Status** leads with the
  things that answer "is it working" — flag, preset, ports, traffic — **System**
  was cut to the numbers worth reading on a phone, and the Tunnels and Metrics
  sections were removed rather than kept as walls of text. `/help` lists what the
  bot can actually do, and a **Backup** button pulls the configs down through
  Telegram. Internal plumbing — the relay port, the SOCKS port, the API host —
  no longer appears anywhere in a message.
- Building from source now requires **Go 1.24 or newer**; the installer checks
  for this and installs a suitable toolchain if needed. Installing from a
  release asset is unaffected — it is a prebuilt binary.

### Security
- **WSS/WSS Mux now serve a decoy website to anything that is not a tunnel.** A
  WSS tunnel is meant to be indistinguishable from an ordinary HTTPS site, but
  answering a browser, a scanner or an active probe with a `401` or a blank close
  gives it away. Every request that is not a genuine tunnel connection — a
  WebSocket upgrade, on a tunnel path, with a valid credential — is now answered
  with a plausible "Welcome to nginx!" page (`200`, `Server: nginx`), so the
  server looks like a normal website. Built in and always on; nothing to
  configure. Combined with the Let's Encrypt certificate and the Chrome TLS
  fingerprint, the server presents as a real HTTPS website to anyone probing it.
- **The WSS credential is bound to the TLS session instead of being sent.** WSS
  and WSS Mux dial with the certificate unverified — the tunnel trusts its token,
  not a CA, and the certificate is often self-signed. That is fine against a
  passive observer but leaves a gap against an active one: on a path the operator
  does not control, something can present its own certificate, terminate the TLS,
  and read the bearer token the client sends next — which is all an impostor
  needs. So the token is no longer sent. Each side derives RFC 5705 keying
  material from its own side of the TLS session, and the client proves it holds
  the token by sending `HMAC(token, keying material)`. A man in the middle that
  terminated the TLS has a different session with each side, so the proof it
  received from the client does not match what the server expects, and it never
  learns the token to forge one. It works the same for self-signed and Let's
  Encrypt certificates, and costs nothing on the wire. **Both ends of a
  wss/wssmux tunnel must be on this version.**
- **The Telegram relay port now listens on loopback only.** The bot reaches
  Telegram by having a server tunnel forward a local port straight to
  `api.telegram.org:443`. That mapping was written as a bare port number, which
  binds every interface — so the port was reachable from the internet on the
  Iran server's public address, and nothing authenticates a forwarded connection
  (the tunnel token guards the tunnel's own channel, not the ports it exposes).
  Anyone who found the port had a free, unauthenticated TCP relay to Telegram
  going out through the peer's IP. The port is only a random number in a
  40 000-wide range, which a port scan finds in seconds, and the mapping is
  hidden from every port listing, so nobody was going to spot it.

  New tunnels bind `127.0.0.1`. **Existing tunnels are migrated automatically**
  the next time the bot resolves its relay — the mapping is rewritten and the
  tunnel restarted — because it is not visible for you to fix by hand.
- **Updates now refuse to install an archive they cannot verify.** The checksum
  published with a release was checked when it was available and skipped with a
  warning when it was not, and the warning was discarded entirely by the web
  panel. Since the binary is replaced and run as root, an unverifiable download
  is now an error instead: the update stops and points at the offline install.
- **Third-party GitHub proxies were removed from downloads and updates.** The
  archive and its `SHA256SUMS` travelled through the same proxy, so a proxy
  serving a modified binary could serve a matching checksum with it — the
  verification proved nothing in exactly the situation that made a proxy get
  used. Downloads now go direct to GitHub or through the tunnel relay, both of
  which terminate TLS at GitHub. A server that can reach neither installs
  offline; the README has the steps, including a by-hand sequence for anyone who
  would rather not run a script.

### Fixed
- **The updater could not find its own tunnel relay.** It looked for the relay
  mapping by the fixed port 1080, but the port has been derived from the tunnel
  token since it stopped colliding with whatever else was already on 1080. No
  mapping written since then matched, so the relay was never offered and the
  updater was left with a direct connection to GitHub — precisely what a server
  in Iran does not have. Both forms are now recognised.
- **Traffic counts read zero on every transport except KCP.** Tunnel Metrics
  showed real numbers for KCP and nothing at all for TCP, TCP Mux, WebSocket and
  the rest. KCP was the only one being counted, and not by Backpack — the KCP
  library keeps its own counters, so those numbers arrived for free while nobody
  had ever counted the others. Bytes are now counted on every transport.
- **Traffic totals reset to zero whenever a tunnel restarted.** They lived only
  in memory, so a restart, an update or a reboot wiped the history. They are now
  written to disk and **survive a backup restore**: restoring picks up from the
  totals in the backup rather than starting again from zero.
- **The web panel now reports what an update actually did.** It fired the update
  off and reloaded after a fixed delay, discarding the log and any error, so a
  refused or failed update left you looking at the old version with nothing
  explaining why. It now follows the update and shows the outcome.
- **The web panel showed working KCP and UDP tunnels as offline.** It decided
  whether a tunnel was up by looking for connected peers in the TCP socket
  table, which is right for the TCP-based transports and meaningless for the
  datagram ones: a KCP listener is a single unconnected UDP socket that keeps no
  record of who is talking to it, so there was never anything to find. The
  tunnel was carrying traffic the whole time.

  The watchdog already handled this correctly. The panel got it wrong because it
  was answering the same question with its own separate code — so both now go
  through one function, and the panel cannot drift away from it again.
- **"connection refused" now says what to do about it.** When the far side
  cannot reach the service it forwards to, the log used to read
  `local dialer: dial tcp <nil>->127.0.0.1:4545: connect: connection refused` —
  accurate, and almost useless. It does not say which of the two machines the
  fault is on, that the tunnel itself worked, or what to check. The reasonable
  conclusion is that the tunnel is broken, and the reasonable next step is to
  uninstall it.

  It now names the machine, says plainly that the tunnel delivered the
  connection, gives the two causes that actually produce it (the service is not
  running, or it is bound to a public IP rather than 127.0.0.1), and prints the
  command that tells them apart. Timeouts get their own wording, since a
  firewall is a different problem from a missing service. Repeats are suppressed
  for 30 seconds per address: a client retrying once a second used to bury
  everything else in the log.
- **Setup now shows what the far side must be listening on.** The port mapping
  is entered on the Iran server but describes something on the kharej one, and
  that indirection is where it goes wrong. After entering the ports, setup
  prints each one resolved — `443 → 127.0.0.1:443` — so a bare port is concrete
  before the tunnel is built rather than a mystery afterwards.
- **pprof listened on every interface.** When the profiling endpoint was
  enabled in a tunnel config it bound `0.0.0.0`, unauthenticated — and a pprof
  heap dump contains whatever is in memory, including the tunnel token, which is
  all an attacker needs to connect. It is now bound to loopback; reach it with
  `ssh -L 6060:127.0.0.1:6060`. It is off by default and the CLI never enables
  it, so an install that has not hand-edited a config was never exposed.
- **Config files could be read while half written.** Backpack runs as several
  processes and they share these files: the CLI writes them, the panel and the
  monitor read them on a timer. A plain write truncates first, so a reader
  landing in that window saw an empty file — read as "the bot is not
  configured", which for the monitor is a cycle with no alerts. They are now
  written to a temporary file and renamed into place, which is atomic.
- **An update left the monitor running the old binary.** The service unit does
  not change between versions, only the binary it points at, so the
  install-if-missing check correctly found nothing to do — and `systemctl start`
  does nothing to a service that is already running. The update and rollback
  paths now restart it explicitly, and the post-update health check judges it,
  so a version whose monitor cannot start is rolled back instead of kept.
- **A SOCKS5 reply was parsed without checking for a short read.** The bound
  address at the end of the handshake was consumed with the error discarded. It
  never failed there; it failed afterwards, when the caller read the leftover
  bytes as the start of its own response — a Telegram request returning garbage
  rather than an honest connection error.
- **A data race on the control channel, in every transport.** The field was
  written by the handshake goroutine and read by the accept loop, the heartbeat
  and the restart path with no synchronisation, so a reader could observe a
  stale or half-published value — the accept loop refusing connections it should
  have allowed. On the client side `Restart()` replaced the context, the control
  channel, the usage monitor and the counters while the previous generation's
  goroutines were still reading them. Both are now published behind a lock, and
  the race detector runs on every CI build.
- **A possible crash when a peer disconnected mid-check.** The "suspicious
  packet" check asked whether a control channel existed and then asked for its
  address as two separate steps; if it was cleared in between, the address came
  back nil and the type assertion panicked. The address is now read once, and
  compared in a way that is correct for IPv6.
- **IPv6 addresses were built by string concatenation** in three places (the
  server bind address, the client's server address, and the CDN edge address),
  which produces something unresolvable for an IPv6 literal. All now use
  `net.JoinHostPort`. There are end-to-end tests running whole tunnels over IPv6.
- **The watchdog could not see UDP-based tunnels.** It read only the TCP socket
  table, so a UDP tunnel never registered as connected. Client tunnels are now
  checked against connected UDP sockets; for a server, a UDP listener genuinely
  cannot report its peers, so the health screen says that plainly instead of
  implying the tunnel is down.
- **Health Check no longer reports a false failure on UDP transports.** A TCP
  connect cannot test a UDP port, so that check now says so rather than showing
  a ✗ for a working tunnel.

### Notes
- **QUIC was built, tested on a real Iran route, and removed.** It never
  completed a handshake there while KCP on the same link worked at full speed,
  so it was dropped rather than shipped as an option that looks available and
  silently fails. The UDP menu offers UDP and UDP + KCP.
- **Compression was considered and deliberately left out.** Almost everything
  these tunnels carry is already encrypted (VPN or TLS traffic), which does not
  compress — enabling it would burn CPU for no gain while appearing to be a
  speed feature.


## v1.4.0 — 2026-07-18

### Added
- **Automatic failover to backup server addresses.** A client tunnel can hold a
  list of extra server addresses (a second IP, a different port, a CDN edge).
  When the main address stops answering — a filtered IP, a blocked port — the
  client rotates to the next one automatically until something connects, and all
  data connections follow it. Set it during **Setup Client** or later from
  **Manage → Manage Tunnels → Edit → Backup server addresses**.
- **Safe updates with automatic rollback.** Every update first saves a **restore
  point** (the binary plus every config), installs the release, then health-checks
  the panel and all tunnels. If anything fails to come back up it restores the
  previous version by itself. Restore points are also listed under
  **Update → Restore points** so you can roll back on demand.
- **Safe edits.** Changing a port, address or transport keeps the previous config,
  verifies the tunnel actually came back up, and **reverts automatically** if it
  did not — reporting the reason from the log (e.g. "address already in use").
  A bad edit can no longer leave a dead tunnel and a lost config behind.
- **Change transport on an existing tunnel** (tcp ↔ tcpmux ↔ udp ↔ ws ↔ wss ↔
  wsmux ↔ wssmux) without recreating it: the name, token and forwarded ports stay
  as they are, mux settings are filled in, and a TLS certificate is generated
  automatically when switching to wss/wssmux.
- **Health Check** (**Manage → Health Check**): one screen that checks the server
  (BBR, queue discipline, socket buffers, open-file limit, binary, root, systemd),
  the web panel (service, port, firewall hint) and every tunnel (state, listening
  port, port syntax, real TCP reachability, TLS certificate expiry, token
  strength) — with a ✓ / ! / ✗ per item and a plain-language fix for each problem.
- **File Locations** (**Manage → File Locations**): every config, service, backup
  and certificate path with a ✓/✗ so you can see what is installed and where.

### Changed
- Reachability is measured over **TCP, never ICMP** — networks that drop ping no
  longer look "offline" when the tunnel port works fine.
- Backups are pruned to the newest 10 archives, and restore points to the newest
  5, so neither can fill the disk.



## v1.3.0 — 2026-07-14

### Added
- **Edit tunnel ports from the CLI.** Every tunnel now has an **Edit** action
  (Manage → Manage Tunnels → tunnel → Edit): change the **tunnel (control)
  port**, the **forwarded ports** (server) or the **server address** (client).
  Changes rewrite the config and restart the tunnel automatically; the hidden
  Telegram/SOCKS relay mapping is preserved.
- **Change the web-panel port** from the CLI (Web Panel → Change panel port)
  and from the panel itself (Settings → Panel port, with auto-redirect).
- **Release-based install & updates.** `install.sh` now installs the prebuilt
  `backpack_linux_amd64.tar.gz` / `backpack_linux_arm64.tar.gz` release assets
  into **`/root/BackPack`**, and the in-app **Update** detects newer versions
  from GitHub releases and installs them — trying **direct → tunnel SOCKS relay
  → public mirrors**, so it works from Iran without Go or git on the server.
  Works for old clone-based installs too: run Update once from ≤ v1.2.0 (final
  git pull + rebuild) and every update after that comes from the releases.
- **Backups folder.** Backups now live in **`/root/BackPack/backups`** by
  default, and Restore lists the archives there so you just pick one.
- Port entries are **validated** before they reach a config (`443`, `400-450`,
  `443=1.1.1.1:443`, …) — a bad entry used to crash-loop the tunnel service.
  Tunnel names are validated too.

### Changed
- **CLI restyled and reorganized.** Three-color theme (red / white / gray),
  a gray description beside **every** menu option, and a cleaner layout:
  Setup Server, Setup Client, Manage (tunnels · status · restart all · auto
  refresh), Backup & Restore, Web Panel, Optimize, Telegram Bot, Update,
  Uninstall, Exit. The big status header is gone — the panel link & login code
  now live inside the **Web Panel** section.
- **The web panel is monitoring-only** (recommended on the IRAN server): live
  system metrics, tunnel state/ping/logs. Tunnel creation/management, Telegram,
  auto-refresh and backup moved to the CLI; Settings keeps theme, update,
  panel port and password. Support stays.
- **Telegram bot defaults to the tunnel relay.** Configuration now asks which
  tunnel to relay through (a random SOCKS5 relay port is added to it), since
  Iran servers can't reach Telegram directly; “direct” remains available for
  kharej-side setups.
- Watchdog client health-check now matches the peer IP (not just the port), so
  an unrelated outbound connection can no longer mask a dropped tunnel.

### Removed
- Web-panel tunnel create/edit/actions, Telegram setup, auto-refresh and
  backup/restore endpoints (moved to the CLI).
- The `prerequisite/` offline bundle (release assets replaced it).



## v1.2.0 — 2026-07-13

### Added
- **Full backup & restore.** Bundle every tunnel (with its token), the web-panel
  password, Telegram settings, TLS certificates, per-tunnel metadata and the
  auto-refresh schedule into a single portable `.tar.gz` — from the CLI
  (**Backup & Restore**) or the web panel (**Settings → Backup &
  restore**) — and restore it on any server. Restore re-registers and starts
  every tunnel, brings the panel back up, and restores the schedule. The archive
  extractor is hardened against path traversal, and the machine-specific
  `install_path` is never overwritten on the target host.

### Changed
- **Friendlier CLI.** The main menu now shows a short description beside each
  option, and the header shows the web-panel URL, login code, tunnel counts,
  auto-refresh status and the version at a glance.
- **Web panel starts on launch.** The panel is brought up as soon as the menu
  opens, instead of only after the first tunnel is created.

### Security
- **Tokens are no longer written to logs.** Invalid-token handshakes previously
  logged the token value (visible via `journalctl` and the panel log drawer);
  the value is now redacted on both the server and client sides.

### Notes
- No new dependencies — the binary still builds from the Go standard library
  plus the existing modules, so one-click updates keep working on restricted
  (e.g. Iran) networks.
