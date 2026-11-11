#!/usr/bin/env bash
# Install the Amnezia Browser VPN helper on Linux (Firefox native messaging).
#
#   ./install/linux/install.sh [path-to-awgbv-binary]
#
# Installs:
#   ~/.local/bin/awgbv                          the helper binary
#   ~/.mozilla/native-messaging-hosts/awgbv.json  native-messaging manifest
#   ~/snap/firefox/common/.mozilla/native-messaging-hosts/awgbv.json  (if Firefox is the snap)
set -euo pipefail

EXT_ID="awgbv@0xoni.ru"
HOST_NAME="awgbv"

if [[ "${EUID}" -eq 0 && -n "${SUDO_USER:-}" ]]; then
  cat >&2 <<'EOF'
error: скрипт запущен через sudo.

Native messaging — per-user штука: манифест должен лежать в домашнем каталоге
того, от чьего имени работает Firefox. Через sudo он попадёт в /root и Firefox
его не увидит (симптом: "No such native application awgbv").

Запустите БЕЗ sudo:
    bash install-linux.sh

Сначала уберите то, что уже установилось в /root:
    sudo rm -f /root/.local/bin/awgbv \
               /root/.mozilla/native-messaging-hosts/awgbv.json \
               /root/snap/firefox/common/.mozilla/native-messaging-hosts/awgbv.json
EOF
  exit 1
fi
if [[ "${EUID}" -eq 0 && "${FORCE_ROOT:-0}" != "1" ]]; then
  echo "warning: вы root — манифест попадёт в ${HOME}. Если это не то, что нужно, прервите (Ctrl+C)." >&2
  echo "         продолжить всё равно: FORCE_ROOT=1 bash install-linux.sh" >&2
  exit 1
fi

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
case "$(uname -m)" in
  x86_64|amd64) ARCH=amd64 ;;
  aarch64|arm64) ARCH=arm64 ;;
  *) ARCH=amd64 ;;
esac
BIN_SRC="${1:-}"
if [[ -z "$BIN_SRC" ]]; then
  for cand in "$HERE/../../dist/awgbv-linux-$ARCH" "$HOME/Downloads/awgbv-linux-$ARCH"; do
    if [[ -f "$cand" ]]; then BIN_SRC="$cand"; break; fi
  done
fi
BIN_SRC="${BIN_SRC:-$HERE/../../dist/awgbv-linux-$ARCH}"

if [[ ! -f "$BIN_SRC" ]]; then
  echo "error: helper binary not found: $BIN_SRC" >&2
  echo "       build it first: ./install/build.sh" >&2
  exit 1
fi

BIN_DIR="$HOME/.local/bin"
BIN_DST="$BIN_DIR/awgbv"
mkdir -p "$BIN_DIR"
install -m 0755 "$BIN_SRC" "$BIN_DST"
echo "installed binary: $BIN_DST"

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
  echo "installed manifest: $dir/$HOST_NAME.json"
}

write_manifest "$HOME/.mozilla/native-messaging-hosts"

if [[ -d "$HOME/snap/firefox" ]]; then
  write_manifest "$HOME/snap/firefox/common/.mozilla/native-messaging-hosts"
  echo
  echo "note: snap Firefox detected — manifest also installed inside the snap home."
fi

cat <<EOF

Готово. Дальше:

1) Откройте Firefox. Если планируете ставить .xpi с подписью — установите его.
   Для разработки: about:debugging -> «Это Firefox» -> «Загрузить временное дополнение»
   -> выберите extension/manifest.json

2) В настройках расширения вставьте ключ vpn://... из приложения Amnezia
   (или из клиента: «Поделиться подключением» -> копировать vpn:// ссылку).

3) Проверка без браузера:
   awgbv --status
   awgbv --connect /path/to/config.conf --listen 127.0.0.1:1081
   curl --socks5-hostname 127.0.0.1:1081 https://api.ipify.org
   awgbv --stop

Лог хелпера: ~/.local/state/awgbv/helper.log
Удалить: $HERE/uninstall.sh
EOF
