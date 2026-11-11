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
    go build -trimpath -ldflags "-s -w -buildid= -X main.version=$VERSION" -o "$DIST/$out" . )
}

# arm64 обязателен: установщик определяет архитектуру и на Windows on ARM просит
# awgbv-windows-arm64.exe — без него установка падала на 404.
ARCHS="${ARCHS:-amd64 arm64}"

for arch in $ARCHS; do
  build linux   "$arch" "awgbv-linux-$arch"
  build windows "$arch" "awgbv-windows-$arch.exe"
done

( cd "$DIST" && sha256sum awgbv-* > SHA256SUMS )
echo
echo "version: $VERSION"
ls -lh "$DIST"
