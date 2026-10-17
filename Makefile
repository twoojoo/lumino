# lumino — native database IDE (Gio UI)
#
# `make run` / `make test-int` build with the `dev` tag: the binary only
# connects to the disposable databases of scripts/dbs (127.0.0.1:55xxx) and
# keeps its settings in .dev/config. Release builds (`make build`) connect
# anywhere.

# Set GIO_TAGS= (empty) once X11/Vulkan dev headers are installed:
#   sudo apt install libx11-dev libx11-xcb-dev libxkbcommon-x11-dev libxcursor-dev libxfixes-dev libvulkan-dev
GIO_TAGS ?= nox11,novulkan
comma := ,
DEV_TAGS := $(if $(GIO_TAGS),$(GIO_TAGS)$(comma))dev
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null | sed 's/^v//')

.PHONY: build build-dev run test test-int test-ui db-up db-down \
	icons notices package-linux package-windows package-macos site site-shots

build:
	go build -tags '$(GIO_TAGS)' -trimpath -ldflags '-s -w -X main.version=$(VERSION)' -o lumino ./cmd/lumino

build-dev:
	go build -tags '$(DEV_TAGS)' -o .dev/lumino-dev ./cmd/lumino

run: build-dev
	LUMINO_CONFIG_DIR=$(CURDIR)/.dev/config .dev/lumino-dev $(ARGS)

test:
	go vet -tags '$(DEV_TAGS)' ./internal/... ./cmd/...
	go test -tags '$(DEV_TAGS)' ./internal/...

# conformance suite of every adapter + headless UI tests (screenshots in
# .dev/shots) against the development databases (make db-up)
test-int:
	go test -p 1 -tags '$(DEV_TAGS),integration' -count=1 -timeout 1200s ./internal/...

test-ui:
	go test -tags '$(DEV_TAGS),integration' -count=1 -timeout 600s ./internal/ui/...

db-up:
	./scripts/dbs/dbs.sh up $(DBS)

db-down:
	./scripts/dbs/dbs.sh down

# ---- website (GitHub Pages, published by .github/workflows/pages.yml) ----
# static site in dist/site; preview: python3 -m http.server -d dist/site
site:
	go run ./scripts/sitegen -out dist/site

# screenshots of the website (internal/site/static/img), headless, from SQLite
site-shots:
	LUMINO_SITE_SHOTS=$(CURDIR)/internal/site/static/img go test -tags '$(DEV_TAGS),integration' -count=1 \
		-run TestUISiteScreenshots ./internal/ui/

# ---- release packages (dist/) ----

# icons for every package, the UI logo and the website logo, from packaging/logo-source.png
icons:
	go run ./scripts/icongen -out packaging/icons

# dist/THIRD_PARTY_NOTICES.txt (shipped in every package)
notices:
	./scripts/notices.sh

# AppImage, .deb, .rpm, .tar.gz (built in Docker, full Wayland+X11+Vulkan)
package-linux:
	VERSION=$(VERSION) ./scripts/package-linux.sh

# portable zips for amd64 and arm64 (cross-compiled, no cgo)
package-windows:
	VERSION=$(VERSION) ./scripts/package-windows.sh

# universal Lumino.app, .dmg and .zip (macOS host only)
package-macos:
	VERSION=$(VERSION) ./scripts/package-macos.sh
