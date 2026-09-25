# Releasing

The process used to live in the Makefile and in one person's head. This is it
written down, so a release made in a hurry is the same release.

## Before you tag

- [ ] **The `RELEASE_SIGNING_KEY` secret exists.** Without it the tag publishes
      no signature, and every updater carrying the pinned public key
      (`internal/app.ReleasePublicKey`) refuses the release — which is worse
      than no signing at all. Check first; it is the one step that cannot be
      fixed after the fact.
- [ ] `CHANGELOG.md` has a section for this version, and it says what a user
      would have seen rather than what the diff did.
- [ ] `VERSION` and `internal/app` agree with the tag. CI checks this before it
      builds, deliberately, because a mismatch is only discovered by an operator
      otherwise.
- [ ] `README.md` **and `README_FA.md`** are both current. They drift apart, and
      the Persian one is the one most users read.
- [ ] `go.mod`'s Go version and `install.sh`'s `GO_VERSION` / `GO_SHA_VERSION`
      agree. The installer refuses to run if they do not, and says so.
- [ ] The full suite is green, with `-race`, and so are `staticcheck` and
      `govulncheck`.
- [ ] The compatibility job passed: the previous release talks to this one in
      both directions.

## Tagging

```
git tag -a v1.8.2 -m "v1.8.2"
git push origin v1.8.2
```

The release workflow builds every architecture, writes `SHA256SUMS`, signs it
with the repository secret, and publishes.

What is signed is the **tag and the list together** (`backpack release <tag>`,
a newline, then `SHA256SUMS`), not the list alone. The list names archives,
not versions, so a signature over it alone would let a mirror serve an older
release's genuine files under a newer tag and have every updater verify and
install them. With the tag signed, a signature is good for its own release
only. To check one by hand, verify the Ed25519 signature in `SHA256SUMS.sig`
over exactly those bytes.

## After the tag

- [ ] Download the published binary for your own architecture and check it
      against `SHA256SUMS`, and check the signature. Verifying your own release
      is the only way to find out that the signing step silently did nothing.
- [ ] Install it over an existing tunnel on a real machine and watch it come
      back. The updater takes a snapshot and rolls back on its own, and you want
      to know that it did not have to.
- [ ] Announce it wherever the users are.

## If a release is bad

The updater already handles the common case: it verifies, it snapshots, and it
rolls back when the tunnels do not come back. For anything worse:

1. Delete the release on GitHub so the updater stops offering it.
2. Tag a fix. Do not re-tag the same version; an updater that has already seen
   it will not look again.



## Reproducible builds and the bill of materials

A release is built with `CGO_ENABLED=0`, `-trimpath` and a version stamped from
`VERSION` rather than from the clock, so two builds of the same source are
byte-identical. That is a property anybody can check rather than a claim to be
believed, which is the only kind worth making:

```
make reproducible
```

Every release publishes `SBOM.txt` beside the archives. It is read **out of the
built binary** with `go version -m`, not assembled from `go.mod`, so it
describes what was actually linked rather than what the manifest asked for —
and those differ the moment anything is replaced or vendored. It also names the
toolchain, which is the dependency with the most reachable CVEs in this
project's history and the one nothing else records.

To check a binary you downloaded against it:

```
go version -m ./backpack
```

## If the signing key is lost or leaked

There is no graceful path, and it is worth reading before it happens rather than
after. The public half is **compiled into every installed binary**, so:

| | what it means | what to do |
|---|---|---|
| **Lost** | no future release can be signed with it; every installed updater refuses every update | a manual install on every machine, carrying a build with a new key |
| **Leaked** | whoever has it can sign a release every installed updater accepts | the same manual install, urgently |

Both end in the same place, and that is the point: **there is no remote
recovery**. The private half lives in the `RELEASE_SIGNING_KEY` repository
secret and nowhere else. It was generated once by `make release-key` and the
file it came from was deleted.

A second, offline key pinned alongside the first would turn either case into a
release rather than a fleet-wide manual install. It is not built — it is
written down here so the decision is made deliberately rather than discovered
during the incident.

---

<div dir="rtl">

## خلاصهٔ فارسی

این صفحه برای کسی است که نسخه منتشر می‌کند، نه برای اپراتور: چک‌لیست انتشار، و
اینکه اگر کلید امضا گم شود چه باید کرد.

نکتهٔ اصلی: هر انتشار یک فایل `SHA256SUMS` دارد که با کلید **Ed25519** امضا
می‌شود، و updater قبل از نصب امضا را بررسی می‌کند. نیمهٔ عمومی کلید داخل خود
باینری pin شده و نیمهٔ خصوصی فقط در یک repository secret زندگی می‌کند و جای
دیگری نه. **تا وقتی آن secret وجود نداشته باشد، انتشارِ tag شده هیچ امضایی
منتشر نمی‌کند و هر updaterای که کلید عمومی را دارد آن را رد می‌کند** — که از
حالتِ قبل از وجود کلید هم بدتر است.

**ساخت تکرارپذیر:** انتشار با `CGO_ENABLED=0`، `-trimpath` و نسخه‌ای که از
`VERSION` می‌آید نه از ساعت ساخته می‌شود، پس دو build از یک سورس بایت‌به‌بایت
یکی‌اند. این خاصیتی است که هر کسی می‌تواند خودش بررسی کند — تنها نوع ادعایی که
ارزش گفتن دارد. با `make reproducible` امتحانش کن.

**اگر انتشاری خراب بود:** آن را روی GitHub پاک کن تا updater دیگر پیشنهادش
نکند، و یک tag اصلاحی بزن. **همان نسخه را دوباره tag نکن** — updaterای که
یک‌بار دیده دیگر نگاه نمی‌کند.

</div>

---
[← Back to the docs index](README.md)

---

*Last verified against Backpack v1.8.3.*
