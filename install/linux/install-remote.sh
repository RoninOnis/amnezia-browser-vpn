#!/usr/bin/env bash
# One-file installer: downloads the right helper binary from GitHub Releases and
# registers the Firefox native-messaging host. No repo checkout needed, no root.
#
#   VERSION=v0.1.1 bash install-linux.sh
#
# Flags / env:
#   VERSION   release tag (default: v0.1.1)
#   DRY_RUN=1 only print what would be done
set -euo pipefail

VERSION="${VERSION:-v0.1.1}"
REPO="${REPO:-RoninOnis/amnezia-browser-vpn}"
HOST_NAME="awgbv"
EXT_ID="awgbv@0xoni.ru"
BIN_DIR="${BIN_DIR:-$HOME/.local/bin}"
BIN_DST="$BIN_DIR/$HOST_NAME"

case "$(uname -s)" in
  Linux) ;;
  *) echo "error: this script is for Linux (use install-windows.ps1 on Windows)" >&2; exit 1 ;;
esac

case "$(uname -m)" in
  x86_64|amd64) ARCH=amd64 ;;
  aarch64|arm64) ARCH=arm64 ;;
  *) echo "error: unsupported architecture: $(uname -m)" >&2; exit 1 ;;
esac

ASSET="awgbv-linux-$ARCH"
BASE="https://github.com/$REPO/releases/download/$VERSION"

echo "→ платформа: linux/$ARCH, версия $VERSION"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

echo "→ скачиваю $ASSET"
curl -fsSL -o "$TMP/$ASSET" "$BASE/$ASSET"
chmod +x "$TMP/$ASSET"

if curl -fsSL -o "$TMP/SHA256SUMS" "$BASE/SHA256SUMS" 2>/dev/null; then
  if ( cd "$TMP" && grep " $ASSET\$" SHA256SUMS > sums.txt 2>/dev/null && sha256sum -c sums.txt >/dev/null 2>&1 ); then
    echo "→ sha256 совпал"
  else
    echo "! sha256 не совпал или отсутствует в SHA256SUMS — прерываю" >&2
    exit 1
  fi
else
  echo "! SHA256SUMS не скачался, продолжаю без проверки" >&2
fi

if [[ "${DRY_RUN:-0}" == "1" ]]; then
  echo "[dry-run] скопировал бы $TMP/$ASSET -> $BIN_DST"
  exit 0
fi

mkdir -p "$BIN_DIR"
install -m 0755 "$TMP/$ASSET" "$BIN_DST"
echo "→ бинарь: $BIN_DST ($("$BIN_DST" --version 2>/dev/null || echo '?'))"

write_manifest() {
  local dir="$1"
  mkdir -p "$dir"
  cat > "$dir/$HOST_NAME.json" <<EOF
{
  "name": "$HOST_NAME",
  "description": "Amnezia Browser VPN helper (AmneziaWG userspace tunnel + local SOCKS5)",
  "path": "$BIN_DST",
  "type": "stdio",
  "allowed_extensions": ["$EXT_ID"]
}
EOF
  echo "→ манифест: $dir/$HOST_NAME.json"
}

write_manifest "$HOME/.mozilla/native-messaging-hosts"
if [[ -d "$HOME/snap/firefox" ]]; then
  write_manifest "$HOME/snap/firefox/common/.mozilla/native-messaging-hosts"
  echo "  (snap Firefox обнаружен — манифест продублирован внутрь песочницы)"
fi

echo
echo "Готово. Дальше:"
echo "  1) установить подписанный .xpi: about:addons → шестерёнка → «Установить дополнение из файла»"
echo "  2) в настройках расширения вставить ключ vpn:// из приложения Amnezia"
echo
echo "Проверка без браузера:"
echo "  $BIN_DST --status"
echo "  $BIN_DST --connect /путь/к/config.conf --listen 127.0.0.1:1081"
echo "  curl --socks5-hostname 127.0.0.1:1081 https://api.ipify.org"
echo "  $BIN_DST --stop"
