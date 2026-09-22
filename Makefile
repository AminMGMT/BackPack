BIN      := backpack
BIN_PATH := /usr/local/bin/backpack
LDFLAGS  := -s -w

.PHONY: all build install uninstall clean tidy run vendor release-linux release version sbom reproducible

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
	@# The bill of materials, published with the release.
	@#
	@# Go records every module and its hash inside the binary it builds, so this
	@# is read back out of the artefact rather than assembled from go.mod — it
	@# describes what was actually linked, not what the manifest asked for, and
	@# those differ the moment anything is replaced or vendored.
	@#
	@# It also names the toolchain, which is the dependency with the most
	@# reachable CVEs in this project's history and the one nothing else records.
	$(MAKE) sbom
	@echo "Release assets ready in ./release"
	@cat release/SHA256SUMS

# sbom writes what the release was actually built from.
#
# No new tool: `go version -m` reads the module graph Go stamps into every
# binary, with the hash of each dependency. That is the bill of materials, and
# it is more trustworthy than one generated from go.mod because it comes out of
# the artefact somebody will actually run.
sbom:
	@mkdir -p release
	@{ 	  echo "# Backpack $$(cat VERSION) — bill of materials"; 	  echo "#"; 	  echo "# Read out of the built binary, so it describes what was linked"; 	  echo "# rather than what go.mod asked for."; 	  echo "#"; 	  echo "# Verify a binary you downloaded against this with:"; 	  echo "#     go version -m ./backpack"; 	  echo; 	  go version -m dist/backpack-linux-amd64; 	} > release/SBOM.txt
	@echo "SBOM -> release/SBOM.txt"

# reproducible checks that two builds of the same source are byte-identical.
#
# They are, and have been: CGO is off, -trimpath removes the build directory,
# and the version is stamped from a file rather than from the clock. This makes
# that a thing somebody can check rather than a claim in a document — which is
# the whole point of a reproducible build, since the property is only useful if
# a third party can confirm it.
reproducible:
	@CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o /tmp/backpack-repro-a .
	@CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o /tmp/backpack-repro-b .
	@if cmp -s /tmp/backpack-repro-a /tmp/backpack-repro-b; then 	  echo "reproducible: two builds are byte-identical"; 	else 	  echo "NOT reproducible: two builds of the same source differ"; exit 1; 	fi
	@rm -f /tmp/backpack-repro-a /tmp/backpack-repro-b

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
