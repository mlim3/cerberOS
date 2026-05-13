#!/usr/bin/env bash
# Run all e2e tests in parallel (default) or series (--serial).
# Output from each test is suppressed unless --verbose is set or the test fails.
#
# Usage:
#   ./run_all.sh [--serial] [--verbose]
#
#   --serial    Run tests one at a time (useful to isolate parallelism failures)
#   --verbose   Print each test's full output regardless of pass/fail

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# ── flags ─────────────────────────────────────────────────────────────────────
SERIAL=false
VERBOSE=false
for arg in "$@"; do
  case "$arg" in
    --serial)  SERIAL=true  ;;
    --verbose) VERBOSE=true ;;
    *)
      printf 'Unknown flag: %s\n' "$arg" >&2
      printf 'Usage: %s [--serial] [--verbose]\n' "$0" >&2
      exit 1
      ;;
  esac
done

# ── colour helpers ─────────────────────────────────────────────────────────────
if [[ -t 1 ]]; then
  GREEN=$'\033[0;32m'; RED=$'\033[0;31m'; YELLOW=$'\033[0;33m'
  BOLD=$'\033[1m'; RESET=$'\033[0m'
else
  GREEN=''; RED=''; YELLOW=''; BOLD=''; RESET=''
fi

pass_line() { printf '%b  PASS%b  %s  %b(%ds)%b\n' "${GREEN}" "${RESET}" "$1" "${YELLOW}" "$2" "${RESET}"; }
fail_line() { printf '%b  FAIL%b  %s  %b(%ds)%b\n' "${RED}" "${RESET}" "$1" "${YELLOW}" "$2" "${RESET}"; }

# ── discover tests ─────────────────────────────────────────────────────────────
TESTS=()
while IFS= read -r test_script; do
  TESTS+=("$test_script")
done < <(
  find "$SCRIPT_DIR" -maxdepth 1 -name '*.sh' ! -name "$(basename "$0")" | sort
)

SERIAL_ONLY_TESTS=(
  "agents_cross_domain_skill_access.sh"
  "agents_nl_skill_create.sh"
  "agents_skill_search.sh"
)

if [[ ${#TESTS[@]} -eq 0 ]]; then
  printf 'No e2e test scripts found in %s\n' "$SCRIPT_DIR" >&2
  exit 1
fi

mode_label="parallel"
$SERIAL && mode_label="serial"

printf '\n%bE2E Test Runner%b — mode: %s\n' "$BOLD" "$RESET" "$mode_label"
printf 'Found %d test(s):\n' "${#TESTS[@]}"
for t in "${TESTS[@]}"; do printf '  %s\n' "$(basename "$t")"; done
printf '\n'

# ── run helpers ────────────────────────────────────────────────────────────────
# Runs a single test, captures output, prints one-line result.
# Writes exit code to <log_file>.exit and output to <log_file>.out
run_test() {
  local script="$1"
  local name
  name="$(basename "$script" .sh)"
  local log_base
  log_base="$(mktemp -t "e2e_${name}_XXXXXX")"
  local out_file="${log_base}.out"
  local exit_file="${log_base}.exit"

  local start
  start="$(date +%s)"

  if $VERBOSE; then
    # tee so we see output live AND capture it for the failure block
    bash "$script" 2>&1 | tee "$out_file"
    echo "${PIPESTATUS[0]}" > "$exit_file"
  else
    bash "$script" >"$out_file" 2>&1
    echo "$?" > "$exit_file"
  fi

  local end elapsed exit_code
  end="$(date +%s)"
  elapsed=$(( end - start ))
  exit_code="$(cat "$exit_file")"

  if [[ "$exit_code" -eq 0 ]]; then
    pass_line "$name" "$elapsed"
  else
    fail_line "$name" "$elapsed"
    if ! $VERBOSE; then
      printf '  ── output from %s ──\n' "$name"
      sed 's/^/  | /' "$out_file"
      printf '  ── end %s ──\n\n' "$name"
    fi
  fi

  rm -f "$out_file" "$exit_file" "$log_base"
  return "$exit_code"
}

# ── execution ──────────────────────────────────────────────────────────────────
FAILURES=0

if $SERIAL; then
  for t in "${TESTS[@]}"; do
    run_test "$t" || FAILURES=$(( FAILURES + 1 ))
  done
else
  serial_only=()
  parallel_tests=()
  for t in "${TESTS[@]}"; do
    base="$(basename "$t")"
    if printf '%s\n' "${SERIAL_ONLY_TESTS[@]}" | rg -q "^${base}$"; then
      serial_only+=("$t")
    else
      parallel_tests+=("$t")
    fi
  done

  # Launch all tests concurrently; collect results in order.
  pids=()
  log_bases=()
  names=()

  for t in "${parallel_tests[@]}"; do
    name="$(basename "$t" .sh)"
    log_base="$(mktemp -t "e2e_${name}_XXXXXX")"
    out_file="${log_base}.out"
    exit_file="${log_base}.exit"
    start_file="${log_base}.start"

    date +%s > "$start_file"

    if $VERBOSE; then
      bash "$t" 2>&1 | tee "$out_file"; echo "${PIPESTATUS[0]}" > "$exit_file" &
    else
      ( bash "$t" >"$out_file" 2>&1; echo "$?" > "$exit_file" ) &
    fi

    pids+=("$!")
    log_bases+=("$log_base")
    names+=("$name")
  done

  for i in "${!pids[@]}"; do
    wait "${pids[$i]}" 2>/dev/null || true

    name="${names[$i]}"
    log_base="${log_bases[$i]}"
    out_file="${log_base}.out"
    exit_file="${log_base}.exit"
    start_file="${log_base}.start"

    exit_code="$(cat "$exit_file" 2>/dev/null || echo 1)"
    start="$(cat "$start_file" 2>/dev/null || echo 0)"
    elapsed=$(( $(date +%s) - start ))

    if [[ "$exit_code" -eq 0 ]]; then
      pass_line "$name" "$elapsed"
    else
      FAILURES=$(( FAILURES + 1 ))
      fail_line "$name" "$elapsed"
      if ! $VERBOSE; then
        printf '  ── output from %s ──\n' "$name"
        sed 's/^/  | /' "$out_file"
        printf '  ── end %s ──\n\n' "$name"
      fi
    fi

    rm -f "$out_file" "$exit_file" "$start_file" "$log_base"
  done

  if [[ ${#serial_only[@]} -gt 0 ]]; then
    printf '\n%bRunning serial-only e2e tests%b\n' "${BOLD}" "${RESET}"
    for t in "${serial_only[@]}"; do
      run_test "$t" || FAILURES=$(( FAILURES + 1 ))
    done
  fi
fi

# ── summary ────────────────────────────────────────────────────────────────────
printf '\n'
total="${#TESTS[@]}"
passed=$(( total - FAILURES ))

if [[ "$FAILURES" -eq 0 ]]; then
  printf '%b%bAll %d test(s) passed.%b\n\n' "${BOLD}" "${GREEN}" "$total" "${RESET}"
  exit 0
else
  printf '%b%b%d/%d test(s) failed.%b\n\n' "${BOLD}" "${RED}" "$FAILURES" "$total" "${RESET}"
  exit 1
fi
