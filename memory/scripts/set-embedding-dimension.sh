#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
TARGET_FILE="${SCRIPT_DIR}/init-db.sql"
DIMENSION="${1:-${EMBEDDING_DIM:-}}"

if [ -z "${DIMENSION}" ]; then
  echo "usage: $0 <dimension>" >&2
  exit 1
fi

if ! [[ "${DIMENSION}" =~ ^[0-9]+$ ]] || [ "${DIMENSION}" -le 0 ]; then
  echo "embedding dimension must be a positive integer, got: ${DIMENSION}" >&2
  exit 1
fi

python3 - "${TARGET_FILE}" "${DIMENSION}" <<'PY'
import pathlib
import re
import sys

path = pathlib.Path(sys.argv[1])
dimension = sys.argv[2]
text = path.read_text()
updated, count = re.subn(r'embedding VECTOR\(\d+\)', f'embedding VECTOR({dimension})', text, count=1)
if count != 1:
    raise SystemExit(f"expected exactly one embedding VECTOR(...) declaration in {path}")
path.write_text(updated)
PY

echo "Updated ${TARGET_FILE} to embedding VECTOR(${DIMENSION})"
