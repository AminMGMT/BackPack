# Access control

The panel had one password and one level of access: anyone who could open it
could change anything, and nothing was written down afterwards.

Three things changed.

## Scopes

Every request is authorised at one function — `guard` in
`internal/webui/server.go` — against one of three scopes:

| Scope   | May                                                                 |
|---------|---------------------------------------------------------------------|
| `read`  | `/metrics`, `/api/stats`, `/api/tunnels`, `/api/alerts`, `/api/fleet/drift` |
| `write` | also everything else that runs the tunnels and the fleet: create, edit, restart, logs, speed tests, updates |
| `admin` | also everything that decides who gets in: tokens, the record, the password, the second factor, signed-in devices, the Telegram admins, the panel's port and certificate, and backup export and restore |

Signing in with the panel password is `admin`. Handing out a credential is
separate from using one, so a `write` token cannot mint itself a better one —
nor reach anything that would amount to the same thing. The password, the
Telegram admin list and a backup (which carries the password out, and on
restore replaces every credential file) are each a way to become `admin`, so
they are `admin` too. The whole table is tested as it is wired, in
`internal/webui/routes_test.go`.

The vocabulary is the Telegram bot's, deliberately. The bot has had
`ReadOnly` / `canWrite` for a while; two permission models in one product is
how a gap opens between them.

## API tokens

For callers that are not browsers. A Prometheus scraper has no cookie, and
`/metrics` is an endpoint built for scrapers.

```
curl -H "Authorization: Bearer <token>" https://panel:8443/metrics
```

Create one under **Settings → Security → API tokens**. The secret is shown
once, when it is created, and never again — only its SHA-256 is stored, so a
token cannot leak with a backup.

The panel had a read-only token before and it was removed, correctly: nothing
issued it, so nothing rotated it. The reasons it was removed are addressed
rather than repeated:

- a **name** is required, so nobody is afraid to revoke an anonymous one;
- an **expiry** is required, so it cannot outlive what it was issued for;
- **last used** is recorded, so a dead token is recognisable as dead;
- they are **listed** on a screen an operator actually opens.

Prometheus:

```yaml
scrape_configs:
  - job_name: backpack
    authorization:
      credentials: <token>
    static_configs:
      - targets: ['panel.example.ir:8443']
```

## The record

Every action taken through the panel or with a token is recorded: who, from
where, what, and what the panel answered. Read it under **Settings → Security →
What has been done here**.

It is written by the authorisation guard, not by the handlers. A log each
handler writes for itself has one hole per handler somebody forgot to update,
and those holes are invisible until the day somebody goes looking. Written at
the choke point, the only way to act without being recorded is to act without
being authorised.

Reads are not recorded. The panel polls itself every few seconds, and thousands
of those lines would bury the handful that matter.

Refused attempts by a known credential *are* recorded, and are often the more
interesting line. A token nobody issued is not: it is counted against the
address by the same limiter as a wrong password, and it is not written down,
because an unauthenticated caller must not be able to fill the record.

The record is a hash chain. Each entry carries the hash of the one before it,
so deleting or editing a line breaks every link after it, and the record says
so at the top where it is read (*Record intact · head #…*, or which entry does
not follow). Every line forwarded to Telegram carries the head of the chain at
that moment, so even a rewrite that recomputes the whole chain disagrees with
the numbers already sitting in Telegram, out of the intruder's reach.

The record lives at `/etc/backpack/audit.json`, holds the last 5,000 entries,
and is readable only by root. An audit file that cannot be written never blocks
an action — a full disk must not lock an operator out of the tool they need to
fix it.

---

<div dir="rtl">

## خلاصهٔ فارسی

پنل یک رمز داشت و یک سطح دسترسی: هر کسی که می‌توانست بازش کند می‌توانست هر چیزی
را عوض کند، و بعدش هم هیچ‌جا نوشته نمی‌شد. سه چیز عوض شد.

**Scope‌ها.** هر درخواست در یک تابع — `guard` — در برابر یکی از سه سطح مجاز
می‌شود: `read` (فقط `/metrics` و وضعیت، تونل‌ها، هشدارها و drift)، `write` (به‌علاوهٔ
ساختن و ویرایش تونل، ری‌استارت سرویس، لاگ، ارتقا) و `admin` (به‌علاوهٔ هر چیزی که
تعیین می‌کند چه کسی وارد شود: توکن‌ها، سابقه، رمز پنل، 2FA، دستگاه‌های واردشده،
ادمین‌های تلگرام، پورت و گواهی پنل، و backup/restore). ورود با رمز پنل یعنی `admin`. *دادن* یک اعتبارنامه از *استفاده* از
آن جداست، پس یک توکن `write` نمی‌تواند برای خودش توکن بهتری بسازد. واژگان عمداً
همان واژگان ربات تلگرام است؛ دو مدل دسترسیِ متفاوت در یک محصول، همان‌جایی است که
شکاف باز می‌شود.

**توکن API.** برای چیزهایی که مرورگر نیستند — یک scraper پرومتئوس کوکی ندارد، و
`/metrics` اصلاً برای scraper ساخته شده:

```
curl -H "Authorization: Bearer <token>" https://panel:8443/metrics
```

از `Settings → Security → API tokens` ساخته می‌شود. رازش **فقط یک‌بار**، موقع
ساخت، نشان داده می‌شود؛ فقط SHA-256 آن ذخیره می‌شود، پس توکن با یک backup لو
نمی‌رود. **نام** اجباری است (تا کسی از revoke کردنِ یک توکن بی‌نام نترسد)،
**انقضا** اجباری است (تا از کاری که برایش صادر شده عمر بیشتری نکند)، و **آخرین
استفاده** ثبت می‌شود (تا توکن مرده قابل تشخیص باشد).

**سابقه (audit).** توسط همان نگهبان مجوز نوشته می‌شود، نه توسط handlerها. لاگی که
هر handler برای خودش می‌نویسد به‌ازای هر handlerی که کسی یادش رفته یک سوراخ دارد،
و آن سوراخ‌ها تا روزی که کسی دنبالشان بگردد نامرئی‌اند. وقتی در همان گلوگاه نوشته
شود، تنها راهِ عمل‌کردن بدون ثبت‌شدن، عمل‌کردن بدون مجوز است.
**خواندن‌ها ثبت نمی‌شوند** — پنل هر چند ثانیه خودش را poll می‌کند و هزاران خطِ آن،
آن چند خطی را که مهم است دفن می‌کند. **تلاش‌های ردشده ثبت می‌شوند** و اغلب همان‌ها
خط جالب‌ترند. سابقه در `/etc/backpack/audit.json` است، ۵۰۰۰ ورودی آخر را نگه
می‌دارد و فقط root می‌تواند بخواندش. فایل سابقه‌ای که نوشته نشود **هیچ‌وقت** جلوی
یک عمل را نمی‌گیرد — دیسک پر نباید اپراتور را از ابزاری که برای درست‌کردنش لازم
دارد بیرون بگذارد.

</div>

---
[← Back to the docs index](README.md)

---

*Last verified against Backpack v1.8.2.*
