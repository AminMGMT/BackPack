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
release-linux:
	mkdir -p dist
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "$(LDFLAGS)" -o dist/backpack-linux-amd64 .
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags "$(LDFLAGS)" -o dist/backpack-linux-arm64 .

# GitHub release assets: backpack_linux_<arch>.tar.gz, each containing a single
# `backpack` binary. These are what install.sh and the in-app updater download.
release: version release-linux
	mkdir -p release
	cp dist/backpack-linux-amd64 dist/backpack && tar -czf release/backpack_linux_amd64.tar.gz -C dist backpack && rm dist/backpack
	cp dist/backpack-linux-arm64 dist/backpack && tar -czf release/backpack_linux_arm64.tar.gz -C dist backpack && rm dist/backpack
	@echo "Release assets ready in ./release"

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
