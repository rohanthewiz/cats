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
# cats-todo lives in its own repo (github.com/rohanthewiz/cats-todo) and is
# installed through the plugin host, so it is no longer built here.
BINS     := catway cathost catctl
VERSION  := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
GOOS     := $(shell go env GOOS)
GOARCH   := $(shell go env GOARCH)
DIST     := dist/cats_$(VERSION)_$(GOOS)_$(GOARCH)

# Build stamp: the git identity linked into the binaries. catway serves it to the
# sidebar brand, so a running instance names the commit it was built from — the
# quickest way to spot a stale install. The commit subject rides base64-encoded
# because it is free to contain spaces, quotes and '$', none of which survive a
# recipe's -ldflags string intact, while base64's alphabet is inert throughout.
# Unstamped builds (plain `go build`) still show a hash — internal/buildinfo
# falls back to the toolchain's own VCS stamping.
STAMP_PKG := github.com/rohanthewiz/cats/internal/buildinfo
GIT_HASH  := $(shell git rev-parse --short HEAD 2>/dev/null)
GIT_SUBJ  := $(shell git log -1 --pretty=%s 2>/dev/null | base64 | tr -d '\n')
STAMP     := -ldflags "-X $(STAMP_PKG).hash=$(GIT_HASH) -X $(STAMP_PKG).subjectB64=$(GIT_SUBJ)"

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
	$(foreach b,$(BINS),$(GHOSTTY) go build $(TAGS) -trimpath $(STAMP) -o bin/$(b) ./cmd/$(b) &&) true
	@ls -lh bin

# --- personal install --------------------------------------------------------

# Install each shipped binary into ~/bin under a short alias.
# The map is "cmd:alias" pairs — edit here to rename or add targets. Splitting
# on ':' keeps the source dir (./cmd/$(cmd)) decoupled from the installed name.
# Every cmd named here must also be in BINS: this installs by copying bin/,
# it does not compile.
LOCAL_BIN := $(HOME)/bin
# cats-todo is deliberately absent: it lives in its own repo and is built by
# the plugin host (`catctl plugin install rohanthewiz/cats-todo` runs the
# manifest's build step), so the local install stays limited to the binaries
# the plugin flow can't provide.
LOCAL_MAP := catway:catway cathost:cathost catctl:catctl

# Copies from bin/ rather than compiling a second time, so a ~/bin install and
# an .app bundle made from the same tree hold byte-identical daemons — two
# compiles of the same source can still differ (a mid-build edit, a stale
# PKG_CONFIG_PATH), and "same bytes" is the only version claim worth making.
# Copy-then-rename because overwriting a *running* binary in place fails with
# ETXTBSY on macOS; a rename swaps the directory entry and leaves the live
# process on its old inode.
local: binaries
	@mkdir -p $(LOCAL_BIN)
	$(foreach p,$(LOCAL_MAP),cp bin/$(word 1,$(subst :, ,$(p))) \
	    $(LOCAL_BIN)/$(word 2,$(subst :, ,$(p))).new && \
	    mv -f $(LOCAL_BIN)/$(word 2,$(subst :, ,$(p))).new \
	    $(LOCAL_BIN)/$(word 2,$(subst :, ,$(p))) &&) true
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

# Where `macapp` installs the finished bundle. /Applications is root:admin with
# group write, so an admin account needs no sudo; a non-admin one does
# (`sudo make macapp` — or point this at ~/Applications, which needs no
# privilege at all). One destination only: a second registered copy of
# $(APP_ID) makes "which build launches" a LaunchServices coin toss.
APP_DEST := /Applications

# macapp (Variant 1, self-contained): launcher + the three static ghostty daemons
# → dist/Cats.app. Runs fully local. Depends on `binaries` for the daemons; the
# vendored VT engine must be built first (`make vt`).
#
# Also depends on `local`: the bundle and ~/bin are two installs of the same
# three daemons, and updating one alone is how they drift — a `catctl` on $PATH
# older than the running app speaks a vocabulary the app has moved past, and the
# version each reports stops meaning anything. Both come off one `binaries`
# build, so this bundles and installs the same bytes in one pass.
#
# The install replaces the bundle wholesale rather than copying *into* the old
# one: cp -R over a live bundle merges, leaving whatever files the new build no
# longer ships behind in Contents/MacOS. It is staged next to the target (same
# filesystem) so the swap is a rename. Removing the bundle of a *running* app is
# safe — the live process holds its inodes and keeps going; it simply is not the
# version on disk until relaunched, which is what the printed version is for.
macapp: binaries local
	scripts/build-macapp.sh self "Cats" $(APP_ID) $(VERSION)
	@echo "==> installing to $(APP_DEST)/Cats.app"
	@rm -rf "$(APP_DEST)/Cats.app.new"
	cp -R "dist/Cats.app" "$(APP_DEST)/Cats.app.new"
	rm -rf "$(APP_DEST)/Cats.app"
	mv "$(APP_DEST)/Cats.app.new" "$(APP_DEST)/Cats.app"
	@/usr/libexec/PlistBuddy -c "Print :CFBundleShortVersionString" \
	    "$(APP_DEST)/Cats.app/Contents/Info.plist"

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
