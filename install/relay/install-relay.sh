#!/usr/bin/env bash
# Установка релея awgbv на сервер (нужен root).
#
# Релей слушает UDP/443 и пересылает пакеты на настоящий сервер AmneziaWG.
# Нужен там, где наружу пускают только UDP 443 (QUIC): корпоративные сети,
# Cisco AnyConnect и подобные. На сервере Amnezia ничего менять не нужно —
# WireGuard проверяет пира по ключам и запоминает адрес, с которого пришёл
# валидный handshake.
#
# Использование:
#   bash install-relay.sh --token ВАШ_ТОКЕН --allow 203.0.113.10:36853[,второй:порт]
#
# Важно: релей должен стоять на ДРУГОМ хосте, не на том же, где сервер AmneziaWG —
# иначе ответы сервера уходят на его же публичный IP и не возвращаются (hairpin).
set -euo pipefail

TOKEN=""
ALLOW=""
LISTEN="0.0.0.0:443"
VERSION="${VERSION:-latest}"
BASE="https://github.com/RoninOnis/amnezia-browser-vpn/releases/${VERSION}/download"

while [ $# -gt 0 ]; do
  case "$1" in
    --token)  TOKEN="$2"; shift 2 ;;
    --allow)  ALLOW="$2"; shift 2 ;;
    --listen) LISTEN="$2"; shift 2 ;;
    -h|--help) sed -n '2,20p' "$0"; exit 0 ;;
    *) echo "неизвестный аргумент: $1" >&2; exit 1 ;;
  esac
done

if [ "$(id -u)" != "0" ] && [ "${FORCE_ROOT:-0}" != "1" ]; then
  echo "нужен root (запустите через sudo)" >&2
  exit 1
fi

if [ -z "$TOKEN" ] || [ -z "$ALLOW" ]; then
  echo "нужны --token и --allow, например:" >&2
  echo "  bash install-relay.sh --token 0123456789abcdef --allow 203.0.113.10:36853" >&2
  exit 1
fi

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

echo "→ скачиваю бинарь"
curl -fsSL -o "$TMP/awgbv" "$BASE/awgbv-linux-amd64"
curl -fsSL -o "$TMP/SHA256SUMS" "$BASE/SHA256SUMS"
( cd "$TMP" && grep 'awgbv-linux-amd64' SHA256SUMS > sums.txt && sha256sum -c sums.txt >/dev/null ) \
  || { echo "sha256 не сошёлся — установка прервана" >&2; exit 1; }
echo "→ sha256 совпал"

install -m 0755 "$TMP/awgbv" /usr/local/bin/awgbv-relay

umask 077
cat > /etc/awgbv-relay.env <<EOF
TOKEN=$TOKEN
ALLOW=$ALLOW
LISTEN=$LISTEN
EOF
chmod 600 /etc/awgbv-relay.env

cat > /etc/systemd/system/awgbv-relay.service <<'EOF'
[Unit]
Description=awgbv relay (AmneziaWG over udp/443)
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
EnvironmentFile=/etc/awgbv-relay.env
ExecStart=/usr/local/bin/awgbv-relay --relay-serve --relay-listen ${LISTEN} --relay-token ${TOKEN} --relay-allow ${ALLOW}
Restart=always
RestartSec=3
NoNewPrivileges=yes
PrivateTmp=yes

[Install]
WantedBy=multi-user.target
EOF

systemctl daemon-reload
systemctl enable --now awgbv-relay >/dev/null 2>&1
sleep 1

if systemctl is-active --quiet awgbv-relay; then
  echo "→ релей запущен"
  journalctl -u awgbv-relay -n 3 --no-pager | tail -3
  echo
  echo "Проверка порта:"
  ss -lunp | grep -E ":${LISTEN##*:} " || true
  echo
  echo "Клиенту в настройках расширения нужно указать:"
  echo "  Релей: $(curl -fsS --max-time 5 https://api.ipify.org 2>/dev/null || echo "<публичный-ip>"):${LISTEN##*:}"
  echo "  Токен: $TOKEN"
else
  echo "релей не поднялся, смотрите: journalctl -u awgbv-relay -n 20" >&2
  exit 1
fi
