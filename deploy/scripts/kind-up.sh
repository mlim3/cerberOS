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

# Load repo `.env` keys only when not already set in the shell (same rule as io/api load-env).
# Supports `KEY=value`, `export KEY=value`, and optional whitespace around `=`.
merge_dotenv() {
  local f="$1"
  [ -f "$f" ] || return 0
  echo "==> Merging $(basename "$f") from repo root (existing shell variables win)"
  local line key v
  while IFS= read -r line || [ -n "$line" ]; do
    [[ "$line" =~ ^[[:space:]]*# ]] && continue
    [[ -z "${line// /}" ]] && continue
    # UTF-8 BOM (some editors prefix the first line)
    if [[ "$line" == $'\xEF\xBB\xBF'* ]]; then
      line="${line:3}"
    fi
    if [[ "$line" =~ ^[[:space:]]*export[[:space:]]+([A-Za-z_][A-Za-z0-9_]*)[[:space:]]*=[[:space:]]*(.*)$ ]]; then
      key="${BASH_REMATCH[1]}"
      v="${BASH_REMATCH[2]}"
    elif [[ "$line" =~ ^[[:space:]]*([A-Za-z_][A-Za-z0-9_]*)[[:space:]]*=[[:space:]]*(.*)$ ]]; then
      key="${BASH_REMATCH[1]}"
      v="${BASH_REMATCH[2]}"
    else
      continue
    fi
    if [ -n "${!key+x}" ]; then
      continue
    fi
    v="${v%$'\r'}"
    if [[ "$v" =~ ^\".*\"$ ]]; then
      v="${v:1:-1}"
    elif [[ "$v" =~ ^\'.*\'$ ]]; then
      v="${v:1:-1}"
    else
      v="${v#"${v%%[![:space:]]*}"}"
      v="${v%%[[:space:]]#*}"
    fi
    export "${key}=${v}"
  done <"$f"
}

merge_dotenv "${REPO_ROOT}/.env"

for arg in "$@"; do
  case $arg in
    --skip-build) SKIP_BUILD=true ;;
    --skip-install) SKIP_INSTALL=true ;;
    -h|--help)
      echo "Usage: ./deploy/scripts/kind-up.sh [OPTIONS]"
      echo ""
      echo "Create the kind cluster, build & load images, and install the cerberOS Helm chart."
      echo ""
      echo "Options:"
      echo "  --skip-build    Skip Docker image builds (use already-loaded images)"
      echo "  --skip-install  Skip Helm chart install/upgrade"
      echo "  -h, --help      Show this help message"
      echo ""
      echo "Environment variables (auto-injected into aegis-agents if set):"
      echo "  ANTHROPIC_API_KEY   Anthropic API key"
      echo "  ANTHROPIC_BASE_URL  Anthropic API base URL (defaults to Anthropic's standard endpoint)"
      echo ""
      echo "  Variables may be set in the shell or in repo-root .env (shell wins if both are set)."
      echo ""
      echo "Examples:"
      echo "  ./deploy/scripts/kind-up.sh                   # full setup"
      echo "  ./deploy/scripts/kind-up.sh --skip-build      # reinstall chart without rebuilding images"
      echo "  ./deploy/scripts/kind-up.sh --skip-install    # rebuild & reload images only"
      echo ""
      echo "Manual helm upgrade after editing deploy/helm/charts/* templates:"
      echo "  helm dependency update ./deploy/helm/cerberos && helm upgrade ..."
      exit 0
      ;;
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
    HELM_SET_ARGS+=(--set-string "aegis-agents.anthropicApiKey=${ANTHROPIC_API_KEY}")
  fi
  if [ -n "${ANTHROPIC_BASE_URL:-}" ]; then
    HELM_SET_ARGS+=(--set-string "aegis-agents.anthropicBaseUrl=${ANTHROPIC_BASE_URL}")
  fi

  # Bash with `set -u` treats "${HELM_SET_ARGS[@]}" as an error when the array is empty
  # on some versions; temporarily allow unset for this invocation.
  set +u
  helm upgrade --install cerberos "${REPO_ROOT}/deploy/helm/cerberos" \
    --namespace "${NAMESPACE}" \
    --values "${REPO_ROOT}/deploy/helm/cerberos/values-dev.yaml" \
    "${HELM_SET_ARGS[@]}"
  set -u

  echo ""
  echo "    Waiting for core workloads to be ready (StatefulSets up to 10 min; deployment/io up to 15 min) ..."
  # StatefulSets: use rollout status (handles the case where the pod
  # hasn't been created yet when we start waiting).
  for sts in memory-db openbao nats; do
    kubectl rollout status statefulset "${sts}" -n "${NAMESPACE}" --timeout=10m \
      || echo "    (warning: statefulset/${sts} did not become ready in time)"
  done
  # Deployments: wait on the deployment condition directly (IO Bun image can be slow on first pull).
  for deploy in memory-api orchestrator io; do
    deploy_timeout=10m
    if [ "${deploy}" = io ]; then deploy_timeout=15m; fi
    kubectl rollout status deployment "${deploy}" -n "${NAMESPACE}" --timeout="${deploy_timeout}" \
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
  echo "      How to inject:"
  echo "        Live cluster:  kubectl set env deployment/aegis-agents ANTHROPIC_API_KEY=<key> -n ${NAMESPACE}"
  echo "        Fresh start:   put ANTHROPIC_API_KEY in repo-root .env or export it, then ./deploy/scripts/kind-up.sh --skip-build"
fi
if [ -n "${ANTHROPIC_BASE_URL:-}" ]; then
  echo "    ANTHROPIC_BASE_URL ✓ injected: ${ANTHROPIC_BASE_URL}"
else
  echo "    ANTHROPIC_BASE_URL ✗ not set (using Anthropic default endpoint)"
  echo "      How to inject:"
  echo "        Live cluster:  kubectl set env deployment/aegis-agents ANTHROPIC_BASE_URL=<url> -n ${NAMESPACE}"
  echo "        Fresh start:   put ANTHROPIC_BASE_URL in repo-root .env or export it, then ./deploy/scripts/kind-up.sh --skip-build"
fi
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
