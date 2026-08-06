# Direct forwarding (iptables)

Backpack can forward TCP and UDP traffic directly through the Linux kernel in
addition to its existing reverse-tunnel mode. Each direct-forward config is a
long-running systemd instance: it installs and watches its own rules, records
traffic counters, and removes only its owned rules when stopped.

Create one from **Setup Direct Forward** in the CLI, or use this TOML form:

```toml
engine = "iptables"

[forward]

[[forward.mappings]]
listen_address = "0.0.0.0"
listen_ports = "443"
target_address = "203.0.113.10"
target_ports = "8443"
protocols = ["tcp", "udp"]
```

`0.0.0.0` listens on every local IPv4 address and `::` does the same for IPv6.
A specific listen address must exist on a local interface when the instance
starts. The target must be an explicit, same-family unicast IP address; host
names, loopback, multicast, unspecified addresses and IPv4/IPv6 translation
are intentionally rejected.

Ports can be single values or equal-length ranges. Range mapping preserves the
offset, for example `10000-10009` to `20000-20009`. A mapping is limited to
1,024 expanded ports and an instance to 4,096. Only `tcp` and `udp` are valid.

## Behaviour and requirements

- Linux and root privileges are required. The installer provides the iptables
  suite. Both `iptables-nft` and `iptables-legacy` are supported, but the
  command, save and restore tools must use the same backend.
- IPv4-only configs do not require IPv6 tools. IPv6 tools are checked only
  when an IPv6 mapping is used.
- Backpack enables `net.ipv4.ip_forward` and, when needed, IPv6 forwarding on
  every start. Stop and uninstall do not turn these shared host settings off.
- Direct forwarding applies only to ingress traffic. Backpack creates no
  general `OUTPUT` rule, so locally generated host traffic is not redirected.
- DNAT connections receive an instance-specific connection mark. FORWARD and
  MASQUERADE rules require that mark and the exact configured tuple.
- Rule changes are prepared in detached chains. Hooks are activated last and
  failures roll back the new generation. Rules carry a structured ownership
  comment, so cleanup never flushes a table or deletes an unrelated rule.

Backpack refuses to start if a mapping overlaps another Backpack config, a
local TCP/UDP listener, or an existing DNAT rule. If an nftables, multiport or
ipset expression may overlap but cannot be interpreted safely, validation
fails closed and reports the rule that needs review.

## Health and counters

Direct health represents local desired state: service, forwarding sysctls,
backend, chains, hooks and the desired rule hash. Target reachability does not
make the service unhealthy. **Diagnose** reports routes and an optional TCP
probe separately; UDP reachability remains unknown because a connected UDP
socket is not proof of a reachable service.

RX/TX packets and bytes are counted by dedicated FORWARD accounting rules.
Cumulative values are persisted under `/etc/backpack/forward-state` before a
generation is removed, so restart, reconcile and reboot do not reduce the
Prometheus totals.

Legacy configs with no `engine` field remain reverse tunnels and need no
migration. `mode` is display-only metadata and must not be written to TOML.

## Integration test

On a disposable Linux CI runner with root and network namespaces enabled, run:

```bash
sudo BACKPACK_NETNS_TEST=1 go test ./internal/engine -run TestDirectNetNSAcceptance -v
```

The test builds isolated client, ingress and target namespaces and covers IPv4
and IPv6, TCP and UDP, single-port and range remapping, MASQUERADE source
visibility, stop cleanup and restart recovery. It is skipped during ordinary
unprivileged unit-test runs.
