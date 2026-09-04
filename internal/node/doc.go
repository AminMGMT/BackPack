// Package node lets one panel configure tunnels on servers it manages.
//
// The problem it solves is the setup itself. A tunnel has two ends and every
// field that matters has to agree on both — the token, the port, the transport,
// the MTU, the forged source address. Doing that by hand means configuring
// Iran, then opening a second terminal, logging into the far server as root,
// and doing it again. Half the support traffic this project generates is one end
// disagreeing with the other because a value was mistyped on the second pass.
//
// So the panel writes both ends.
//
// # How it reaches the far server
//
// Over that server's own SSH, with the address and root login the operator
// gives it. The panel dials out; nothing listens for this on either side beyond
// the sshd that was already running.
//
// One command runs there — Execute, reached through `backpack node exec` — and
// it performs a single operation from the list in ops.go and refuses anything
// else. There is no state on the far machine that belongs to being managed: no
// service, no config, nothing to clean up when it leaves the fleet.
//
// # What this costs, said plainly
//
// The panel holds root on every server in the fleet. Anyone who takes the panel
// takes the fleet with it.
//
// That is a real cost and it was not always paid. The design this replaced had
// the far server run an agent that dialled the panel and accepted a fixed list
// of operations, so the panel held an authorisation rather than an identity: a
// compromised panel could misconfigure tunnels, which is bad and recoverable,
// but could not read a file or run a command of its choosing.
//
// It was the better shape and it was worse in practice, for reasons that had
// nothing to do with security:
//
//   - Every server needed its own inbound port on the panel, opened in the
//     firewall and not colliding with anything.
//   - Setting one up meant pasting a command on that machine, so the operator
//     left the panel for a terminal on a server they might only have a password
//     for.
//   - The agent was a third service to install, keep running and debug — and
//     when it was not running, the panel just said the server was offline.
//
// Each of those was a way for a server to be listed and unreachable anyway, and
// between them they accounted for most of what went wrong with the feature.
// SSH is already there, already authenticated, already how the machine is
// administered.
//
// The operations list still exists and is still enforced by Execute, but it is
// no longer a security boundary — anything holding the panel's credentials can
// open a shell. It is kept because it is the right shape for the protocol: one
// request, one answer, and a refusal for anything the far side does not do.
//
// # The host key
//
// Trust on first use. The first connection records the SHA-256 of the key the
// server presents and every one after that must match, which is the same
// bargain as typing "yes" at ssh's own prompt. Accepting any key instead would
// mean anything that can answer on that address gets a root shell, and the
// panel would never say a word about it.
//
// A key that changes is reported, not accepted: either the server was rebuilt,
// or something is answering in its place, and those are not for a panel to
// decide between.
//
// # Applying a configuration is one verb
//
// Create and edit are the same operation. The panel sends the complete desired
// state of a tunnel and the far server reconciles: write it, restart, and keep
// what was there before. It is not two verbs because the second would have to
// answer "what if it does not exist yet" and the first "what if it does", and
// both answers are the same code.
//
// The rollback that makes this safe over a network already existed for the
// local case — applySpec writes, restarts, waits, and puts the old file back if
// the tunnel does not come up — and matters far more here. A bad push to a
// machine on another continent that leaves it unable to start is the failure
// with no recovery path, except that this channel does not run over the tunnel:
// a tunnel that is down does not take away the ability to fix it.
package node
