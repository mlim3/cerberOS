#!/bin/sh
# auto-unseal.sh — sidecar loop that keeps OpenBao unsealed for the demo.
#
# Why this exists: OpenBao seals on every container restart by design (a
# safety property), but during local demos that means any `docker compose
# restart vault` or `up -d` of an upstream service can wedge the whole
# vault path until someone manually re-runs `bao operator unseal`. We hit
# this twice in one debug session. This sidecar makes "restart anything,
# OpenBao stays usable" the default.
#
# Production: do NOT use this. Use a real auto-unseal backend (cloud KMS,
# transit, HSM). Storing the unseal key on disk next to the data defeats
# the seal entirely. We're optimizing for demo reliability, not security.
#
# How it works:
#   - Reads the unseal key from /init.json (mounted from
#     vault/.openbao-init.json, written by `bao operator init` during
#     bootstrap.sh).
#   - Polls OpenBao's /v1/sys/seal-status every 5s.
#   - When sealed, POSTs the key to /v1/sys/unseal.
#   - Logs each unseal action so it's visible in `docker logs`.

set -eu

OPENBAO_ADDR="${OPENBAO_ADDR:-http://openbao:8200}"
INIT_FILE="${INIT_FILE:-/init.json}"
POLL_INTERVAL="${POLL_INTERVAL:-5}"

echo "[auto-unseal] starting; openbao=${OPENBAO_ADDR} init_file=${INIT_FILE} poll=${POLL_INTERVAL}s"

# load_key — re-read INIT_FILE and extract unseal_keys_b64[0]. Returns
# empty string if the file doesn't exist yet, is empty, or is mid-write
# during a fresh bootstrap. Caller decides what to do.
#
# bao operator init -format=json pretty-prints across multiple lines, so we
# flatten with `tr` first to make the key/value match independent of layout.
load_key() {
  if [ ! -s "${INIT_FILE}" ]; then
    echo ''
    return
  fi
  tr -d '\n\r\t ' < "${INIT_FILE}" \
    | grep -o '"unseal_keys_b64":\["[^"]*' 2>/dev/null \
    | sed 's/.*"//' || true
}

UNSEAL_KEY=''
while true; do
  if [ -z "${UNSEAL_KEY}" ]; then
    UNSEAL_KEY=$(load_key)
    if [ -n "${UNSEAL_KEY}" ]; then
      echo "[auto-unseal] unseal key loaded (${#UNSEAL_KEY} chars)"
    fi
  fi
  STATUS_BODY=$(curl -fs --max-time 3 "${OPENBAO_ADDR}/v1/sys/seal-status" 2>/dev/null || echo '')
  if [ -z "${STATUS_BODY}" ]; then
    sleep "${POLL_INTERVAL}"
    continue
  fi
  case "${STATUS_BODY}" in
    *'"sealed":true'*)
      if [ -z "${UNSEAL_KEY}" ]; then
        # Fresh bootstrap: bao operator init hasn't run yet. bootstrap.sh
        # will run it shortly and write INIT_FILE; then we'll pick up the
        # key on the next loop iteration. Stay quiet to avoid log spam.
        :
      else
        echo "[auto-unseal] OpenBao is sealed; submitting unseal key..."
        RESP=$(curl -fs --max-time 5 -X POST \
          -H 'Content-Type: application/json' \
          -d "{\"key\":\"${UNSEAL_KEY}\"}" \
          "${OPENBAO_ADDR}/v1/sys/unseal" 2>&1 || echo 'UNSEAL_FAILED')
        case "${RESP}" in
          *'"sealed":false'*) echo "[auto-unseal] OpenBao unsealed successfully" ;;
          *)                  echo "[auto-unseal] unseal call returned: ${RESP}" ;;
        esac
      fi
      ;;
  esac
  sleep "${POLL_INTERVAL}"
done
