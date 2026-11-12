BIN      := backpack
BIN_PATH := /usr/local/bin/backpack
LDFLAGS  := -s -w

# The PCK carrier is paqet's engine, which captures packets with libpcap
# through cgo, so a build that is meant to run it needs libpcap's headers and a
# static libpcap.a. `make libpcap` builds them into LIBPCAP_PREFIX without root
# (see scripts/build-libpcap.sh and CONTRIBUTING.md). A build without them still
# compiles and runs everything but PCK, which says so when it starts.
LIBPCAP_PREFIX ?= $(HOME)/.local/libpcap
GOTAGS         := netgo,osusergo
# Present only when libpcap is where LIBPCAP_PREFIX says. Without it the build
# is CGO-free, as it was before PCK moved to paqet.
PCAP_ENV = $(if $(wildcard $(LIBPCAP_PREFIX)/include/pcap.h),CGO_ENABLED=1 CGO_CFLAGS="-I$(LIBPCAP_PREFIX)/include" CGO_LDFLAGS="-L$(LIBPCAP_PREFIX)/lib -lpcap",CGO_ENABLED=0)

.PHONY: all build install uninstall clean tidy run vendor release-linux release version sbom reproducible check libpcap

all: build

tidy:
	go mod tidy

# Sync the raw VERSION file (used by the updater's mirror path) with the
# app.Version constant, so they can never drift.
version:
	@grep -oE 'Version = "[^"]+"' internal/app/app.go | grep -oE 'v[0-9.]+' > VERSION
	@echo "VERSION -> $$(cat VERSION)"

# libpcap builds libpcap.a and its headers into LIBPCAP_PREFIX with the host
# compiler. No root. Needs flex and bison, which the script builds itself when
# the machine has none.
libpcap:
	LIBPCAP_PREFIX=$(LIBPCAP_PREFIX) scripts/build-libpcap.sh

# build links libpcap statically when `make libpcap` has been run, and builds
# without PCK support when it has not (and says so).
build: tidy
	@if [ -z "$(wildcard $(LIBPCAP_PREFIX)/include/pcap.h)" ]; then \
	  echo "note: no libpcap in $(LIBPCAP_PREFIX) — building WITHOUT the PCK carrier (run 'make libpcap' first to include it)"; fi
	$(PCAP_ENV) go build -trimpath -tags $(GOTAGS) -ldflags "$(LDFLAGS)" -o $(BIN) .

vendor:
	go mod tidy
	go mod vendor

# Cross-compile static Linux binaries (no libc / no Go needed to run).
#
# Every one is linked with libpcap, because the PCK carrier needs it, and every
# one is still a single static file: scripts/build-linux.sh builds a libpcap for
# each target with Zig's C compiler (or, for ARMv5, which Zig's musl cannot
# target, with arm-linux-gnueabi-gcc) and links it in. `make release-linux`
# therefore needs zig on PATH, plus gcc-arm-linux-gnueabi for armv5; neither
# needs root to run. CI installs both.
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
	@for a in $(ARCHES); do \
	  echo "  building linux/$$a"; \
	  VERSION_LDFLAGS="" scripts/build-linux.sh $$a dist/backpack-linux-$$a || exit 1; \
	done
	@for v in $(ARMS); do \
	  echo "  building linux/armv$$v"; \
	  VERSION_LDFLAGS="-X github.com/backpack/backpack/internal/app.GOARM=$$v" \
	    scripts/build-linux.sh armv$$v dist/backpack-linux-armv$$v || exit 1; \
	done

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
# They are, and have been: -trimpath removes the build directory, the version
# is stamped from a file rather than from the clock, and the C side is built by
# Zig's compiler with a pinned libpcap, which embeds neither paths nor times.
# This makes that a thing somebody can check rather than a claim in a document — which is
# the whole point of a reproducible build, since the property is only useful if
# a third party can confirm it.
reproducible:
	@scripts/build-linux.sh amd64 /tmp/backpack-repro-a >/dev/null
	@scripts/build-linux.sh amd64 /tmp/backpack-repro-b >/dev/null
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

# check runs what CI runs, before a push: formatting, vet, staticcheck, the
# tests, and the tests again under the race detector. staticcheck is skipped
# with a note when it is not installed.
check:
	@unformatted="$$(gofmt -l .)"; if [ -n "$$unformatted" ]; then echo "not gofmt'd:"; echo "$$unformatted"; exit 1; fi
	$(PCAP_ENV) go vet ./...
	@if command -v staticcheck >/dev/null 2>&1; then staticcheck ./...; else echo "staticcheck not installed: go install honnef.co/go/tools/cmd/staticcheck@latest"; fi
	$(PCAP_ENV) go test ./... -timeout 20m
	$(PCAP_ENV) go test ./... -race -timeout 20m

clean:
	rm -f $(BIN)
