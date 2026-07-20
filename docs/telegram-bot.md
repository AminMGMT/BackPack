# Telegram bot

Status reports and [alerts](alerts.md) delivered to Telegram — even from Iran,
where Telegram is blocked.

## How it reaches Telegram from Iran

A loopback port on a tunnel forwards straight to the Telegram API, and the
**far end** (kharej) makes the outbound connection. The traffic stays TLS
between the bot and Telegram; the tunnel only moves bytes, it cannot read them.

The bot **picks a live tunnel itself and switches to another when that one
drops**, so you never have to choose or re-choose which tunnel relays. When it
still cannot get out, **Diagnose** walks the chain hop by hop and names the
exact link that is broken.

## Setup

**Telegram Bot → Configure** in the CLI (or **Settings** in the [web
panel](web-panel.md)). You need a bot token from `@BotFather` and your numeric
user id from `@userinfobot`.

## What it offers

Buttons and commands for **Status**, **System**, **Alerts**, **Backup**,
**Web UI** and **Support**. Internal plumbing — the relay port, any SOCKS port,
the API host — never appears in a message.

---
[← Back to the main README](../README.md)
