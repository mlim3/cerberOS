#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
CANONICAL_FILE="${SCRIPT_DIR}/init-db.sql"
K8S_COPY="${REPO_ROOT}/k8s/helm/charts/postgres/files/init-db.sql"
DIMENSION="${1:-${EMBEDDING_DIM:-}}"

if [ -z "${DIMENSION}" ]; then
  echo "usage: $0 <dimension>" >&2
  exit 1
fi

if ! [[ "${DIMENSION}" =~ ^[0-9]+$ ]] || [ "${DIMENSION}" -le 0 ]; then
  echo "embedding dimension must be a positive integer, got: ${DIMENSION}" >&2
  exit 1
fi

python3 - "${CANONICAL_FILE}" "${DIMENSION}" <<'PY'
import pathlib
import re
import sys

path = pathlib.Path(sys.argv[1])
dimension = sys.argv[2]
text = path.read_text()
updated, count = re.subn(r'embedding VECTOR\(\d+\)', f'embedding VECTOR({dimension})', text)
if count < 1:
    raise SystemExit(f"expected at least one embedding VECTOR(...) declaration in {path}")
path.write_text(updated)
PY

echo "Updated ${CANONICAL_FILE} to embedding VECTOR(${DIMENSION})"

# Mirror the canonical schema into the Helm chart so the K8s Postgres initdb
# ConfigMap can never drift from memory/scripts/init-db.sql. Helm's Files.Get
# is restricted to the chart directory, so we copy rather than symlink.
if [ -f "${K8S_COPY}" ] || [ -d "$(dirname "${K8S_COPY}")" ]; then
  cp "${CANONICAL_FILE}" "${K8S_COPY}"
  echo "Mirrored to ${K8S_COPY}"
fi
