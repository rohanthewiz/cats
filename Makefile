# cats build entry points. The ghostty-tagged targets need the vendored
# libghostty-vt built first (`make vt`, or scripts/build-libghostty-vt.sh) —
# the CGO seam behind internal/terminal only compiles with -tags ghostty and
# PKG_CONFIG_PATH pointing at the built pkgconfig.
SHELL := /bin/bash

VT_DIR   := third_party/libghostty-vt
PC_DIR   := $(abspath $(VT_DIR))/zig-out/share/pkgconfig
GHOSTTY  := PKG_CONFIG_PATH=$(PC_DIR)
TAGS     := -tags ghostty

# The shipped binaries. The other cmd/ entries are development spikes.
BINS     := catway cathost catctl
VERSION  := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
GOOS     := $(shell go env GOOS)
GOARCH   := $(shell go env GOARCH)
DIST     := dist/cats_$(VERSION)_$(GOOS)_$(GOARCH)

.PHONY: all vt build test build-ghostty test-ghostty race-ghostty binaries \
        local dist macapp macapp-client fmt-check vet vet-ghostty check clean

all: binaries

# --- vendored VT engine ------------------------------------------------------

vt:
	scripts/build-libghostty-vt.sh

# --- untagged (no CGO — internal packages and stubs) --------------------------

build:
	go build ./...

test:
	go test ./...

# --- ghostty-tagged (the real terminal path) ----------------------------------

build-ghostty:
	$(GHOSTTY) go build $(TAGS) ./...

test-ghostty:
	$(GHOSTTY) go test $(TAGS) ./...

race-ghostty:
	$(GHOSTTY) go test $(TAGS) -race ./...

binaries:
	@mkdir -p bin
	$(foreach b,$(BINS),$(GHOSTTY) go build $(TAGS) -trimpath -o bin/$(b) ./cmd/$(b) &&) true
	@ls -lh bin

# --- personal install --------------------------------------------------------

# Build each shipped binary straight into ~/bin under a short alias.
# The map is "cmd:alias" pairs — edit here to rename or add targets. Splitting
# on ':' keeps the source dir (./cmd/$(cmd)) decoupled from the installed name.
LOCAL_BIN := $(HOME)/bin
LOCAL_MAP := catway:hway cathost:thost catctl:hctl

local:
	@mkdir -p $(LOCAL_BIN)
	$(foreach p,$(LOCAL_MAP),$(GHOSTTY) go build $(TAGS) -trimpath \
	    -o $(LOCAL_BIN)/$(word 2,$(subst :, ,$(p))) ./cmd/$(word 1,$(subst :, ,$(p))) &&) true
	@for p in $(LOCAL_MAP); do ls -lh $(LOCAL_BIN)/$${p#*:}; done

# --- packaging ----------------------------------------------------------------

dist: binaries
	@mkdir -p $(DIST)
	cp bin/catway bin/cathost bin/catctl $(DIST)/
	cp config.example.yaml README.md $(DIST)/
	tar -czf $(DIST).tar.gz -C dist $(notdir $(DIST))
	@echo "==> $(DIST).tar.gz"

# --- macOS app bundles --------------------------------------------------------
# Both variants are built from the one cmd/catapp launcher; the variant chooses
# what gets bundled and the baked-in default mode. Unsigned/personal: another Mac
# needs a one-time right-click -> Open. scripts/build-macapp.sh does the assembly.

APP_ID        := dev.cats.app
APP_CLIENT_ID := dev.cats.client

# macapp (Variant 1, self-contained): launcher + the three static ghostty daemons
# → dist/Cats.app. Runs fully local. Depends on `binaries` for the daemons; the
# vendored VT engine must be built first (`make vt`).
macapp: binaries
	scripts/build-macapp.sh self "Cats" $(APP_ID) $(VERSION)

# macapp-client (Variant 2, thin): launcher only, baked to remote mode →
# dist/Cats Client.app. No backend binaries, so no ghostty/Zig toolchain needed.
macapp-client:
	scripts/build-macapp.sh client "Cats Client" $(APP_CLIENT_ID) $(VERSION)

# --- hygiene ------------------------------------------------------------------

fmt-check:
	@bad=$$(gofmt -l cmd internal); if [ -n "$$bad" ]; then \
	  echo "gofmt needed:"; echo "$$bad"; exit 1; fi

vet:
	go vet ./...

vet-ghostty:
	$(GHOSTTY) go vet $(TAGS) ./...

# Everything CI runs, in order.
check: fmt-check vet build test vet-ghostty race-ghostty

clean:
	rm -rf bin dist
