# cerberOS on Kubernetes

Each service runs in its own pod, distributed across nodes, orchestrated via Helm on a [kind](https://kind.sigs.k8s.io/) cluster (or any Kubernetes ≥ 1.28).

---

## Prerequisites

| Tool | Version | Install |
|---|---|---|
| `kind` | ≥ 0.22 | `brew install kind` |
| `kubectl` | ≥ 1.28 | `brew install kubectl` |
| `helm` | ≥ 3.14 | `brew install helm` |
| `docker` | any recent | Docker Desktop / Colima |

---

## Quickstart (single command)

```bash
# 1. Create the kind cluster, build & load images, install the umbrella chart
./deploy/scripts/kind-up.sh

# 2. Open the web UI
open http://localhost:3001

# 3. Check all pods are distributed across nodes
kubectl get pods -n cerberos -o wide
```

That's it. The script handles everything:
- Creates a 3-node kind cluster (`1 control-plane + 2 workers`)
- Builds all service images locally
- Loads them into the cluster (no registry needed)
- Installs the umbrella Helm chart with `values-dev.yaml`

### Embedding model selection

`kind-up.sh` now configures the memory embedding model for you.

- Default: `harrier`
  - model: `microsoft/harrier-oss-v1-270m`
  - dimensions: `640`
  - prompt style: `harrier`
- Optional preset: `embeddinggemma`
  - model: `google/embeddinggemma-300m`
  - dimensions: `768`
  - prompt style: `embeddinggemma`
  - requires `HF_TOKEN` because the model is gated on Hugging Face
- Custom model:
  - pass any Hugging Face model id with `--embedding-model`
  - also provide `--embedding-dim`
  - optionally provide `--embedding-prompt-style`

Examples:

```bash
# Default local path: Harrier
./deploy/scripts/kind-up.sh

# Explicit Harrier
./deploy/scripts/kind-up.sh --embedding-model harrier

# EmbeddingGemma (requires Hugging Face token)
HF_TOKEN=<token> ./deploy/scripts/kind-up.sh --embedding-model embeddinggemma

# Any Hugging Face model id
./deploy/scripts/kind-up.sh \
  --embedding-model BAAI/bge-small-en-v1.5 \
  --embedding-dim 384 \
  --embedding-prompt-style plain
```

Important:
- the selected embedding dimension is used consistently for:
  - the embedding service
  - `memory-api`
  - the Postgres `VECTOR(...)` column created during DB init
- if you change models, do a clean restart so the DB is recreated with the new vector size:

```bash
./deploy/scripts/kind-down.sh
./deploy/scripts/kind-up.sh --embedding-model harrier
```

---

## Manual step-by-step

```bash
# Create cluster only
kind create cluster --name cerberos --config deploy/kind/cluster.yaml

# Create namespace
kubectl create namespace cerberos

# Build & load images
./deploy/scripts/build-and-load.sh

# Add required Helm repos
helm repo add nats https://nats-io.github.io/k8s/helm/charts
helm repo add grafana https://grafana.github.io/helm-charts
helm repo add prometheus-community https://prometheus-community.github.io/helm-charts
helm repo update

# Resolve umbrella dependencies
helm dependency update deploy/helm/cerberos

# Install (substitute your real secrets)
helm upgrade --install cerberos deploy/helm/cerberos \
  --namespace cerberos \
  -f deploy/helm/cerberos/values-dev.yaml \
  --set aegis-agents.anthropicApiKey=$ANTHROPIC_API_KEY \
  --set memory-api.vaultMasterKey=$VAULT_MASTER_KEY \
  --set memory-api.internalVaultApiKey=$INTERNAL_VAULT_API_KEY \
  --wait --timeout 10m
```

---

## Service → Pod → Port map

| Service | K8s resource | Internal port | Host port (kind) |
|---|---|---|---|
| `io` | `Deployment` | 3001 | **3001** |
| `orchestrator` | `Deployment` | 8080 | **8080** |
| `nats` | `StatefulSet` (upstream) | 4222 | **4222** |
| `grafana` | `Deployment` (upstream) | 80 | **3000** |
| `memory-api` | `Deployment` | 8081 | port-forward only |
| `memory-db` | `StatefulSet` | 5432 | port-forward only |
| `openbao` | `StatefulSet` | 8200 | port-forward only |
| `vault` (engine) | `Deployment` | 8000 | port-forward only |
| `aegis-databus` | `Deployment` | 9091 | port-forward only |
| `aegis-agents` | `Deployment` | 9090 | port-forward only |
| `simulator` | `Deployment` | — | — |
| `prometheus` | `Deployment` (upstream) | 9090 | port-forward only |
| `tempo` | `Deployment` (upstream) | 3200 | port-forward only |
| `loki` | `Deployment` (upstream) | 3100 | port-forward only |

---

## Useful commands

```bash
# All pods with node placement (proves distributed)
kubectl get pods -n cerberos -o wide

# Tail orchestrator logs
kubectl logs -n cerberos -l app.kubernetes.io/name=orchestrator -f

# Port-forward a ClusterIP service
kubectl port-forward -n cerberos svc/memory-api 8081:8081
kubectl port-forward -n cerberos svc/openbao 8200:8200
kubectl port-forward -n cerberos svc/grafana 3000:80

# Re-install after a code change
./deploy/scripts/build-and-load.sh
helm upgrade cerberos deploy/helm/cerberos -n cerberos -f deploy/helm/cerberos/values-dev.yaml

# Tear down
./deploy/scripts/kind-down.sh
```

---

## Secrets reference

| Key | Who uses it | How to supply |
|---|---|---|
| `memory-api.vaultMasterKey` | memory-api | `--set memory-api.vaultMasterKey=<32 ASCII chars>` |
| `memory-api.internalVaultApiKey` | memory-api, vault-engine | `--set memory-api.internalVaultApiKey=<key>` |
| `memory-api.db.password` | memory-api | `--set memory-api.db.password=<pw>` (must match `postgres.auth.password`) |
| `postgres.auth.password` | memory-db | `--set postgres.auth.password=<pw>` |
| `openbao.baoToken` | vault-engine | set after openbao init (see unseal section below) |
| `vault-engine.baoToken` | vault-engine | same as above |
| `aegis-agents.anthropicApiKey` | aegis-agents | `--set aegis-agents.anthropicApiKey=$ANTHROPIC_API_KEY` |

**Extension hook — External Secrets Operator:** set `externalSecrets.enabled: true` in values (stub in `_helpers.tpl`) to create `ExternalSecret` CRs pointing at AWS Secrets Manager / Vault / GCP SM instead of K8s `Secret` objects.

---

## OpenBao unseal runbook

By default (`openbao.devMode: true` in `values-dev.yaml`) OpenBao runs in in-memory dev mode — auto-unsealed, no PVC needed. For persistent storage:

```bash
# 1. values-dev.yaml: set openbao.devMode: false, redeploy
helm upgrade cerberos ... --set openbao.devMode=false

# 2. Initialize
kubectl exec -n cerberos statefulset/openbao -- \
  bao operator init -key-shares=1 -key-threshold=1

# 3. Unseal (save the unseal key and root token securely)
kubectl exec -n cerberos statefulset/openbao -- \
  bao operator unseal <unseal-key>

# 4. Set the root token for vault-engine
helm upgrade cerberos ... \
  --set openbao.baoToken=<root-token> \
  --set vault-engine.baoToken=<root-token>
```

---

## Extension recipes

### Enable 3-node NATS JetStream cluster
```yaml
# values-prod.yaml (already stubbed)
nats:
  server:
    config:
      cluster:
        enabled: true
        replicas: 3
```

### Enable Ingress + TLS for io
```yaml
io:
  ingress:
    enabled: true
    className: nginx
    host: cerberos.example.com
    tls:
      enabled: true
      secretName: cerberos-tls
    annotations:
      cert-manager.io/cluster-issuer: letsencrypt-prod
```

### Enable NetworkPolicies (requires Calico/Cilium CNI)
```yaml
network-policies:
  enabled: true
```

### Enable Firecracker microVM lifecycle for agents
```yaml
aegis-agents:
  firecracker:
    enabled: true
    socketDir: "/run/firecracker-containerd"
    nodeSelectorKey: "firecracker"
    nodeSelectorValue: "true"
```
Provision a tainted node pool with `/dev/kvm` access first.

### Scale to managed cloud (EKS/GKE/AKS)
1. Provision a cluster (any method — eksctl, gcloud, az aks).
2. Point `kubectl` at it.
3. `helm dependency update deploy/helm/cerberos`
4. `helm upgrade --install cerberos deploy/helm/cerberos -f deploy/helm/cerberos/values-prod.yaml --set ...secrets...`
5. Update `storageClass` in `values-prod.yaml` to match your cloud's provisioner.

---

## Architecture: how services communicate

```
User → io (3001)
         │ NATS pub/sub ──────────────────────────────────┐
         │ HTTP                                            │
         ▼                                                 │
   orchestrator (8080) ─── HTTP ──► memory-api (8081)     │
         │                               │                 │
         │ HTTP                          │ SQL             │
         ▼                               ▼                 │
     openbao (8200)              memory-db (5432)          │
         ▲                                                 │
         │ HTTP                                            │
     vault-engine (8000)                                   │
                                                           │
   aegis-agents ◄──────────────────── NATS ◄──────────────┘
   aegis-databus ◄──────────────────── NATS
```

All services share a single NATS JetStream bus as the primary backbone.  
Direct HTTP calls are restricted to the paths shown above (and enforced by NetworkPolicies when enabled).

---

## Directory layout

```
deploy/
  helm/
    charts/
      nats/               # Wrapper around nats-io/nats upstream chart
      postgres/           # StatefulSet + PVC + init SQL ConfigMap
      openbao/            # StatefulSet + PVC + HCL ConfigMap
      vault-engine/       # Deployment (credential broker)
      orchestrator/       # Deployment
      io/                 # Deployment + NodePort + Ingress hook
      memory-api/         # Deployment
      aegis-databus/      # Deployment (hardened: read-only FS, uid 65532)
      aegis-agents/       # Deployment (process-manager mode; Firecracker hook)
      simulator/          # Deployment (uses aegis-agents image)
      observability/      # Prom + Grafana + Loki + Tempo upstream charts
      network-policies/   # NetworkPolicy set (disabled by default)
    cerberos/             # Umbrella chart (depends on all above)
      values.yaml         # Shared defaults
      values-dev.yaml     # kind / local overrides
      values-prod.yaml    # HA / cloud extension profile
  kind/
    cluster.yaml          # 3-node kind config with NodePort mappings
  scripts/
    kind-up.sh            # One-command bring-up
    kind-down.sh          # Cluster teardown
    build-and-load.sh     # Build images + kind load (no registry needed locally)
```
