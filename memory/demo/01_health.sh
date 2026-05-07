#!/usr/bin/env bash
set -euo pipefail

THIS_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$THIS_DIR/common.sh"

require_basics
ensure_api_up

bold "Demo 01: Health"
resp="$(curl -sS "$BASE_URL/healthz")"
echo "$resp" | jq .

ok "Health endpoint reached."
