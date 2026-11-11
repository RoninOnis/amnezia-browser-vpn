#!/usr/bin/env bash
# Спутник сервера AmneziaWG: слушает UDP/443 и передаёт пакеты локальному AWG.
#
# Зачем: корпоративные сети часто выпускают наружу UDP только на 443 (QUIC), а
# AmneziaWG слушает случайный порт вроде 36853. Спутник ставится на ТОМ ЖЕ сервере,
# где уже стоит Amnezia: он ничего не меняет в контейнере, не трогает ключи и не
# требует новых ссылок vpn://. Клиенту тоже ничего настраивать не нужно — наш
# хелпер сам пробует 443, если штатный порт не ответил.
#
# Запуск (root, на сервере с AmneziaWG):
#   bash install-satellite.sh --dry-run     # только посмотреть, ничего не менять
#   bash install-satellite.sh               # установить
#
# Что делает установка: кладёт бинарь в /usr/local/bin, конфиг в /etc и создаёт
# systemd-службу awgbv-satellite. Docker только читается (ps/inspect/exec), ни один
# контейнер не пересоздаётся и не перезапускается. Удаление — в конце вывода.
set -euo pipefail

TARGET=""
CONTAINER=""
LISTEN_PORT="${LISTEN_PORT:-443}"
VERSION="${VERSION:-latest}"
DRY_RUN=0
OPEN_FIREWALL=0
BASE="https://github.com/RoninOnis/amnezia-browser-vpn/releases"
TOKEN="${TOKEN:-}"

while [ $# -gt 0 ]; do
  case "$1" in
    --target)    TARGET="$2"; shift 2 ;;
    --container) CONTAINER="$2"; shift 2 ;;
    --port)      LISTEN_PORT="$2"; shift 2 ;;
    --dry-run)   DRY_RUN=1; shift ;;
    --open-firewall) OPEN_FIREWALL=1; shift ;;
    -h|--help)   sed -n '2,18p' "$0"; exit 0 ;;
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

# Порт, который реально слушает AWG внутри контейнера. Спрашиваем у конфига и у самого
# интерфейса: docker port для этого не годится — у Amnezia внутри может быть 443, а
# наружу опубликован совсем другой порт, и тогда цель была бы неверной. Именно так
# скрипт однажды принял чужой контейнер (amnezia-*) за AWG и взял порт 443.
awg_port_of() {
  local c="$1" p=""
  p="$(docker exec "$c" sh -c 'grep -rhoiE "^[[:space:]]*ListenPort[[:space:]]*=[[:space:]]*[0-9]+" /opt/amnezia/awg/ 2>/dev/null | grep -oE "[0-9]+$" | head -1' 2>/dev/null || true)"
  if [ -z "$p" ]; then
    p="$(docker exec "$c" sh -c 'awg show all 2>/dev/null | grep -i "listening port" | grep -oE "[0-9]+$" | head -1' 2>/dev/null || true)"
  fi
  if [ -z "$p" ]; then
    p="$(docker exec "$c" sh -c 'wg show all 2>/dev/null | grep -i "listening port" | grep -oE "[0-9]+$" | head -1' 2>/dev/null || true)"
  fi
  printf '%s' "$p"
}

# Адрес контейнера. Предпочитаем сеть со словом amnezia в названии, иначе первый непустой.
container_ip_of() {
  docker inspect -f '{{range $k, $v := .NetworkSettings.Networks}}{{$k}}={{$v.IPAddress}}{{"\n"}}{{end}}' "$1" 2>/dev/null \
    | awk -F= '$2!="" {print}' \
    | { grep -i amnezia || cat; } \
    | head -1 | cut -d= -f2
}

echo "→ ищу контейнеры AmneziaWG"
FOUND_ANY=0
DETECTED_PORT=""
DETECTED_IP=""
while read -r c; do
  [ -z "$c" ] && continue
  port="$(awg_port_of "$c")"
  if [ -n "$port" ]; then
    ip="$(container_ip_of "$c")"
    FOUND_ANY=1
    echo "   $c — AmneziaWG, слушает $port, адрес ${ip:-?}"
    if [ -z "$CONTAINER" ]; then
      CONTAINER="$c"; DETECTED_PORT="$port"; DETECTED_IP="$ip"
    fi
  else
    echo "   $c — не AmneziaWG (пропускаю)"
  fi
done <<EOF
$(docker ps --format '{{.Names}}')
EOF

if [ "$FOUND_ANY" = "0" ]; then
  echo "контейнер AmneziaWG не найден." >&2
  echo "Если Amnezia стоит не в docker, задайте цель вручную: --target <ip:порт>" >&2
  exit 1
fi

if [ -z "$TARGET" ]; then
  if [ -n "$DETECTED_IP" ] && [ -n "$DETECTED_PORT" ]; then
    TARGET="$DETECTED_IP:$DETECTED_PORT"
    echo "   цель: $TARGET (контейнер $CONTAINER)"
  else
    echo "не смог определить адрес/порт контейнера. Задайте вручную: --target <ip:порт>" >&2
    echo "подсказка: docker inspect -f '{{range .NetworkSettings.Networks}}{{.IPAddress}} {{end}}' $CONTAINER" >&2
    exit 1
  fi
fi

case "$TARGET" in
  127.0.0.1:*)
    echo "цель 127.0.0.1 не работает: docker не транслирует loopback, пакеты уйдут в никуда." >&2
    echo "укажите адрес контейнера внутри docker-сети" >&2
    exit 1 ;;
esac

if ss -lunp 2>/dev/null | grep -q ":${LISTEN_PORT} "; then
  BUSY="$(ss -lunp | grep ":${LISTEN_PORT} " | head -1)"
  echo "порт ${LISTEN_PORT}/udp уже занят:" >&2
  echo "   $BUSY" >&2
  echo "спутнику нужен свободный ${LISTEN_PORT}/udp. Разберитесь, что там висит." >&2
  exit 1
fi

# Файрвол хоста. Тонкость, на которой уже обожглись: порт Amnezia в docker
# публикуется через DNAT/FORWARD и политику INPUT не задевает, поэтому AWG
# работает. Спутник же слушает обычный сокет хоста, и его UDP-пакеты идут в
# INPUT: при политике DROP они тихо умирают, снаружи это выглядит как «сервер
# молчит», хотя служба запущена и порт слушается.
FW_STATE="нет"
FW_HINT=""
if command -v ufw >/dev/null 2>&1 && ufw status 2>/dev/null | head -1 | grep -qi active; then
  FW_STATE="ufw (включён)"
  if ufw status 2>/dev/null | grep -qE "^${LISTEN_PORT}/udp +ALLOW"; then
    FW_HINT=""
  else
    FW_HINT="ufw allow ${LISTEN_PORT}/udp"
  fi
elif command -v iptables >/dev/null 2>&1; then
  POLICY="$(iptables -S INPUT 2>/dev/null | head -1 | awk '{print $NF}' || true)"
  if [ "$POLICY" = "DROP" ]; then
    FW_STATE="iptables INPUT DROP"
    if ! iptables -S INPUT 2>/dev/null | grep -q -- "--dport ${LISTEN_PORT} -j ACCEPT"; then
      FW_HINT="iptables -I INPUT -p udp --dport ${LISTEN_PORT} -j ACCEPT"
    fi
  else
    FW_STATE="iptables INPUT ${POLICY:-ACCEPT}"
  fi
fi

echo
echo "План:"
echo "  бинарь:  /usr/local/bin/awgbv-satellite"
echo "  конфиг:  /etc/awgbv-satellite.env (600)"
echo "  служба:  awgbv-satellite, слушает 0.0.0.0:${LISTEN_PORT}/udp"
echo "  цель:    ${TARGET}"
echo "  docker:  только чтение, контейнеры не трогаются"
echo "  файрвол: ${FW_STATE}"

if [ -n "$FW_HINT" ]; then
  echo
  echo "ВНИМАНИЕ: ${LISTEN_PORT}/udp снаружи, скорее всего, не пройдёт — файрвол хоста."
  echo "Порт Amnezia обходит это, потому что docker публикует его через DNAT/FORWARD,"
  echo "а спутник слушает обычный сокет хоста и попадает в INPUT."
  if [ "$OPEN_FIREWALL" = "1" ]; then
    echo "→ открываю порт (--open-firewall)"
    if command -v ufw >/dev/null 2>&1 && ufw status 2>/dev/null | head -1 | grep -qi active; then
      ufw allow "${LISTEN_PORT}/udp" >/dev/null && echo "   ufw: ${LISTEN_PORT}/udp разрешён"
    else
      iptables -I INPUT -p udp --dport "${LISTEN_PORT}" -j ACCEPT
      echo "   iptables: правило добавлено"
      echo "   Оно не переживёт перезагрузку. Сохранить:"
      echo "     apt install -y iptables-persistent && netfilter-persistent save"
    fi
  else
    echo "Разрешить вручную одной из команд:"
    echo "  ${FW_HINT}"
    echo "Либо запустить этот скрипт с --open-firewall."
  fi
fi

if [ "$DRY_RUN" = "1" ]; then
  echo
  echo "--dry-run: ничего не менял. Запустите без флага, чтобы установить."
  exit 0
fi

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

echo
echo "→ скачиваю бинарь"
curl -fsSL -o "$TMP/awgbv-linux-amd64" "$BASE/$VERSION/download/awgbv-linux-amd64"
curl -fsSL -o "$TMP/SHA256SUMS" "$BASE/$VERSION/download/SHA256SUMS"
( cd "$TMP" && grep 'awgbv-linux-amd64' SHA256SUMS > sums.txt && sha256sum -c sums.txt >/dev/null ) \
  || { echo "sha256 не сошёлся — установка прервана, ничего не изменено" >&2; exit 1; }
echo "→ sha256 совпал"

install -m 0755 "$TMP/awgbv-linux-amd64" /usr/local/bin/awgbv-satellite

umask 077
cat > /etc/awgbv-satellite.env <<EOF2
TARGET=$TARGET
LISTEN=0.0.0.0:$LISTEN_PORT
TOKEN=$TOKEN
EOF2
chmod 600 /etc/awgbv-satellite.env

cat > /etc/systemd/system/awgbv-satellite.service <<'EOF2'
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
EOF2

systemctl daemon-reload
systemctl enable --now awgbv-satellite >/dev/null 2>&1
sleep 1

if systemctl is-active --quiet awgbv-satellite; then
  echo "→ спутник запущен"
  journalctl -u awgbv-satellite -n 3 --no-pager | tail -3
  echo
  echo "Проверка:"
  echo "  ss -lunp | grep ':${LISTEN_PORT} '          # порт слушается"
  echo "  journalctl -u awgbv-satellite -n 20        # что происходит"
  echo
  if [ -n "$FW_HINT" ] && [ "$OPEN_FIREWALL" != "1" ]; then
    echo "НЕ ЗАБЫТЬ: ${LISTEN_PORT}/udp закрыт файрволом хоста (${FW_HINT}),"
    echo "иначе снаружи будет тишина при внешне исправной службе."
    echo
  fi
  echo "Клиенту в расширении достаточно нажать «Подключить»: хелпер сам попробует 443."
  echo "В его логе появится: handshake ok ... (mtu 1280, endpoint <ip-сервера>:${LISTEN_PORT})"
  echo
  echo "Удалить:"
  echo "  systemctl disable --now awgbv-satellite && rm -f /etc/awgbv-satellite.env \\"
  echo "    /etc/systemd/system/awgbv-satellite.service /usr/local/bin/awgbv-satellite"
else
  echo "спутник не поднялся: journalctl -u awgbv-satellite -n 20" >&2
  echo "контейнер AmneziaWG не тронут, его штатный порт работает как раньше" >&2
  exit 1
fi
