# Setting up Vision

Last verified against Backpack v2.0.0, 2026-11-28.

Vision shows a real site's TLS handshake to anyone who looks at the tunnel
port. It needs no certificate and no domain of your own. It runs the Xray
helper, and Backpack installs the pinned one itself when a server has none,
also when a tunnel starts
(see [Helpers without the installer](transports.md#helpers-without-the-installer)).
Up to v2.0.0 it was called REALITY / Vision; tunnels and setup links made
under that name keep working.

## Iran

Open **Setup Iran → Reverse → HTTPS → Vision**. The wizard asks what every
reverse tunnel asks:

- **Iran IP Or Domain**: the detected public address is suggested.
- **Tunnel Port**: 443 when it is free, otherwise a free HTTPS-like port.
- **Forwarded Ports**: the services on the kharej, the one thing it cannot guess.
- **Tunnel Name**.

Press **Space or Enter** to keep a suggestion; type to replace it.

Then come Vision's own steps:

1. **Cover Site.** The wizard checks the suggested sites (Microsoft, Bing,
   then Apple) from this server, through the real Xray helper, and lists the
   ones that pass. Pick one, or choose **Another Site** to type and check your
   own. A site that fails shows why.
2. **Encryption.** `None`, `Stealth` or `Stealth+`, run inside Vision. See
   [Encryption inside Vision](transports.md#vision). The setup link carries
   the choice to the kharej.
3. **Preset**, then the usual optional fine-tuning and scheduled restart.

The rest is done for you. The wizard finds the Xray helper, installs it when
it is missing, and asks for its path only when that fails. It picks a free local port for the engine
behind the helper. It generates the security token, UUID, X25519 key pair and
Short ID. The summary shows the cover site and the setup link.

## Kharej

Choose **Setup Link** and paste the link from Iran. It carries the address,
port, public key, cover site, UUID, Short ID and encryption, so there is
nothing else to type, and Xray is installed first when the kharej has none.
The private key stays on Iran.

Setting up by hand is still possible. The UUID, cover site, Short ID, public
key, token and encryption must then match Iran exactly. Iran prints the public
key for this once, and the setup link under **Manage Tunnels** holds the rest.

## Changing it later

**Edit** a Vision tunnel to change the public port or cover site, the
encryption, the preset, the MSS clamp or the forwarded ports. An existing
tunnel keeps its key, UUID and Short ID unless you choose **Change The
Identity**, and that choice is offered only when editing. Change the other
side to match afterwards. The setup link is the easy way.

## What the checks prove

A cover check proves that the site works with Xray from this server. It does
not prove that the path between Iran and the kharej carries Vision. The
[Connection Test](cli-menu.md) checks that path.

The wizard never stops another service to free a port. No tunnel is created
until you confirm its summary.

## خلاصهٔ فارسی

<div dir="rtl">

در سمت ایران، Vision را انتخاب کنید و پورت‌های سرویس را وارد کنید. بقیهٔ موارد
پیشنهادی را با Space یا Enter تأیید کنید. اگر Xray نصب نباشد، خودش نصب می‌شود. ویزارد سایت‌های پوشش را خودش تست
می‌کند و فقط موارد موفق را نشان می‌دهد. رمزگذاری داخل Vision (هیچ، Stealth یا
Stealth+) را هم همان‌جا انتخاب کنید. کلیدها و شناسه‌ها خودکار ساخته می‌شوند. در
سمت خارج فقط لینک ایران را وارد کنید. تست سایت پوشش به‌تنهایی موفقیت تونل بین
دو سرور را تضمین نمی‌کند.

</div>
