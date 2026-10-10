# Experimental Short HTTPS

Last verified against Backpack v1.9.0, 2026-10-11.

`backpack short-https` is an **experimental, standalone TCP port relay** for
paths that allow short HTTPS exchanges but stop established connections after
a few packets. It keeps a logical backend TCP connection while sending small
authenticated HTTPS requests over fresh physical connections. Byte offsets and
acknowledgements avoid duplicate delivery after a lost response. Logical EOF
preserves replies after the application closes its write direction.

It works with IP addresses and generated private IP certificates; no owned
domain, external helper or outbound proxy is needed. Certificate verification
remains enabled. The server permits only one configured literal loopback
backend. The client exposes that fixed backend on its configured local port.
This command does not install services or edit existing tunnel configurations,
and is not part of the ordinary HTTPS menu or Connection Test matrix yet.

## Trying it without changing an existing tunnel

On the **backend server**, choose a free public port and a local TCP service:

```sh
backpack short-https init --ip SERVER_PUBLIC_IP --target 127.0.0.1:BACKEND_PORT --port 8443 --dir /root/short-https-test
backpack short-https serve --config /root/short-https-test/server.json
```

`init` creates a new directory with owner-only files and refuses to overwrite
existing state. Copy **only `client.json`** to the connecting server using SSH.
It contains the public certificate and shared secret; keep it private. The
server key and `server.json` stay on the backend server.

Change `listen` in the copied `client.json` from `127.0.0.1:0` to a free local
port, for example `127.0.0.1:2096`, then run on the **connecting server**:

```sh
backpack short-https client --config /root/client.json
```

Connect to the displayed local TCP address to reach the fixed backend. Set an
explicit public listen address only if you intend to expose that backend.
Both processes stay in the foreground; Ctrl+C stops them. Existing BackPack,
Backhaul, Xray and TUN listeners are not stopped or reconfigured.

## Limits and interpretation

This is connection recovery, not browser impersonation or guaranteed filtering
evasion. Native TLS 1.3 uses classical ECDHE to keep handshakes small; HTTP/1.1
uses a new connection per request. No custom cryptography or disabled certificate
verification is used. Repeated small requests and the native TLS fingerprint
remain observable. A filter that blocks the IP or the initial handshake can
still prevent it from working.

Each request carries at most 2 KiB in either direction. Throughput is lower and
TLS/CPU overhead higher than ordinary persistent tunnels. There are at most
64 logical streams and 64 KiB of buffered backend data per stream. Idle server
sessions expire after 90 seconds; a client gives up after 15 seconds without a
successful exchange. It cannot restore a backend TCP connection after process
restart or session expiry. Only TCP is supported: no UDP, TUN, automatic
failover, systemd integration or certificate renewal is implemented.

Generated certificates expire after seven days. Create a new paired test state
and transfer its new `client.json` when needed. Do not use this experimental
command as an unattended permanent replacement for a working tunnel.

## خلاصهٔ فارسی

<div dir="rtl">

این رلهٔ آزمایشی بدون دامنه و با IP کار می‌کند. به‌جای اتصال HTTPS طولانی،
درخواست‌های کوتاه با اتصال تازه می‌فرستد و شمارهٔ بایت‌ها را برای جلوگیری از
تکرار داده نگه می‌دارد. روی سرور مقصد، یک سرویس TCP محلی و پورت عمومی آزاد
انتخاب کنید؛ فقط فایل `client.json` را به سرور دیگر منتقل کنید و پورت محلی
آن را تنظیم کنید. گواهی خصوصی بررسی می‌شود و کلید خصوصی روی سرور مقصد می‌ماند.

این گزینه هنوز در منوی HTTPS و Connection Test نیست؛ با دستور `short-https`
اجرا می‌شود و تانل‌های قبلی را تغییر نمی‌دهد. سرعت آن کمتر است، UDP پشتیبانی
نمی‌شود و گواهی آزمایشی هفت روز اعتبار دارد. این روش تضمین پنهان‌ماندن ندارد
و برای جایگزینی دائمی تانل سالم آماده نیست.

</div>
