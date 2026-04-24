#!/usr/bin/env bash
# Tear down the kind cluster entirely.
# Usage: ./deploy/scripts/kind-down.sh
set -euo pipefail

CLUSTER="cerberos"

echo "==> Deleting kind cluster '${CLUSTER}' ..."
kind delete cluster --name "${CLUSTER}"
echo "Done."
