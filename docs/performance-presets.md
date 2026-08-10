# Performance presets

Instead of a yes/no "best performance" switch, every tunnel picks one of three
presets. Each fills in every tuning value at once — connection pools, socket
buffers, receive windows — and applies kernel tuning (BBR + fq, buffer ceilings,
file limits).

| Preset | For | Notes |
|--------|-----|-------|
| **Balance** | a small or shared VPS | Light on CPU and RAM. |
| **Turbo** | most people (recommended) | **Byte-for-byte identical to the old "Best Performance"**, so upgrading changes nothing about an existing tunnel. |
| **Aggressive** | maximum throughput | Noticeably more CPU. |

## Changing it later

**Edit → Change performance preset**. Configs written before presets existed
carry no preset field and are left exactly as they are.

The [TCP MSS clamp](mss-clamp.md) is not one of these values and a preset change
leaves it alone: it describes the path the tunnel crosses rather than how hard
the tunnel is being pushed.

---
[← Back to the main README](../README.md)
