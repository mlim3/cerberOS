#!/usr/bin/env bash
# install.sh — public-clone bootstrap entry point for cerberOS.
#
# Wraps ./bootstrap.sh so the README quickstart can use the canonical
#   git clone <repo> && cd <repo> && ./install.sh
# shape from the FP-Stefan requirements.
#
# All flags are forwarded to bootstrap.sh verbatim. Two convenience behaviours
# layered on top:
#   1. If .env is missing, copy .env.example into place so the first run does
#      not fail on the .env precondition.
#   2. After the stack comes up, print the "next steps" cheat-sheet that maps
#      requirements to URLs (web UI signup, admin panel, CLI examples).

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$ROOT"

if [[ ! -x "./bootstrap.sh" ]]; then
  echo "install.sh: ./bootstrap.sh not found or not executable in $ROOT" >&2
  exit 1
fi

if [[ ! -f .env ]]; then
  if [[ -f .env.example ]]; then
    echo "install.sh: copying .env.example -> .env (edit it later to add API keys)"
    cp .env.example .env
  else
    echo "install.sh: warning — no .env and no .env.example to copy from" >&2
  fi
fi

# Forward all arguments to bootstrap.sh. `down --delete-volumes` etc. all work.
./bootstrap.sh "$@"

# Skip the cheat-sheet on teardown commands.
case "${1:-}" in
  down|stop|--help|-h)
    exit 0
    ;;
esac

cat <<'EOF'

────────────────────────────────────────────────────────────────────────────
cerberOS is up. First-run checklist (FP-Stefan):

  1. Open http://localhost:5173 — the "Create your account" overlay should
     appear. Enter an email; this becomes the system root.

  2. (Optional) Set the LLM key from the UI:
        Admin → LLM Provider Key → Save
     Then: docker compose restart aegis-agents

  3. Install Superpowers for all users:
        Admin → Skills → Install Superpowers (all users)
     or:  io/surfaces/cli$ bun run src/cli.ts skill import \
                            github.com/obra/superpowers --all-users

  4. Try the easy scenarios in chat:
       E2: "Use the Superpowers code-review skill to review <file> in <repo>"
       E3: "Create a quick_note skill that saves text to ~/notes.txt"
       E4: "What's happening in SF this weekend, weather, and what to wear?"

  See context/fp-stefan-demo-script.md for the full walk-through.
────────────────────────────────────────────────────────────────────────────
EOF
