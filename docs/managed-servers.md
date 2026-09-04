# Managed servers

A tunnel has two ends, and every value that matters has to agree on both: the
token, the port, the transport, the MTU, the forged source address. Setting one
up used to mean configuring Iran, then opening a second terminal, logging into
the far server as root, and doing it again by hand. Half the support traffic
this project generates is one end disagreeing with the other because a value was
mistyped on the second pass.

A **managed server** removes the second pass. You give the panel the server's
address and its root login, and the panel writes both ends.

> Available in the **new panel only**. The classic dashboard does not have this
> screen.

---

## What it is

The panel logs into the far server over **SSH** — the same way you would — and
runs one command there.

That is a plain statement of what you are agreeing to, and it is worth reading
twice: **the panel holds root on that machine.** It can do anything root can do
there, and anyone who takes the panel takes the server with it.

This replaced an earlier design that avoided exactly that. In it, the far server
ran an agent that dialled the panel and accepted a fixed list of operations, so
the panel held an authorisation rather than an identity. It was the better shape
on paper and it was worse in practice, for reasons that were not about security
at all:

- Every server needed **its own inbound port on the panel**, opened in the
  firewall, remembered, and not colliding with anything.
- Setting one up meant pasting a command on that machine — so you left the
  panel, found a terminal for a server you might only have a password for, and
  came back.
- The agent was a **third service to install, keep running and debug**, and when
  it was not running the panel simply said the server was offline.

Each of those was a way for a server to be listed in the panel and unreachable
anyway, and between them they accounted for most of what went wrong with the
feature. SSH is already running on that machine, already authenticated, and
already how it is administered. Nothing to install, no port to open, nothing to
paste.

If you are not comfortable giving the panel root on a server, do not add it —
build its tunnels from the CLI on each machine instead. The panel still shows
their cards and their logs from this side.

## Which way the connection goes

The panel dials out. The far server opens nothing for Backpack, and the panel
opens nothing for the fleet — there is no listener on either side beyond the
sshd that was already there.

That also means a server behind NAT with no inbound route cannot be managed,
which the old design allowed. In exchange, a panel with no public address can
manage servers perfectly well, which it could not.

## Setting one up

**Servers → Add a server.** Four things, which is everything SSH needs:

| | |
|---|---|
| **Address** | the server's IP or hostname |
| **SSH port** | usually 22 |
| **Username** | usually root |
| **Password** | that user's password |

Plus a **name** for it — what the panel and its tunnels will call it, fixed once
it is added.

The panel reaches the server while you wait, so a wrong password is a message
now rather than a server that sits in the fleet doing nothing. A server that
does not answer is not saved: an entry that has never worked is not a server, it
is a typo.

If Backpack is not installed there, the panel installs it — that is the ordinary
case for a server you have just bought, not a failure. It fetches the same
installer you would run by hand, from the same place, so the archive and its
checksum still arrive from one origin.

### The host key

The first time a server answers, the panel records the SHA-256 of the host key
it presented, and every connection after that must match — trust on first use,
the same bargain as typing `yes` at ssh's own prompt.

If it ever changes, the panel refuses to connect and says so. Either that server
was rebuilt, or something is answering in its place. Remove it from the fleet
and add it again if the change was expected.

Changing a server's address clears the key with it: a different machine is
entitled to a different one.

### Where the password is kept

In `/etc/backpack/nodes.json`, on the panel's own server, `0600` and owned by
root — the same file and the same permissions as every other secret Backpack
holds. It is never sent to the browser.

## Building a tunnel on both ends

**Tunnels → Add tunnel.** Pick the managed server that holds the other end, fill
the form once, and the panel writes this end here and mirrors it there.

There is no second form and nothing to carry anywhere. Without a managed server
the screen says so and points at the fleet, because with nothing to write the
far end on it could only build half a tunnel.

The far server's address is not asked for. It reported what it is when the panel
first reached it, and a value it reports is better than one typed: it cannot be
mistyped and it does not go stale.

### If the far end does not take

The reply says `partial`, names the server, and says what failed there. This end
is real and running; the other is not. Fix what it says and edit the tunnel —
the edit pushes the far end again.

### Editing later

An edit here is an edit on both. What the far end answered for itself — its own
connection tuning — is read back and carried across, so a rebuild does not drop
settings you gave when the tunnel was paired.

### Editing from the CLI menu

The menu edits this machine only. It says so when the tunnel has a far end,
because an edit that silently changes one side is how the two come to disagree.

### Starting, stopping and restarting

All three reach both ends. If the far server cannot be reached, this end still
does what you asked and the reply says the other did not.

### Deleting

Deleting removes this end. The far end keeps running: a delete on this machine
is not consent to one on another, and there is deliberately no operation that
removes a tunnel on a managed server.

## Logs from both ends

A tunnel is one thing in two places and its log is not. The tunnel's **Logs**
screen has a switch — this server, or the one holding the other end — so a
client that cannot dial, a certificate it could not read or a port already held
over there says so without logging into that machine.

## Speed testing

The measurement needs something at the far end to sink the bytes. When the
tunnel was built across a managed server, the panel starts that itself for the
length of the test.

A refused connection on the *local* port is not the far end's fault and is no
longer reported as one: it means nothing is listening here, usually because the
tunnel is stopped.

## Keeping them up to date

Each card has **Upgrade**, and when a release lands the fleet page says which
servers are behind it with one button for all of them. It is the same installer:
it replaces the binary and restarts what was running.

## Removing a server

**Remove** on its card. The tunnels built there keep running on both machines —
what goes is this panel's ability to reach one end of them, and its record that
the two were a pair.

## On the managed server

Nothing. There is no Backpack service, no config and no state that belongs to
being managed: the panel logs in, runs one command, and logs out. Removing the
server from the fleet leaves nothing behind to clean up.

The one command is `backpack node exec`, which performs a single operation from
a fixed list — create or update a tunnel, start, stop, restart, report, read a
log, sink a speed test — and refuses anything else. It is not meant to be typed.

## What to think about before turning it on

- The panel holds a root login for every server in the fleet. Its own password
  and its own exposure now matter as much as theirs.
- A server behind NAT with no inbound route cannot be managed.
- The far server's sshd must accept password authentication for the user you
  give.

## See also

- [The web panel](web-panel.md)
- [Choosing a transport](choosing-a-transport.md)
- [Tunnel metrics](tunnel-metrics.md)
