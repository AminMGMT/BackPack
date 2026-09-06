# Changelog

All notable changes to Backpack are documented here.

## v1.7.7 — 2026-09-04

Managed servers are reached over SSH now, and the panel that drives them is the
only panel. Those are the same change told twice. The old node model was an
agent Backpack installed on the far server, listening on a port of its own,
speaking a protocol of its own — and it was the reason adding a server took
minutes, reported three tunnels where there was one, and built tunnels that did
not carry traffic. What it was really doing was reimplementing, badly, something
every one of these servers already runs. So it is gone: a server is added with
an address, a port, a username and a password, and Backpack logs in the way you
would. There is no agent, no port to open, and nothing to install before the
first connection.

The rebuilt panel replaces the classic one rather than sitting beside it. It was
served at /panel/ behind a per-server setting for as long as it was unfinished,
with an escape hatch back to the single-file dashboard; that scaffolding, and
the 350 KB dashboard it protected, are both removed. The panel answers at "/".

The rest is what a week of using it turned up. Every figure the overview showed
was formatted before it was totalled, so the totals were wrong in a way no
screenshot could reveal. The open-file-limit health check measured the wrong
process and advised a fix that could not work. The speed test blamed the far
server for a connection this one refused. A layer-3 tunnel whose far side had
nothing listening on the mapped port said nothing at all about it. None of these
announce themselves; each was found by running the thing and reading what it
claimed against what was true.

### Changed

- **Managed servers speak SSH.** Adding one asks for the four things an SSH
  login needs — address, port (22 unless yours differs), username (usually
  root) and password — and the host key is trusted on first use and pinned
  after. Backpack installs itself on the far server over that connection,
  reports its version, system and uptime on the server card, and upgrades it
  from the panel with one click when a release lands. **The node agent, its
  listening port and its wire protocol are removed**; a server enrolled the old
  way must be added again.

- **The panel is served at "/".** /panel/ redirects there, so a bookmark, a
  pinned tab or a link somebody was sent keeps working. The Interface setting
  that chose between the two panels, the /api/panelui endpoint behind it and the
  ?panel=classic escape hatch are gone with the panel they switched to.

- **Reverse tunnels are built through managed servers only.** The panel used to
  offer "I will set the other one up myself" and a setup link to paste on the
  far server; both are removed. A tunnel to a server that is not in the fleet is
  made from the CLI menu, and the panel shows its card like any other.

- **IP spoofing and SNI spoofing are CLI-only.** They stay available from the
  menu and stay fully supported; what they no longer have is a place in the
  panel's Add tunnel, where they were the two carriers most likely to be picked
  without understanding what they do to a route.

### Fixed

- **Every traffic figure on the overview was computed from a formatted string.**
  "1.2 TB" parsed back as 1.2, so totals across tunnels were arithmetic on
  numbers that had lost their units — the all-time traffic, the per-tunnel
  shares and the split between directions were all wrong together, and
  consistently wrong, which is why they looked right. The API sends the raw byte
  counts beside the formatted ones now, and the panel computes with those.

- **The open-file-limit health check measured the panel, not the tunnels.** It
  read the limit of whichever process happened to answer, reported 1024, and
  told the operator to run Optimize and reboot — which changed nothing, because
  the tunnel services carried no LimitNOFILE at all. It reads the limit of the
  tunnel's own process now, and the unit files set one.

- **The speed test blamed the far server for a local refusal.** A connection
  this machine refused was reported as the other end needing its receiver turned
  on, sending operators to a server that was working. A locally refused
  connection is now named as one.

- **A layer-3 tunnel whose far side has nothing listening now says so.** Every
  signal an operator can see said the tunnel was healthy — a session on both
  ends, a completed MTU probe, clean pings, rekeys on schedule — and all of it
  was true; the service behind the forwarded port simply was not there. That
  failure was logged at debug, so the log was silent about the one thing that
  was wrong. It is a warning now, repeated at most once a minute, and the
  recovery is reported too.

- **A pck listener could not start without a default route.** The listening side
  has no peer to route toward, so it probed 1.1.1.1 to find its egress
  interface, and a machine using policy routing, an IPv6-only default or a
  private segment failed with an error naming an address the operator had never
  configured. The listener falls back to the first usable interface; the dialling
  side, which does have a real peer to reach, is unchanged.

- **Enrolling a server could no longer half-finish.** A shutdown or a dropped
  network during enrolment could leave the far server holding credentials this
  one had discarded, unreachable from either side. Enrolment is claimed,
  acknowledged and given a grace period, and a shutdown waits for it rather than
  cutting it short.

- **A tunnel could be held down by a peer that stopped reading, until somebody
  restarted it by hand.** The server writes one byte at a time on its control
  channel — a heartbeat, a request for a pool connection — and those writes had
  no bound. A write lands in the kernel's send buffer and returns; when the path
  to the client goes and the buffer fills, it blocks until the kernel stops
  retransmitting, which is around fifteen minutes on Linux defaults. For that
  whole window the server still believed it had a control channel, so it refused
  every attempt by the client to establish a new one ("a control channel is
  already established"), could not ask for pool connections because the request
  reached nobody, and dropped every user connection with the queue full —
  thousands of lines a second. Nothing it could do would recover it. Control
  writes are bounded at ten seconds now, on all seven transports: past that the
  channel is treated as gone, the transport restarts, and the client's next
  claim is accepted.

- **"the queue is full, dropping a client from ..." named the wrong machine.**
  It printed the local address of the accepted connection, so every one of those
  lines carried the server's own address and the forwarded port — which reads as
  the server connecting to itself, and sends anyone debugging it in the opposite
  direction from the problem. It names the client now.

- **A failed control write reported itself as a read.** "failed to read message
  from net.Conn: write tcp ...: write: connection timed out" was the wire
  helper's error text on the write path.

- **The "Random" button no longer offered a random forwarded port.** It sat
  beside Forwarded ports as well as Tunnel port, and asks the server for a port
  that is free *on this machine* — right for the port this side binds, and the
  opposite of right for a forwarded one, which the field's own hint says goes to
  the same port on the kharej machine. A port chosen for being free here is by
  construction one nothing is listening on there, so the button could only build
  a tunnel that comes up, reports a peer, shows green and refuses every
  connection at the last hop. It is offered for the tunnel port alone, and bound
  to that field rather than to every button carrying the label.

- **A tunnel delivering into nothing no longer reads as healthy.** When the
  service on the far machine is not listening, every connection crosses a
  working tunnel and dies one hop past the end of it — and every reading the
  panel had said so: control channel up, peer connected, counters moving, all
  true. The client records the failing last hop in its metrics snapshot now
  instead of only logging it, the far end reports it to the panel over the same
  SSH connection the fleet already uses, and the card says which address is not
  answering and how many connections have been lost since. The state stays
  "online", because the tunnel is: restarting it would fix nothing, and the
  watchdog reads that field.

- **A server already running an older Backpack is upgraded, not refused.**
  Adding one reached it, found a Backpack that did not understand the panel's
  command, and reported it as a server that could not be reached — printing the
  far machine's own help into the add form, which told the operator to run
  `backpack node setup`, the flow this release removes. Every server in an
  existing fleet is in that state the day the panel is upgraded, so this was the
  ordinary path rather than an edge. A binary that is too old is now recognised
  as one to install over, the same as one that is missing, and the install is
  what upgrades it. The panel no longer repeats whatever the far machine
  printed.

- **Creating a tunnel with an unknown preset is refused by name.** It used to be
  accepted and silently ignored, so the tunnel was built on defaults nobody
  chose.

- **Closing a dialog returns to the screen it was opened from.** Every dialog
  drew the tunnels list underneath it, so closing one opened from the overview
  left you somewhere you had not been.

### Added

- **A tunnel that already exists can be linked to the server holding its other
  end.** Everything the fleet does for a tunnel — carrying an edit across,
  starting and stopping both halves together, reading the far server's journal,
  standing the speed test's receiver up over there — is gated on a record of
  where that other half lives, and that record could only ever be written at the
  moment the panel built both ends at once. So a tunnel made any other way, or
  made before its server joined the fleet, was permanently outside it: **this is
  why the speed test still did nothing on an existing tunnel after its server
  was added.** The tunnel's own menu links it now, and unlinks it again.

  The match is demonstrated rather than guessed. A reverse client dials its
  server's host and port, and that port is the one the server binds — so a
  tunnel on the far machine aimed at this one, on this tunnel's port, is this
  tunnel's other end whatever either of them is called. Those are marked; the
  rest are offered unmarked and never claimed. Nothing is linked without the
  operator saying so, because a pairing decides where their next edit is sent.

- **A server joining the fleet says which tunnels it already holds the far end
  of.** The same matching, run once when the server is added, asked one tunnel
  at a time. It offers and never links.

- **Deleting a tunnel can now remove the far end too, as its own question.** It
  never crossed before, on the reasoning that a delete here is not consent to
  one there — which is right, and unchanged. What changed is that the panel asks
  the second question instead of leaving the operator to go and do it by hand:
  a separate dialog, after this end is settled, naming the other machine, with
  the safe answer under the cursor and under Enter. Saying nothing still leaves
  the far end alone.

### Security

- **golang.org/x/crypto updated to v0.56.0**, which closes the SSH
  source-address advisory reported against v0.54.0 and a second client-side one
  fixed in the same series. golang.org/x/net and golang.org/x/text move with it.

### Removed

- **Remote access, two-factor authentication and new-sign-in notifications**,
  from the settings screen and from the code behind them. None of the three did
  anything useful in their current state.

- **The on/off switch on managed servers**, which guarded a listener that no
  longer exists — the panel dials out, so with no servers in the fleet it does
  nothing at all, and turning "nothing at all" off could only ever be in the way.

- **The classic dashboard** (internal/webui/assets/dashboard.html) and the 57
  tests that pinned its markup. What those guarded about the rebuilt panel — the
  setup and edit forms matching the structures they post into, field by field in
  both directions — is guarded there instead.

## v1.7.6 — 2026-09-03

IP spoofing was a reverse transport that could not work, and this release makes
it a direct-tunnel carrier that does. A reverse tunnel is a control channel plus
a pool of connections, each its own session, and a forged-source packet carries
nothing a receiver can tell those sessions apart by — every one of them arrives
at the same address, the session layer keys on that address, and each new
session closed the one before it. The tunnel reported itself connected and
carried nothing. The direct tunnel has one session, which is the shape the
carrier can actually serve. The same fault, from the same cause, is fixed in
xDi. And the carrier picked up the two things it was missing against the tool it
was modelled on: error correction, and several sockets. Everything below was
proven end to end — a real tunnel over real sockets moving real traffic — not
just unit tested.

The direct tunnel also gained three carriers and lost a choice. QUIC carries the
tunnel in real QUIC datagrams, so a path sees HTTP/3; ICMP is offered again; and
SNI spoofing sends a TLS hello naming a domain the route allows, so a box that
classifies by server name lets the rest through. The encapsulation is GRE and
only GRE — two of them were a way for the ends to disagree, and a pair that did
came up, reported a peer, logged nothing and carried nothing.

And the new panel stopped being a drawing. It was built from a preview, and a
preview draws controls rather than making them work: this release fixes
thirty-six faults found by clicking every control on every screen against a real
server — a running tunnel shown as stopped, an edit that could not be saved, a
certificate section that refused every mode, invented figures presented as
readings. Its second source of data is gone with them, because somewhere for
made-up numbers to come from is what let so many of them survive.

### Changed

- **IP spoofing is a direct-tunnel carrier now, not a reverse transport.**
  `transport = "spoof"` is refused at startup, by name, with what to build
  instead: a direct tunnel on the spoof carrier, which forwards the same ports
  over the same forged packets. The wizard offers it under **Direct**, and the
  panel builds and edits it in full. **This is a breaking change** for anyone
  running a reverse spoof tunnel — which could not have been carrying traffic,
  for the reason above — so rebuild it as a direct one. Relay mode
  (`spoof_mode`, `spoof_pipe`) went with the reverse transport: a direct tunnel
  is a private network, so WireGuard and the like are routed over it rather than
  piped through it.

- **xDi tells its sessions apart by the ICMP identifier.** It had the same
  collapse as reverse spoof — every session of a tunnel identical on the wire,
  so the listener folded them onto one entry and closed each as the next
  arrived. The identifier the protocol has for exactly this was being derived
  from the token, so it was the same for every session; it is drawn per session
  now. **A breaking wire change for xDi** — both ends must run this version — but
  since the transport could not carry traffic before, there is nothing to lose.

### Added

- **QUIC as a direct carrier.** A real QUIC session — real TLS 1.3, ALPN `h3` —
  carrying the tunnel in RFC 9221 datagrams. It is the only obfuscated carrier
  that needs no root, and it is not imitating HTTP/3: it is HTTP/3's transport.
  Datagrams rather than a stream, because a layer-3 tunnel carries IP packets
  whose flows already handle their own loss, and stacking retransmission on that
  makes throughput collapse instead of degrade.
- **SNI spoofing as a direct carrier.** The pck carrier plus one extra segment at
  the start of the flow: a TLS ClientHello naming a domain the path allows. A
  filter that decides by server name reads it and stops looking. The technique is
  patterniha's, by way of therealaleph/sni-spoofing-rust. It wraps the pck
  carrier rather than changing it, and the far end drops the hello before the
  tunnel sees it — so nothing has to be smuggled past a stranger's stack.
- **ICMP (`xdi`) is offered again.** It was built, tested and still running; it
  had simply been taken out of the menus.
- **The updater can be pointed at a tunnel.** It already tried a relay after a
  direct connection, but only one that already carried the relay port — which on
  an Iran server with working tunnels and no route to GitHub meant no relay at
  all. The CLI now names the tunnels that are up, says which would cost a restart,
  and fetches through the one chosen.
- **A way out of two-factor from the CLI.** It is set from the panel and the code
  is delivered by the bot; when that delivery stops working the panel refuses
  every sign-in, and the setting that would fix it was behind that sign-in. Web
  Panel → Two-factor sign-in is the way back.
- **The watchdog says whether a tunnel came back.** It restarts one that has
  stopped carrying traffic and said so only into a file the bot read when asked.
  The half of the story an operator waits for now arrives.
- **A one-click switch between the panels, in both of them.** The classic panel's
  was a checkbox two clicks deep in Settings; the new one's was a menu item. Both
  are menu items now.

- **Managed servers.** A foreign server can register itself with a panel and be
  configured from it, so a tunnel is set up in one place instead of twice. On
  the panel: **Servers** → turn the listener on, name the server, copy the line
  it produces. That line **installs Backpack and enrols the server in one go** —
  `bash <(curl -fsSL .../install.sh) node --panel <ip>:<port> --key <key>` — so
  a bare VPS is one paste rather than two steps in an order that has to be got
  right. The installer validates the arguments before it downloads anything, and
  a server that already has Backpack can use the short `backpack node setup …`
  form the panel keeps one click away. After that **Add
  tunnel** offers it under *The other server*, and Create builds both ends —
  every transport and carrier, reverse or direct. Editing sends the complete
  state the tunnel should be in, so a changed MTU or transport rewrites the
  config on the far server too, with the same write-restart-and-roll-back
  protection a local edit has always had. An edit that cannot reach the server
  is reported as exactly that — this end changed, that one did not, and which
  one is behind — rather than as plain success. Deleting a tunnel removes it
  here only; the panel names the server that still has it. The CLI menu warns
  before editing a paired tunnel, because that process has no channel to the
  node and the change would not travel.

  The panel is **never given a login** for the machine. The command installs a
  local service that holds the privilege; what crosses the wire is a request to
  perform one of four operations — apply, list, status, restart — and the node
  refuses anything else. There is deliberately no operation that runs a command,
  reads a file, or removes a tunnel. The server dials the panel rather than the
  reverse, so it opens no inbound port of its own, and the channel is a Noise
  `NNpsk0` session with no fingerprint to match on. Setup keys are single-use
  and expire in a day; each server's own credential is issued at enrolment and
  revoked by **Remove**, which leaves its tunnels running. The channel is
  independent of the tunnels, so a tunnel that is down does not take away the
  way to fix it. A speed test on a tunnel whose far end is managed now starts
  the receiver there itself — it was the last step that still needed somebody
  on the other machine — and that sink closes itself when the run is over. A tunnel's
  card drives both ends too: start, stop and restart reach the managed server,
  because a tunnel in two places has one state and an end stopped on its own
  just leaves the other half dialling. Delete stays local by design. **New panel only.** See
  [docs/managed-servers.md](docs/managed-servers.md).


- **Error correction on the direct tunnel.** For every `fec_data` datagrams the
  tunnel sends `fec_parity` spare ones, and any `fec_parity` of a group may be
  lost without loss — redundancy, never retransmission, which is the only thing
  a layer-3 tunnel may safely stack. Measured against a link dropping 20%, an
  application saw **3.5% loss with it on and 39% with it off**. It works over
  every carrier, both ends must set the same pair, and it is one question in the
  wizard and a checkbox in the panel.

- **Several sockets for the UDP carrier (`paths`).** A tunnel on one socket is
  one flow, and a provider that limits each flow separately gives it one flow's
  allowance however fast the link is. Spreading over several sockets — on
  consecutive ports from the tunnel port — makes it several flows. Measured
  against a link capped at 8 Mbit/s per flow, one socket carried **5.8 Mbit/s
  and four carried 23.6**. It is the UDP carrier only; the obfuscated carriers
  vary their source per packet already.

- **Two more spoof packet profiles, `proto58` and the rest, in the menus.**
  `icmpv6`, `ipip` and `gre` existed in the engine but only a hand-edited config
  could reach them; `proto58` — an ICMPv6 echo's protocol number carried bare,
  which some filters leave open when they clamp ICMP and UDP — is new. All seven
  are now offered by the CLI wizard and the panel, from one shared list.

- **The panel configures the spoof carrier in full.** It could create a spoof
  tunnel and set one thing about it — the peer address — while the CLI asked
  six questions. The direct form and the edit screen now carry the packet
  profile, the forged sources, Stealth, error correction and the socket count,
  each shown only for the carrier that has it.

- **Reverse-path filtering is checked and offered.** A strict `rp_filter` drops
  every forged-source packet before the tunnel sees it — the most common reason
  a spoof tunnel is up, connected, and silent. Health Check now reports it per
  tunnel with the exact `sysctl` to fix it, the startup check reads the
  *effective* value (the max of `conf.all` and the receiving interface, which is
  what the kernel applies — reading only `conf.all` missed a strict interface),
  and the wizard offers to relax it after a spoof setup.

### Fixed

- **A running tunnel was shown as stopped, everywhere in the new panel.** The
  server reports `online`, `offline` or `stopped`; six screens compared against
  `running`, which nothing produces. The card said Stopped, the overview counted
  it among the ones that were not running, and neither the speed test nor the
  link test would offer it.
- **Nothing on the edit screen could be saved.** Its Save button shipped disabled
  and nothing ever enabled it; once enabled, every save was refused because the
  form posted fields the transport does not have, and the tunnel port as a number
  where the server wants a string.
- **The panel could not be put behind a certificate of any kind.** The three
  options were drawn but were not controls, and Apply posted no mode at all, so
  every attempt came back "unknown mode" — Let's Encrypt included.
- **A failed Let's Encrypt issuance took the panel down with it.** autocert fails
  the handshake when it has no certificate, so a domain that does not resolve
  yet, a blocked port 80 or a rejected contact address locked the operator out of
  the page they would have fixed it from. The self-signed certificate is served
  meanwhile, and the panel switches over on its own once issuance succeeds.
- **A tunnel's certificate could only be set from the CLI.** The panel now reads
  and sets it, on the transports that present one.
- **Invented figures shown as readings.** Signed-in devices that belonged to
  whoever drew the screen, a bot named `@my_backpack_bot`, a backup taken
  "yesterday at 03:00", a week's traffic on a tunnel that had carried none, and
  every dialog headed with another machine's hostname and version.
- **`?mock=1` drew an empty server over a busy one.** The panel's fixtures were
  never shipped inside the binary, so forcing that mode fetched files that were
  not there. There is one source of data now.
- **One click sent as many requests as the screen had been visited.** Delegated
  handlers accumulated on a container that outlives the view, so the fifth visit
  meant five restarts from one press.
- **Ports, sizes and states rendered as nonsense.** Forwarded ports crashed the
  whole tunnels page, memory and disk read `NaN / NaN GB`, and a configured swap
  reported itself as not configured.
- **A direct tunnel with the spoof carrier could never have its far end built.**
  The producer's real address is what the listening side cannot learn for itself,
  and it was carried only when some spoof tuning had also been set — so the
  ordinary case, accepting the defaults, built one end and could never build the
  other.
- **Four lists of direct carriers.** The CLI's, the panel's, the create path's
  and the wizard's markup drifted, so a carrier added to some was offered by a
  screen whose endpoint then refused it by name. There is one list, checked
  against the engine that has to open them.
- **The updater's `already up to date` read as a contradiction** when the build
  was newer than the last published release.
- **Shutting the node hub down did not wait for it.** Closing the listeners and
  the sessions unblocks its goroutines, but one already past its read keeps
  running — mid-handshake, or writing the fleet to disk. A caller that then took
  away what those goroutines were using raced them.
- **Test ports collided between packages on CI.** The sequence started from the
  clock in milliseconds, and `go test ./...` starts a binary per package, several
  within the same millisecond.

- **The setup link was only offered on reverse tunnels.** The box that takes a
  link from the other server sat inside the reverse half of the setup form, so
  on a direct tunnel — which is what the wizard builds — it was never on screen,
  and the paired values had to be retyped after all. It now sits above the
  split, where it applies to both. The decoder always handled either kind; only
  the box was unreachable. *(New panel.)*


- **The icmp spoof profile silenced ICMP on the tunnel itself.** To stop the
  kernel answering the forged echo requests — a wasted reply per data packet, on
  the download path — the carrier set `net.ipv4.icmp_echo_ignore_all`, which is
  per-namespace and also silenced ICMP arriving on the tunnel. A layer-3 tunnel
  is a private network people ping across, so this broke a first-class use: ping
  across an icmp-profile tunnel was 100% loss while TCP was fine. It is a
  targeted iptables rule now, matched on the carrier's ICMP identifier, so the
  kernel's replies are dropped and the tunnel's own ICMP is untouched.

- **The XDP fast path declined without a word.** `spoof_xdp_interface` computed
  whether the kernel program attached or fell back and then threw the answer
  away — a loose end from the carrier's own refactor — so an operator who turned
  it on had no way to learn which happened. The tunnel now logs
  `XDP receive fast path attached to …` or `XDP receive unavailable … <reason>`
  on start.

## v1.7.5 — 2026-08-28

Every fix in this release came from somebody's tunnel, and almost all of them
turned out to be the same shape: the tunnel was broken and the system said it
was fine. A control channel that had been dead for eleven minutes and had not
noticed. A layer-3 tunnel writing segments past the end of the buffers it was
given, because the buffers were sized for a packet the kernel had stopped
sending. A watchdog reading health off a socket table that keeps saying
ESTABLISHED after the far end is gone. A second tunnel that started cleanly and
was refused on every handshake with no reason given, forever. A spoof carrier
whose sockets were healthy while nothing it sent survived the path. So a good
deal of the work below is not new behaviour but the system telling the truth
about itself sooner. Every fix has a regression test that fails without it.

### Added

- **Speed Test on the tunnel card.** The measurement existed, in the menu, over
  SSH. Now it is a button next to Start/Stop/Restart/Delete: the panel works out
  what this tunnel can be measured through — a layer-3 tunnel across its own
  subnet, a port forwarder through one of the mappings it already carries —
  shows what it will displace while it runs, and draws the result on a gauge.
  Mappings that cannot carry a measurement are listed with the reason rather
  than offered and then failing.

  The one thing it cannot check is the thing that matters: whether the receiver
  is running on the other server. There is no way to find that out from here, so
  it is asked rather than assumed, and a measurement against a machine that is
  not sinking fails in about a second.

- **Live bandwidth on every tunnel card.** Each card carries its own sparkline
  of what that tunnel is actually moving, animated between samples rather than
  jumping, so a tunnel that has gone quiet is visible from the dashboard instead
  of from a log.

- **Every configuration change can be undone.** `applySpec` already covered the
  loud failure: write, restart, wait, and put the old file back if the tunnel
  does not come up. The quiet failure had no cover at all — a config that starts
  perfectly well and is simply worse leaves nothing to go back to, and half an
  hour and three edits later "what was it before I started?" has no answer
  anywhere on the machine.

  Each accepted change now files the configuration it replaced, ten deep per
  tunnel, restorable from the panel and from the menu. Restoring writes the old
  file back verbatim rather than re-rendering it, so keys the edit form does not
  carry are not silently dropped. The timestamps are drawn on the traffic chart
  as well, which turns "did that change help?" from an impression into a line on
  a graph.

- **`mss` for direct tunnels.** See the MTU fix below; the knob is there for the
  paths where the automatic value is still wrong.

### Fixed

- **A dead control channel took eleven and a half minutes to notice.** The
  client's read on the control channel had no deadline of its own, so a link
  that went away without closing — the normal case for a middlebox dropping
  state, or a route change — was left to TCP keepalive. Go's `KeepAliveConfig`
  with a zero `Idle` uses a 15-second default and then nine probes at 75
  seconds: 690 seconds before the read fails. For eleven and a half minutes the
  tunnel is down, the process is healthy, the panel is green, and no new
  connection can be made.

  The read now carries a deadline derived from the tunnel's own keepalive —
  half as long again, floored at 30 seconds — so a silent path is noticed within
  a keepalive interval or two and the reconnect starts there. This was already
  right in two engines and wrong in the rest, which is a recurring fault of its
  own; there is now a test that reads the engine list out of the dispatch
  itself, so an engine cannot quietly opt out of it.

- **The client blamed the server's version for a broken path.** One closed
  handshake made it announce that the server was running an older release and
  fall back, which sent people to upgrade a server that was already current
  while the real fault — a connection closed in transit — went unexamined. The
  fallback now needs two unanswered attempts, and says both things it can be.

- **A second tunnel could be created that had no chance of working.** From a
  new user: one tunnel set up, working, in daily use; a second one made the same
  way that would not come up, with `EOF` on every handshake, forever. One or two
  others in the same chat had it too.

  Both ends have a version of this and neither was refused. On the Iran side,
  two tunnels binding the same port — the second cannot bind and dies, which at
  least appears in its own log. On the kharej side, two tunnels dialling the
  same server and port, which is worse: both start perfectly well, the server
  hands its single control channel to whichever arrived first, and the second is
  refused for as long as it runs. It is easy to do by accident — copy the tunnel
  that works, change the name, forget that the port belongs to the other end as
  much as to this one.

  Creating either is now refused at the moment it is typed, with the port to
  change named, on both the panel and the CLI wizard. `[::]` and `0.0.0.0` count
  as the same bind; two clients reaching different servers on the same port
  number do not.

- **A refused handshake said nothing at all.** A server that rejects a client —
  wrong token, or a control channel already held by somebody else — used to
  close the connection, which is indistinguishable from a network failure. The
  client retried forever and logged `EOF`, and the server logged the real reason
  at debug, where nobody was looking.

  The refusal now carries its reason to the client, which prints it and stops
  guessing: `the token does not match the server's` or `the server already has a
  control channel from somebody else`. The server logs it at warn rather than
  debug. This is backwards-compatible in both directions — an old client sees
  the same closed connection it always did.

  The scope is the transports where the fault was reported and where the
  handshake carries a reply the client is already reading: `tcp` and `tcpmux`
  understand both reasons end to end, and `kcp` and `quic` send the token
  refusal. The rest are unchanged for now.

- **The watchdog decided whether a tunnel was up by looking at the socket
  table.** `ss -Htn state established` is a poor witness: a socket stays
  ESTABLISHED long after the peer has gone, which is the exact condition the
  watchdog exists to catch. Engines now report whether they actually hold a
  control channel, and the watchdog believes that in preference to the socket.
  The report is a tri-state — connected, disconnected, or has not said — because
  during an upgrade an engine that has not been taught to report must not be
  read as reporting "down".

- **A tunnel that restarted all day looked like twenty unrelated events.** A
  watchdog restart is worth a line on its own, so "why did my tunnel reset
  overnight" can be answered. It is the wrong output for a tunnel doing it every
  three minutes: twenty separate lines are indistinguishable from twenty
  unrelated incidents across a week, and nobody reads that list and concludes
  the tunnel is flapping. Which matters, because flapping is how several real
  faults present — a path that drops full-sized packets, a liveness deadline set
  too tight — and all of them look from outside like a tunnel that mostly works,
  which is worse than one that is plainly down because nobody investigates.
  Repeated restarts inside an hour are now reported once, as one condition. The
  restarting itself is unchanged.

- **The fine-tune drawer would not open a second time.** Reported by two people
  independently: edit a tunnel, close the dialog, open it on another tunnel, and
  Fine Tune never opens again for the life of the page. Only a refresh brought
  it back.

  The drawer animates its height and cleans up when `transitionend` fires. Edit
  collapses its drawers while the form is still `display:none` — and an element
  with no boxes runs no transitions, so nothing ever started, nothing ever
  ended, and the cleanup that clears the height never ran. The drawer was left
  hidden *and* pinned to a height, which is the state that wedges it.

  Each animation now carries a token so a second one supersedes the first
  cleanly, a collapse on an element that is not being laid out lands on the
  final state directly instead of waiting for an event that cannot come, and
  every animation has a backstop timer behind the event.

- **The spoof transport carried no traffic on paths that watch TCP.** Several
  people tested it and none of them got bytes across. Three faults, and the
  first alone is enough:

  Every forged segment carried `PSH|ACK`, chosen to read as traffic on an
  established connection. That is precisely the wrong thing to look like: no
  handshake ever happened, so to anything on the path that tracks connection
  state every segment is out of state, and dropping out-of-state TCP is the
  first thing a stateful firewall does. The tunnel comes up, the sockets are
  healthy, and nothing crosses. Segments now carry `SYN`, which is the packet
  that starts a connection and which a stateful device has nothing to reject —
  as the spoof-tunnel reference sends. Receivers ignore the flags entirely, so
  this interoperates with a peer of any version.

  The sending socket was opened on the profile's own protocol, which meant the
  kernel queued a copy of every packet of that protocol on the machine into a
  buffer that was never read. It is now `IPPROTO_RAW`: send-only by definition.

  And the sequence number advanced by the payload length plus one, leaving a
  one-byte hole in the sequence space on every segment — visible to anything
  that follows a flow, and not what a real sender does. IP IDs were sequential
  for the same reason and are now random.

- **The direct tunnel stalled and had to be restarted by hand — on some servers
  and not others.** That pattern is the signature of a path MTU problem rather
  than a fault in the tunnel: the connection establishes, small packets pass,
  and the first full-sized segment is dropped by a link that cannot carry it and
  cannot say so. The socket stays ESTABLISHED throughout, which is why it looks
  like a hang rather than a failure.

  The TCP maximum segment size is now clamped on every direct connection — the
  edge dial, the accepted connection and the websocket path alike — so segments
  are sized to what the path takes. `mss` is exposed for the paths where the
  default is still too large.

- **A layer-3 tunnel crashed the process, and then would not stay up.** From
  the field, both on one server: `backpack` died with a memory fault inside the
  TUN read and systemd restarted it, and the tunnel that came back logged
  `reading from bp0: too many segments` and tore itself down every few seconds.
  Two symptoms, one wrong assumption.

  Turning on the kernel's segmentation offload — the batching added in v1.7.4 —
  changed what a read returns. It is no longer packets the kernel built to fit
  this interface. It is one run of up to 64 KB, split for us into the segments
  the *sending* side chose, whose size came from the sender's path and has
  nothing to do with the MTU here. The read buffers were still MTU-sized, and
  the split does not copy into them: it slices each buffer to the length of its
  segment and writes. A segment longer than the buffer is therefore not a short
  read but a write past the end of a slice, which takes the process down. The
  buffers are now sized to the largest run a read can return, which is the bound
  that makes it impossible rather than unlikely. Only the first page of each is
  ever touched, so what the process occupies barely moves.

  The second is the same assumption at the other end of the scale. When the
  kernel coalesces many small packets, one run can split into more segments than
  there are buffers, and the library says so. That is a short read — the packets
  that fit are perfectly good — but the pump took any error from the device as
  the device having failed, so a condition that costs a few packets cost the
  whole tunnel, over and over, for as long as the traffic causing it kept
  flowing. It is now handled as what it is, reported once a minute rather than
  once a read, and the tunnel stays up.

- **A layer-3 forwarder that could not bind reported success.** `Run` waited for
  its goroutines and then discarded their error if the context had ended, so a
  port it could not take produced a forwarder that returned nil and forwarded
  nothing. Bind failures are now returned and logged with the address that
  failed. This surfaced as a test that looked flaky and was not — twice this
  release, an intermittent failure turned out to be a real fault plus a test
  port allocator that checked a port was free and then raced another test to
  bind it. The allocator is now shared and issues each port once.

## v1.7.4 — 2026-08-23

Almost all of this release came from the field: tunnels that dropped every so
often and had to be restarted by hand, a certificate that would not issue, a
panel that could take journald to the top of `top`. Every fix below has a
regression test that fails without it.

### Added

- **The TUN device moves packets in batches.** A read used to be one packet and
  one syscall, and a busy tunnel does thousands a second. The device now comes
  from wireguard-go's `tun` package, which turns on the kernel's segmentation
  offload: one read can return a whole 64 KB run of a single TCP stream, already
  split into MTU-sized packets, for the cost of one syscall. The session and
  peer lookups moved out of the per-packet path with it — both take the state
  lock, and it was being taken twice per packet for values that change every two
  minutes. Everything above the device is unchanged: the addresses, the MTU, the
  queue, the queueing discipline and the MSS clamp still go through `ip`.

- **Speed Test measures a port forwarder, not just a full IP tunnel.** It used
  to answer "No full IP tunnel found on this server." and stop, which is true
  and useless: the menu offers the entry to everybody and most tunnels forward
  ports. A layer-3 tunnel is still measured across its private subnet; a port
  forwarder is now measured through one of the mappings it already carries — the
  side that exposes the ports connects to one on its own loopback, the side that
  holds the backends puts the sink where that mapping points. What travels is a
  real forwarded connection over the real transport, which is the thing being
  measured.

  The cost is stated rather than hidden: the sink has to bind the port the real
  backend uses, so that service is down for the length of the measurement. The
  receiver checks the port is free, says plainly what it is about to displace,
  and refuses to start on top of something. Mappings that cannot carry a
  measurement — a backend on another machine, one load-balanced across several —
  are listed with the reason instead of being offered and then failing.

### Fixed

- **A layer-3 tunnel never came back after its device or carrier failed.** The
  restart loop was in place and correct, and it was never reached. `Run` waits
  for its goroutines, and the handshake and MTU-probe loops watched the caller's
  context — which outlives every generation of the tunnel — so when the pumps
  stopped, those two carried on running against a carrier that had already been
  closed, holding `Run` inside its wait forever. Nothing was reopened, while the
  handshake loop logged a retry every few seconds and made it look like the
  tunnel was busy reconnecting. Only restarting the process brought it back.

  Each generation now has its own context, cancelled the moment either pump
  stops, so the whole generation ends together and the restart happens. A
  failure on one device also releases the pump blocked on the other, which used
  to wait on a read that was never going to return.

- **A `pck` tunnel dropped every half hour or so and had to be restarted by
  hand.** Each of the client's carriers must send from its own source port:
  kcp-go tells peers apart by address alone, so two carriers sharing a port
  arrive as one peer, and a packet claiming a new conversation on an existing
  entry closes the old one. The allocator was a counter taken modulo a 128-port
  span and it never came back down, so carrier 128 was handed carrier 0's port —
  and carrier 0 is the control channel. A pool dialling every sixteen seconds,
  which is what the logs from the field showed, walks the whole span in about
  thirty-four minutes. Restarting the process worked because it started the
  counter again from zero.

  Ports are now claimed and released. One that belongs to a live carrier is
  never handed out again, however long the tunnel stays up, and exhausting the
  span is an error rather than a silent reuse.

- **The startup notes told operators to match a number that does not have to
  match.** Every KCP-family tunnel printed `both ends MUST match MTU and FEC or
  the tunnel never connects` on every start, healthy or not — the only line on a
  clean startup that sounds like a fault, so people went looking for one. Half
  of it was untrue: `SetMtu` sizes only the segments this end sends and a
  receiver parses whatever arrives, so the two ends may run different values
  quite happily. What MTU has to do is fit the path. The advice therefore sent
  people to equalise a figure that was never the problem while the real fault
  survived the change they were told to make.

  The parameters now print plainly for everyone, and the advice prints only for
  the tunnels it applies to: FEC shard counts genuinely must match, and if a FEC
  tunnel will not carry traffic the first thing to try is a lower `kcp_mtu`.

- **`kcp_mtu` defaults to 1250 when FEC is on**, down from 1350. FEC is what
  makes a too-large MTU fatal rather than merely wasteful: kcp-go pads every
  shard in a group out to the largest packet in it, so the parity packets are
  always full size. A tunnel without FEC slips its small packets through a short
  path; one with FEC offers that path a steady stream of maximum-size packets
  and loses all of them. Existing configs name the key and keep whatever they
  say; the two ends need not match, so a server can be changed on its own.

- **A tunnel could not come up behind Cloudflare over WSS**, while the same
  server worked perfectly on its IP. The client wears a browser TLS fingerprint,
  and a browser offers `h2` before `http/1.1`. Our own server pins `http/1.1` and
  so never took the `h2` on offer; a CDN terminates the TLS itself and has no
  such instruction, took it, and answered the websocket upgrade with an HTTP/2
  SETTINGS frame — which the HTTP/1.1 parser reported as
  `malformed HTTP response "\x00\x00\x12\x04..."`. A websocket cannot be
  carried over HTTP/2 at all, so offering it was the fault. The offer is now
  narrowed to what the client can actually speak.

- **Let's Encrypt certificates were never obtained.** Two faults, and either
  alone is enough. `autocert` identifies the certificate to serve by the name in
  the ClientHello and refuses outright when there is none — and a tunnel's
  remote address is normally the server's IP, which a client dials without
  sending a name. Every handshake was refused before an issuance was ever
  attempted. A handshake that carries no name is now answered with the one
  domain the listener was configured for.

  And issuance was lazy: nothing was requested until a handshake asked, so a
  failure surfaced only as a broken connection on whoever happened to arrive,
  with nothing at all on the server. The certificate is now fetched at startup
  and the answer — issued, or the exact reason not — goes in the tunnel's own
  log. Nothing was wrong with Let's Encrypt.

- **The control-channel handshake was too tight for a lossy path.** On a path
  that drops a packet the wait is not a round trip but a round trip plus however
  long TCP takes to retransmit: a second, then three, then seven. The `udp`
  transport allowed two seconds for the whole exchange on both ends, so one lost
  packet failed it — the client closed, backed off and dialled again, and the
  tunnel churned without ever reporting a disconnection. Every transport now
  uses the same named budget, generous for the same reason: failing fast here
  costs a full redial, which is far more than waiting would have.

- **The Path MTU check condemned healthy tunnels.** Two mistakes. The kernel's
  `snd_mss` already has the TCP option bytes taken out of it, so testing it
  against a figure that subtracts them a second time calls an ordinary socket
  oversized. And the ICMP probe under-reports on any path that filters large
  pings, which is ordinary on the routes this project exists for. The check now
  asks the kernel for the path MTU it learned from these very connections, uses
  the probe only as a fallback, and treats a probe that contradicts sockets
  which are visibly moving traffic as the probe being wrong.

- **One end of a datagram tunnel showed offline while the other showed online**,
  with traffic flowing the whole time. The panel asked the socket table, which
  can only see carriers that leave a socket behind — plain `kcp`, `udp` and
  `quic` dial a connected UDP socket the kernel names a peer for, but `xdi`
  rides in ICMP, `pck` builds its own TCP segments through a packet socket and
  `spoof` sends from a raw one. Only the listening side wrote down what it knew.
  Both ends record their peer now, and both are asked.

- **The panel's log drawer could take `systemd-journald` and `rsyslogd` to the
  top of `top`.** It polls every two seconds while open and every poll forked
  `journalctl -n 150`. On a host with a large journal one read can take longer
  than the gap to the next, at which point the requests overlap, each overlap
  adds another journalctl, and the load climbs on its own until the tab is
  closed. A journal read is now shared: whoever asks while one is running waits
  for it, and a result stands for two seconds — so the cost is bounded however
  many panels are open. The page also refuses to overlap its own requests and
  stops asking while its tab is hidden.

- **The panel forked `systemctl` once per tunnel per poll.** "Is this unit
  running" is the question it asks most: once per tunnel every four seconds for
  the system card, again every six for the tunnel list, each one a process and a
  round trip to systemd. The answer is now shared for two seconds — well inside
  both intervals, so nothing is less live — and anything that starts, stops or
  restarts a unit clears it, so a button press is reflected at once.

- **Editing a direct tunnel in the panel opened the reverse editor.** The server
  had answered correctly all along; the page never looked at which kind it had
  been given and fell straight through into the reverse form, which reads fields
  a direct tunnel does not have. Every box came up blank, the transport picker
  offered the wrong transports, and saving posted an empty edit that restarted
  the tunnel having changed nothing. Direct tunnels now get their own editor,
  showing what can be changed and naming — read-only — the carrier, addresses
  and side that are settled when the tunnel is made.

- **Speed Test's receiver killed the whole CLI.** The screen said "Press Ctrl+C
  when the other end reports its result" and installed no handler for it, so the
  interrupt took Go's default and ended the program — menu, tunnel list and
  whatever else was in progress. It now stops the sink and returns to the menu,
  as every other screen that asks for Ctrl+C already did. A receiver that times
  out waiting for a sender says so, rather than being indistinguishable from one
  that finished.

- **The ICMP carrier allocated twice per packet**, once to frame the payload and
  once to read one — both now come from a pool, as the pck carrier's already
  did.

## v1.7.3 — 2026-08-19

### Added

- **Direct tunnelling: Iran dials out instead of waiting to be dialled.**

  A reverse tunnel needs kharej to reach a port on Iran. Where that inbound
  connection cannot be made — a provider that filters connections arriving from
  abroad, a port blocked in one direction only — the tunnel never comes up, even
  though the user-facing ports on Iran are perfectly fine. A direct tunnel turns
  it around: Iran reaches out, which is the ordinary direction and the one a
  filter is least likely to touch. The ports do not move. Iran still exposes
  them and kharej still holds the real service.

  It is a **full IP tunnel**: a network interface on each host carrying whole IP
  packets, so the two servers get a point-to-point link over which anything can
  be routed, plus the same `ports = [...]` forwarding a reverse tunnel has. The
  packets are framed as **GRE and sealed in a Noise session**, then handed to
  one of three carriers — `pck` (raw TCP segments with no socket a firewall can
  hold), `udp`, or `spoof` (a forged source address).

  This is not the kernel's GRE. Kernel GRE travels as bare IP protocol 47:
  unencrypted, unmistakable, and removed by one firewall rule. Backpack writes
  the same header — RFC 2784, with the RFC 2890 key — and then encrypts it and
  hides it inside a carrier, so there is no protocol 47 to block. The cost is
  that it talks only to Backpack.

  **Menu → Setup Iran** (or **Setup Kharej**) **→ Direct**. Linux only: it needs
  `/dev/net/tun` and root.

- **The MTU is measured, not guessed.**

  The MTU is the one setting on a layer-3 tunnel that cannot be derived from the
  configuration, and the one that fails worst when it is wrong: set it too high
  and the tunnel comes up, answers every health check, carries ping and SSH —
  and stalls every download and every TLS handshake, because the packets that
  matter are the large ones and they are dropped out on the path with nothing
  coming back to say so.

  Once a session is up, each end sends probes padded to exactly the size a full
  data packet would be and binary-searches for the largest that is acknowledged,
  then sets the interface to match. On one pair of servers the true figure was
  **1371** against a configured **1400**, and those 29 bytes were the difference
  between a tunnel that looked healthy and one that worked.

  Probes are sealed under the tunnel session, so only a peer holding the token
  can answer one. Re-measured every 30 minutes. A peer too old to answer leaves
  the configured MTU untouched. `auto_mtu = false` turns it off.

- **The TCP segment size is clamped to fit the tunnel.** The same fault from the
  other side: two endpoints agree a segment size from *their* interfaces and
  then send segments the tunnel cannot carry, and the ICMP message that would
  correct them is dropped by a great many networks. Backpack rewrites the MSS in
  the SYN of every TCP connection leaving the interface, on both chains and both
  address families, so nothing has to be discovered.

- **Performance presets for a full IP tunnel** — Balance, Turbo and Aggressive.
  They tune the queue between the kernel and the tunnel and the carrier's socket
  buffers. All three queue with `fq_codel`, which is what lets a deep queue
  absorb a burst without becoming latency: it drops when packets start waiting,
  so the sender backs off before the queue turns into jitter.

- **Speed Test** (Manage → Speed Test). Measures what a tunnel actually carries,
  end to end — encapsulation, encryption, carrier and path together. Link Test
  next door measures latency, jitter and loss and says nothing about throughput,
  and finding out used to mean `dd | nc` on both servers by hand.

- **Direct tunnels can be created and edited from the web panel.** Add Tunnel
  now asks the direction first, then the side, then the carrier. The lists it
  offers come from the server, so the panel and the CLI wizard cannot drift into
  offering different things.

- **The kharej side refuses to dial a cloud metadata service.** The origin dials
  whatever address the stream names — that is the design, and it is why changing
  the forwarded ports touches only one machine. Private addresses stay allowed,
  because forwarding to a backend on kharej's own network is a documented use.
  The metadata addresses are the exception: they answer credentials to anything
  on the instance, so a peer holding the token could have read the kharej
  server's whole cloud identity.

### Added (documentation)
- **A `tutorial/` folder: a step-by-step setup walkthrough for every transport.**
  The docs said what each setting *is*; nothing said what to type, in what order,
  to get a working tunnel. Each page now walks the wizard question by question —
  the answer to give and the reason for it — for TCP, TCP Mux, Stealth, PCK, UDP,
  KCP+FEC, QUIC, the WebSocket pair, xDi and IP Spoofing, plus
  [before you start](tutorial/before-you-start.md) (roles, token, port mapping,
  firewall), [adding UDP to a tunnel](tutorial/udp-forwarding.md) and
  [behind a panel](tutorial/behind-a-panel.md). Every page ends with a Persian
  summary, as does every page under `docs/`.
- **[docs/ip-spoofing.md](docs/ip-spoofing.md) — the spoof carrier documented
  setting by setting**, including the ones no menu asks about: which settings
  must match the peer and which are local, every fingerprint and evasion knob
  with its config key and its cost, and how to drive the two-node tester.
- **[docs/cli-menu.md](docs/cli-menu.md) rewritten as a complete reference.** It
  was a summary that had fallen behind the menu; it now covers every option in
  every menu — both setup wizards prompt by prompt, all twelve Manage entries,
  the Edit screens for each role and transport, and the whole **Fine Tune**
  block, which was documented nowhere.
- **[docs/install.md](docs/install.md)**, holding the offline and manual install
  paths the README used to carry inline.

### Changed

- **"Setup Server" and "Setup Client" are now "Setup Iran" and "Setup Kharej".**
  The old names were already a little confusing — in a reverse tunnel the Iran
  machine is the server — and with a direct tunnel they become actively wrong,
  because there the Iran machine is the one that dials. Geography is the part
  that does not change with the direction. Each entry asks reverse or direct
  first, and the rest of the wizard follows from that answer.

- **A tunnel card shows two badges instead of one.** It used to print the
  internal name — `l3/pck` — because there was a single field where there were
  always two facts: which way the tunnel was built, and what carries it. Now it
  reads **DIRECT · PCK**, and the direction badge takes the theme's accent
  colour rather than one of its own.

- **The direct wizard asks one question about how the tunnel travels.** It used
  to ask what kind of tunnel, then how to wrap the packets. Both have a single
  sensible answer now, so both are gone; the carriers are offered in the order
  worth trying them — PCK, UDP, Spoof.

- **`nodelay` is on in every direct preset.** The engine calls
  `SetNoDelay(cfg.Nodelay)`, so leaving the key unset did not leave the socket
  alone — it turned Nagle *on*, over Go's own default of off. On a tunnel where
  one mux session carries every connection, that delays every stream at once.

### Changed (documentation)
- **The WSS decoy site is a different web server on every install, and answers
  like a file server rather than a program.** The decoy existed so a probe would
  see a website instead of a tunnel, and for one server it worked. Across the
  fleet it did the opposite: every Backpack on earth returned byte-identical
  bytes — the same trimmed page, `Server: nginx`, and nothing else. No
  `Last-Modified`, no `ETag`, no `Accept-Ranges`, the same `Content-Length`
  everywhere. One internet-wide scan for that exact response enumerated every
  Backpack server there is, no token and no probing required; a camouflage
  everybody wears identically is a uniform.

  Each install now derives its own identity from **its tunnel token** — which
  real distro nginx version it claims to be (and whether that build prints its
  version at all), when its `index.html` was written, and the `ETag` computed
  from that date and the page size in nginx's own format, so the three can never
  contradict each other. nginx changed its default page and its error pages in
  the 1.23 series, so each version serves the pages that version really ships.
  The token is secret and different everywhere, so the values cannot be
  predicted from outside; it is a hash rather than a random draw, so a server
  keeps its identity across restarts, as a real file on a real disk does.

  The responses themselves are a file server's now: `/` is served with the full
  static-file header set and honours conditional and range requests — a probe
  that hands back the `ETag` gets a `304`, not another `200` — and **every other
  path gets nginx's own `404`**, the tunnel's `/channel` included. Serving the
  welcome page on every path was the older behaviour and was a tell twice over:
  no static site answers `200` for arbitrary paths, and the tunnel path was only
  unremarkable next to the equally wrong `200` that every other path got. Being
  a `404` among `404`s hides it properly. Nothing to configure, and the two ends
  do not have to agree on any of it — the client never looks at the decoy.
- **The README is an introduction again.** It had grown into a manual: install,
  offline install, quick start, the full feature list and a link farm. What is
  left is what a first-time reader needs — what it is, how it works, install,
  the five-minute quick start, a transport table pointing at the walkthroughs,
  and the highlights (the long list is still there, folded away). Everything else
  moved into `docs/` or `tutorial/`. `README_FA.md` follows the same shape.

### Fixed

Everything below is a direct-tunnel fault found while running one on real
servers between Iran and abroad. They are listed because several presented as
the same thing — "the tunnel is up and nothing works" — and each had a different
cause.

- **A tunnel whose two ends wrapped packets differently came up and carried
  nothing.** The handshake had no encapsulation in it, so a pair with `ipip` on
  one end and `gre` on the other agreed on keys, established a session, reported
  a peer, and showed green on both panels. Every data packet then decrypted
  perfectly and was discarded one layer later, because an IPIP sender's payload
  is an IP packet and a GRE receiver reads its first four bytes as a GRE header.
  Both interfaces showed zero packets received. Nothing logged above debug.

  The encapsulation now travels in the handshake, encrypted and authenticated
  with everything else, and a mismatch is refused **by name on both ends** —
  including a GRE key mismatch, which fails in exactly the same silent way. An
  older peer that announces nothing is not judged, so an upgrade cannot take a
  working pair down.

- **The panel showed the Iran side offline while the tunnel was carrying
  traffic.** The dialling side runs a liveness probe the listening side does
  not, and the probe was a TCP connect — chosen because the transport was not
  recognised as a datagram one. `l3/pck` never matched the bare names the check
  compared against, so the panel dialled a carrier that has no socket to dial by
  design, read the inevitable refusal as the tunnel being down, and overrode a
  perfectly good "online". The kharej card stayed green because only the
  dialling side runs that probe.

- **The listening side reported no peer until traffic happened to cross it.** A
  session is promoted on the first authenticated *data* packet, which is right
  for deciding which keys to seal with and wrong for deciding what the screens
  say. An idle tunnel therefore read online on one machine and offline on the
  other. A completed handshake now publishes the peer.

- **A session was never retired.** `rejectAfterTime` was documented as the point
  a session stops being usable and was only ever applied to the replaced and
  pending ones, so a tunnel whose peer had gone away held its last session for
  as long as the process lived — and with it the peer in the metrics file, so
  the panel showed "peer connected" for a tunnel that had been down for hours.

- **Health Check told you to restore from a backup.** Its per-tunnel checks went
  through the reverse config loader, which reads `[server]` and `[client]` and
  refuses anything else by design. A healthy layer-3 tunnel came back as
  *"Config unreadable: not a client tunnel"* with *"restore from a backup"* as
  the suggested fix. It now checks what these tunnels actually have.

- **Edit did nothing for a direct tunnel in the web panel**, for the same
  reason, which is why the only way to build one was the CLI.

- **The panel reported no traffic for direct tunnels.** The reverse transports
  count inside their copy loops; these engines had no copy loop to inherit it
  from, so the card showed nothing for a tunnel moving real data.

- **A rekey was logged as a new connection.** The dialling side rekeys every two
  minutes by design, and every one printed "session ... established" — which
  read, quite reasonably, as a tunnel dropping and redialling every two minutes,
  and was reported as exactly that.

- **The MSS clamp was never installed.** The whole command was kept in one list
  and the verb was written over index 2 — which held the chain — so every
  invocation came out as `iptables -t mangle -A -o bp0 …` with no chain at all.
  iptables refused all of them and the refusal was logged at debug level, so the
  clamp was absent from every machine it was meant to protect while the code
  that built it passed its tests: they inspected the rule description, never the
  command line. The same shape was in the kernel-GRE rules.

- **Firewall rules accumulated.** A rule added on every start and removed only
  on a clean stop is a rule that piles up; one machine in the field had 546 of
  them. Every rule Backpack installs is now deleted until the delete fails
  before a new one is added, so a process that was killed rather than stopped
  leaves nothing for the next start to add to.

- **A token containing a backslash produced a config that would not parse.** The
  direct renderer wrapped strings in quotes by hand; a backslash is an escape
  character in a TOML basic string exactly as it is in a Go one. The failure was
  either a file that did not load or, worse, one that loaded a *different* token
  — and a mismatched token is answered with silence by design, so it presents as
  a blocked port.

- **Editing a direct tunnel silently deleted settings it never showed.** The
  editor re-renders the file from a spec, so every key the spec could not hold
  disappeared on an edit that had nothing to do with it: changing the MTU
  reverted a whole spoof carrier to its defaults. The carrier tables are now
  carried whole.

- **Pressing 0 to leave the layer-3 editor changed a setting.** The kharej side
  is offered fewer entries, so its indices were shifted up to compensate — and
  the shift was applied to the "go back" answer too, which landed on the UDP
  toggle and restarted the tunnel.

- **UDP flows were not counted against the connection cap.** A flow is
  recognised by its source address and UDP source addresses cost nothing to
  invent, so the one protocol where the cap matters most was the one outside it.

- **A goroutine leaked per session on the kharej side**, each holding a dead mux
  session. Nothing on a steady tunnel; a month of a flapping link is a leak with
  no symptom until the process is large.

- **CPU climbed with the packet rate for no reason.** Three allocations per
  packet: the raw connection and the destination address were rebuilt on every
  send although neither changes for the life of the socket, and the receive path
  compared the peer's address by formatting both sides into strings. At a few
  thousand packets a second that is the garbage collector doing the work rather
  than the network.

- **A tunnel with clamping turned off and no logger panicked.** The nil-logger
  guard sat below the early return, so the one combination that skipped it was
  the one that dereferenced it. Caught by CI on Linux; the non-Linux build of
  that function does nothing, so it passed locally.

- **A typo in `role` built the wrong side of the tunnel.** Both constructors set
  the role themselves before validating, so the engine's own "unknown role"
  branch could never be reached and `role = "kharje"` fell through to the Iran
  side.

- **Two layer-3 tunnels on one host collided.** The wizard offered every tunnel
  the same interface name and the same `10.10.0.1/30` — on the same screen that
  suggested running more than one between the same two servers.

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
