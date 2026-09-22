# Security policy

## Reporting a vulnerability

Email **600o60o6@gmail.com** with `BACKPACK SECURITY` in the subject.

Please do not open a public issue first. Backpack is used to get past national
filtering; a vulnerability published before there is a release to move to is a
vulnerability handed to the people the tool exists to get past, and the people
it costs are not the maintainers.

Tell us:

- what you found, and what an attacker gets from it;
- how to reproduce it — a config, a packet capture, a patch, whatever you have;
- which version (`backpack version`), and which transport or carrier.

You will get an acknowledgement within **72 hours**. If you do not, assume the
mail was lost and try again.

## What happens next

1. We confirm it and tell you whether we agree on the severity.
2. We fix it, with a regression test, and tell you when a release is ready.
3. The release notes say what was wrong and what it let somebody do. They do
   not include a working exploit.
4. You are credited by whatever name you choose, or not at all if you prefer.

There is no bounty. This is a project one person maintains.

## Supported versions

The current release, and only the current release. There are no long-term
support branches: the update path is built into the product, it verifies a
signature and a checksum, and it rolls itself back if the tunnels do not come
back up. Staying current is the supported configuration.

## What is in scope

- The tunnel protocols: the reverse transports, the layer-3 carriers, the
  handshakes, the replay windows, the nonce handling.
- The web panel, its authentication, its API tokens and its audit record.
- The updater and the release signing.
- The Telegram bot and its permission model.
- The managed-server fleet: the SSH runner, the sealed credentials, the
  restricted RPC a node accepts.
- The installer.

## What is out of scope

- **Traffic analysis that identifies Backpack as Backpack.** The obfuscating
  carriers raise the cost of classification; none of them claims to be
  indistinguishable from the traffic it imitates, and an adversary with a full
  view of both ends of a flow can correlate it. This is a property of the
  problem, not a bug, and `docs/camouflage.md` says so.
- **Denial of service by flooding a public port.** A tunnel binds a port and
  anything can send to it. The handshake is cheap to refuse and the replay
  window is bounded, which is as far as this goes.
- **Anything requiring root on a machine already running Backpack.** The engine
  runs as root by design — it writes systemd units, tunes sysctls and opens raw
  sockets.
- **Social engineering, physical access, and vulnerabilities in dependencies
  that this code does not reach.** `govulncheck` runs in CI and reports only
  reachable paths; a CVE it does not flag is one nothing here calls.

## Things already known and written down

These are in the repository rather than in a report, and they are open on
purpose:

- **Layer-3 handshake freshness.** NNpsk0's init payload carries a session
  identifier and no timestamp, so a recorded handshake is replayable until it
  falls out of the responder's memory. The half that needed no wire change is
  shipped — a responder refuses an identifier it has already answered — and the
  timestamp that closes it properly is written, tested, and gated behind the
  next protocol version, because an old responder compares the initiator's
  payload whole and would refuse every new dialler in the field. See
  `internal/tunnel/l3/initreplay.go`.
- **Release signing is only as good as the key.** `internal/app.ReleasePublicKey`
  is pinned in the binary. If the private half is lost, every updater refuses
  every update until a build carrying a new key is installed by hand.

## Cryptography

Backpack uses the Noise protocol framework (NNpsk0) with ChaCha20-Poly1305,
explicit nonces, and an RFC 4303 replay window. It does not invent primitives.

The implementation has not had a third-party review. Two people have read it
and both of them want it to be right, which is not the same thing — if you are
qualified to review it and willing to, that is the single most useful thing
anyone could contribute.
