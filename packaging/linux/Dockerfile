# Release build environment for Linux: Ubuntu 22.04 keeps the glibc baseline
# low (2.35) so the binary runs on most current distributions, and has the
# Wayland, X11, EGL and Vulkan headers for a full Gio build.
FROM ubuntu:22.04

ARG GO_VERSION=1.25.8
ARG APPIMAGETOOL_VERSION=1.9.0
ARG NFPM_VERSION=2.43.0

ENV DEBIAN_FRONTEND=noninteractive
RUN apt-get update && apt-get install -y --no-install-recommends \
      ca-certificates curl desktop-file-utils file git gcc libc6-dev pkg-config xz-utils zip \
      libwayland-dev libx11-dev libx11-xcb-dev libxkbcommon-x11-dev libxkbcommon-dev \
      libgles2-mesa-dev libegl1-mesa-dev libffi-dev libxcursor-dev libxfixes-dev libvulkan-dev \
    && rm -rf /var/lib/apt/lists/*

RUN set -eux; arch="$(dpkg --print-architecture)"; \
    curl -fsSL "https://go.dev/dl/go${GO_VERSION}.linux-${arch}.tar.gz" | tar -C /usr/local -xz; \
    case "$arch" in amd64) aarch=x86_64 ;; arm64) aarch=aarch64 ;; esac; \
    curl -fsSL -o /usr/local/bin/appimagetool \
      "https://github.com/AppImage/appimagetool/releases/download/${APPIMAGETOOL_VERSION}/appimagetool-${aarch}.AppImage"; \
    chmod +x /usr/local/bin/appimagetool; \
    curl -fsSL "https://github.com/goreleaser/nfpm/releases/download/v${NFPM_VERSION}/nfpm_${NFPM_VERSION}_Linux_${aarch}.tar.gz" \
      | tar -C /usr/local/bin -xz nfpm

# appimagetool is itself an AppImage: no FUSE inside containers
ENV PATH=/usr/local/go/bin:$PATH \
    GOTOOLCHAIN=local \
    APPIMAGE_EXTRACT_AND_RUN=1
