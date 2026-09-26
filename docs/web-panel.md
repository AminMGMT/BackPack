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

## Using your own certificate (certbot or any other)

If Let's Encrypt cannot verify this server from here — port 80 is taken, or
inbound traffic from its validators does not reach the machine — get the
certificate your own way and give the panel the two files.

- **Format:** PEM. The certificate file holds the certificate followed by its
  chain; the key file holds its private key. From certbot these are
  `fullchain.pem` and `privkey.pem`, for example:

  ```
  /etc/letsencrypt/live/panel.example.com/fullchain.pem
  /etc/letsencrypt/live/panel.example.com/privkey.pem
  ```

- **Where:** anywhere on the server; give full paths. Nothing is copied — the
  panel reads the files where they are.
- **Panel:** Settings → Panel access → Certificate → *HTTPS, my own
  certificate*. **CLI:** Web Panel → Certificate → *HTTPS, my own certificate*.
- Both check the pair before saving: a key that is not the certificate's, an
  expired certificate or an unreadable file is refused there and then.
- **Renewals** are picked up on the next connection, with no restart: point the
  panel at certbot's `live/` paths and let certbot renew as usual.
- If the files later disappear or become unreadable, the panel does not lock
  you out: it serves its self-signed certificate instead and says why in its
  log (`journalctl -u backpack-webui`).

For a **tunnel** (WSS / WSS Mux), the setup wizard's *Use existing
certificate/key files* does the same, and writes `tls_cert` and `tls_key` into
the server's config.

## Two-factor sign-in

The panel is root on this machine, and by default one password opens it. A
second factor is a code from an authenticator app — the ordinary kind, RFC 6238,
six digits every thirty seconds, so any app you already use works.

**Turning it on** is *Settings → Security → Two-factor sign-in → Turn on*. The
panel shows a key to scan or type into the app, and nothing changes until you
type back the six digits it produces — an app that never got the secret cannot
lock you out. Then it shows **ten recovery codes**, once.

**Keep the recovery codes somewhere that is not this server.** Each one signs
you in once, in the same box as the code, and they are the way back if the phone
is gone. Fresh ones can be issued at any time from the same screen, which
retires the old set.

**If the phone and the codes are both gone**, the way back is the machine
itself: CLI → **Web Panel** → **Two-factor sign-in** → turn it off. That asks
for no password on purpose. Anyone who can run it is already root on the server
and can read the file the secret is in, so a prompt would protect nothing and
would strand an operator who had also forgotten the password.

**What it does not protect.** API tokens are a separate credential and are not
affected — a token is for things that are not browsers, and a second factor has
nothing to prompt. Sessions already signed in stay signed in; sign them out from
the same Security pane if that matters.

## See also

- [Managed servers (nodes)](managed-servers.md) — registering a foreign server
  with this panel and building both ends of a tunnel from one screen. New panel
  only.

---

<div dir="rtl">

## خلاصهٔ فارسی

یک داشبورد **فقط-پایشی** روی **پورت ۷۷۷۷** با ظاهری هماهنگ با CLI: پردازنده،
حافظه، دیسک و ترافیک زنده، وضعیت هر تونل، پینگ واقعی و لاگ‌ها. پشتیبان‌گیری،
تنظیمات تلگرام و رمز پنل در بخش **Settings** است.

روی سرور **ایران** اجرایش کن، همان‌جا که از آن نظارت می‌کنی. تونل نمی‌سازد و
تغییر نمی‌دهد — آن کارِ CLI است.

**ورود دو مرحله‌ای:** پنل روی این سرور root است و به‌صورت پیش‌فرض فقط یک رمز
جلوی آن است. از `Settings → Security → Two-factor sign-in` می‌توانی کد یک‌بارمصرف
اپلیکیشن authenticator را روشن کنی؛ تا وقتی شش رقمی که اپ نشان می‌دهد را برنگردانی
چیزی فعال نمی‌شود. بعدش **ده کد بازیابی** یک‌بار نشان داده می‌شود — آن‌ها را جایی
بیرون از همین سرور نگه دار. اگر هم گوشی و هم کدها را از دست دادی، از خود سرور:
`CLI → Web Panel → Two-factor sign-in` و خاموشش کن؛ آنجا رمز نمی‌پرسد، چون هر کسی
که بتواند آن را اجرا کند همین حالا root است.

**ورود:** لینک و کد ورود در CLI زیر گزینهٔ **Web Panel** نشان داده می‌شود (پورت،
رمز و گواهی پنل هم همان‌جا تنظیم می‌شود). اول پورت را باز کن:
`sudo ufw allow 7777`.

</div>

**Every screen it has** — what each one shows, its address, and the CLI entry
that does the same job — is in
[the web panel, screen by screen](web-panel-screens.md).

---
[← Back to the docs index](README.md)

---

*Last verified against Backpack v1.8.4.*
