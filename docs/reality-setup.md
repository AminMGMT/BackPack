# Setting up REALITY / Vision

On Iran, open **Setup Iran → Reverse → HTTPS → REALITY / Vision**. The wizard
suggests the detected public address and a free HTTPS port (443 when available).
Enter the ports you actually want to forward; the wizard cannot infer the
service on Kharej. The tunnel name, private internal port and Balance preset
have editable defaults.

Press **Space or Enter** to keep a displayed suggestion. On a Linux terminal,
Space advances immediately. To override a value, start typing and finish with
Enter. Advanced settings and scheduled restarts are off by default. In piped
input, submit each answer as a line; closed input cancels rather than saving.

The wizard detects the installed Xray helper and tests the suggested cover
endpoints from Iran. Each successful endpoint must pass certificate verification,
TLS 1.3, HTTP/2 and an authenticated data transfer using the actual Xray helper.
Only successful endpoints appear in the selection menu. SNI is filled from the
chosen hostname. This is a local cover compatibility check; it does not prove
that the Iran/Kharej path allows REALITY.

Choose **Custom endpoint and SNI** to enter your own destination and hostname.
These are tested together before saving. Failed checks show their reason and
offer a retry. Checks use temporary loopback listeners and have bounded
timeouts; helper output goes to a private temporary log that is removed afterward.

The security token, VLESS UUID, X25519 key pair and Short ID are generated for
Iran. **Change generated identity settings** lets you override them. Keeping
an existing configuration preserves its key, UUID, Short ID, target and SNI;
rescanning or changing the cover is an explicit choice.

On Kharej, choose **Setup Link** and paste the link printed by Iran. It carries
the public key and all matching settings, so you do not need to enter SNI or
identity fields again. The private key stays on Iran. Manual setup remains
available, but its token, UUID, SNI, Short ID, internal port and public key must
match Iran; they cannot be safely guessed or generated independently on Kharej.

The wizard checks existing listeners when selecting defaults. It never frees a
port by stopping another service. The final operating-system bind can still race
another process, and no tunnel is created until you confirm its summary.

## فارسی

برای سمت ایران، Reality را انتخاب کنید و پورت‌های سرویس موردنظر را وارد کنید.
بقیهٔ موارد پیشنهادی را می‌توانید با Space یا Enter تأیید کنید؛ برای تغییر، مقدار
جدید را بنویسید و Enter بزنید. مقصدهای پیشنهادی خودکار تست می‌شوند و فقط موارد
موفق قابل انتخاب‌اند. SNI از مقصد انتخاب‌شده پر می‌شود و کلیدها و شناسه‌ها
خودکار ساخته می‌شوند. برای سمت خارج، لینک ایران را وارد کنید تا تنظیمات دو طرف
یکسان بماند. تست مقصد به‌تنهایی موفقیت تونل بین دو سرور را تضمین نمی‌کند.
