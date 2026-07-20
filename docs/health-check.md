# Health Check

**Manage → Health Check** runs a full check of the server and every tunnel, and
prints a concrete fix under each problem it finds. It verifies:

- kernel tuning is applied,
- the web panel is running,
- every tunnel's state,
- real **TCP reachability** (not just whether systemd is happy),
- TLS certificate expiry, and
- token strength.

It also reports whether the [monitor service](monitor-service.md) is running,
and says plainly that dropped tunnels will **not** be restarted if it is down.

## File Locations

Right next to it, **Manage → File Locations** lists where every config, service
and backup lives on the machine. See [Server layout](server-layout.md) for the
full map.

---
[← Back to the main README](../README.md)
