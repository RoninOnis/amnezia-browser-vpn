#!/usr/bin/env bash
# Sign the extension on addons.mozilla.org as an unlisted (self-distributed) addon.
#
# Credentials: get them at https://addons.mozilla.org/developers/addon/api/key/
# and provide them either in the environment
#     AMO_API_KEY='user:12345678:123' AMO_API_SECRET='...' ./install/sign.sh
# or in ~/.hermes/amo_creds.yaml:
#     amo:
#       api_key: "user:12345678:123"
#       api_secret: "..."
#
# Requires: node/npx, network access.
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$HERE/.." && pwd)"
CREDS="${AMO_CREDS:-$HOME/.hermes/amo_creds.yaml}"

if [[ -z "${AMO_API_KEY:-}" && -f "$CREDS" ]]; then
  AMO_API_KEY="$(sed -n 's/^[[:space:]]*api_key:[[:space:]]*"\{0,1\}\([^"]*\)"\{0,1\}$/\1/p' "$CREDS" | head -1)"
  AMO_API_SECRET="$(sed -n 's/^[[:space:]]*api_secret:[[:space:]]*"\{0,1\}\([^"]*\)"\{0,1\}$/\1/p' "$CREDS" | head -1)"
fi

if [[ -z "${AMO_API_KEY:-}" || -z "${AMO_API_SECRET:-}" ]]; then
  echo "error: AMO_API_KEY / AMO_API_SECRET not provided (env or $CREDS)" >&2
  exit 1
fi

mkdir -p "$ROOT/dist/signed"
cd "$ROOT"
npx --yes web-ext@8 sign \
  --source-dir extension \
  --artifacts-dir dist/signed \
  --ignore-files "tests/**" \
  --channel unlisted \
  --api-key "$AMO_API_KEY" \
  --api-secret "$AMO_API_SECRET" \
  --timeout 600000

echo
echo "signed artifacts:"
ls -lh dist/signed/
echo
echo "Поставить: about:addons -> шестерёнка -> «Установить дополнение из файла» -> выбрать .xpi"
echo "Либо: firefox dist/signed/<name>.xpi"
