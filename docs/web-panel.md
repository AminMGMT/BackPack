# Web panel

A **monitoring-only** dashboard on **port 7777**, matching the CLI's look. It
shows live CPU / RAM / disk / traffic, each tunnel's state, real ping, and logs.
Backup, Telegram setup and the panel password live in **Settings**.

Run it on the **Iran** server, where you watch things from. It does not create
or change tunnels — that is the CLI's job.

## Getting in

The link and login code are shown in the CLI under **Web Panel** (whose settings
also cover update, panel port and password). Open the port first:

```bash
sudo ufw allow 7777
```

---
[← Back to the main README](../README.md)
