#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

SKIP_E2E="${SKIP_E2E:-false}"
SKIP_MEMORY_SETUP="${SKIP_MEMORY_SETUP:-false}"
E2E_FLAGS=()

for arg in "$@"; do
  case "$arg" in
    --skip-e2e)
      SKIP_E2E="true"
      ;;
    --skip-memory-setup)
      SKIP_MEMORY_SETUP="true"
      ;;
    --e2e-serial)
      E2E_FLAGS+=("--serial")
      ;;
    --e2e-verbose)
      E2E_FLAGS+=("--verbose")
      ;;
    *)
      printf 'Unknown flag: %s\n' "$arg" >&2
      printf 'Usage: %s [--skip-e2e] [--skip-memory-setup] [--e2e-serial] [--e2e-verbose]\n' "$0" >&2
      exit 1
      ;;
  esac
done

if [[ -t 1 ]]; then
  BOLD='\033[1m'
  GREEN='\033[0;32m'
  RED='\033[0;31m'
  RESET='\033[0m'
else
  BOLD=''
  GREEN=''
  RED=''
  RESET=''
fi

section() {
  printf '\n%s%s%s\n' "${BOLD}" "$*" "${RESET}"
}

info() {
  printf '==> %s\n' "$*"
}

pass() {
  printf '%s✔ %s%s\n' "${GREEN}" "$*" "${RESET}"
}

fail() {
  printf '%s✖ %s%s\n' "${RED}" "$*" "${RESET}" >&2
  exit 1
}

require_cmd() {
  command -v "$1" >/dev/null 2>&1 || fail "missing required command: $1"
}

run_step() {
  local label="$1"
  shift
  section "$label"
  info "$*"
  "$@"
  pass "$label"
}

run_shell_step() {
  local label="$1"
  local workdir="$2"
  shift 2
  section "$label"
  info "(cd ${workdir} && $*)"
  (
    cd "${workdir}"
    "$@"
  )
  pass "$label"
}

setup_memory_db() {
  if [[ "${SKIP_MEMORY_SETUP}" == "true" ]]; then
    info "Skipping memory DB setup"
    return 0
  fi

  require_cmd docker

  section "Memory DB Setup"
  info "Starting docker compose memory-db"
  (
    cd "${ROOT_DIR}"
    docker compose up -d memory-db
  )

  info "Waiting for memory-db readiness"
  for _ in $(seq 1 30); do
    if (
      cd "${ROOT_DIR}" &&
      docker compose exec -T memory-db pg_isready -U user -d memory_db
    ) >/dev/null 2>&1; then
      pass "Memory DB Setup"
      return 0
    fi
    sleep 2
  done

  fail "memory-db did not become ready in time"
}

discover_go_modules() {
  find "${ROOT_DIR}" \
    \( \
      -path "${ROOT_DIR}/.git" -o \
      -path "${ROOT_DIR}/.claude" -o \
      -path "${ROOT_DIR}/.worktrees" -o \
      -path '*/vendor' -o \
      -path '*/node_modules' \
    \) -prune -o \
    -name go.mod -print \
    | sed 's|/go.mod$||' \
    | sort
}

discover_bun_test_packages() {
  find "${ROOT_DIR}" \
    \( \
      -path "${ROOT_DIR}/.git" -o \
      -path "${ROOT_DIR}/.claude" -o \
      -path "${ROOT_DIR}/.worktrees" -o \
      -path '*/vendor' -o \
      -path '*/node_modules' \
    \) -prune -o \
    -name package.json -print \
    | while IFS= read -r package_json; do
        if rg -q '"test"\s*:' "${package_json}"; then
          dirname "${package_json}"
        fi
      done \
    | sort
}

run_go_module_tests() {
  local module_dir
  while IFS= read -r module_dir; do
    [[ -n "${module_dir}" ]] || continue

    if [[ "${module_dir}" == "${ROOT_DIR}/memory" ]]; then
      setup_memory_db
      section "Go Tests: memory"
      info "(cd memory && go test -count=1 ./internal/api ./cmd/server)"
      (
        cd "${module_dir}"
        DB_HOST="${DB_HOST:-localhost}" \
        DB_PORT="${DB_PORT:-5432}" \
        DB_USER="${DB_USER:-user}" \
        DB_PASSWORD="${DB_PASSWORD:-password}" \
        DB_NAME="${DB_NAME:-memory_db}" \
        EMBEDDING_MODEL="${EMBEDDING_MODEL:-microsoft/harrier-oss-v1-270m}" \
        EMBEDDING_DIM="${EMBEDDING_DIM:-640}" \
        EMBEDDING_PROMPT_STYLE="${EMBEDDING_PROMPT_STYLE:-harrier}" \
        VAULT_MASTER_KEY="${VAULT_MASTER_KEY:-0123456789abcdef0123456789abcdef}" \
        INTERNAL_VAULT_API_KEY="${INTERNAL_VAULT_API_KEY:-test-vault-key}" \
        GOCACHE="${GOCACHE:-/tmp/go-build}" \
        go test -count=1 ./internal/api ./cmd/server
      )
      info "(cd memory && go test -count=1 ./tests)"
      (
        cd "${module_dir}"
        DB_HOST="${DB_HOST:-localhost}" \
        DB_PORT="${DB_PORT:-5432}" \
        DB_USER="${DB_USER:-user}" \
        DB_PASSWORD="${DB_PASSWORD:-password}" \
        DB_NAME="${DB_NAME:-memory_db}" \
        EMBEDDING_MODEL="${EMBEDDING_MODEL:-microsoft/harrier-oss-v1-270m}" \
        EMBEDDING_DIM="${EMBEDDING_DIM:-640}" \
        EMBEDDING_PROMPT_STYLE="${EMBEDDING_PROMPT_STYLE:-harrier}" \
        VAULT_MASTER_KEY="${VAULT_MASTER_KEY:-0123456789abcdef0123456789abcdef}" \
        INTERNAL_VAULT_API_KEY="${INTERNAL_VAULT_API_KEY:-test-vault-key}" \
        GOCACHE="${GOCACHE:-/tmp/go-build}" \
        go test -count=1 ./tests
      )
      pass "Go Tests: memory"
      continue
    fi

    run_shell_step "Go Tests: ${module_dir#${ROOT_DIR}/}" "${module_dir}" go test -count=1 ./...
  done < <(discover_go_modules)
}

discover_bun_workspaces() {
  find "${ROOT_DIR}" \
    \( \
      -path "${ROOT_DIR}/.git" -o \
      -path "${ROOT_DIR}/.claude" -o \
      -path "${ROOT_DIR}/.worktrees" -o \
      -path '*/vendor' -o \
      -path '*/node_modules' \
    \) -prune -o \
    -name package.json -print \
    | while IFS= read -r package_json; do
        if rg -q '"workspaces"' "${package_json}"; then
          dirname "${package_json}"
        fi
      done \
    | sort
}

ensure_bun_workspaces_installed() {
  local workspace_dir
  while IFS= read -r workspace_dir; do
    [[ -n "${workspace_dir}" ]] || continue
    require_cmd bun
    section "Bun Install: ${workspace_dir#${ROOT_DIR}/}"
    info "(cd ${workspace_dir} && bun install)"
    (cd "${workspace_dir}" && bun install)
    pass "Bun Install: ${workspace_dir#${ROOT_DIR}/}"
  done < <(discover_bun_workspaces)
}

run_bun_package_tests() {
  local package_dir
  while IFS= read -r package_dir; do
    [[ -n "${package_dir}" ]] || continue
    require_cmd bun
    run_shell_step "Bun Tests: ${package_dir#${ROOT_DIR}/}" "${package_dir}" bun run test
  done < <(discover_bun_test_packages)
}

run_repo_e2e() {
  if [[ "${SKIP_E2E}" == "true" ]]; then
    section "E2E Tests"
    info "Skipping tests/e2e/run_all.sh"
    return 0
  fi

  require_cmd bash
  run_shell_step "E2E Tests" "${ROOT_DIR}" bash tests/e2e/run_all.sh "${E2E_FLAGS[@]+"${E2E_FLAGS[@]}"}"
}

main() {
  require_cmd go
  require_cmd rg

  section "Repo Test Runner"
  printf 'Root: %s\n' "${ROOT_DIR}"
  printf 'Skip e2e: %s\n' "${SKIP_E2E}"
  printf 'Skip memory setup: %s\n' "${SKIP_MEMORY_SETUP}"

  run_go_module_tests
  ensure_bun_workspaces_installed
  run_bun_package_tests
  run_repo_e2e

  printf '\n%sAll discovered repo tests passed.%s\n' "${GREEN}" "${RESET}"
}

main
