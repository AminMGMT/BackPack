# Tunnel Metrics

**Manage → Tunnel Metrics** shows traffic and connection counts per tunnel, and
for **KCP** the numbers that actually explain a slow link:

- retransmits,
- lost and duplicated segments, and
- how many packets **forward error correction (FEC)** repaired.

That last one is the direct answer to "is KCP earning its overhead on my route?"

Traffic totals are counted on **every** transport and are kept across restarts,
so the numbers do not reset when a tunnel bounces (and they carry on after a
[backup restore](backup-restore.md)).

---
[← Back to the main README](../README.md)
