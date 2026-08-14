# Updates & rollback

The **Update** menu detects a newer GitHub release and installs the prebuilt
`backpack_linux_<arch>.tar.gz`.

## How it downloads

**Direct from GitHub, or through a tunnel peer** when this server cannot reach
GitHub itself. Both paths terminate TLS at GitHub, so the download is verified
against the release's published **SHA-256**.

**An archive that cannot be verified is refused, not installed.** A server that
can reach neither path installs offline instead — see the offline install
section in the main README.

## Safety net

Before touching anything, Update saves a **restore point**. After installing it
health-checks the result and **rolls back by itself** if anything fails to come
back up. You can also roll back on demand from **Update → Restore points**.

## Channels

Follow **stable** (default) or **beta** under **Update → Release channel**.

## Upgrading a very old install

From a clone-based install (≤ v1.2.0): run Update once; after that it is
release-based.

---

<div dir="rtl">

## خلاصهٔ فارسی

منوی **Update** ریلیز جدیدتر گیت‌هاب را پیدا و نصب می‌کند.

**چطور دانلود می‌کند:** مستقیم از گیت‌هاب، یا **از طریق یک تونل** وقتی خود این
سرور به گیت‌هاب دسترسی ندارد. هر دو مسیر TLS را در گیت‌هاب terminate می‌کنند، پس
دانلود با **SHA-256** منتشرشدهٔ ریلیز تأیید می‌شود.
**آرشیوی که تأیید نشود نصب نمی‌شود، رد می‌شود.**

**تور ایمنی:** قبل از دست زدن به چیزی یک **restore point** ذخیره می‌کند، بعد از
نصب نتیجه را health-check می‌کند و **اگر چیزی بالا نیامد خودش برمی‌گردد عقب**.
دستی هم می‌شود از `Update → Restore points` برگشت.

**کانال:** پیش‌فرض **stable**؛ اگر بخواهی pre-release‌ها را هم بگیری، از
`Update → Release channel` روی **beta** بگذار.

سروری که به هیچ‌کدام دسترسی ندارد، [آفلاین نصب می‌کند](install.md).

</div>

---
[← Back to the docs index](README.md)
