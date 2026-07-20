# Monitor service

The watchdog, the [Telegram bot](telegram-bot.md) and the [alerts](alerts.md)
run as their own systemd unit, **`backpack-monitor.service`**, separately from
the [web panel](web-panel.md).

## Why it is separate

Monitoring used to run inside the panel process, which made the panel a
dependency of monitoring — backwards. Stopping the panel, or the panel crashing,
silently stopped dropped tunnels from being restarted and stopped every alert.

Now monitoring depends on nothing but the machine being up, restarts itself if
it dies, and keeps working when the panel is stopped.

## Nothing to do by hand

It is installed automatically — the CLI installs it on launch and the updater
installs it as part of an update. [Health Check](health-check.md) reports if it
is not running.

---
[← Back to the main README](../README.md)
