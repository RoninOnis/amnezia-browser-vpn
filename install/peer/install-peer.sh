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
case "$PUBKEY" in
  ssh-ed25519\ *|ssh-rsa\ *|ecdsa-sha2-nistp256\ *) ;;
  "") log "предупреждение: публичный ключ не передан — authorized_keys не трогаю" ;;
  *) die "некорректная строка публичного ключа (ожидался ssh-ed25519 AAAA...)" ;;
esac

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

if [ -z "$SCRIPT_SRC" ]; then
  log "→ скачиваю awgbv-peer.sh"
  curl -fsSL -o "$TMP/awgbv-peer.sh" "$BASE_URL/awgbv-peer.sh" || die "не удалось скачать awgbv-peer.sh"
  SCRIPT_SRC="$TMP/awgbv-peer.sh"
  # сверяем с SHA256SUMS из того же релиза, если он там есть
  if curl -fsSL -o "$TMP/SHA256SUMS" "$BASE_URL/SHA256SUMS" 2>/dev/null; then
    want=$(awk '$2 == "awgbv-peer.sh" {print $1}' "$TMP/SHA256SUMS")
    if [ -n "$want" ]; then
      got=$(sha256sum "$SCRIPT_SRC" | cut -d' ' -f1)
      [ "$want" = "$got" ] || die "sha256 awgbv-peer.sh не совпал (ожидался $want, получен $got)"
      log "→ sha256 совпал"
    fi
  fi
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
else
  useradd --system --no-create-home --home-dir /nonexistent \
          --shell /usr/sbin/nologin "$PEER_USER" 2>/dev/null \
    || useradd -r -M -d /nonexistent -s /usr/sbin/nologin "$PEER_USER"
  log "→ создан пользователь $PEER_USER"
fi

# --- 3. sudoers --------------------------------------------------------------
# Разрешаем ровно одну команду и без пароля. Никакого ALL, никакого shell.
cat > "$TMP/sudoers" <<SUDO
# Управление пирами AmneziaWG из браузерного VPN. Только одна команда, без пароля.
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
  [ "$HOME_DIR" = "/nonexistent" ] && HOME_DIR="/var/lib/awgbv"
  install -d -m 0700 -o "$PEER_USER" -g "$PEER_USER" "$HOME_DIR" "$HOME_DIR/.ssh"
  AUTH="$HOME_DIR/.ssh/authorized_keys"
  # restrict отключает всё лишнее (pty, forward, agent), command — запирает на один скрипт
  LINE="restrict,command=\"sudo $PEER_BIN\" $PUBKEY"
  if ! grep -qF "$PUBKEY" "$AUTH" 2>/dev/null; then
    printf '%s\n' "$LINE" >> "$AUTH"
    log "→ ключ добавлен в $AUTH"
  else
    log "→ такой ключ уже есть в $AUTH"
  fi
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
