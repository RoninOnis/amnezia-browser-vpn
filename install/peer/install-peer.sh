#!/usr/bin/env bash
# install-peer.sh — одноразовая установка серверной части панели администратора.
#
# Что делает:
#   1. ставит /usr/local/sbin/awgbv-peer (list/add/remove пиров AmneziaWG);
#   2. создаёт пользователя awgbv без оболочки и домашнего каталога;
#   3. разрешает ему sudo БЕЗ пароля ровно на один этот скрипт;
#   4. добавляет присланный публичный ключ в authorized_keys с restrict и
#      forced command: этим ключом нельзя выполнить ничего, кроме awgbv-peer.
#
# После этого пароль root серверу больше не нужен — хелпер работает по ключу и
# только через ограниченную команду.
#
# Использование:
#   sudo bash install-peer.sh --pubkey "ssh-ed25519 AAAA... awgbv"
#   sudo bash install-peer.sh --dry-run
set -euo pipefail

PEER_BIN="/usr/local/sbin/awgbv-peer"
SUDOERS="/etc/sudoers.d/awgbv-peer"
PEER_USER="awgbv"
BASE_URL="${AWGBV_BASE_URL:-https://github.com/RoninOnis/amnezia-browser-vpn/releases/latest/download}"
PUBKEY=""
DRY_RUN=0
SCRIPT_SRC=""

log() { printf '%s\n' "$*" >&2; }
die() { printf 'ОШИБКА: %s\n' "$*" >&2; exit 1; }

while [ $# -gt 0 ]; do
  case "$1" in
    --pubkey) PUBKEY="${2:-}"; shift 2 ;;
    --script) SCRIPT_SRC="${2:-}"; shift 2 ;;
    --url) BASE_URL="${2:-}"; shift 2 ;;
    --dry-run) DRY_RUN=1; shift ;;
    -h|--help) sed -n '2,20p' "$0"; exit 0 ;;
    *) die "неизвестный аргумент: $1" ;;
  esac
done

[ "$(id -u)" = "0" ] || die "нужен root: sudo bash $0 ..."
# Строка ключа уходит в authorized_keys как есть, поэтому проверяем её ЦЕЛИКОМ:
# раньше хватало проверки префикса, и аргумент с переводом строки добавлял второй,
# НЕограниченный ключ (то есть снимал всю защиту forced command).
if [ -n "$PUBKEY" ]; then
  case "$PUBKEY" in
    *$'\n'*|*$'\r'*) die "в строке ключа есть перевод строки — так можно добавить лишний ключ" ;;
  esac
  [[ "$PUBKEY" =~ ^(ssh-ed25519|ecdsa-sha2-nistp256|ssh-rsa)\ [A-Za-z0-9+/]+={0,3}(\ [^[:cntrl:]]{1,80})?$ ]] \
    || die "некорректная строка публичного ключа (ожидался ssh-ed25519 AAAA... необязательный комментарий)"
  if command -v ssh-keygen >/dev/null 2>&1; then
    printf '%s\n' "$PUBKEY" | ssh-keygen -lf /dev/stdin >/dev/null 2>&1 \
      || die "ssh-keygen не смог прочитать ключ — проверьте, что он скопирован целиком"
  fi
else
  log "предупреждение: публичный ключ не передан — authorized_keys не трогаю"
fi

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

if [ -z "$SCRIPT_SRC" ]; then
  log "→ скачиваю awgbv-peer.sh"
  curl -fsSL -o "$TMP/awgbv-peer.sh" "$BASE_URL/awgbv-peer.sh" || die "не удалось скачать awgbv-peer.sh"
  SCRIPT_SRC="$TMP/awgbv-peer.sh"
  # сверяем с SHA256SUMS из того же релиза, если он там есть
  # Сверка обязательна: этот скрипт потом выполняется от root по sudo NOPASSWD.
  # Раньше при отсутствии SHA256SUMS или строки в нём проверка молча пропускалась.
  curl -fsSL -o "$TMP/SHA256SUMS" "$BASE_URL/SHA256SUMS" \
    || die "не удалось скачать SHA256SUMS — без сверки скрипт не ставлю"
  want=$(awk '$2 == "awgbv-peer.sh" {print $1}' "$TMP/SHA256SUMS")
  [ -n "$want" ] || die "в SHA256SUMS нет строки awgbv-peer.sh — установка отменена, ничего не изменено"
  got=$(sha256sum "$SCRIPT_SRC" | cut -d' ' -f1)
  [ "$want" = "$got" ] || die "sha256 awgbv-peer.sh не совпал (ожидался $want, получен $got) — ничего не изменено"
  log "→ sha256 совпал"
fi
[ -f "$SCRIPT_SRC" ] || die "нет файла скрипта: $SCRIPT_SRC"

log ""
log "План:"
log "  скрипт:   $PEER_BIN (0700, root)"
log "  sudoers:  $SUDOERS (NOPASSWD ровно на $PEER_BIN)"
log "  польз.:   $PEER_USER (без оболочки и без домашнего каталога)"
[ -n "$PUBKEY" ] && log "  ключ:     добавляется в authorized_keys с restrict,command="
log ""

if [ "$DRY_RUN" = "1" ]; then
  log "dry-run: ничего не меняю"
  exit 0
fi

# --- 1. скрипт ---------------------------------------------------------------
install -d -m 0755 /usr/local/sbin
install -m 0700 -o root -g root "$SCRIPT_SRC" "$PEER_BIN"
log "→ установлен $PEER_BIN"

# --- 2. пользователь ---------------------------------------------------------
if id "$PEER_USER" >/dev/null 2>&1; then
  log "→ пользователь $PEER_USER уже есть"
  case "$(getent passwd "$PEER_USER" | cut -d: -f7)" in
    */nologin|/bin/false) usermod -s /bin/sh "$PEER_USER"; log "→ оболочка исправлена на /bin/sh" ;;
  esac
else
  # Домашний каталог = куда кладём ключ: sshd ищет authorized_keys по passwd,
  # с домашним /nonexistent вход по ключу молча не работает.
  # Оболочка — /bin/sh, НЕ nologin: sshd запускает forced command через шелл
  # пользователя, и с nologin выход «This account is currently not available».
  # Доступ запирает restrict,command= в authorized_keys, а не шелл.
  useradd --system --no-create-home --home-dir /var/lib/awgbv \
          --shell /bin/sh "$PEER_USER" 2>/dev/null \
    || useradd -r -M -d /var/lib/awgbv -s /bin/sh "$PEER_USER"
  log "→ создан пользователь $PEER_USER"
fi

# --- 3. sudoers --------------------------------------------------------------
# Разрешаем ровно одну команду и без пароля. Никакого ALL, никакого shell.
cat > "$TMP/sudoers" <<SUDO
# Управление пирами AmneziaWG из браузерного VPN. Только одна команда, без пароля.
# env_keep нужен для SSH_ORIGINAL_COMMAND: sudo вычищает окружение, и без этого
# forced command не видит, какой глагол запросил клиент.
Defaults:$PEER_USER env_keep += "SSH_ORIGINAL_COMMAND"
$PEER_USER ALL=(root) NOPASSWD: $PEER_BIN
SUDO
if command -v visudo >/dev/null 2>&1; then
  visudo -cf "$TMP/sudoers" >/dev/null || die "sudoers не прошёл проверку"
fi
install -m 0440 -o root -g root "$TMP/sudoers" "$SUDOERS"
log "→ установлен $SUDOERS"

# --- 4. ключ -----------------------------------------------------------------
if [ -n "$PUBKEY" ]; then
  HOME_DIR=$(getent passwd "$PEER_USER" | cut -d: -f6)
  if [ "$HOME_DIR" = "/nonexistent" ] || [ -z "$HOME_DIR" ]; then
    HOME_DIR="/var/lib/awgbv"
    usermod -d "$HOME_DIR" "$PEER_USER"
    log "→ домашний каталог $PEER_USER исправлен на $HOME_DIR"
  fi
  install -d -m 0700 -o "$PEER_USER" -g "$PEER_USER" "$HOME_DIR" "$HOME_DIR/.ssh"
  AUTH="$HOME_DIR/.ssh/authorized_keys"
  # restrict отключает всё лишнее (pty, forward, agent), command — запирает на один скрипт.
  # Глагол передаём аргументом: sshd раскрывает $SSH_ORIGINAL_COMMAND в своей оболочке
  # до запуска sudo, иначе sudo теряет переменную вместе с окружением.
  LINE="restrict,command=\"sudo $PEER_BIN \$SSH_ORIGINAL_COMMAND\" $PUBKEY"
  # Строку с этим ключом ПЕРЕЗАПИСЫВАЕМ: иначе после исправления формата forced
  # command на сервере остаётся старая строка, и переустановка ничего не меняет.
  BLOB=$(printf '%s' "$PUBKEY" | awk '{print $2}')
  if grep -qF "$BLOB" "$AUTH" 2>/dev/null; then
    grep -vF "$BLOB" "$AUTH" > "$AUTH.tmp" || true
    mv "$AUTH.tmp" "$AUTH"
  fi
  printf '%s\n' "$LINE" >> "$AUTH"
  log "→ ключ записан в $AUTH"
  chown "$PEER_USER:$PEER_USER" "$AUTH"
  chmod 0600 "$AUTH"

  # sshd должен пускать этого пользователя по ключу. Правим только если запрещено.
  if [ -d /etc/ssh/sshd_config.d ] && ! grep -rqsE "^\s*AllowUsers" /etc/ssh/sshd_config /etc/ssh/sshd_config.d/; then
    printf '# awgbv: доступ панели по ключу, пароль не нужен\nMatch User %s\n    PasswordAuthentication no\n    AllowTcpForwarding no\n    X11Forwarding no\n' "$PEER_USER" \
      > /etc/ssh/sshd_config.d/awgbv-peer.conf
    log "→ добавлен /etc/ssh/sshd_config.d/awgbv-peer.conf (перезапуск sshd не требуется)"
  fi
fi

# --- проверка ----------------------------------------------------------------
log ""
log "Проверка:"
if sudo -u "$PEER_USER" sudo -n "$PEER_BIN" selftest 2>/dev/null | grep -q '"ok":true'; then
  log "  ✓ selftest отвечает: $(sudo -u "$PEER_USER" sudo -n "$PEER_BIN" selftest 2>/dev/null)"
else
  log "  ! selftest не ответил — проверьте, что контейнер AmneziaWG запущен:"
  log "    $PEER_BIN selftest"
fi
log ""
log "Удалить:"
log "  rm -f $PEER_BIN $SUDOERS /etc/ssh/sshd_config.d/awgbv-peer.conf"
log "  userdel $PEER_USER   # и удалить его строку из authorized_keys"
