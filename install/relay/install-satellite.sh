#!/usr/bin/env bash
# Спутник сервера AmneziaWG: слушает UDP/443 и передаёт пакеты локальному AWG.
#
# Зачем: корпоративные сети часто выпускают наружу UDP только на 443 (QUIC), а
# AmneziaWG слушает случайный порт вроде 36853. Спутник ставится на ТОМ ЖЕ сервере,
# где уже стоит Amnezia: он ничего не меняет в контейнере, не трогает ключи и не
# требует новых ссылок vpn://. Клиенту тоже ничего настраивать не нужно — наш
# хелпер сам пробует 443, если штатный порт не отвечает.
#
# Запуск (root, на сервере с AmneziaWG):
#   bash install-satellite.sh
#
# Скрипт сам находит контейнер AmneziaWG, его адрес и порт внутри docker.
# Если автоопределение не сработало — задайте цель вручную:
#   bash install-satellite.sh --target 172.29.172.2:37689
set -euo pipefail

TARGET=""
LISTEN_PORT="${LISTEN_PORT:-443}"
VERSION="${VERSION:-latest}"
BASE="https://github.com/RoninOnis/amnezia-browser-vpn/releases/${VERSION}/download"
TOKEN="${TOKEN:-}"

while [ $# -gt 0 ]; do
  case "$1" in
    --target) TARGET="$2"; shift 2 ;;
    --port)   LISTEN_PORT="$2"; shift 2 ;;
    -h|--help) sed -n '2,20p' "$0"; exit 0 ;;
    *) echo "неизвестный аргумент: $1" >&2; exit 1 ;;
  esac
done

if [ "$(id -u)" != "0" ] && [ "${FORCE_ROOT:-0}" != "1" ]; then
  echo "нужен root (запустите через sudo)" >&2
  exit 1
fi

if ! command -v docker >/dev/null 2>&1; then
  echo "docker не найден: этот скрипт ставится на сервер, где уже работает AmneziaWG" >&2
  exit 1
fi

if [ -z "$TARGET" ]; then
  echo "→ ищу контейнер AmneziaWG"
  CONTAINER="$(docker ps --format '{{.Names}}' | grep -iE 'awg|amnezia' | head -1 || true)"
  if [ -z "$CONTAINER" ]; then
    echo "контейнер AmneziaWG не найден. Запустите с --target <ip:порт>" >&2
    echo "подсказка: docker ps --format '{{.Names}}\t{{.Ports}}'" >&2
    exit 1
  fi
  IP="$(docker inspect -f '{{range .NetworkSettings.Networks}}{{.IPAddress}} {{end}}' "$CONTAINER" | awk '{print $1}')"
  PORT="$(docker port "$CONTAINER" | head -1 | sed 's|.*/udp||; s|.*-> ||' | cut -d: -f2)"
  if [ -z "$IP" ] || [ -z "$PORT" ]; then
    echo "не смог определить адрес/порт контейнера $CONTAINER. Запустите с --target <ip:порт>" >&2
    docker ps --format '{{.Names}}\t{{.Ports}}' >&2
    exit 1
  fi
  TARGET="$IP:$PORT"
  echo "   контейнер $CONTAINER → цель $TARGET"
fi

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

echo "→ скачиваю бинарь"
curl -fsSL -o "$TMP/awgbv" "$BASE/awgbv-linux-amd64"
curl -fsSL -o "$TMP/SHA256SUMS" "$BASE/SHA256SUMS"
( cd "$TMP" && grep 'awgbv-linux-amd64' SHA256SUMS > sums.txt && sha256sum -c sums.txt >/dev/null ) \
  || { echo "sha256 не сошёлся — установка прервана" >&2; exit 1; }
echo "→ sha256 совпал"

install -m 0755 "$TMP/awgbv" /usr/local/bin/awgbv-satellite

umask 077
cat > /etc/awgbv-satellite.env <<EOF
TARGET=$TARGET
LISTEN=0.0.0.0:$LISTEN_PORT
TOKEN=$TOKEN
EOF
chmod 600 /etc/awgbv-satellite.env

cat > /etc/systemd/system/awgbv-satellite.service <<'EOF'
[Unit]
Description=awgbv satellite (AmneziaWG reachable on udp/443)
After=network-online.target docker.service
Wants=network-online.target

[Service]
Type=simple
EnvironmentFile=/etc/awgbv-satellite.env
ExecStart=/usr/local/bin/awgbv-satellite --relay-serve --relay-listen ${LISTEN} --relay-target ${TARGET} --relay-token ${TOKEN}
Restart=always
RestartSec=3
NoNewPrivileges=yes
PrivateTmp=yes

[Install]
WantedBy=multi-user.target
EOF

systemctl daemon-reload
systemctl enable --now awgbv-satellite >/dev/null 2>&1
sleep 1

if systemctl is-active --quiet awgbv-satellite; then
  echo "→ спутник запущен"
  journalctl -u awgbv-satellite -n 3 --no-pager | tail -3
  echo
  echo "Проверка:"
  echo "  1) порт слушается:  ss -lunp | grep ':${LISTEN_PORT} '"
  echo "  2) снаружи в расширении просто нажать «Подключить»: хелпер сам попробует 443"
  echo "  3) в логе клиента должно появиться:"
  echo "     handshake ok after ... (mtu 1280, endpoint <ip-сервера>:${LISTEN_PORT})"
  echo
  echo "Удалить: systemctl disable --now awgbv-satellite && rm -f /etc/awgbv-satellite.env \\"
  echo "         /etc/systemd/system/awgbv-satellite.service /usr/local/bin/awgbv-satellite"
else
  echo "спутник не поднялся, смотрите: journalctl -u awgbv-satellite -n 20" >&2
  exit 1
fi
