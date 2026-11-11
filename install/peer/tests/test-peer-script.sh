#!/usr/bin/env bash
# Тест серверного скрипта awgbv-peer.sh в локальном режиме (AWGBV_PEER_LOCAL_DIR).
#
# Зачем: скрипт правит конфиг живого VPN на чужом сервере. Ошибка в awk-фильтре
# («вырезали не тот [Peer]») тихо ломает всех клиентов, поэтому логику проверяем
# до выезда на сервер. Проверяются: список, выбор свободного адреса, отказ на
# дубле и мусорном ключе, удаление строго одного блока, восстановление конфига
# при сбое применения.

set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SCRIPT="$HERE/../awgbv-peer.sh"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

CONF_DIR="$WORK/awg"
BIN="$WORK/bin"
mkdir -p "$CONF_DIR" "$BIN"

cat > "$CONF_DIR/awg0.conf" <<'CONF'
[Interface]
PrivateKey = SERVERPRIVATEKEY0000000000000000000000000000=
ListenPort = 36853
Address = 10.8.1.1/24
Jc = 5
Jmin = 50
Jmax = 1000
S1 = 40
S2 = 60
H1 = 1234567
HeaderProtectionKey = HEADERPROTECTIONKEY000000000000000000000=

[Peer]
PublicKey = EXISTINGPEERKEY000000000000000000000000000000=
PresharedKey = EXISTINGPSK00000000000000000000000000000000=
AllowedIPs = 10.8.1.2/32
CONF

# shim awg: `show X dump` читает применённое состояние, `syncconf <iface> <файл>`
# пересобирает его из переданного файла — как настоящий, только без ядра.
cat > "$BIN/awg" <<MOCK
#!/usr/bin/env bash
STATE="$WORK/applied.dump"
case "\$1" in
  show)
    echo "show: args=[\$*]" >> "$WORK/shim.log"
    if [ "\${3:-}" = "public-key" ]; then echo "SERVERPUBLICKEY0000000000000000000000000000="; exit 0; fi
    if [ "\${3:-}" = "dump" ]; then cat "\$STATE" 2>/dev/null; echo "--- dump выше ---" >> "$WORK/shim.log"; cat "\$STATE" >> "$WORK/shim.log" 2>/dev/null; exit 0; fi
    cat "\$STATE" 2>/dev/null || true
    ;;
  syncconf)
    src="\${3:-/dev/stdin}"
    echo "syncconf: args=[\$*] src=\$src" >> "$WORK/shim.log"
    if [ -f "\$src" ]; then echo "src размер: \$(wc -c < "\$src")" >> "$WORK/shim.log"; else echo "src нет" >> "$WORK/shim.log"; fi
    # как настоящий awg show dump: первая строка — интерфейс, дальше пиры.
    # Ключи режем по ПЕРВОМУ «=», а не split: base64-ключ сам кончается на «=»,
    # и наивный split съедал паддинг (на этом тест однажды соврал).
    echo "serverprivkey serverpubkey 36853 off" > "\$STATE"
    awk '
      /^\[Peer\]/ { pub=""; psk="(none)"; next }
      /^PublicKey/ { v=\$0; sub(/^[^=]*=[ \t]*/,"",v); pub=v }
      /^PresharedKey/ { v=\$0; sub(/^[^=]*=[ \t]*/,"",v); psk=v }
      /^AllowedIPs/ { v=\$0; sub(/^[^=]*=[ \t]*/,"",v); if (pub != "") print pub " " psk " (none) " v " 0 0 0 off" }
    ' "\$src" >> "\$STATE"
    ;;
esac
MOCK

cat > "$BIN/awg-quick" <<MOCK
#!/usr/bin/env bash
[ "\$1" = "strip" ] && cat "\$2"
MOCK
chmod +x "$BIN/awg" "$BIN/awg-quick"
export PATH="$BIN:$PATH"
export AWGBV_PEER_LOCAL_DIR="$CONF_DIR"
export AWGBV_PEER_LOCK="$WORK/peer.lock"
export AWGBV_PEER_DEBUG=1

# первичное «применение», как будто контейнер уже работает
"$BIN/awg" syncconf awg0 < "$CONF_DIR/awg0.conf"

run() { bash "$SCRIPT" "$@" 2>"$WORK/err.txt"; }

fail=0
ok() { echo "  ✓ $1"; }
no() { echo "  ✗ $1"; fail=1; }
check() { if [ "$1" = "1" ]; then ok "$2"; else no "$2"; fi }

echo "== selftest =="
self=$(run selftest); echo "  $self"
echo "$self" | grep -q '"config":"awg0.conf"' && check 1 "конфиг найден" || check 0 "конфиг найден"
echo "$self" | grep -q '10.8.1.0/24' && check 1 "подсеть из Address (адрес сети, не интерфейса)" || check 0 "подсеть из Address"

echo "== list: один пир =="
run list > "$WORK/l1.json"; cat "$WORK/l1.json"
grep -q 'EXISTINGPEERKEY' "$WORK/l1.json" && check 1 "пир виден" || check 0 "пир виден"
grep -q '"listenPort":36853' "$WORK/l1.json" && check 1 "порт из дампа" || check 0 "порт"

echo "== list: публичный ключ сервера и параметры обфускации =="
grep -q '"serverPublicKey":"SERVERPUBLICKEY' "$WORK/l1.json" && check 1 "публичный ключ сервера отдан" || check 0 "публичный ключ сервера отдан"
grep -q '"Jc=5"' "$WORK/l1.json" && check 1 "Jc в параметрах" || check 0 "Jc в параметрах"
grep -q '"HeaderProtectionKey=' "$WORK/l1.json" && check 1 "HeaderProtectionKey в параметрах" || check 0 "HeaderProtectionKey в параметрах"
grep -q 'комментарий' "$WORK/l1.json" && no "комментарии не попали в параметры" || check 1 "комментарии не попали в параметры"
grep -q 'SERVERPRIVATEKEY' "$WORK/l1.json" && no "приватный ключ сервера НЕ утёк" || check 1 "приватный ключ сервера не утёк"

echo "== add: свободный адрес .3, а не занятый .2 =="
PUB1="AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="
PSK1="BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB="
add=$(run add --pubkey "$PUB1" --psk "$PSK1" --name "ноутбук" || true); echo "  $add"
if [ -s "$WORK/err.txt" ] && grep -q 'ОШИБКА' "$WORK/err.txt"; then
echo "  --- shim.log ---"; sed 's/^/  | /' "$WORK/shim.log" 2>/dev/null
echo "  --- applied.dump ---"; sed 's/^/  | /' "$WORK/applied.dump" 2>/dev/null
fi
echo "$add" | grep -q '10.8.1.3/32' && check 1 "выдан .3" || check 0 "выдан .3"
grep -q "$PUB1" "$CONF_DIR/awg0.conf" && check 1 "пир в конфиге" || check 0 "пир в конфиге"
run list > "$WORK/l2.json"
grep -q "$PUB1" "$WORK/l2.json" && check 1 "пир в list" || check 0 "пир в list"
grep -q 'ноутбук' "$WORK/l2.json" && check 1 "имя сохранено" || check 0 "имя сохранено"
grep -q 'EXISTINGPEERKEY' "$WORK/l2.json" && check 1 "старый пир не потерян" || check 0 "старый пир"

echo "== add: дубль ключа отклоняется =="
if run add --pubkey "$PUB1" --psk "$PSK1" >/dev/null; then no "дубль отклонён"; else
  grep -q 'уже есть' "$WORK/err.txt" && check 1 "дубль отклонён" || check 0 "дубль отклонён по правильной причине"
fi

echo "== add: мусорный ключ отклоняется, конфиг не тронут =="
before=$(md5sum "$CONF_DIR/awg0.conf" | cut -d' ' -f1)
if run add --pubkey "плохой" --psk "$PSK1" >/dev/null; then no "мусор отклонён"; else
  after=$(md5sum "$CONF_DIR/awg0.conf" | cut -d' ' -f1)
  [ "$before" = "$after" ] && check 1 "мусор отклонён, конфиг цел" || check 0 "конфиг цел"
fi

echo "== add: второй пир получает следующий адрес =="
PUB2="CCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCC="
PSK2="DDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDD="
add2=$(run add --pubkey "$PUB2" --psk "$PSK2" --name "телефон" || true); echo "  $add2"
[ -s "$WORK/err.txt" ] && sed 's/^/  stderr: /' "$WORK/err.txt"
echo "$add2" | grep -q '10.8.1.4/32' && check 1 "выдан .4" || check 0 "выдан .4"

echo "== remove: удаляется только нужный блок =="
run remove --pubkey "$PUB1" || true
grep -q "$PUB1" "$CONF_DIR/awg0.conf" && no "пир удалён из конфига" || check 1 "пир удалён из конфига"
grep -q "$PUB2" "$CONF_DIR/awg0.conf" && check 1 "сосед на месте" || check 0 "сосед на месте"
grep -q 'EXISTINGPEERKEY' "$CONF_DIR/awg0.conf" && check 1 "исходный пир на месте" || check 0 "исходный пир"
grep -c '^\[Peer\]' "$CONF_DIR/awg0.conf" | grep -qx '2' && check 1 "секций [Peer] ровно две" || no "секций [Peer] ровно две"
run list > "$WORK/l3.json"
grep -q "$PUB1" "$WORK/l3.json" && no "удалённого нет в list" || check 1 "удалённого нет в list"

echo "== remove: несуществующий ключ =="
if run remove --pubkey "$PUB1" >/dev/null; then no "повторное удаление отклонено"; else
  grep -q 'нет в конфиге' "$WORK/err.txt" && check 1 "повторное удаление отклонено" || check 0 "по правильной причине"
fi

echo "== сбой применения: конфиг возвращается как был =="
cp "$CONF_DIR/awg0.conf" "$WORK/before_fail.conf"
cat > "$BIN/awg" <<MOCK
#!/usr/bin/env bash
STATE="$WORK/applied.dump"
case "\$1" in
  show) if [ "\${3:-}" = "dump" ]; then cat "\$STATE" 2>/dev/null; exit 0; fi; cat "\$STATE" 2>/dev/null || true ;;
  syncconf) exit 1 ;;
esac
MOCK
chmod +x "$BIN/awg"
PUB3="EEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEE="
PSK3="FFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFF="
if run add --pubkey "$PUB3" --psk "$PSK3" --name "сбойный" >/dev/null; then no "сбой применения замечен"; else
  if diff -q "$WORK/before_fail.conf" "$CONF_DIR/awg0.conf" >/dev/null; then
    check 1 "конфиг восстановлен после сбоя"
  else
    no "конфиг восстановлен после сбоя"
  fi
  grep -q "$PUB3" "$CONF_DIR/awg0.conf" && no "мусорный пир не остался" || check 1 "мусорный пир не остался"
fi

echo
if [ "$fail" = "0" ]; then echo "РЕЗУЛЬТАТ: всё прошло"; exit 0; fi
echo "РЕЗУЛЬТАТ: есть провалы"; exit 1
