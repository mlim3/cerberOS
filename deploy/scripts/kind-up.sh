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

echo "==> [1/5] Creating kind cluster '${CLUSTER}' ..."
if kind get clusters 2>/dev/null | grep -q "^${CLUSTER}$"; then
  echo "    Cluster already exists, skipping creation."
else
  kind create cluster --name "${CLUSTER}" --config "${SCRIPT_DIR}/../kind/cluster.yaml"
fi

echo ""
echo "==> [2/5] Creating namespace '${NAMESPACE}' ..."
kubectl create namespace "${NAMESPACE}" --dry-run=client -o yaml | kubectl apply -f -

echo ""
echo "==> [3/5] Ensuring required Helm repos are configured ..."
helm repo add grafana https://grafana.github.io/helm-charts --force-update >/dev/null 2>&1 || true
helm repo add prometheus-community https://prometheus-community.github.io/helm-charts --force-update >/dev/null 2>&1 || true
helm repo update >/dev/null

if [ "$SKIP_BUILD" = false ]; then
  echo ""
  echo "==> [4/5] Building & loading images ..."
  "${SCRIPT_DIR}/build-and-load.sh" "${CLUSTER}"
else
  echo ""
  echo "==> [4/5] Skipping image build (--skip-build)."
fi

if [ "$SKIP_INSTALL" = false ]; then
  echo ""
  echo "==> [5/5] Installing umbrella Helm chart ..."
  # Resolve sub-chart deps for any component chart that declares them
  # (e.g. observability → prometheus/grafana/loki/tempo). Must run BEFORE
  # the umbrella's helm dependency update, otherwise the observability
  # sub-chart gets packaged without its own dependencies and none of the
  # monitoring pods ever deploy.
  for chart in "${REPO_ROOT}"/deploy/helm/charts/*/; do
    if grep -q "^dependencies:" "$chart/Chart.yaml" 2>/dev/null; then
      echo "    Resolving deps for $(basename "$chart") ..."
      helm dependency update "$chart" >/dev/null
    fi
  done
  helm dependency update "${REPO_ROOT}/deploy/helm/cerberos" >/dev/null
  HELM_SET_ARGS=()
  if [ -n "${ANTHROPIC_API_KEY:-}" ]; then
    HELM_SET_ARGS+=(--set "aegis-agents.anthropicApiKey=${ANTHROPIC_API_KEY}")
  fi
  if [ -n "${ANTHROPIC_BASE_URL:-}" ]; then
    HELM_SET_ARGS+=(--set "aegis-agents.anthropicBaseUrl=${ANTHROPIC_BASE_URL}")
  fi

  helm upgrade --install cerberos "${REPO_ROOT}/deploy/helm/cerberos" \
    --namespace "${NAMESPACE}" \
    --values "${REPO_ROOT}/deploy/helm/cerberos/values-dev.yaml" \
    "${HELM_SET_ARGS[@]}"

  echo ""
  echo "    Waiting for core workloads to be ready (up to 5 min) ..."
  # StatefulSets: use rollout status (handles the case where the pod
  # hasn't been created yet when we start waiting).
  for sts in memory-db openbao nats; do
    kubectl rollout status statefulset "${sts}" -n "${NAMESPACE}" --timeout=5m \
      || echo "    (warning: statefulset/${sts} did not become ready in time)"
  done
  # Deployments: wait on the deployment condition directly.
  for deploy in memory-api orchestrator io; do
    kubectl rollout status deployment "${deploy}" -n "${NAMESPACE}" --timeout=5m \
      || echo "    (warning: deployment/${deploy} did not become ready in time)"
  done
else
  echo ""
  echo "==> [5/5] Skipping Helm install (--skip-install)."
fi

echo ""
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo "  cerberOS is up!"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo ""
echo "  Web UI:         http://localhost:3001"
echo "  Orchestrator:   http://localhost:8080/health"
echo "  Grafana:        http://localhost:3000  (admin / admin)"
echo "  NATS:           nats://localhost:4222"
echo ""
echo "  OpenBao (dev):  kubectl port-forward -n ${NAMESPACE} svc/openbao 8200:8200"
echo "                  root token: root"
echo ""
echo "  All pods:       kubectl get pods -n ${NAMESPACE} -o wide"
echo "  Tear down:      ./deploy/scripts/kind-down.sh"
echo ""
echo "  Anthropic (aegis-agents):"
if [ -n "${ANTHROPIC_API_KEY:-}" ]; then
  echo "    ANTHROPIC_API_KEY  ✓ injected"
else
  echo "    ANTHROPIC_API_KEY  ✗ not set"
  echo "      Live cluster:  kubectl set env deployment/aegis-agents ANTHROPIC_API_KEY=<key> -n ${NAMESPACE}"
  echo "      Fresh start:   export ANTHROPIC_API_KEY=<key> && ./deploy/scripts/kind-up.sh --skip-build"
fi
if [ -n "${ANTHROPIC_BASE_URL:-}" ]; then
  echo "    ANTHROPIC_BASE_URL ✓ injected: ${ANTHROPIC_BASE_URL}"
else
  echo "    ANTHROPIC_BASE_URL ✗ not set (using Anthropic default endpoint)"
  echo "      Live cluster:  kubectl set env deployment/aegis-agents ANTHROPIC_BASE_URL=<url> -n ${NAMESPACE}"
  echo "      Fresh start:   export ANTHROPIC_BASE_URL=<url> && ./deploy/scripts/kind-up.sh --skip-build"
fi
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
