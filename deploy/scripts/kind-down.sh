#!/usr/bin/env bash
# Tear down the kind cluster entirely.
# Usage: ./deploy/scripts/kind-down.sh
set -euo pipefail

CLUSTER="cerberos"

for arg in "$@"; do
  case $arg in
    -h|--help)
      echo "Usage: ./deploy/scripts/kind-down.sh"
      echo ""
      echo "Tear down the '${CLUSTER}' kind cluster entirely."
      echo ""
      echo "Options:"
      echo "  -h, --help  Show this help message"
      exit 0
      ;;
  esac
done

echo "==> Deleting kind cluster '${CLUSTER}' ..."
kind delete cluster --name "${CLUSTER}"
echo "Done."
