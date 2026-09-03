# Managed servers (nodes)

A tunnel has two ends, and every value that matters has to agree on both: the
token, the port, the transport, the MTU, the forged source address. Setting one
up used to mean configuring Iran, then opening a second terminal, logging into
the foreign server as root, and doing it again by hand.

A **node** removes the second pass. The foreign server registers itself with the
panel, once, and after that the panel writes both ends.

> Available in the **new panel only** (Settings → Interface → experimental
> panel). The classic dashboard does not have this screen.

---

## What it is not

It is not remote root access, and the difference is the whole design.

The panel is never given a credential for the machine. You run one command on
your own server, as root, yourself; that installs a service which runs locally
with the privilege it needs. What travels over the wire afterwards is not a
login — it is a request to perform one of a fixed set of operations, and the
node refuses anything else.

| | SSH from the panel | A node |
|---|---|---|
| What the panel holds | an **identity** — root on that box | an **authorisation** for four operations |
| What it can do there | anything | create, update, restart, or report on a tunnel |
| Where root lives | handed to the panel | stays local to the server |
| If the panel is compromised | the server is fully owned | tunnels can be misconfigured |

There is no operation that runs a command, reads a file, installs software, or
deletes a tunnel. Adding one would turn every property in that table into its
opposite, which is why the list lives in one place — `internal/node/ops.go` —
and is enforced by the node rather than by the panel's good manners.

## Which way the connection goes

The node dials the panel, never the other way round.

- The foreign server **opens no inbound port**. Its panel is not on the
  internet, and there is nothing on it to find.
- It works from behind NAT, on a provider that firewalls inbound by default,
  and on a host with no stable address.
- The panel is the machine that already accepts connections.

The cost is one open port **on the panel**, which you choose when you turn the
feature on.

The channel is a Noise `NNpsk0` session — the same handshake the stealth
transport uses, two short bursts of bytes with nothing for deep packet
inspection to match on — carrying a few kilobytes a day. It is **not** the
tunnel: a tunnel that is down does not take away the ability to fix it.

## Setting one up

**On the panel (Iran)** — menu → **Servers**:

1. Turn **Accept servers** on and choose a port. Open that port in the
   firewall; it is where foreign servers reach this panel.
2. Type a name for the server and press **Get the command**.
3. Copy the line it gives you.

**On the foreign server**, as root — one line, on a machine with nothing on it:

```
bash <(curl -fsSL https://raw.githubusercontent.com/AminMGMT/BackPack/main/install.sh) \
     node --panel <panel-ip>:<port> --key <setup-key>
```

That installs Backpack and enrols the server in the same step. There is no
"first install it, then run this": a second line is the one that gets pasted on
the wrong server, or before the first has finished, or never — and the symptom
of the last is a server that simply never appears in the panel.

If the server **already has Backpack**, the panel offers the short form behind
*use the short line*:

```
backpack node setup --panel <panel-ip>:<port> --key <setup-key>
```

Either way the panel shows the server as connected within a few seconds.
Nothing else is done on that machine, ever.

The installer checks `--panel` and `--key` **before** it downloads anything, so
a mistyped key is a message straight away rather than a failure at the end of a
build.

### About the key

The line carries a **single-use** key. It stops working the moment that server
uses it, and expires after a day. Until then it is a secret — send it the way
you would send a password. If it goes somewhere it should not, remove the
pending entry in the panel and generate a new one.

Behind that one string are two keys with different jobs:

- The **hub key** is per-panel and shared. It is the Noise pre-shared key, so it
  buys confidentiality and a channel a censor cannot pick out of a link. It
  authorises nothing: someone holding only this completes a handshake and then
  fails authentication.
- The **node key** is per-server, issued at enrolment, and never travels in the
  clear. It is what identifies a server and what **Remove** revokes.

## Building a tunnel on both ends

Once a server is connected, **Add tunnel** grows a field: *The other server*.
It sits at the top of the settings step and applies to **both kinds of tunnel**,
reverse and direct.

Pick a managed server and the form works exactly as it always has — every
transport, every carrier, every drawer — but on **Create** the panel:

1. builds this end here,
2. reads that finished configuration back and mirrors it, the same way the
   setup link does,
3. and applies the mirror on the node.

The far end is derived from this end's finished config rather than from the
form, so the two cannot be built from different opinions of what the form meant.

Leave the field on *I will set the other server up myself* and nothing changes:
you get the setup link to paste, exactly as before.

### If the far end does not take

The panel says so, plainly, and does not pretend the whole thing failed: this
end is real and running, and the tunnel's setup link is still there to paste on
the other server by hand.

### Editing later

Applying is one operation, not two. The panel sends the complete state a tunnel
should be in and the node puts the machine in that state — so the same path that
creates a tunnel is the one that changes its MTU or its transport, and the
config file on the far server is rewritten.

A change that will not start is **rolled back on the node**: it writes the new
config, restarts, waits, and puts the old file back if the tunnel does not come
up. The configuration it replaced is filed in that server's own history. This is
the same protection an edit made locally has always had, and it matters far more
here — a bad push to a machine on another continent is the failure with no
recovery path.

If the node is offline when you save, the panel says so and names it. This end
has changed and the other has not, which is a state worth knowing about rather
than hiding: the two ends now disagree, and saving again once the server is back
is what fixes it.

### Editing from the CLI menu

It does not carry across, and the menu warns you before you start.

The channel to a node belongs to the running web panel. The CLI is a separate
process with no connection to any node, so a change made there can only touch
this end — and both ends would then report themselves as running while no
traffic passed. Edit a paired tunnel from the panel.

### Starting, stopping and restarting

The buttons on a tunnel's card reach both ends. A tunnel across a managed server
is one tunnel in two places and its state is one state — an end stopped on its
own is not a stopped tunnel, it is a tunnel with one half dialling something
that will never answer, retrying on its timer until somebody notices.

If the far end could not be reached, the panel says which end actually moved
rather than reporting a clean stop over a server still trying to connect.

### Deleting

Deleting a tunnel here removes it **here**. The other end keeps running: there
is deliberately no operation that removes a tunnel on a node, and a delete on
this machine is not consent to one on another. The panel says which server still
has it; remove it there yourself.

## Speed testing

A throughput measurement pushes bytes at a sink on the other server, and that
sink used to be a person's job: the panel's own error told the operator to go
and start a receiver from a CLI menu on a machine they were not sitting at.

On a managed server the panel asks the node to start it. Press GO and it
measures. The sink opens on the tunnel's own backend port, discards everything
that arrives, and closes itself after the run whether or not anything connected
— nobody has to remember to stop it, which is the other half of nobody having to
remember to start it.

**No new ports are opened for it.** The measurement is sent into the tunnel's
own forwarded port, which is already listening, and the sink on the other server
takes that mapping's **backend port** — the one the real service normally holds.
So nothing is exposed that was not exposed already, and the only cost is that
the service behind that port is unavailable for the ten seconds it runs. The
sink gives the port back afterwards.

If the real backend is still running there, the sink cannot take the port. The
measurement still happens — the bytes cross the tunnel and that service reads
them — but the result is then partly its appetite rather than the tunnel's
capacity, and the panel says so beside the number rather than presenting it as
a clean reading.

A tunnel whose far end is not a managed server behaves exactly as it did.

## Removing a server

**Remove** revokes the node's key immediately. It does **not** stop the tunnels:
those are systemd services on that machine, with configs on its disk, and they
have nothing to do with the channel that was used to write them. Unmanaging a
server must not be a way to take its traffic down. The confirmation names what
this panel built there, so you know what you are walking away from.

What is forgotten is the pairing — this panel's record of which tunnels have
their other end on that server. Those tunnels can still be edited here; the
change simply stops travelling, exactly as it does for a tunnel that was never
paired.

On the server itself, `backpack node remove` stops and uninstalls the agent —
again leaving the tunnels running.

## On the managed server

```
backpack node status     # which panel manages this server, and whether it is connected
backpack node remove     # stop being managed; tunnels keep running
```

The agent runs as `backpack-node.service` and retries on its own, backing off to
a minute between attempts, so a panel that is restarting or briefly unreachable
needs nothing done on the far side.

```
journalctl -u backpack-node.service -f
```

## What to think about before turning it on

The panel becomes the machine that can configure every server registered to it —
and the panel is usually the more exposed one, since it is the end users
connect to. That trade is deliberate and bounded by the operation list above,
but it is a real trade:

- Keep the panel's password and its 2FA in good order; they now protect more
  than one machine.
- Give the node port a firewall rule rather than leaving it open to everything.
- Remove servers you no longer manage, rather than leaving live credentials
  outstanding.

## See also

- [The web panel](web-panel.md)
- [Direct tunnel](l3-direct-tunnel.md)
- [Transports](transports.md)
