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

# читает ЛЮБОЙ файл из каталога конфигов без обрезки хвоста (см. cmd_remove)
conf_read_to() { in_ctr "cat '$CONF_DIR/$1'"; }

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
    cat > "$tmp" && cp -f "$CONF_DIR/$conf" "$CONF_DIR/$conf.bak" && mv -f "$tmp" "$CONF_DIR/$conf"
  else
    docker exec -i "$CTR" bash -c "cat > '$tmp' && cp -f '$CONF_DIR/$conf' '$CONF_DIR/$conf.bak' && mv -f '$tmp' '$CONF_DIR/$conf'"
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

# Копию делаем КОПИРОВАНИЕМ (mv израсходовал бы единственный .bak), и с меткой
# времени — чтобы после неудачного восстановления было из чего восстанавливаться.
restore_conf() {  # имя конфига
  # cp, а не mv: после восстановления .bak должен оставаться на месте — иначе
  # второй сбой уже нечем откатывать
  in_ctr "cp -f '$CONF_DIR/$1.bak' '$CONF_DIR/$1'"
}

peer_present() {  # iface pubkey
  in_ctr "$tool_name show '$1' dump | grep -q '$2'"
}

# --- метаданные (имя клиента) ----------------------------------------------
# Построчный файл "pubkey<TAB>имя": в контейнере нет python, а JSON без него
# правится только костылями.

meta_init() { in_ctr "test -f '$META_FILE' || : > '$META_FILE'"; }

# Имя приходит от пользователя, а раньше подставлялось прямо в строку `bash -c`
# в одинарных кавычках: имя вида  x'; id > /tmp/pwn; : '  выполняло команду от
# root внутри контейнера (проверено). Теперь имя валидируется, а сами данные
# уходят через stdin, а не в текст команды.
# Возвращает безопасное имя (1-40 символов) или код 1 с пояснением в stderr.
# НЕ вызывает die: внутри $( ) это вышло бы за пределы подоболочки, скрипт
# продолжил бы работу с пустым именем — так и получилось в первой версии.
check_name() {  # name
  local name="$1"
  case "$name" in
    *"'"*|*'"'*|*'\\'*|*'$'*|*'`'*|*';'*|*'|'*|*'&'*|*'<'*|*'>'*|*'('*|*')'*)
      log "ОШИБКА: в имени недопустимые символы (кавычки, слэш, ; | & $ \` < >)"
      return 1 ;;
  esac
  if LC_ALL=C printf '%s' "$name" | LC_ALL=C grep -q '[[:cntrl:]]'; then
    log "ОШИБКА: в имени есть управляющие символы"
    return 1
  fi
  printf '%s' "$name" | cut -c1-40
}

meta_set() {  # pubkey name (имя уже проверено в cmd_add)
  local pub="$1" safe="${2:-client}"
  [ -n "$safe" ] || safe="client"
  # ключ проверен регуляркой выше по коду, имя — check_name; данные идут stdin
  printf '%s\t%s\n' "$pub" "$safe" | in_ctr_stdin "grep -v -F -- '$pub' '$META_FILE' > '$META_FILE.tmp' 2>/dev/null || :; cat >> '$META_FILE.tmp'; mv '$META_FILE.tmp' '$META_FILE'"
}

meta_del() {  # pubkey
  in_ctr "grep -v -F -- '$1' '$META_FILE' > '$META_FILE.tmp' 2>/dev/null || :; mv '$META_FILE.tmp' '$META_FILE'"
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
      # presharedKey НЕ отдаём: для выдачи нового ключа нужен только PSK самого
      # нового пира (он генерируется локально), а список уходит в браузер целиком
      printf "{\"publicKey\":\"%s\",\"endpoint\":\"%s\",\"allowedIps\":\"%s\",\"lastHandshake\":%s,\"rx\":%s,\"tx\":%s,\"name\":\"%s\"}",
        $1, ($3=="(none)"?"":$3), $4, ($5+0), ($6+0), ($7+0), esc(name_of($1))
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
  local pub="" psk="" name="" ip_req=""
  while [ $# -gt 0 ]; do
    case "$1" in
      --pubkey) pub="${2:-}"; shift 2 ;;
      --psk) psk="${2:-}"; shift 2 ;;
      --ip) ip_req="${2:-}"; shift 2 ;;
      --name)
        # имя может прийти несколькими аргументами: sshd подставляет строку без
        # повторного разбора кавычек, поэтому «Мой ноутбук» приезжает двумя словами
        shift
        name="$*"
        set --
        ;;
      *) die "неизвестный аргумент: $1" ;;
    esac
  done
  # кавычки могли приехать как часть аргумента (sshd подставляет строку без
  # повторного разбора кавычек) — снимаем, чтобы формат не зависел от клиента
  pub="${pub#\'}"; pub="${pub%\'}"; psk="${psk#\'}"; psk="${psk%\'}"
  name="${name#\'}"; name="${name%\'}"
  # проверка ДО любых изменений: иначе невалидное имя всплывало уже после записи
  # конфига, и пир оставался в интерфейсе без имени
  # Переносы строк и табуляции в имени превращаем в пробелы, повторы схлопываем:
  # человек мог вставить имя из другого документа, а в списке это выглядело как
  # обрыв строки. Пробельное имя считаем пустым.
  name=$(printf '%s' "$name" | tr '\t\r\n' '   ' | sed 's/  */ /g; s/^ *//; s/ *$//')
  name=$(check_name "$name") || exit 1
  [[ "$pub" =~ ^[A-Za-z0-9+/]{43}=$ ]] || die "некорректный публичный ключ"
  [[ "$psk" =~ ^[A-Za-z0-9+/]{43}=$ ]] || die "некорректный PSK"
  # имя не задали — не оставляем пир безымянным: в списке должно быть что читать
  [ -n "$name" ] || name="устройство"

  local conf iface subnet base
  find_container; conf=$(find_conf); pick_tool; meta_init
  iface="${conf%.conf}"
  subnet=$(conf_subnet "$conf")
  [ -n "$subnet" ] || die "не удалось определить подсеть из Address в конфиге"
  base=$(printf '%s' "$subnet" | cut -d/ -f1 | awk -F. '{print $1"."$2"."$3}')

  if in_ctr "grep -q '$pub' '$CONF_DIR/$conf'"; then
    die "пир с таким ключом уже есть в конфиге"
  fi

  # Явный адрес (--ip) нужен для восстановления пира на его прежний адрес: клиент
  # на устройстве настроен на конкретный client_ip, и выдача другого адреса его
  # ломает. Проверяем, что адрес внутри своей подсети и не занят.
  if [ -n "$ip_req" ]; then
    case "$ip_req" in
      "$base."[0-9]*)
        [[ "$ip_req" =~ ^[0-9]{1,3}\.[0-9]{1,3}\.[0-9]{1,3}\.[0-9]{1,3}$ ]] \
          || die "--ip должен быть адресом вида 10.8.1.15" ;;
      *) die "--ip $ip_req вне подсети $subnet" ;;
    esac
    if in_ctr "$tool_name show '$iface' dump | awk '{print \$4}' | grep -qx '$ip_req/32'"; then
      die "адрес $ip_req уже занят другим пиром"
    fi
  fi

  local used ip n
  ip=""
  if [ -n "$ip_req" ]; then
    ip="$ip_req"
  else
    # свободный адрес: берём занятые AllowedIPs и ищем первый пропуск
    used=$(in_ctr "grep -E '^AllowedIPs' '$CONF_DIR/$conf' | cut -d= -f2 | tr -d ' ' | cut -d/ -f1")
    for n in $(seq 2 254); do
      if ! printf '%s\n' "$used" | grep -qx "$base.$n"; then
        ip="$base.$n"; break
      fi
    done
  fi
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
  # Метаданные — до ответа «успех»: если имя не записалось, откатываем пира,
  # иначе клиент видит ошибку, а пир остаётся в конфиге сиротой без имени.
  if ! meta_set "$pub" "$name"; then
    restore_conf "$conf"
    apply_conf "$conf" 2>/dev/null || true
    die "не удалось сохранить имя пира, добавление отменено"
  fi
  # имя возвращаем уже проверенное и обрезанное, а не то, что пришло
  printf '{"ok":true,"publicKey":"%s","allowedIps":"%s/32","name":"%s"}\n' "$pub" "$ip" "$(printf '%s' "$name" | sed 's/["\\]/_/g')"
}

cmd_remove() {
  local pub=""
  while [ $# -gt 0 ]; do
    case "$1" in
      --pubkey) pub="${2:-}"; shift 2 ;;
      *) die "неизвестный аргумент: $1" ;;
    esac
  done
  pub="${pub#\'}"; pub="${pub%\'}"
  [[ "$pub" =~ ^[A-Za-z0-9+/]{43}=$ ]] || die "некорректный публичный ключ"

  local conf iface filtered
  find_container; conf=$(find_conf); pick_tool; meta_init
  iface="${conf%.conf}"

  in_ctr "grep -q '$pub' '$CONF_DIR/$conf'" || die "такого пира нет в конфиге"

  # Вырезаем блок [Peer] целиком, но только тот, где встречается нужный ключ.
  # Пишем результат в файл, а НЕ в $( ): подстановка команды срезает хвостовые
  # переводы строк, и конфиг после отката оказывался на байт короче исходного.
  local newf="$CONF_DIR/$conf.new"
  in_ctr "awk -v key='$pub' '
    function flush() { if (block != \"\" && index(block, key) == 0) printf \"%s\", block; block = \"\" }
    /^\[Peer\]/ { flush(); block = \$0 \"\\n\"; next }
    /^\[/ { if (block != \"\") flush(); print; next }
    { if (block != \"\") block = block \$0 \"\\n\"; else print }
    END { flush() }
  ' '$CONF_DIR/$conf' > '$newf'"

  conf_read_to "$conf.new" | conf_write "$conf"
  in_ctr "rm -f '$newf'"
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

# Команда приходит от sshd в $SSH_ORIGINAL_COMMAND, а не в аргументах: forced
# command запускается как `sh -c "sudo awgbv-peer"`.
#
# Разбираем БЕЗ eval: сначала строгая проверка формата целиком, и только потом
# выдёргиваем известные поля по позициям. Строка приходит от клиента, поэтому
# превращать её в код нельзя ни при каких условиях.
# Результат разбора кладём в ГЛОБАЛЬНЫЙ массив: `set --` внутри функции меняет
# только её собственные позиционные параметры, а не аргументы main (на этом
# первая версия молча не работала — весь путь через SSH_ORIGINAL_COMMAND был мёртв).
PARSED=()
parse_ssh_command() {
  local cmd="$1" key='[A-Za-z0-9+/]{43}=' name=""
  PARSED=()
  if [[ "$cmd" =~ ^(list|selftest)$ ]]; then
    PARSED=("$cmd")
    return 0
  fi
  if [[ "$cmd" =~ ^remove\ --pubkey\ $key$ ]]; then
    PARSED=(remove --pubkey "$(awk '{print $3}' <<< "$cmd")")
    return 0
  fi
  if [[ "$cmd" =~ ^add\ --pubkey\ $key\ --psk\ $key(\ --ip\ [0-9.]+)?(\ --name\ .{1,42})?$ ]]; then
    if [[ "$cmd" =~ \ --name\ (.+)$ ]]; then
      name="${BASH_REMATCH[1]}"
      name="${name#\'}"; name="${name%\'}"
    fi
    local iparg=()
    if [[ "$cmd" =~ \ --ip\ ([0-9.]+) ]]; then
      iparg=(--ip "${BASH_REMATCH[1]}")
    fi
    PARSED=(add --pubkey "$(awk '{print $3}' <<< "$cmd")" \
            --psk "$(awk '{print $5}' <<< "$cmd")" "${iparg[@]}" --name "$name")
    return 0
  fi
  die "недопустимая команда для этого ключа"
}

# Живой путь — argv из forced command; применяем к нему ту же строгую проверку
# формата целиком, а не только по отдельным полям.
check_argv_format() {  # все аргументы main
  local key='[A-Za-z0-9+/]{43}='
  local joined
  joined=$(printf '%s ' "$@")
  joined="${joined% }"
  [[ "$joined" =~ ^(list|selftest)$ ]] && return 0
  [[ "$joined" =~ ^remove\ --pubkey\ $key$ ]] && return 0
  [[ "$joined" =~ ^add\ --pubkey\ $key\ --psk\ $key(\ --ip\ [0-9.]+)?(\ --name\ .+)?$ ]] && return 0
  die "недопустимая команда для этого ключа"
}

main() {
  need_root
  # Путь 1: глагол пришёл аргументами (живой forced command).
  # Путь 2: переменная окружения — только если argv пуст.
  if [ $# -eq 0 ] && [ -n "${SSH_ORIGINAL_COMMAND:-}" ]; then
    parse_ssh_command "$SSH_ORIGINAL_COMMAND" || exit 1
    set -- "${PARSED[@]}"
  fi
  [ $# -gt 0 ] || die "глаголы: list | add --pubkey K --psk P [--name N] | remove --pubkey K | selftest"
  check_argv_format "$@"
  local verb="${1:-}"
  shift
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
