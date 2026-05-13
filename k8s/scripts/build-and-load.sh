#!/usr/bin/env bash
# Build all service images locally and load them into the kind cluster.
# Use this instead of a registry during local development.
# Usage: ./k8s/scripts/build-and-load.sh [cluster-name]
set -euo pipefail

for arg in "$@"; do
  case $arg in
    -h|--help)
      echo "Usage: ./k8s/scripts/build-and-load.sh [CLUSTER_NAME]"
      echo ""
      echo "Build all service Docker images and load them into a kind cluster."
      echo ""
      echo "Arguments:"
      echo "  CLUSTER_NAME  Name of the kind cluster to load images into (default: cerberos)"
      echo ""
      echo "Options:"
      echo "  -h, --help  Show this help message"
      echo ""
      echo "Examples:"
      echo "  ./k8s/scripts/build-and-load.sh            # load into 'cerberos' cluster"
      echo "  ./k8s/scripts/build-and-load.sh my-cluster # load into 'my-cluster'"
      exit 0
      ;;
  esac
done

CLUSTER="${1:-cerberos}"
REPO_ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
TAG="local"

echo "==> Building and loading images into kind cluster '${CLUSTER}'"

build_and_load() {
  local name="$1"
  local ctx="$2"
  local target="${3:-}"
  echo ""
  echo "--- Building ${name}:${TAG} from ${ctx} ${target:+(target: $target)} ---"
  if [ -n "$target" ]; then
    docker build --target "$target" -t "${name}:${TAG}" "$ctx"
  else
    docker build -t "${name}:${TAG}" "$ctx"
  fi
  echo "--- Loading ${name}:${TAG} into kind ---"
  kind load docker-image "${name}:${TAG}" --name "${CLUSTER}"
}

# Pre-pull upstream third-party images on the host and load them onto every
# kind node. Without this each worker has to pull (e.g. pgvector ~400MB) via
# its own kubelet, which on Docker Hub can stretch to 15+ minutes per node
# and is the primary cause of "did not become ready in time" rollouts.
prepull_and_load() {
  local image="$1"
  echo ""
  echo "--- Pre-pulling ${image} ---"
  docker pull "${image}"
  echo "--- Loading ${image} into kind ---"
  kind load docker-image "${image}" --name "${CLUSTER}"
}

build_and_load cerberos-orchestrator   "${REPO_ROOT}/orchestrator"
build_and_load cerberos-io             "${REPO_ROOT}/io"
build_and_load cerberos-memory-api     "${REPO_ROOT}/memory"
build_and_load cerberos-embedding-api  "${REPO_ROOT}/embedding-api"
build_and_load cerberos-vault-engine   "${REPO_ROOT}/vault/engine"
build_and_load cerberos-aegis-agents   "${REPO_ROOT}/agents-component"
build_and_load cerberos-aegis-databus  "${REPO_ROOT}/aegis-databus" aegis-databus

# Third-party infra images. Keep this list in sync with the image refs in
# k8s/helm/charts/{postgres,nats,openbao}/values.yaml.
prepull_and_load pgvector/pgvector:pg16
prepull_and_load nats:2.10-alpine
prepull_and_load openbao/openbao:latest

echo ""
echo "==> All images loaded. Run 'kubectl get nodes' to verify the cluster."
