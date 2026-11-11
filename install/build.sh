#!/usr/bin/env bash
# Cross-compile the helper for all supported targets.
#   ./install/build.sh            # version from git describe
#   VERSION=0.1.0 ./install/build.sh
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$HERE/.." && pwd)"
DIST="$ROOT/dist"
VERSION="${VERSION:-$(git -C "$ROOT" describe --tags --always --dirty 2>/dev/null || echo dev)}"

mkdir -p "$DIST"

build() {
  local goos="$1" goarch="$2" out="$3"
  echo "→ $goos/$goarch -> dist/$out"
  ( cd "$ROOT/helper" && \
    CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" \
    go build -trimpath -ldflags "-s -w -X main.version=$VERSION" -o "$DIST/$out" . )
}

build linux   amd64 awgbv-linux-amd64
build linux   arm64 awgbv-linux-arm64
build windows amd64 awgbv-windows-amd64.exe
build windows arm64 awgbv-windows-arm64.exe

( cd "$DIST" && sha256sum awgbv-* > SHA256SUMS )
echo
echo "version: $VERSION"
ls -lh "$DIST"
