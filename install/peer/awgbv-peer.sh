#!/usr/bin/env bash
# awgbv-peer — операции с пирами AmneziaWG по запросу от хелпера.
#
# Устанавливается на сервер один раз (см. install-peer.sh) и вызывается только
# через forced command: ssh-ключ в authorized_keys содержит
#   restrict,command="sudo /usr/local/sbin/awgbv-peer"
# поэтому произвольную команду этим ключом выполнить нельзя — только list/add/remove.
#
# В stdout идёт только JSON, диагностика — в stderr.
# Контейнер не перезапускается: изменения применяются `awg syncconf`, иначе
# остальные клиенты теряли бы сессию при каждом новом пире.
#
# Реализация собственная (MIT). Скрипты Amnezia под GPL-3.0, копировать их сюда нельзя.
#
# Отладка без docker: AWGBV_PEER_LOCAL_DIR=/path/to/fake-opt/amnezia/awg bash awgbv-peer.sh list
# (в этом каталоге должны лежать awg0.conf и, для применения, shim-ы awg/wg в PATH).

set -euo pipefail

LOCAL_DIR="${AWGBV_PEER_LOCAL_DIR:-}"
if [ -n "$LOCAL_DIR" ]; then
  CONF_DIR="$LOCAL_DIR"
else
  CONF_DIR="/opt/amnezia/awg"
fi
META_FILE="$CONF_DIR/awgbv-peers.txt"
LOCK_FILE="${AWGBV_PEER_LOCK:-/run/awgbv-peer.lock}"
CTR=""

log() { printf '%s\n' "$*" >&2; }
die() { printf 'ОШИБКА: %s\n' "$*" >&2; exit 1; }

# Единая точка «выполнить в контейнере». В локальном режиме — тот же bash здесь.
in_ctr() {
  if [ -n "$LOCAL_DIR" ]; then
    bash -c "$1"
  else
    docker exec "$CTR" bash -c "$1"
  fi
}

# То же, но со stdin (для записи файлов и syncconf из process substitution).
in_ctr_stdin() {
  if [ -n "$LOCAL_DIR" ]; then
    bash -c "$1"
  else
    docker exec -i "$CTR" bash -c "$1"
  fi
}

need_root() {
  [ -n "$LOCAL_DIR" ] && return 0
  [ "$(id -u)" = "0" ] || die "нужен root (скрипт запускается через sudo)"
}

# --- поиск контейнера и конфига ---------------------------------------------

find_container() {
  [ -n "$LOCAL_DIR" ] && { CTR="local"; return 0; }
  local names name
  names=$(docker ps --format '{{.Names}}' 2>/dev/null | grep -E '^amnezia-(awg|awg2|wireguard)$' || true)
  [ -n "$names" ] || die "не найден контейнер AmneziaWG (amnezia-awg / amnezia-awg2 / amnezia-wireguard)"
  while read -r name; do
    [ -n "$name" ] || continue
    if docker exec "$name" test -d "$CONF_DIR" 2>/dev/null; then
      CTR="$name"
      return 0
    fi
  done <<< "$names"
  die "в найденных контейнерах нет $CONF_DIR"
}

find_conf() {
  local f
  for f in awg0.conf wg0.conf; do
    if in_ctr "test -f '$CONF_DIR/$f'"; then
      printf '%s' "$f"
      return 0
    fi
  done
  die "не найден конфиг в $CONF_DIR (ожидался awg0.conf или wg0.conf)"
}

tool_name=""
pick_tool() {
  if in_ctr "command -v awg >/dev/null"; then
    tool_name="awg"
  elif in_ctr "command -v wg >/dev/null"; then
    tool_name="wg"
  else
    die "в контейнере нет ни awg, ни wg"
  fi
}

conf_subnet() {  # имя конфига
  # Address в конфиге — адрес самого сервера (10.8.1.1/24), а нужен адрес сети:
  # иначе выбор свободного /32 для пира пойдёт от чужой базы
  in_ctr "awk -F'[ =/]+' '/^Address/{
      split(\$2, q, \".\"); q[4] = 0;
      printf \"%s.%s.%s.0/%s\n\", q[1], q[2], q[3], \$3; exit
    }' '$CONF_DIR/$1'"
}

conf_read() { in_ctr "cat '$CONF_DIR/$1'"; }

# Публичный ключ сервера нужен клиенту для его конфига. Приватный не отдаём
# никогда: он остаётся только на сервере.
conf_public_key() {  # имя конфига
  local conf="$1" iface="${1%.conf}" pk=""
  # ключ WireGuard/AmneziaWG — base64 на 43 символа плюс «=»; всё остальное
  # (например, целый дамп от старого awg) отбрасываем
  is_key() { [ "${#1}" -ge 43 ] && [ "${#1}" -le 44 ] && case "$1" in *[!A-Za-z0-9+/=]*) return 1;; esac; }
  pk=$(in_ctr "$tool_name show '$iface' public-key 2>/dev/null || true" | tr -d '\r\n ' || true)
  is_key "$pk" || pk=""
  if [ -z "$pk" ]; then
    # запасной путь: посчитать из приватного ключа, не выводя его наружу
    pk=$(in_ctr "awk -F' = ' '/^PrivateKey/{print \$2; exit}' '$CONF_DIR/$conf' | $tool_name pubkey 2>/dev/null || true" | tr -d '\r\n ' || true)
    is_key "$pk" || pk=""
  fi
  printf '%s' "$pk"
}

# Параметры обфускации AmneziaWG — общие для сервера и клиента. Без них
# handshake не пройдёт, поэтому клиентский конфиг собирается только с ними.
# Печатает ПЛОСКИЕ строки "ключ=значение": собирать JSON прямо в awk пришлось бы
# с тройным экранированием кавычек, а разбирать такой вывод потом всё равно в Go.
conf_params() {  # имя конфига
  # Никаких двойных кавычек внутри: строка идёт в bash -c внутри "...", и bash
  # съел бы "" (awk получал sub(/…/, , line) — синтаксическая ошибка).
  # Берём только блок [Interface]: Jc/S1/H1 описывают сервер, а не пиров.
  # диапазон /^\[Interface\]/,/^\[/ не годится: конечный шаблон совпадает с самой
  # первой строкой, и блок получается пустым. Поэтому флаг.
  in_ctr "awk '/^\[Interface\]/{f=1;next} /^\[/{f=0} f' '$CONF_DIR/$1' \
    | grep -E '^(Jc|Jmin|Jmax|S1|S2|S3|S4|H1|H2|H3|H4|I1|I2|I3|I4|I5|HeaderProtectionKey)[[:space:]]*=' \
    | tr -d ' \t' | sed 's/[#].*//' || true"
}

# атомарная запись: сначала .tmp рядом, потом подмена; бэкап уже сделан
conf_write() {  # stdin -> конфиг
  local conf="$1" tmp="$CONF_DIR/$1.tmp"
  if [ -n "$LOCAL_DIR" ]; then
    cat > "$tmp" && cp "$CONF_DIR/$conf" "$CONF_DIR/$conf.bak" && mv "$tmp" "$CONF_DIR/$conf"
  else
    docker exec -i "$CTR" bash -c "cat > '$tmp' && cp '$CONF_DIR/$conf' '$CONF_DIR/$conf.bak' && mv '$tmp' '$CONF_DIR/$conf'"
  fi
}

apply_conf() {  # имя конфига
  local conf="$1" iface="${1%.conf}" stripped="$CONF_DIR/$1.stripped"
  # syncconf принимает путь к файлу — process substitution здесь лишняя и в
  # контейнере ведёт себя по-разному; strip нужен, потому что syncconf не
  # понимает Address/PostUp и падает на них
  in_ctr "$tool_name-quick strip '$CONF_DIR/$conf' > '$stripped'" || return 1
  in_ctr "$tool_name syncconf '$iface' '$stripped'"
  local rc=$?
  in_ctr "rm -f '$stripped'"
  return $rc
}

restore_conf() {  # имя конфига
  in_ctr "mv -f '$CONF_DIR/$1.bak' '$CONF_DIR/$1'"
}

peer_present() {  # iface pubkey
  in_ctr "$tool_name show '$1' dump | grep -q '$2'"
}

# --- метаданные (имя клиента) ----------------------------------------------
# Построчный файл "pubkey<TAB>имя": в контейнере нет python, а JSON без него
# правится только костылями.

meta_init() { in_ctr "test -f '$META_FILE' || : > '$META_FILE'"; }

meta_set() {  # pubkey name
  local pub="$1" safe
  safe=$(printf '%s' "$2" | tr -d '\t\r\n' | cut -c1-40)
  in_ctr "grep -v '^$pub' '$META_FILE' > '$META_FILE.tmp' 2>/dev/null || :; printf '%s\t%s\n' '$pub' '$safe' >> '$META_FILE.tmp'; mv '$META_FILE.tmp' '$META_FILE'"
}

meta_del() {  # pubkey
  in_ctr "grep -v '^$1' '$META_FILE' > '$META_FILE.tmp' 2>/dev/null || :; mv '$META_FILE.tmp' '$META_FILE'"
}

meta_text() { in_ctr "cat '$META_FILE' 2>/dev/null || true"; }

# --- вывод -----------------------------------------------------------------

peers_json() {  # conf
  local conf="$1" iface="${1%.conf}" dump subnet names pubkey params
  dump=$(in_ctr "$tool_name show '$iface' dump" 2>/dev/null) || die "не удалось получить список пиров"
  subnet=$(conf_subnet "$conf")
  names=$(meta_text)
  pubkey=$(conf_public_key "$conf")
  # параметры обфускации — массив строк "ключ=значение", JSON собирает Go
  params=$(conf_params "$conf" | awk 'NF { printf "%s\"%s\"", (n++ ? "," : ""), $0 }' || true)
  [ -n "${AWGBV_PEER_DEBUG:-}" ] && log "debug: pubkey='$pubkey' params='$params'"

  printf '%s' "$dump" | awk -v subnet="$subnet" -v names="$names" -v conf="$conf" -v ctr="${CTR}" \
      -v pubkey="$pubkey" -v params="$params" '
    function esc(s){ gsub(/\\/,"\\\\",s); gsub(/"/,"\\\"",s); return s }
    function name_of(pub,  n,i,parts,line){
      n = split(names, parts, "\n")
      for (i = 1; i <= n; i++) {
        line = parts[i]
        if (index(line, pub "\t") == 1) return substr(line, length(pub) + 2)
      }
      return ""
    }
    NR==1 {
      printf "{\"container\":\"%s\",\"config\":\"%s\",", esc(ctr), esc(conf)
      printf "\"listenPort\":%s,\"subnet\":\"%s\",", ($3+0), esc(subnet)
      printf "\"serverPublicKey\":\"%s\",\"params\":[%s],\"peers\":[", esc(pubkey), params
      next
    }
    {
      if (count++) printf ","
      printf "{\"publicKey\":\"%s\",\"presharedKey\":\"%s\",\"endpoint\":\"%s\",\"allowedIps\":\"%s\",\"lastHandshake\":%s,\"rx\":%s,\"tx\":%s,\"name\":\"%s\"}",
        $1, $2, ($3=="(none)"?"":$3), $4, ($5+0), ($6+0), ($7+0), esc(name_of($1))
    }
    END { printf "]}\n" }
  '
}

# --- глаголы ---------------------------------------------------------------

cmd_selftest() {
  local conf
  find_container; conf=$(find_conf); pick_tool
  printf '{"ok":true,"container":"%s","config":"%s","tool":"%s","subnet":"%s"}\n' \
    "$CTR" "$conf" "$tool_name" "$(conf_subnet "$conf")"
}

cmd_list() {
  local conf
  find_container; conf=$(find_conf); pick_tool; meta_init
  peers_json "$conf"
}

cmd_add() {
  local pub="" psk="" name=""
  while [ $# -gt 0 ]; do
    case "$1" in
      --pubkey) pub="${2:-}"; shift 2 ;;
      --psk) psk="${2:-}"; shift 2 ;;
      --name) name="${2:-}"; shift 2 ;;
      *) die "неизвестный аргумент: $1" ;;
    esac
  done
  [[ "$pub" =~ ^[A-Za-z0-9+/]{43}=$ ]] || die "некорректный публичный ключ"
  [[ "$psk" =~ ^[A-Za-z0-9+/]{43}=$ ]] || die "некорректный PSK"
  [ -n "$name" ] || name="client"

  local conf iface subnet base
  find_container; conf=$(find_conf); pick_tool; meta_init
  iface="${conf%.conf}"
  subnet=$(conf_subnet "$conf")
  [ -n "$subnet" ] || die "не удалось определить подсеть из Address в конфиге"
  base=$(printf '%s' "$subnet" | cut -d/ -f1 | awk -F. '{print $1"."$2"."$3}')

  if in_ctr "grep -q '$pub' '$CONF_DIR/$conf'"; then
    die "пир с таким ключом уже есть в конфиге"
  fi

  # свободный адрес: берём занятые AllowedIPs и ищем первый пропуск
  local used ip n
  used=$(in_ctr "grep -E '^AllowedIPs' '$CONF_DIR/$conf' | cut -d= -f2 | tr -d ' ' | cut -d/ -f1")
  ip=""
  for n in $(seq 2 254); do
    if ! printf '%s\n' "$used" | grep -qx "$base.$n"; then
      ip="$base.$n"; break
    fi
  done
  [ -n "$ip" ] || die "в подсети $subnet нет свободных адресов"

  {
    conf_read "$conf"
    printf '[Peer]\nPublicKey = %s\nPresharedKey = %s\nAllowedIPs = %s/32\n' "$pub" "$psk" "$ip"
  } | conf_write "$conf"

  if [ -n "${AWGBV_PEER_DEBUG:-}" ]; then
    log "debug: конфиг перед применением:"
    conf_read "$conf" >&2
    log "debug: подсеть $subnet, выбран адрес $ip"
  fi

  if ! apply_conf "$conf" || ! peer_present "$iface" "$pub"; then
    # откат обязателен: кривой конфиг убьёт VPN при следующем рестарте контейнера
    restore_conf "$conf"
    apply_conf "$conf" 2>/dev/null || true
    die "не удалось применить конфиг, изменения отменены"
  fi
  meta_set "$pub" "$name"
  printf '{"ok":true,"publicKey":"%s","allowedIps":"%s/32","name":"%s"}\n' "$pub" "$ip" "$name"
}

cmd_remove() {
  local pub=""
  while [ $# -gt 0 ]; do
    case "$1" in
      --pubkey) pub="${2:-}"; shift 2 ;;
      *) die "неизвестный аргумент: $1" ;;
    esac
  done
  [[ "$pub" =~ ^[A-Za-z0-9+/]{43}=$ ]] || die "некорректный публичный ключ"

  local conf iface filtered
  find_container; conf=$(find_conf); pick_tool; meta_init
  iface="${conf%.conf}"

  in_ctr "grep -q '$pub' '$CONF_DIR/$conf'" || die "такого пира нет в конфиге"

  # вырезаем блок [Peer] целиком, но только тот, где встречается нужный ключ;
  # блок заканчивается строкой AllowedIPs, следующая [Peer] начинает новый
  filtered=$(conf_read "$conf" | awk -v key="$pub" '
    function flush() { if (block != "" && index(block, key) == 0) printf "%s", block; block = "" }
    /^\[Peer\]/ { flush(); block = $0 "\n"; next }
    /^\[Interface\]/ || /^\[/ { if (block != "") flush(); print; next }
    { if (block != "") block = block $0 "\n"; else print }
    END { flush() }
  ')

  printf '%s\n' "$filtered" | conf_write "$conf"
  if ! apply_conf "$conf"; then
    restore_conf "$conf"
    apply_conf "$conf" || true
    die "не удалось применить конфиг, изменения отменены"
  fi
  if peer_present "$iface" "$pub"; then
    restore_conf "$conf"
    apply_conf "$conf" || true
    die "пир остался в интерфейсе, изменения отменены"
  fi
  meta_del "$pub"
  printf '{"ok":true,"publicKey":"%s","removed":true}\n' "$pub"
}

main() {
  need_root
  local verb="${1:-}"
  [ $# -gt 0 ] && shift
  case "$verb" in
    list) cmd_list ;;
    add) cmd_add "$@" ;;
    remove) cmd_remove "$@" ;;
    selftest) cmd_selftest ;;
    *) die "глаголы: list | add --pubkey K --psk P [--name N] | remove --pubkey K | selftest" ;;
  esac
}

# Один исполнитель за раз: два параллельных add выбрали бы один и тот же адрес.
# Внимание: redirect stderr на exec сделал бы глухим весь остаток скрипта —
# ошибки и диагностика должны оставаться видимыми.
LOCK_DIR="$(dirname "$LOCK_FILE")"
if mkdir -p "$LOCK_DIR" 2>/dev/null && : >>"$LOCK_FILE" 2>/dev/null; then
  exec 9>"$LOCK_FILE"
  if command -v flock >/dev/null 2>&1; then
    flock -w 30 9 || die "не удалось взять блокировку (другой процесс уже работает)"
  else
    log "предупреждение: flock недоступен, параллельные запуски не защищены"
  fi
fi

main "$@"
