#!/usr/bin/env bash
set -euo pipefail

THIS_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

"$THIS_DIR/01_health.sh"
"$THIS_DIR/02_chat_idempotency.sh"
"$THIS_DIR/03_personal_info_lifecycle.sh"
"$THIS_DIR/04_agent_logs.sh"
"$THIS_DIR/05_system_and_vault.sh"

echo
echo "All demos completed."
