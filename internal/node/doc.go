// Package node lets one panel configure tunnels on servers it does not have a
// login for.
//
// The problem it solves is the setup itself. A tunnel has two ends and every
// field that matters has to agree on both — the token, the port, the transport,
// the MTU, the forged source address. Until now the only way to arrange that was
// to configure Iran, then open a second terminal, SSH into the foreign server as
// root, and do it again by hand. Half the support traffic this project generates
// is one end disagreeing with the other because a value was mistyped on the
// second pass.
//
// So the foreign server registers itself with the panel instead, and the panel
// writes both ends.
//
// # Which way the connection goes
//
// The node dials the panel, never the other way round. That ordering is not
// arbitrary:
//
//   - The foreign server opens no inbound port. Its panel is not on the
//     internet, and there is nothing on it to find or brute-force.
//   - It works from behind NAT, on a provider that firewalls inbound by
//     default, and on a host that has no stable address.
//   - The panel is the machine that already accepts connections, because that
//     is what a panel is.
//
// The cost is that the panel needs one open port for nodes, which is documented
// where the operator turns the feature on.
//
// # What the panel is trusted with
//
// This is the part worth being precise about, because "manage a server from
// another server" describes both this and a backdoor.
//
// The panel is never given a credential for the machine. The operator runs the
// node command themselves, once, as root, on their own server; that installs a
// service which runs locally with the privilege it needs to write a config and
// talk to systemd. What travels over the wire afterwards is not a login — it is
// a request to perform one of a fixed set of operations, listed in ops.go and
// enforced by the node, not by the panel's good manners.
//
// The distinction is the whole design. With an SSH key the panel holds an
// identity and can do anything the identity can do, so a compromised panel is a
// compromised fleet. Here the panel holds an authorisation for four verbs. A
// compromised panel can misconfigure tunnels, which is bad and recoverable; it
// cannot read files, install software, or run a command of its choosing,
// because no message exists that would ask a node to do any of those things.
//
// Nothing here can be repaired by adding an "exec" operation later. That one
// addition would convert every property described above into its opposite.
//
// # Two layers of key, and why one is not enough
//
// Noise NNpsk0 needs the pre-shared key before the handshake starts, and the
// panel cannot know which node is dialling until the handshake has finished.
// One shared key for everyone would make revoking a single node impossible;
// sending the node's name in the clear first would hand an observer the shape
// of the fleet.
//
// So there are two:
//
//   - The hub key is per-panel and stable, and every node has it. It is the
//     Noise PSK, so it buys confidentiality and — because a Noise handshake is
//     two bursts of bytes with no structure to match on — a control channel
//     that deep packet inspection cannot pick out of a link. It authorises
//     nothing.
//   - The node key is per-node and is presented inside the encrypted channel,
//     where the panel can look it up. It is what identifies a node and what
//     gets revoked.
//
// A leaked hub key therefore lets someone complete a handshake and then fail
// authentication, which is the correct outcome. Enrolment tokens are the third
// kind: single-use, exchanged for a node key on first contact, and burned.
//
// # Applying a configuration is one verb
//
// Create and edit are the same operation here. The panel sends the complete
// desired state of a tunnel and the node reconciles: write it, restart, and
// keep what was there before. It is not two verbs because the second one would
// have to answer "what if it does not exist yet" and the first "what if it
// does", and both answers are the same code.
//
// The rollback that makes this safe over a network already existed for the
// local case — applySpec writes, restarts, waits, and puts the old file back if
// the tunnel does not come up — and matters far more here. A bad push to a
// machine on another continent that leaves it with a config that will not start
// is exactly the failure that has no recovery path, except that the node
// channel does not run over the tunnel, so a tunnel that is down does not take
// the ability to fix it away with it.
package node
