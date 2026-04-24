#!/usr/bin/env bash
# Create the kind cluster, load local images, and install the umbrella Helm chart.
# Usage: ./deploy/scripts/kind-up.sh [--skip-build] [--skip-install]
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
CLUSTER="cerberos"
NAMESPACE="cerberos"
SKIP_BUILD=false
SKIP_INSTALL=false

for arg in "$@"; do
  case $arg in
    --skip-build) SKIP_BUILD=true ;;
    --skip-install) SKIP_INSTALL=true ;;
  esac
done

echo "==> [1/4] Creating kind cluster '${CLUSTER}' ..."
if kind get clusters 2>/dev/null | grep -q "^${CLUSTER}$"; then
  echo "    Cluster already exists, skipping creation."
else
  kind create cluster --name "${CLUSTER}" --config "${SCRIPT_DIR}/../kind/cluster.yaml"
fi

echo ""
echo "==> [2/4] Creating namespace '${NAMESPACE}' ..."
kubectl create namespace "${NAMESPACE}" --dry-run=client -o yaml | kubectl apply -f -

if [ "$SKIP_BUILD" = false ]; then
  echo ""
  echo "==> [3/4] Building & loading images ..."
  "${SCRIPT_DIR}/build-and-load.sh" "${CLUSTER}"
else
  echo ""
  echo "==> [3/4] Skipping image build (--skip-build)."
fi

if [ "$SKIP_INSTALL" = false ]; then
  echo ""
  echo "==> [4/4] Installing umbrella Helm chart ..."
  helm dependency update "${REPO_ROOT}/deploy/helm/cerberos"
  helm upgrade --install cerberos "${REPO_ROOT}/deploy/helm/cerberos" \
    --namespace "${NAMESPACE}" \
    --values "${REPO_ROOT}/deploy/helm/cerberos/values-dev.yaml" \
    --wait --timeout 10m
else
  echo ""
  echo "==> [4/4] Skipping Helm install (--skip-install)."
fi

echo ""
echo "==> Done! Useful commands:"
echo "    kubectl get pods -n ${NAMESPACE} -o wide"
echo "    kubectl port-forward -n ${NAMESPACE} svc/io 3001:3001"
echo "    kubectl port-forward -n ${NAMESPACE} svc/grafana 3000:80"
echo "    helm uninstall cerberos -n ${NAMESPACE}"
