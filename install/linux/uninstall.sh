#!/usr/bin/env bash
# Remove the Amnezia Browser VPN helper (Linux).
set -euo pipefail

HOST_NAME="awgbv"

# Погасить демон, если он запущен.
if command -v awgbv >/dev/null 2>&1; then
  awgbv --stop >/dev/null 2>&1 || true
fi
if [[ -x "$HOME/.local/bin/awgbv" ]]; then
  "$HOME/.local/bin/awgbv" --stop >/dev/null 2>&1 || true
fi

rm -f "$HOME/.local/bin/awgbv"
rm -f "$HOME/.mozilla/native-messaging-hosts/$HOST_NAME.json"
rm -f "$HOME/snap/firefox/common/.mozilla/native-messaging-hosts/$HOST_NAME.json"
rm -rf "$HOME/.local/state/awgbv"

echo "helper removed. Расширение удаляется в about:addons."
