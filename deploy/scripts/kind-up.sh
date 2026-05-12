#!/usr/bin/env bash
# Create the kind cluster, load local images, and install the umbrella Helm chart.
# Usage: ./deploy/scripts/kind-up.sh [--skip-build] [--skip-install] [--embedding-model MODEL]
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
CLUSTER="cerberos"
NAMESPACE="cerberos"
SKIP_BUILD=false
SKIP_INSTALL=false
EMBEDDING_MODEL="harrier"
EMBEDDING_DIM=""
EMBEDDING_PROMPT_STYLE=""
EMBEDDING_HF_TOKEN="${HF_TOKEN:-}"

print_deployment_debug() {
  local deploy="$1"
  local label_name="$2"

  echo ""
  echo "    Debug for deployment/${deploy}:"
  kubectl get pods -n "${NAMESPACE}" -l "app.kubernetes.io/name=${label_name}" -o wide || true

  local pod_name=""
  pod_name="$(kubectl get pods -n "${NAMESPACE}" -l "app.kubernetes.io/name=${label_name}" -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || true)"
  if [ -n "${pod_name}" ]; then
    echo ""
    echo "    describe pod/${pod_name}"
    kubectl describe pod -n "${NAMESPACE}" "${pod_name}" || true

    echo ""
    echo "    recent logs for pod/${pod_name}"
    kubectl logs -n "${NAMESPACE}" "${pod_name}" --all-containers=true --tail=200 || true

    echo ""
    echo "    previous logs for pod/${pod_name}"
    kubectl logs -n "${NAMESPACE}" "${pod_name}" --all-containers=true --previous --tail=200 || true
  fi
}

print_help() {
  echo "Usage: ./deploy/scripts/kind-up.sh [OPTIONS]"
  echo ""
  echo "Create the kind cluster, build & load images, and install the cerberOS Helm chart."
  echo ""
  echo "Options:"
  echo "  --skip-build                   Skip Docker image builds (use already-loaded images)"
  echo "  --skip-install                 Skip Helm chart install/upgrade"
  echo "  --embedding-model MODEL        Model preset or Hugging Face model id"
  echo "                                 Presets: embeddinggemma, harrier"
  echo "  --embedding-dim N              Override embedding vector dimensions"
  echo "  --embedding-prompt-style NAME  Override prompt formatting style"
  echo "                                 Styles: embeddinggemma, harrier, plain"
  echo "  --hf-token TOKEN               Hugging Face token for gated models"
  echo "  -h, --help                     Show this help message"
  echo ""
  echo "Environment variables:"
  echo "  ANTHROPIC_API_KEY   Anthropic API key"
  echo "  ANTHROPIC_BASE_URL  Anthropic API base URL"
  echo "  HF_TOKEN            Hugging Face token for gated embedding models"
  echo ""
  echo "Examples:"
  echo "  ./deploy/scripts/kind-up.sh"
  echo "  ./deploy/scripts/kind-up.sh --skip-build --embedding-model harrier"
  echo "  HF_TOKEN=<token> ./deploy/scripts/kind-up.sh --embedding-model embeddinggemma"
  echo "  ./deploy/scripts/kind-up.sh --embedding-model BAAI/bge-small-en-v1.5 --embedding-dim 384 --embedding-prompt-style plain"
}

while [ "$#" -gt 0 ]; do
  case "$1" in
    --skip-build)
      SKIP_BUILD=true
      shift
      ;;
    --skip-install)
      SKIP_INSTALL=true
      shift
      ;;
    --embedding-model)
      EMBEDDING_MODEL="${2:-}"
      if [ -z "${EMBEDDING_MODEL}" ]; then
        echo "error: --embedding-model requires a value" >&2
        exit 1
      fi
      shift 2
      ;;
    --embedding-dim)
      EMBEDDING_DIM="${2:-}"
      if [ -z "${EMBEDDING_DIM}" ]; then
        echo "error: --embedding-dim requires a value" >&2
        exit 1
      fi
      shift 2
      ;;
    --embedding-prompt-style)
      EMBEDDING_PROMPT_STYLE="${2:-}"
      if [ -z "${EMBEDDING_PROMPT_STYLE}" ]; then
        echo "error: --embedding-prompt-style requires a value" >&2
        exit 1
      fi
      shift 2
      ;;
    --hf-token)
      EMBEDDING_HF_TOKEN="${2:-}"
      if [ -z "${EMBEDDING_HF_TOKEN}" ]; then
        echo "error: --hf-token requires a value" >&2
        exit 1
      fi
      shift 2
      ;;
    -h|--help)
      print_help
      exit 0
      ;;
    *)
      echo "error: unknown option '$1'" >&2
      echo "" >&2
      print_help >&2
      exit 1
      ;;
  esac
done

case "${EMBEDDING_MODEL}" in
  embeddinggemma)
    EMBEDDING_MODEL="google/embeddinggemma-300m"
    : "${EMBEDDING_DIM:=768}"
    : "${EMBEDDING_PROMPT_STYLE:=embeddinggemma}"
    ;;
  harrier)
    EMBEDDING_MODEL="microsoft/harrier-oss-v1-270m"
    : "${EMBEDDING_DIM:=640}"
    : "${EMBEDDING_PROMPT_STYLE:=harrier}"
    ;;
  *)
    if [ -z "${EMBEDDING_DIM}" ]; then
      echo "error: custom --embedding-model requires --embedding-dim" >&2
      exit 1
    fi
    : "${EMBEDDING_PROMPT_STYLE:=plain}"
    ;;
esac

echo "==> Syncing memory schema to embedding dimension ${EMBEDDING_DIM} ..."
bash "${REPO_ROOT}/memory/scripts/set-embedding-dimension.sh" "${EMBEDDING_DIM}"

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
  HELM_SET_ARGS+=(--set "global.embedding.model=${EMBEDDING_MODEL}")
  HELM_SET_ARGS+=(--set "global.embedding.dimensions=${EMBEDDING_DIM}")
  HELM_SET_ARGS+=(--set "global.embedding.promptStyle=${EMBEDDING_PROMPT_STYLE}")
  if [ -n "${EMBEDDING_HF_TOKEN}" ]; then
    HELM_SET_ARGS+=(--set "global.embedding.hfToken=${EMBEDDING_HF_TOKEN}")
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
  echo "    Waiting for core workloads to be ready (up to 5 min) ..."
  # StatefulSets: use rollout status (handles the case where the pod
  # hasn't been created yet when we start waiting).
  for sts in memory-db openbao nats; do
    kubectl rollout status statefulset "${sts}" -n "${NAMESPACE}" --timeout=5m \
      || echo "    (warning: statefulset/${sts} did not become ready in time)"
  done
  # Deployments: wait on the deployment condition directly.
  for deploy in memory-api orchestrator io; do
    if ! kubectl rollout status deployment "${deploy}" -n "${NAMESPACE}" --timeout=5m; then
      echo "    (warning: deployment/${deploy} did not become ready in time)"
      if [ "${deploy}" = "memory-api" ]; then
        echo "    memory-api depends on embedding-api and postgres. Printing startup diagnostics..."
        print_deployment_debug "embedding-api" "embedding-api"
      fi
      print_deployment_debug "${deploy}" "${deploy}"
    fi
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
echo "  Embeddings:"
echo "    Model:        ${EMBEDDING_MODEL}"
echo "    Dimensions:   ${EMBEDDING_DIM}"
echo "    Prompt style: ${EMBEDDING_PROMPT_STYLE}"
if [ -n "${EMBEDDING_HF_TOKEN}" ]; then
  echo "    HF_TOKEN:     ✓ injected"
else
  echo "    HF_TOKEN:     ✗ not set"
  if [ "${EMBEDDING_MODEL}" = "google/embeddinggemma-300m" ]; then
    echo "      How to inject:"
    echo "        Fresh start:   export HF_TOKEN=<token> && ./deploy/scripts/kind-up.sh --skip-build --embedding-model embeddinggemma"
  fi
fi
echo "    Change model:"
echo "      Fresh start:   ./deploy/scripts/kind-down.sh && ./deploy/scripts/kind-up.sh --embedding-model harrier"
echo "      Custom model:  ./deploy/scripts/kind-down.sh && ./deploy/scripts/kind-up.sh --embedding-model <hf-model-id> --embedding-dim <n> --embedding-prompt-style <style>"
echo ""
echo "  Anthropic (aegis-agents):"
if [ -n "${ANTHROPIC_API_KEY:-}" ]; then
  echo "    ANTHROPIC_API_KEY  ✓ injected"
else
  echo "    ANTHROPIC_API_KEY  ✗ not set"
  echo "      How to inject:"
  echo "        Live cluster:  kubectl set env deployment/aegis-agents ANTHROPIC_API_KEY=<key> -n ${NAMESPACE}"
  echo "        Fresh start:   export ANTHROPIC_API_KEY=<key> && ./deploy/scripts/kind-up.sh --skip-build"
fi
if [ -n "${ANTHROPIC_BASE_URL:-}" ]; then
  echo "    ANTHROPIC_BASE_URL ✓ injected: ${ANTHROPIC_BASE_URL}"
else
  echo "    ANTHROPIC_BASE_URL ✗ not set (using Anthropic default endpoint)"
  echo "      How to inject:"
  echo "        Live cluster:  kubectl set env deployment/aegis-agents ANTHROPIC_BASE_URL=<url> -n ${NAMESPACE}"
  echo "        Fresh start:   export ANTHROPIC_BASE_URL=<url> && ./deploy/scripts/kind-up.sh --skip-build"
fi
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
