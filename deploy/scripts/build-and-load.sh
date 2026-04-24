#!/usr/bin/env bash
# Build all service images locally and load them into the kind cluster.
# Use this instead of a registry during local development.
# Usage: ./deploy/scripts/build-and-load.sh [cluster-name]
set -euo pipefail

CLUSTER="${1:-cerberos}"
REPO_ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
TAG="local"

declare -A SERVICES=(
  ["cerberos-orchestrator"]="${REPO_ROOT}/orchestrator"
  ["cerberos-io"]="${REPO_ROOT}/io"
  ["cerberos-memory-api"]="${REPO_ROOT}/memory"
  ["cerberos-vault-engine"]="${REPO_ROOT}/vault/engine"
  ["cerberos-aegis-agents"]="${REPO_ROOT}/agents-component"
)

echo "==> Building and loading images into kind cluster '${CLUSTER}'"

for svc in "${!SERVICES[@]}"; do
  ctx="${SERVICES[$svc]}"
  echo ""
  echo "--- Building ${svc}:${TAG} from ${ctx} ---"

  # aegis-databus uses a multi-stage target
  if [ "$svc" = "cerberos-aegis-databus" ]; then
    docker build --target aegis-databus -t "${svc}:${TAG}" "${ctx}"
  else
    docker build -t "${svc}:${TAG}" "${ctx}"
  fi

  echo "--- Loading ${svc}:${TAG} into kind ---"
  kind load docker-image "${svc}:${TAG}" --name "${CLUSTER}"
done

# aegis-databus is a separate build-target so add it explicitly
echo ""
echo "--- Building cerberos-aegis-databus:${TAG} ---"
docker build --target aegis-databus -t "cerberos-aegis-databus:${TAG}" "${REPO_ROOT}/aegis-databus"
echo "--- Loading cerberos-aegis-databus:${TAG} into kind ---"
kind load docker-image "cerberos-aegis-databus:${TAG}" --name "${CLUSTER}"

echo ""
echo "==> All images loaded. Run 'kubectl get nodes' to verify the cluster."
