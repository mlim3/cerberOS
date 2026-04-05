# cerberOS Vault — Architecture & patterns

This document merges the former `DOCS.md` (operational notes) and `DESIGN_PATTERNS.md` (pattern rationale). It describes the **current** credential-broker design first, then **historical** material from the retired QEMU execution path for context.

---

## Current architecture

Vault is a **credential broker**. Agents submit scripts with `{{PLACEHOLDER}}` markers; the service resolves secrets, substitutes values, and returns the **completed script**. The agent runs that script in its own environment.

```
compose.yaml
  └─ vault (Go binary, :8000)
       ├─ SecretManager  (pluggable — mock by default)
       ├─ Preprocessor   (placeholder scan + atomic resolution + substitution)
       └─ Audit Logger   (structured events — key names, never values)

  └─ ui (:80) — static UI; nginx proxies /inject → vault

  └─ openbao (:8200) — optional; Postgres-backed via memory stack (see setup-openbao.sh)

  └─ swagger (:8080) — OpenAPI UI for openapi.yaml
```

`compose.yaml` uses the external Docker network `memory_default` so OpenBao can reach Postgres as `db` when the memory stack is up.

---

## Running locally

```bash
cd vault
docker compose build
docker compose up
```

The vault service listens on `:8000`. Use the CLI or `POST /inject`:

```bash
# CLI (build from engine/: go build -o vault ./cmd/vault/)
vault inject -s 'echo {{API_KEY}}'

# curl
curl -X POST http://localhost:8000/inject \
  -H 'Content-Type: application/json' \
  -d '{"agent":"test","script":"echo {{API_KEY}}"}'
```

See `engine/README.md` for CLI flags and request/response shapes.

---

## Secret injection flow

1. Agent sends a script containing `{{KEY}}` placeholders (regex: `{{` + identifier + `}}`).
2. The preprocessor collects **unique** keys in order.
3. `SecretManager.Resolve(keys)` runs **once**, atomically — all keys or an error.
4. Placeholders are replaced with resolved values.
5. The injected script is returned in the JSON response.

If any secret is missing or denied, the **entire** request fails — no partial injection.

### `SecretManager` interface

```go
type SecretManager interface {
    Resolve(keys []string) (map[string]string, error)
}
```

The codebase uses `MockSecretManager` for development. Replace with OpenBao, HashiCorp Vault, AWS Secrets Manager, etc., by implementing the same interface and wiring it in `main.go`.

---

## Design patterns (current code)

### Secret backend — Strategy

**Secret resolution** is a **Strategy**: the preprocessor depends on `preprocessor.SecretStore` (same contract as `secretmanager.SecretManager` — batch `Resolve`), not on a concrete backend.

- Swap `NewMock()` for a real implementation without changing placeholder logic.
- **Why Strategy (not Factory / Adapter / Facade here):** the caller chooses the implementation explicitly; the interface is designed for substitution, not for hiding a subsystem.

### Preprocessing — Template Method (lightweight)

`Preprocessor.Process(agent, raw)` follows a **fixed sequence**: collect keys → `Resolve` → audit (names only) → substitute → return `Result` with script + injected values for potential downstream use.

Hooks such as pre/post validators are _not_ implemented today but could extend this skeleton (e.g. `[]Validator`) without changing callers.

### Audit — Observer

`audit.Logger` fans each `Event` to every `audit.Exporter` (`JSONExporter`, `MultiExporter`, or custom).

```go
type Exporter interface {
    Export(e Event) error
}
```

**Why Observer (not Decorator / Chain of Responsibility / Mediator):** multiple independent sinks should all receive the same events (fan-out), not a single wrapped chain.

| Kind            | When                       | Records                                         |
| --------------- | -------------------------- | ----------------------------------------------- |
| `injection`     | `/inject` handler          | Agent id, optional `keys` from request, message |
| `secret_access` | After successful `Resolve` | Agent id, resolved secret **names** only        |

Values are never logged.

### Adding a custom audit sink

Implement `Exporter` and pass it to `audit.New`:

```go
auditor := audit.New(
    audit.NewJSONExporter(os.Stdout),
    &MyExporter{...},
)
```

---

## Historical: QEMU execution engine (V0)

The following applied when vault included a **VM orchestrator**: preprocessor → initrd → QEMU → extract output → scrub secrets. That pipeline has been **removed** from the tree; the broker model above replaces it. This section is kept so design discussions and old notes stay understandable.

### Former orchestrator pipeline (Pipeline / Chain of Responsibility)

Each stage depended on the previous:

```
Request
  → Preprocessor.Process()   — inject secrets, collect injected values
  → initrd.Builder.Build()   — embed script in ephemeral initramfs
  → vm.Run()                 — boot VM, run script, capture output
  → extractJobOutput()       — strip boot noise via sentinel markers
  → scrubSecrets()           — redact injected values from output
  → Response
```

That design aimed for an invariant: **the agent never saw raw secret values** (secrets existed only inside the VM; output was scrubbed). The **current** `/inject` API intentionally returns substituted scripts to the caller — different threat model.

### VM backends — Strategy (historical)

A `VM` interface was the extension point for QEMU vs other hypervisors (`Start` / `Stop` / `Run`). Adding a backend meant implementing the interface and selecting it from configuration. Shared config was embedded per backend (e.g. QEMU vs Firecracker-specific fields).

### QEMU / Alpine notes (V0 troubleshooting)

These were collected while maintaining the QEMU-based engine:

- **Alpine `/bin/sh`** is busybox ash — no brace expansion. Use `mkdir -p /a /b /c`, not `mkdir -p /{a,b,c}`.
- **`-cpu host`** only works with `kvm` or `hvf`; with `tcg` use a model like `max`.
- **Non-root containers:** use `COPY --chown=uid:gid` so runtime users can read VM assets.
- **ARM `virt`:** serial console is **`ttyAMA0`**, not `ttyS0`.
- **Virtio block vs initramfs:** Alpine `linux-virt` often had virtio block as modules; initramfs-first avoided boot panics when modules were not loaded.

---

## See also

- `engine/README.md` — API, CLI, Docker build
- `CLAUDE.md` — agent guidance for this component
- `openapi.yaml` — HTTP contract
- `setup-openbao.sh` — OpenBao + Postgres bootstrap
