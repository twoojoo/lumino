# kubeon — native Kubernetes IDE (Gio UI + k9s engine)
#
# `make run` / `make test-int` build with the `dev` tag: the binary is locked
# to the disposable kind cluster in .dev/kubeconfig and refuses any other
# kubeconfig or context. Release builds (`make build`) behave like kubectl.

# Set GIO_TAGS= (empty) once X11/Vulkan dev headers are installed:
#   sudo apt install libx11-dev libx11-xcb-dev libxkbcommon-x11-dev libxcursor-dev libxfixes-dev libvulkan-dev
GIO_TAGS ?= nox11,novulkan
comma := ,
DEV_TAGS := $(if $(GIO_TAGS),$(GIO_TAGS)$(comma))dev
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null | sed 's/^v//')

.PHONY: build build-dev run test test-int dev-up dev-down vendor-k9s \
	icons notices package-linux package-windows package-macos

build:
	go build -tags '$(GIO_TAGS)' -trimpath -ldflags '-s -w -X main.version=$(VERSION)' -o kubeon ./cmd/kubeon

build-dev:
	go build -tags '$(DEV_TAGS)' -o .dev/kubeon-dev ./cmd/kubeon

run: build-dev
	env -u KUBECONFIG .dev/kubeon-dev --kubeconfig .dev/kubeconfig $(ARGS)

test:
	go vet -tags '$(DEV_TAGS)' ./internal/... ./cmd/...
	go test -tags '$(DEV_TAGS)' ./internal/...

# integration + headless UI screenshots (in .dev/shots) against kind only;
# packages run one at a time: they share (and mutate) the cluster
test-int:
	env -u KUBECONFIG go test -p 1 -tags '$(DEV_TAGS),integration' -count=1 -timeout 600s ./internal/...

dev-up:
	./scripts/dev-cluster.sh up

dev-down:
	./scripts/dev-cluster.sh down

vendor-k9s:
	./scripts/vendor-k9s.sh && go mod tidy

# ---- release packages (dist/) ----

# icons for every package + the UI logo, from packaging/logo-source.png
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

# universal kubeon.app, .dmg and .zip (macOS host only)
package-macos:
	VERSION=$(VERSION) ./scripts/package-macos.sh
