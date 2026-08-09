# Changelog

All notable changes to Backpack are documented here.

## v1.7.1 — 2026-08-10

Two halves. The web panel stops being a window and becomes a way to work: it
creates, edits and drives tunnels itself, so the CLI is a choice rather than the
only door. The other half is the UDP side of the engine: the three transports
that carry their data in datagrams — udp, kcp (and xdi, which is kcp over ICMP) —
plus quic, which joins them.

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
