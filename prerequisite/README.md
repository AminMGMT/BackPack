# prerequisite/

This folder makes Backpack installable on a **fully offline** VPS (e.g. an Iran
server where `go.dev` is blocked).

## How to use

1. On a machine **with internet** (ideally your **kharej / abroad server**), run:

   ```bash
   bash prerequisite/download-prerequisites.sh
   ```

   It fills this folder with:
   - `go1.23.4.linux-amd64.tar.gz` and `go1.23.4.linux-arm64.tar.gz` — the Go toolchains.
   - If Go is installed on that machine, also `backpack-linux-amd64` / `-arm64`
     (prebuilt static binaries) and `../vendor` (all dependencies).

2. Copy the **whole BackPack folder** (including this `prerequisite/` folder) to
   the offline VPS.

3. On the VPS run:

   ```bash
   sudo bash install.sh
   ```

   `install.sh` will, with **no internet**:
   - install the prebuilt binary directly if one is here (fastest), **or**
   - extract the bundled Go toolchain and build from `../vendor`.

## Why

From Iran, `go.dev` and `dl.google.com` are usually blocked, so the VPS cannot
download Go or Go modules itself. Running the downloader once on an unrestricted
machine (or your kharej server) captures everything needed, so the restricted
VPS needs nothing.
