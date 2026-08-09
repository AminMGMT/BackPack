# Direct connection mode

Backpack supports two directions for its application tunnel. Both directions
use the transport selected by the user (TCP, TCP Mux, Stealth, UDP, KCP, QUIC,
WS, WSS, WS Mux or WSS Mux):

- **Direct:** the Iran server initiates the selected tunnel toward Kharej. The
  public user ports remain on Iran and the backend service remains on Kharej.
- **Reverse:** the legacy behaviour; Kharej initiates the selected tunnel
  toward Iran.

Run `sudo backpack`, choose **Setup Server** on Iran or **Setup Client** on
Kharej, select the transport family and concrete transport, then select the
connection mode. The mode question appears after every concrete transport and
before the port questions. Both sides must use the same mode, transport,
tunnel port and token.

Direct application configs use `engine = "forward"`. Iran is operationally the
dialling `[client]` and owns `ports`; Kharej is operationally the listening
`[server]`. The CLI hides that implementation detail and continues to call the
machines Server (Iran) and Client (Kharej).

```toml
# Iran
engine = "forward"

[client]
remote_addr = "KHAREJ_IP:8443"
transport = "tcpmux"
token = "SAME_LONG_TOKEN"
ports = ["443=127.0.0.1:443"]
```

```toml
# Kharej
engine = "forward"

[server]
bind_addr = "0.0.0.0:8443"
transport = "tcpmux"
token = "SAME_LONG_TOKEN"
```

A bare mapping such as `443` exposes port 443 on Iran and connects it to
`127.0.0.1:443` on Kharej. Ranges preserve offsets, for example
`10000-10009=127.0.0.1:20000-20009`. IPv6 endpoints must use brackets. A pipe
separates multiple backends and Direct load-balances over available members:
`443=127.0.0.1:8443|127.0.0.1:9443`. An instance may expand at most 4096
ingress ports, with at most 1024 ports from any one mapping.

Legacy configs without `engine` remain Reverse and require no migration.
`mode` is display metadata and is never written to TOML.

## Advanced iptables engine

The separate `engine = "iptables"` provider remains available for operators
who intentionally want kernel DNAT/MASQUERADE without an application tunnel.
It is not the Direct/Reverse choice in the normal setup wizard and must be
configured explicitly with `[forward]`. It supports IPv4/IPv6 TCP/UDP mappings,
owned generation chains, rollback, conflict detection and persistent counters.
See the example config and engine tests under `internal/engine` for that
advanced deployment path.
