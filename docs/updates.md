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
[← Back to the main README](../README.md)
