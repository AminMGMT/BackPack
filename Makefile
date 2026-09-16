BIN      := backpack
BIN_PATH := /usr/local/bin/backpack
LDFLAGS  := -s -w

.PHONY: all build install uninstall clean tidy run vendor release-linux release version

all: build

tidy:
	go mod tidy

# Sync the raw VERSION file (used by the updater's mirror path) with the
# app.Version constant, so they can never drift.
version:
	@grep -oE 'Version = "[^"]+"' internal/app/app.go | grep -oE 'v[0-9.]+' > VERSION
	@echo "VERSION -> $$(cat VERSION)"

build: tidy
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN) .

vendor:
	go mod tidy
	go mod vendor

# Cross-compile static Linux binaries (no libc / no Go needed to run).
#
# The three 32-bit ARM variants are built and named apart because they are not
# interchangeable: a v7 binary on a v5 board is an illegal instruction, not a
# slow one. Every ARM build reports GOARCH=arm at runtime whatever it was
# compiled for, so the variant is stamped in here — app.GOARM — and that is what
# lets a running binary ask for its own successor rather than a sibling that
# will not execute. See app.AssetName.
ARCHES := amd64 arm64 386 s390x
ARMS   := 5 6 7

release-linux:
	mkdir -p dist
	@for a in $(ARCHES); do 	  echo "  building linux/$$a"; 	  CGO_ENABLED=0 GOOS=linux GOARCH=$$a 	    go build -trimpath -ldflags "$(LDFLAGS)" -o dist/backpack-linux-$$a . || exit 1; 	done
	@for v in $(ARMS); do 	  echo "  building linux/armv$$v"; 	  CGO_ENABLED=0 GOOS=linux GOARCH=arm GOARM=$$v 	    go build -trimpath -ldflags "$(LDFLAGS) -X github.com/backpack/backpack/internal/app.GOARM=$$v" 	    -o dist/backpack-linux-armv$$v . || exit 1; 	done

# GitHub release assets: backpack_linux_<arch>.tar.gz, each containing a single
# `backpack` binary. These are what install.sh and the in-app updater download.
release: version release-linux
	mkdir -p release
	@for a in $(ARCHES) $(addprefix armv,$(ARMS)); do 	  cp dist/backpack-linux-$$a dist/backpack && 	  tar -czf release/backpack_linux_$$a.tar.gz -C dist backpack && 	  rm dist/backpack || exit 1; 	done
	@# A checksum file published beside the assets is what lets the installer and
	@# the updater prove that a mirror handed them the real binary. Users on
	@# restricted networks fetch these through third-party proxies, so this is
	@# the only integrity check they get.
	cd release && (sha256sum backpack_linux_*.tar.gz > SHA256SUMS 2>/dev/null || shasum -a 256 backpack_linux_*.tar.gz > SHA256SUMS)
	@# And a signature over that list. The checksum proves the download is
	@# intact; the signature proves the list is the publisher's, which the
	@# checksum cannot, because it travels the same channel as the archive it
	@# describes. Nothing happens without RELEASE_SIGNING_KEY in the
	@# environment, so a fork and a local build still produce a full set.
	go run ./tools/signsums release/SHA256SUMS
	@echo "Release assets ready in ./release"
	@cat release/SHA256SUMS

# release-key generates the signing pair, once. It prints both halves and keeps
# neither: the public half is pasted into app.ReleasePublicKey, the private half
# goes into the RELEASE_SIGNING_KEY repository secret and nowhere else.
release-key:
	@go run ./tools/releasekey

install: build
	install -m 0755 $(BIN) $(BIN_PATH)
	mkdir -p /etc/backpack
	@echo "Installed. Run: backpack"

uninstall:
	rm -f $(BIN_PATH)

run: build
	sudo ./$(BIN)

clean:
	rm -f $(BIN)
