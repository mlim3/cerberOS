#!/usr/bin/env bash
# Create the kind cluster, load local images, and install the umbrella Helm chart.
# Usage: ./k8s/scripts/kind-up.sh [--skip-build] [--skip-install] [--embedding-model MODEL]
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

print_help() {
  echo "Usage: ./k8s/scripts/kind-up.sh [OPTIONS]"
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
  echo "  ./k8s/scripts/kind-up.sh"
  echo "  ./k8s/scripts/kind-up.sh --skip-build --embedding-model harrier"
  echo "  HF_TOKEN=<token> ./k8s/scripts/kind-up.sh --embedding-model embeddinggemma"
  echo "  ./k8s/scripts/kind-up.sh --embedding-model BAAI/bge-small-en-v1.5 --embedding-dim 384 --embedding-prompt-style plain"
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

check_dependencies() {
  if ! kind help >/dev/null 2>&1; then
  echo "    kind not found, please install it: https://kind.sigs.k8s.io/docs/user/quick-start#installation"
  exit 1
  fi
  if ! kubectl version --client >/dev/null 2>&1; then
    echo "    kubectl not found, please install it: https://kubernetes.io/docs/tasks/tools/install-kubectl/"
    exit 1
  fi
  if ! helm version >/dev/null 2>&1; then
    echo "    helm not found, please install it: https://helm.sh/docs/intro/install/"
    exit 1
  fi
}

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
echo "==> [0/5] Checking if dependencies are installed ..."
check_dependencies

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
  # Clear any stale half-resolved sub-chart bundles from previous failed runs
  # so `helm dependency update` always starts from a clean slate.
  rm -rf "${REPO_ROOT}"/k8s/helm/cerberos/tmpcharts-* 2>/dev/null || true
  # Resolve sub-chart deps for any component chart that declares them
  # (e.g. observability → prometheus/grafana/loki/tempo). Must run BEFORE
  # the umbrella's helm dependency update, otherwise the observability
  # sub-chart gets packaged without its own dependencies and none of the
  # monitoring pods ever deploy.
  for chart in "${REPO_ROOT}"/k8s/helm/charts/*/; do
    if grep -q "^dependencies:" "$chart/Chart.yaml" 2>/dev/null; then
      echo "    Resolving deps for $(basename "$chart") ..."
      helm dependency update "$chart" >/dev/null
    fi
  done
  helm dependency update "${REPO_ROOT}/k8s/helm/cerberos" >/dev/null

  # Optional: load repo-root .env, then restore any variable already set in the
  # invoking shell so exported values win over the file.
  _env_file="${REPO_ROOT}/.env"
  if [[ -f "${_env_file}" ]]; then
    echo "    Loading optional overrides from ${_env_file} (exported shell values win) ..."
    _saved_ANTHROPIC_API_KEY="${ANTHROPIC_API_KEY-}"
    _saved_ANTHROPIC_BASE_URL="${ANTHROPIC_BASE_URL-}"
    _saved_TAVILY_API_KEY="${TAVILY_API_KEY-}"
    _saved_OPENAI_API_KEY="${OPENAI_API_KEY-}"
    _saved_GMAIL_DEMO_EMAIL="${GMAIL_DEMO_EMAIL-}"
    _saved_GMAIL_DEMO_APP_PASSWORD="${GMAIL_DEMO_APP_PASSWORD-}"
    _saved_GOOGLE_CLIENT_ID="${GOOGLE_CLIENT_ID-}"
    _saved_GOOGLE_CLIENT_SECRET="${GOOGLE_CLIENT_SECRET-}"
    _saved_GOOGLE_REDIRECT_URI="${GOOGLE_REDIRECT_URI-}"
    set -a
    # shellcheck disable=SC1090
    source "${_env_file}"
    set +a
    [[ -n "${_saved_ANTHROPIC_API_KEY}" ]] && export ANTHROPIC_API_KEY="${_saved_ANTHROPIC_API_KEY}"
    [[ -n "${_saved_ANTHROPIC_BASE_URL}" ]] && export ANTHROPIC_BASE_URL="${_saved_ANTHROPIC_BASE_URL}"
    [[ -n "${_saved_TAVILY_API_KEY}" ]] && export TAVILY_API_KEY="${_saved_TAVILY_API_KEY}"
    [[ -n "${_saved_OPENAI_API_KEY}" ]] && export OPENAI_API_KEY="${_saved_OPENAI_API_KEY}"
    [[ -n "${_saved_GMAIL_DEMO_EMAIL}" ]] && export GMAIL_DEMO_EMAIL="${_saved_GMAIL_DEMO_EMAIL}"
    [[ -n "${_saved_GMAIL_DEMO_APP_PASSWORD}" ]] && export GMAIL_DEMO_APP_PASSWORD="${_saved_GMAIL_DEMO_APP_PASSWORD}"
    [[ -n "${_saved_GOOGLE_CLIENT_ID}" ]] && export GOOGLE_CLIENT_ID="${_saved_GOOGLE_CLIENT_ID}"
    [[ -n "${_saved_GOOGLE_CLIENT_SECRET}" ]] && export GOOGLE_CLIENT_SECRET="${_saved_GOOGLE_CLIENT_SECRET}"
    [[ -n "${_saved_GOOGLE_REDIRECT_URI}" ]] && export GOOGLE_REDIRECT_URI="${_saved_GOOGLE_REDIRECT_URI}"
    unset _saved_ANTHROPIC_API_KEY _saved_ANTHROPIC_BASE_URL _saved_TAVILY_API_KEY \
      _saved_OPENAI_API_KEY _saved_GMAIL_DEMO_EMAIL _saved_GMAIL_DEMO_APP_PASSWORD \
      _saved_GOOGLE_CLIENT_ID _saved_GOOGLE_CLIENT_SECRET _saved_GOOGLE_REDIRECT_URI
  fi

  HELM_SET_ARGS=()
  if [ -n "${ANTHROPIC_API_KEY:-}" ]; then
    HELM_SET_ARGS+=(--set-string "aegis-agents.anthropicApiKey=${ANTHROPIC_API_KEY}")
  fi
  if [ -n "${ANTHROPIC_BASE_URL:-}" ]; then
    HELM_SET_ARGS+=(--set-string "aegis-agents.anthropicBaseUrl=${ANTHROPIC_BASE_URL}")
  fi
  if [ -n "${TAVILY_API_KEY:-}" ]; then
    HELM_SET_ARGS+=(--set-string "vault-engine.tavilyApiKey=${TAVILY_API_KEY}")
  fi
  if [ -n "${OPENAI_API_KEY:-}" ]; then
    HELM_SET_ARGS+=(--set-string "memory-api.openaiApiKey=${OPENAI_API_KEY}")
    HELM_SET_ARGS+=(--set-string "io.openaiApiKey=${OPENAI_API_KEY}")
    HELM_SET_ARGS+=(--set-string "vault-engine.openaiApiKey=${OPENAI_API_KEY}")
  fi
  if [ -n "${GMAIL_DEMO_EMAIL:-}" ] && [ -n "${GMAIL_DEMO_APP_PASSWORD:-}" ]; then
    HELM_SET_ARGS+=(--set-string "vault-engine.gmailDemoEmail=${GMAIL_DEMO_EMAIL}")
    HELM_SET_ARGS+=(--set-string "vault-engine.gmailDemoAppPassword=${GMAIL_DEMO_APP_PASSWORD}")
  fi
  if [ -n "${GOOGLE_CLIENT_ID:-}" ] && [ -n "${GOOGLE_CLIENT_SECRET:-}" ]; then
    # IO drives the Admin UI "Connect Google Account" flow; vault-engine uses
    # the same client id/secret for token refresh on long-running sessions.
    HELM_SET_ARGS+=(--set-string "io.googleOAuth.clientId=${GOOGLE_CLIENT_ID}")
    HELM_SET_ARGS+=(--set-string "io.googleOAuth.clientSecret=${GOOGLE_CLIENT_SECRET}")
    HELM_SET_ARGS+=(--set-string "vault-engine.googleOAuth.clientId=${GOOGLE_CLIENT_ID}")
    HELM_SET_ARGS+=(--set-string "vault-engine.googleOAuth.clientSecret=${GOOGLE_CLIENT_SECRET}")
  fi
  if [ -n "${GOOGLE_REDIRECT_URI:-}" ]; then
    HELM_SET_ARGS+=(--set-string "io.googleOAuth.redirectUri=${GOOGLE_REDIRECT_URI}")
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
  helm upgrade --install cerberos "${REPO_ROOT}/k8s/helm/cerberos" \
    --namespace "${NAMESPACE}" \
    --values "${REPO_ROOT}/k8s/helm/cerberos/values-dev.yaml" \
    "${HELM_SET_ARGS[@]}"
  set -u

  echo ""
  echo "    Waiting for core workloads to be ready ..."
  ROLLOUT_FAILED=false
  ROLLOUT_TIMEOUT="8m"
  ROLLOUT_TMP="$(mktemp -d)"
  trap 'rm -rf "${ROLLOUT_TMP}"' EXIT

  # --- Embedding model warmup (cold-start friendly) -------------------------
  # On a fresh cluster, embedding-api downloads ~500 MB+ of model weights from
  # Hugging Face during its FastAPI startup. memory-api blocks on this via its
  # `wait-for-embedding-api` init container, so without a dedicated waiter the
  # whole stack looks "stuck on memory-api" to a first-time user. Poll the
  # embedding-api pod, print download progress, and only fall through to the
  # parallel rollout waits once it's Ready (or the warmup budget is exhausted).
  EMBED_WARMUP_BUDGET_SEC=2400  # 40 min, matches the chart startup probe
  EMBED_WARMUP_DEADLINE=$(( $(date +%s) + EMBED_WARMUP_BUDGET_SEC ))
  embed_pod=""
  echo ""
  echo "    [warmup] Waiting for embedding-api to load the model (cold boot can take 10-20 min on slow HF links) ..."
  # The warmup loop intentionally tolerates every transient kubectl failure
  # (pod missing, container not yet exec-able, exec returning empty output).
  # Disable `set -e` for the loop body so a single bad exec doesn't abort
  # the entire kind-up.sh run, and re-enable it at the end.
  set +e
  while [ "$(date +%s)" -lt "${EMBED_WARMUP_DEADLINE}" ]; do
    embed_pod="$(kubectl get pod -n "${NAMESPACE}" -l app.kubernetes.io/name=embedding-api -o jsonpath='{.items[0].metadata.name}' 2>/dev/null)"
    if [ -z "${embed_pod}" ]; then
      sleep 10
      continue
    fi
    ready="$(kubectl get pod -n "${NAMESPACE}" "${embed_pod}" -o jsonpath='{.status.containerStatuses[0].ready}' 2>/dev/null)"
    if [ "${ready}" = "true" ]; then
      echo "    [warmup] embedding-api is ready."
      break
    fi
    cache_size="$(kubectl exec -n "${NAMESPACE}" "${embed_pod}" -- du -sh /root/.cache/huggingface 2>/dev/null | awk '{print $1}')"
    incomplete_raw="$(kubectl exec -n "${NAMESPACE}" "${embed_pod}" -- sh -c 'ls /root/.cache/huggingface/hub/*/blobs/*.incomplete 2>/dev/null | wc -l' 2>/dev/null | tr -d '[:space:]')"
    # Force a numeric default so the -gt comparison can never error.
    case "${incomplete_raw}" in
      ''|*[!0-9]*) incomplete=0 ;;
      *)           incomplete="${incomplete_raw}" ;;
    esac
    if [ -n "${cache_size}" ]; then
      if [ "${incomplete}" -gt 0 ]; then
        echo "    [warmup] downloading model weights ... cache=${cache_size}, ${incomplete} file(s) still incomplete"
      else
        echo "    [warmup] cache=${cache_size}, loading model into RAM ..."
      fi
    else
      echo "    [warmup] pod ${embed_pod} not yet exec-able, waiting ..."
    fi
    sleep 20
  done
  if [ -n "${embed_pod}" ]; then
    final_ready="$(kubectl get pod -n "${NAMESPACE}" "${embed_pod}" -o jsonpath='{.status.containerStatuses[0].ready}' 2>/dev/null)"
    if [ "${final_ready}" != "true" ]; then
      echo "    [warmup] (warning: embedding-api still not ready after ${EMBED_WARMUP_BUDGET_SEC}s; downstream waits will likely fail)"
    fi
  fi
  set -e
  echo ""

  wait_for() {
    local kind="$1"  # statefulset | deployment
    local name="$2"
    local logfile="${ROLLOUT_TMP}/${kind}-${name}.log"
    if kubectl rollout status "${kind}" "${name}" -n "${NAMESPACE}" --timeout="${ROLLOUT_TIMEOUT}" >"${logfile}" 2>&1; then
      echo "ok" >"${ROLLOUT_TMP}/${kind}-${name}.status"
    else
      echo "fail" >"${ROLLOUT_TMP}/${kind}-${name}.status"
    fi
  }

  diagnose() {
    local kind="$1"  # statefulset | deployment
    local name="$2"
    local label_sel
    case "${kind}-${name}" in
      deployment-vault) label_sel="app.kubernetes.io/name=vault-engine" ;;
      *) label_sel="app.kubernetes.io/name=${name}" ;;
    esac
    local pod
    pod="$(kubectl get pod -n "${NAMESPACE}" -l "${label_sel}" -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || true)"
    if [ -z "${pod}" ]; then
      echo "        (no pod found for ${kind}/${name})"
      return
    fi
    # Init container blocker (e.g. memory-api → wait-for-embedding-api)
    local init_blocker
    init_blocker="$(kubectl get pod -n "${NAMESPACE}" "${pod}" -o jsonpath='{range .status.initContainerStatuses[?(@.ready==false)]}{.name}{"\n"}{end}' 2>/dev/null | head -1)"
    if [ -n "${init_blocker}" ]; then
      echo "        blocked on init container: ${init_blocker}"
      return
    fi
    # Main container last termination reason (e.g. OOMKilled, Error)
    local reason exit_code
    reason="$(kubectl get pod -n "${NAMESPACE}" "${pod}" -o jsonpath='{.status.containerStatuses[0].lastState.terminated.reason}' 2>/dev/null || true)"
    exit_code="$(kubectl get pod -n "${NAMESPACE}" "${pod}" -o jsonpath='{.status.containerStatuses[0].lastState.terminated.exitCode}' 2>/dev/null || true)"
    if [ -n "${reason}" ]; then
      echo "        last termination: ${reason} (exit ${exit_code})"
      return
    fi
    local phase
    phase="$(kubectl get pod -n "${NAMESPACE}" "${pod}" -o jsonpath='{.status.phase}' 2>/dev/null || echo Unknown)"
    echo "        pod phase: ${phase} (use: kubectl logs -n ${NAMESPACE} ${pod})"
  }

  # StatefulSets: use rollout status (handles the case where the pod
  # hasn't been created yet when we start waiting).
  STS_LIST=(memory-db openbao nats)
  # Deployments: wait on the deployment condition directly. Note that the
  # vault-engine chart names its Deployment 'vault' (not 'vault-engine').
  DEPLOY_LIST=(vault memory-api orchestrator io aegis-databus aegis-agents)

  for sts in "${STS_LIST[@]}"; do
    wait_for statefulset "${sts}" &
  done
  for deploy in "${DEPLOY_LIST[@]}"; do
    wait_for deployment "${deploy}" &
  done
  wait

  for sts in "${STS_LIST[@]}"; do
    if [ "$(cat "${ROLLOUT_TMP}/statefulset-${sts}.status" 2>/dev/null || echo fail)" != "ok" ]; then
      echo "    (warning: statefulset/${sts} did not become ready in time)"
      diagnose statefulset "${sts}"
      ROLLOUT_FAILED=true
    fi
  done
  for deploy in "${DEPLOY_LIST[@]}"; do
    if [ "$(cat "${ROLLOUT_TMP}/deployment-${deploy}.status" 2>/dev/null || echo fail)" != "ok" ]; then
      echo "    (warning: deployment/${deploy} did not become ready in time)"
      diagnose deployment "${deploy}"
      ROLLOUT_FAILED=true
    fi
  done
else
  echo ""
  echo "==> [5/5] Skipping Helm install (--skip-install)."
fi

echo ""
if [ "${ROLLOUT_FAILED:-false}" = true ]; then
  echo "  ⚠  Some workloads did not become ready in time."
  echo "     Check status with: kubectl get pods -n ${NAMESPACE}"
  echo ""
fi
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
echo "  Tear down:      ./k8s/scripts/kind-down.sh"
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
    echo "        Fresh start:   export HF_TOKEN=<token> && ./k8s/scripts/kind-up.sh --skip-build --embedding-model embeddinggemma"
  fi
fi
echo "    Change model:"
echo "      Fresh start:   ./k8s/scripts/kind-down.sh && ./k8s/scripts/kind-up.sh --embedding-model harrier"
echo "      Custom model:  ./k8s/scripts/kind-down.sh && ./k8s/scripts/kind-up.sh --embedding-model <hf-model-id> --embedding-dim <n> --embedding-prompt-style <style>"
echo ""
echo "  Anthropic (aegis-agents):"
if [ -n "${ANTHROPIC_API_KEY:-}" ]; then
  echo "    ANTHROPIC_API_KEY  ✓ injected"
else
  echo "    ANTHROPIC_API_KEY  ✗ not set"
  echo "      How to inject:"
  echo "        Live cluster:  kubectl set env deployment/aegis-agents ANTHROPIC_API_KEY=<key> -n ${NAMESPACE}"
  echo "        Fresh start:   export ANTHROPIC_API_KEY=<key> && ./k8s/scripts/kind-up.sh --skip-build"
fi
if [ -n "${ANTHROPIC_BASE_URL:-}" ]; then
  echo "    ANTHROPIC_BASE_URL ✓ injected: ${ANTHROPIC_BASE_URL}"
else
  echo "    ANTHROPIC_BASE_URL ✗ not set (using Anthropic default endpoint)"
  echo "      How to inject:"
  echo "        Live cluster:  kubectl set env deployment/aegis-agents ANTHROPIC_BASE_URL=<url> -n ${NAMESPACE}"
  echo "        Fresh start:   export ANTHROPIC_BASE_URL=<url> && ./k8s/scripts/kind-up.sh --skip-build"
fi
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
