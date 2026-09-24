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

## Who may do what

The admin set up with the bot is its **owner**. Further admins can be added
beside it, each with full or read-only (`:ro`) access:

| | Owner | Admin | Read-only admin |
|---|:--:|:--:|:--:|
| Status, tunnels, alerts, health, history | ✅ | ✅ | ✅ |
| Start, stop, restart tunnels | ✅ | ✅ | — |
| **Web UI** (the panel's address *and password*) | ✅ | — | — |
| **Backup** (the archive carries the panel password) | ✅ | — | — |

The last two are the owner's alone because the panel password is full access —
including who else gets in — which is the panel's `admin` scope. An added admin
is the bot's equivalent of a `write` token, and a write token cannot reach it
either. See [access control](access-control.md).

---

<div dir="rtl">

## خلاصهٔ فارسی

گزارش وضعیت و [هشدارها](alerts.md) در تلگرام — حتی از داخل ایران که تلگرام
بسته است.

**چطور از ایران به تلگرام می‌رسد؟** یک پورت لوکال روی تونل مستقیم به API تلگرام
forward می‌شود و **سمت خارج** اتصال بیرونی را برقرار می‌کند. ترافیک بین ربات و
تلگرام همچنان TLS است؛ تونل فقط بایت جابه‌جا می‌کند و نمی‌تواند چیزی بخواند.

ربات **خودش یک تونل زنده را انتخاب می‌کند و وقتی آن یکی بیفتد به تونل دیگری
می‌رود**، پس لازم نیست تو انتخاب کنی. اگر باز هم بیرون نرفت، گزینهٔ **Diagnose**
زنجیره را قدم‌به‌قدم می‌رود و می‌گوید دقیقاً کدام حلقه خراب است.

**راه‌اندازی:** `Telegram Bot → Configure` در CLI (یا Settings در
[پنل وب](web-panel.md)). به یک توکن ربات از `@BotFather` و آیدی عددی خودت از
`@userinfobot` نیاز داری.

دکمه‌ها و دستورها: Status، System، Alerts، Backup، Web UI و Support. جزئیات
داخلی (پورت رله، پورت SOCKS، هاست API) هیچ‌وقت در پیام‌ها ظاهر نمی‌شود.

</div>

---
[← Back to the docs index](README.md)

---

*Last verified against Backpack v1.8.2.*
