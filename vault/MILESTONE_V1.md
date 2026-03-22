# cerberOS Vault — Milestone V1

## What Is cerberOS Vault?

### The Problem

AI agents that call external APIs need credentials — API keys, database passwords, tokens. The naive approach puts those secrets directly in the agent's context, which means they appear in prompts, in logs, in conversation history, and in whatever the agent decides to do with them. Once a secret is in an agent's context, it is no longer under your control.

### The Solution

cerberOS Vault is a secure, ephemeral script execution service. It accepts shell scripts over HTTP, injects secrets at runtime, runs each script inside an isolated QEMU virtual machine that boots fresh for every request, and returns the output with all secret values scrubbed. The core guarantee: **agents that submit scripts never touch raw secret values** — not in the request, not in the output, not in any log.

An agent writes a script with placeholder syntax instead of real values:

```sh
#!/bin/sh
KEY={{MY_API_KEY}}
curl --max-time 10 -H "Authorization: Bearer $KEY" https://api.example.com/data
```

The agent submits this script to vault. Vault resolves `{{MY_API_KEY}}` internally, injects the real value into the VM, runs the script, and returns only the output — with the secret value replaced by `[REDACTED]`. The agent gets the API response. It never sees the key.

### What runs inside the VM

Scripts run in a minimal busybox environment. The agent skill (`bare-metal-bash`) is constrained to bash v4+ and curl — no `jq`, no `sed`, no `awk`, no package manager. String manipulation uses bash parameter expansion exclusively. This is intentional: a minimal environment has a minimal attack surface.

---

## System Architecture

Three services, one compose stack:

```
                        ┌──────────────┐
          POST /execute │              │
Agent ──────────────────▶   engine     │ :8000
                        │  (Go binary) │
                        │              │
                        └──────┬───────┘
                               │ POST /secrets/resolve
                               │ X-Engine-Token: <token>
                        ┌──────▼───────┐
                        │  secretstore │ :8001
                        │  (Go binary) │
                        └──────────────┘

                        ┌──────────────┐
                        │  internal-ui │ :80
                        │  (Next.js)   │
                        └──────────────┘
```

`compose.yaml` dependency order: `secretstore` starts first, `engine` depends on it, `ui` depends on both.

### Execution pipeline (inside engine)

```
POST /execute
      │
      ▼
[1] Preprocessor          Replaces {{PLACEHOLDER}} tokens with secrets from SecretStore
      │                   Returns: processed script + list of injected secret values
      ▼
[2] InitRD Builder        Packs script into a per-job initramfs alongside a custom /init
      │                   Writes: temp .gz file (deleted after VM exits)
      ▼
[3] QEMU VM               Boots kernel with the custom initrd; init executes the script
      │                   Output wrapped in sentinel markers:
      │                     === cerberOS job start ===
      │                     <script output>
      │                     === cerberOS job exit_code=N ===
      ▼
[4] extractJobOutput()    Strips kernel boot noise — keeps only content between sentinels
      │
      ▼
[5] scrubSecrets()        Replaces injected secret values with [REDACTED]
      │
      ▼
JSON response             { "output": "...", "exit_code": 0 }
```

**Why sentinel markers?** QEMU with `-nographic` dumps kernel boot messages, driver initialization, and filesystem mount logs to the same output stream as the script. Sentinel markers printed by the custom `/init` before and after script execution let `extractJobOutput()` slice out exactly the script's output without any kernel noise — no fragile line-count parsing, no log suppression flags.

---

## Design Decisions & Patterns

### VM Package — Strategy Pattern

**Problem:** We need to support multiple hypervisor backends (QEMU today, Firecracker tomorrow). Coupling orchestration logic to QEMU would require rewriting the pipeline every time the backend changes.

**Solution:** The `VM` interface defines the contract; each hypervisor is a concrete strategy.

```go
type VM interface {
    Start(ctx context.Context) error
    Stop() error
    Run(ctx context.Context) (*RunResult, error)
}
```

Callers program against the interface, never the concrete type:

```go
var vm engine.VM = engine.NewQEMU(cfg)
vm.Run(ctx)
```

| Pattern | Why not |
|---|---|
| **Factory** | We don't need runtime selection from a string/enum — the caller explicitly picks a backend. |
| **Adapter** | Adapter wraps an incompatible interface. Here we're designing from scratch, not adapting. |
| **Facade** | Facade hides complexity. Our interface enables substitution, not simplification. |

Config composition uses embedding — backend-specific structs embed shared `Config` to keep universal fields (kernel, initrd, vcpus, memory) in one place.

---

### Preprocessor Package — Strategy + Template Method

**Strategy** for secret resolution — the `SecretStore` interface abstracts where secrets come from:

```go
type SecretStore interface {
    Get(key string) (string, error)
}
```

**Template Method** for the processing pipeline — `Process()` is a fixed sequence with open hooks:

```go
// validate → substitute → validate
func (p *Preprocessor) Process(raw []byte) (*Result, error) {
    // [hook] pre-substitution validators (syntax check, disallowed commands)
    // substitute {{PLACEHOLDER}} → secret values
    // [hook] post-substitution validators (no unresolved markers, size limits)
    // return Result{Script, InjectedSecrets}
}
```

Strategy makes the secret backend swappable (Mock → HashiCorp Vault → KMS) without touching `Process()`. Template Method makes the validator pipeline extensible without changing callers.

---

### Orchestrator Package — Pipeline / Chain of Responsibility

Each step transforms or consumes the output of the previous one. The order is not optional — it encodes the security invariant.

| Pattern | Why not |
|---|---|
| **Facade** | A facade hides complexity but doesn't enforce ordering. The pipeline's sequential dependency is the point. |
| **Mediator** | Mediator manages many-to-many coordination. This is strictly linear. |
| **Builder (GoF)** | Builder constructs objects. The pipeline constructs an execution result imperatively. |

**Security invariant:** The agent submits a script with `{{PLACEHOLDER}}` markers only. Step 1 resolves them inside vault, unreachable by the agent. Step 5 scrubs any secrets that leaked into output before returning.

---

### Audit Package — Observer Pattern

`Logger` is the subject. It holds a list of `Exporter` observers and fans each `Event` out to all of them:

```go
type Exporter interface {
    Export(e Event) error
}

type Logger struct {
    exporters []Exporter
}

func (l *Logger) Log(e Event) { /* fan-out to all exporters */ }
```

Built-in exporters: `JSONExporter` (NDJSON to any `io.Writer`), `MultiExporter` (compose N exporters into one).

| Pattern | Why not |
|---|---|
| **Decorator** | Decorator wraps a single object. Here we're broadcasting to N independent consumers. |
| **Chain of Responsibility** | CoR passes until one handler claims it. We want *all* exporters to receive every event. |
| **Mediator** | Mediator is bidirectional many-to-many. Log emission is strictly one-way. |

The secretstore mirrors this pattern locally (`auditLogger` / `auditExporter`) rather than importing `engine/audit`.

**Audit event schema** — NDJSON to stdout:

```json
{"time":"2026-03-08T12:00:00Z","kind":"execution","agent":"my-agent","message":"agent submitted script for execution"}
{"time":"2026-03-08T12:00:00Z","kind":"secret_access","agent":"my-agent","keys":["API_KEY","DB_PASS"],"message":"agent requested secrets"}
```

Two events per execution, always in order: submission first, then which secrets were accessed. Keys logged, values never.

---

### Secretstore — Bulk Request + Strategy + Middleware Guard

**Bulk Request pattern:** The preprocessor scans the entire script once, collects all unique keys, and resolves them in a single `POST /secrets/resolve` call. One execution = one secretstore round-trip, regardless of how many secrets the script uses. If one key is missing, the whole batch fails — a partially-substituted script executing in a VM would be worse than a clean error.

**Strategy pattern:** `SecretManager` interface makes the backend pluggable:

```go
type SecretManager interface {
    GetSecrets(keys []string) (map[string]string, error)
}
```

`MockSecretManager` satisfies it in-process. Drop in HashiCorp Vault, AWS SDK, etc. — `main.go` is the only wiring point.

**Middleware Guard:** `engineOnly` wraps every route and rejects requests without `X-Engine-Token` before the body is read. Shared secret is the minimum viable service-to-service auth on a private network; upgrade to mTLS when the deployment model warrants it.

---

## Workflow & Dependencies

### Execution flow (end to end)

1. Agent POSTs `{ "agent": "name", "script": "#!/bin/sh\necho {{API_KEY}}" }` to engine `:8000`
2. Preprocessor scans for `{{...}}` tokens, calls secretstore with the key list
3. Secretstore authenticates the engine token, delegates to `SecretManager`, returns `{ "secrets": { "API_KEY": "..." } }`
4. Preprocessor substitutes values; returns processed script + list of injected values
5. InitRD Builder decompresses base initrd, appends `/job/script.sh` + custom `/init`, re-gzips to a temp file
6. QEMU boots with the per-job initrd; `/init` runs the script wrapped in sentinel markers, then powers off via `sysrq-trigger`
7. Orchestrator extracts output between sentinels, scrubs all injected values to `[REDACTED]`
8. Response `{ "output": "...", "exit_code": 0 }` returned to caller

### Dependencies

**Zero external Go dependencies.** The entire engine and secretstore are standard library only (`go 1.24`). No `go.sum` bloat, no supply chain surface.

The `SecretStore` interface in the preprocessor is the designed seam for the one real external dependency (a secrets backend). The current `MockSecretManager` fills that seam so the system runs fully without a real backend:

```go
// To swap in a real backend, implement one interface in one file:
type VaultManager struct { client *vault.Client }
func (v *VaultManager) GetSecrets(keys []string) (map[string]string, error) { ... }

// Wire in main.go — nothing else changes:
s := &server{ manager: &VaultManager{client: vaultClient}, ... }
```

### VM networking

QEMU user-mode networking (`-netdev user`) provides outbound connectivity without requiring host bridge or tap interfaces. QEMU always assigns static addresses — no DHCP needed:

| Address | Role |
|---|---|
| `10.0.2.15` | Guest IP |
| `10.0.2.2` | Default gateway |
| `10.0.2.3` | DNS forwarder (proxies to host resolver) |

The `virtio_net` driver and its dependency chain (`failover → net_failover → virtio_net`, `virtio → virtio_ring → virtio_pci`) are pre-installed in the base initramfs and loaded via `modprobe` at boot.

---

## Code Structure & Build Process

### Engine

```
engine/
  cmd/vault/          CLI tool
    main.go           Entry point (flags: -s, -f, -e, stdin)
    main_test.go      Unit tests (httptest.Server mock, no Docker)
    integration_test.go  Integration tests (build tag: integration)
  orchestrator/       Pipeline coordinator
  preprocessor/       Secret injection + validator pipeline
  initrd/             Per-job initramfs builder (cpio newc format)
  vm/                 VM interface + QEMU backend
    qemu.go           buildArgs(), arch detection, accelerator detection
  audit/              Structured audit logging (Observer)
  main.go             HTTP server :8000 (/start, /stop, /execute)
  Dockerfile.qemu     Three-stage build
```

**Three-stage Docker build:**

| Stage | What it does |
|---|---|
| `build` | `go build` — static binary, no CGO, no runtime deps |
| `artifacts` | Downloads Alpine `linux-virt` kernel; builds minimal busybox rootfs; installs virtio modules; creates `initrd.gz` via `find | cpio | gzip` |
| production | Assembles QEMU + kernel + initrd + binary; sets `--chown=1001:1001`; runs as `altuser` (UID 1001) |

**Why initramfs instead of a block device?**

The original design used an ext2 disk image attached via `virtio-blk`. Alpine's `linux-virt` kernel compiles `virtio_blk` as a loadable module, not built-in. Without an initramfs to load it first, the kernel panics before it can mount anything:

```
VFS: Unable to mount root fs on unknown-block(0,0)
Kernel panic — not syncing: VFS: Unable to mount root fs
```

Switching to a **cpio initramfs** sidesteps this entirely — the kernel decompresses it directly into memory before any drivers are needed. No `-drive` flag, no `virtio_blk`, no block device at all. The per-job script is appended to the base initramfs at request time and written to a temp file. After the VM exits, the temp file is deleted — nothing persists.

QEMU accelerator auto-detection at runtime:

| Environment | Accelerator |
|---|---|
| Linux + `/dev/kvm` | `kvm` |
| macOS Docker / no KVM | `tcg` (software emulation) |

Architecture defaults:

| Arch | Binary | Machine | Console |
|---|---|---|---|
| `aarch64` | `qemu-system-aarch64` | `virt` | `ttyAMA0` |
| `x86_64` | `qemu-system-x86_64` | `microvm` | `ttyS0` |

### Secretstore

```
secretstore/
  main.go       HTTP server :8001; engineOnly middleware; wires SecretManager
  manager.go    SecretManager interface + MockSecretManager
  audit.go      Local Observer pattern mirror (no import of engine/audit)
  Dockerfile    Single-stage Go build (static binary)
```

Single-stage build — no kernel or rootfs work needed:

```sh
docker build -f Dockerfile -t vault-secretstore .
docker run -e ENGINE_TOKEN=<token> -p 8001:8001 vault-secretstore
```

### Running everything

```sh
docker compose build
docker compose up
```

KVM passthrough for hardware acceleration (Linux):

```yaml
# compose.yaml under services.engine:
devices:
  - /dev/kvm:/dev/kvm
```

---

## Hard Problems Solved

These are real issues encountered during development, documented because they inform the final architecture.

### ARM serial console is `ttyAMA0`, not `ttyS0`

On QEMU's `virt` machine type (used for aarch64), the UART is a PL011 — it appears in the guest as `/dev/ttyAMA0`, not `ttyS0`. Using the wrong device means the kernel boots silently with no output. `ttyS0` is correct for x86 (`microvm` machine type). The engine auto-selects the right console per architecture.

### Alpine sh doesn't support brace expansion

Alpine's `/bin/sh` is busybox ash — POSIX only. `mkdir -p /rootfs/{bin,dev,etc}` silently creates one directory literally named `{bin,dev,etc}`. Every directory creation in the Dockerfile uses explicit separate paths.

### virtio modules aren't built into `linux-virt`

Alpine's `linux-virt` kernel ships virtio drivers as loadable `.ko.gz` modules, not built-in. Getting networking required:
1. Copying the virtio module tree into the initramfs during the Docker build
2. Decompressing `.ko.gz` → `.ko` (busybox `insmod` can't load compressed modules)
3. Running `depmod` to rebuild the module dependency map
4. Calling `modprobe virtio_net` at boot (which auto-loads the full dependency chain: `failover → net_failover → virtio → virtio_ring → virtio_pci → virtio_net`)

### DHCP doesn't work — `AF_PACKET` not supported

`udhcpc` (busybox DHCP client) uses raw `AF_PACKET` sockets. `CONFIG_PACKET` is not in `linux-virt`. Solution: skip DHCP entirely. QEMU user-mode networking always assigns the same static addresses (`10.0.2.15` guest, `10.0.2.2` gateway, `10.0.2.3` DNS), so static configuration is reliable and requires no kernel support beyond the NIC driver.

### `-cpu host` only works with KVM or HVF

When QEMU falls back to software emulation (`tcg`), `-cpu host` is invalid. The engine detects the accelerator at runtime and selects `max` (best emulated CPU model) for `tcg`, `host` for `kvm`/`hvf`.

### File permissions for non-root containers

The engine runs as `altuser` (UID 1001). Files `COPY`ed into the Docker image default to root ownership — QEMU can't open them. Fixed with `--chown=1001:1001` on every `COPY` in the production stage.

---

## Best Practices

### Security

| Property | How it's enforced |
|---|---|
| **VM isolation** | Each script runs in its own kernel instance — no shared filesystem, no shared network between executions |
| **Ephemeral** | VMs boot from a read-only initramfs written to a temp file; nothing persists between runs |
| **Secret scrubbing** | `scrubSecrets()` replaces all injected values with `[REDACTED]` before output leaves the pipeline |
| **Secret hygiene** | Secretstore never logs secret values; audit events record key *names* only |
| **Minimal rootfs** | Guest has busybox only — no package manager, no shell history, no network stack by default |
| **Non-root container** | Engine runs as UID 1001 (`altuser`); files copied with `--chown=1001:1001` |
| **Token auth** | `engineOnly` middleware reads and validates `X-Engine-Token` before touching the request body; service refuses to start if `ENGINE_TOKEN` is unset |
| **No external deps** | Zero Go modules outside stdlib — no supply chain attack surface |

### Deployment

- One command: `docker compose build && docker compose up`
- `privileged: true` required for QEMU to run inside Docker on Linux
- KVM passthrough: add `/dev/kvm` device for hardware acceleration
- macOS: TCG software emulation works correctly, just slower (seconds vs milliseconds)
- Override accelerator: `QEMU_ACCEL=kvm|hvf|tcg`

### High Availability / Operational

**Stateless execution:** VMs are ephemeral and the engine holds no state between requests. The engine can be horizontally scaled — any instance can handle any request.

**Pluggable audit trail:** Every execution and every secret access is logged as NDJSON. Add destinations by implementing one interface:

```go
type Exporter interface {
    Export(e Event) error
}
```

No changes to `Orchestrator`, `Preprocessor`, or any handler — add exporters in `main.go` only:

```go
audit.New(
    audit.NewJSONExporter(os.Stdout),  // stdout
    &PostgresExporter{db: db},          // database
    &WebhookExporter{url: "..."},       // Splunk, etc.
)
```

**Interface-driven substitution:** Every major component is swappable at startup without touching the pipeline:
- VM backend: implement `VM` interface → new hypervisor
- Secret store: implement `SecretManager` → HashiCorp Vault, AWS KMS, GCP Secret Manager
- Audit exporter: implement `Exporter` → any destination

---

## Testing & Demo

### Unit tests (no Docker required)

```sh
cd engine && go test ./cmd/vault/
cd engine && go test -v ./cmd/vault/
```

Uses `httptest.Server` to stand in for the engine — runs instantly, works offline.

| Category | Tests |
|---|---|
| Script input | `-s` flag, `-f` flag, stdin pipe, `-f` wins over `-s` |
| Env flags | Multiple `-e KEY=VAL` pairs forwarded; missing `=` exits non-zero |
| Exit code | Engine codes 0, 1, 2, 42 all propagate as CLI exit code |
| Error handling | No script provided, missing file, engine HTTP 500, engine unreachable |
| CLI interface | Unknown command, `-h`/`--help`, no args |

### Integration tests (spins up real Docker stack)

```sh
cd engine && go test -tags integration -timeout 5m ./cmd/vault/
cd engine && go test -tags integration -v -timeout 5m -run TestIntegration_InlineEcho ./cmd/vault/
```

`TestMain` runs `docker compose up --build --wait -d` before any test, `docker compose down` on exit. Each test hits a real QEMU VM.

| Test | What it proves |
|---|---|
| `TestIntegration_InlineEcho` | Script executes in a real VM and returns output |
| `TestIntegration_FileScript` | File-based script works end to end |
| `TestIntegration_StdinScript` | Stdin pipe works end to end |
| `TestIntegration_ExitCodePassthrough` | `exit 42` in VM propagates as CLI exit code |
| `TestIntegration_SecretPlaceholder` | `{{API_KEY}}` injected from MockStore, scrubbed to `[REDACTED]` in output |
| `TestIntegration_MultilineScript` | Multi-statement scripts run correctly |
| `TestIntegration_IsolationBetweenRuns` | File written to `/tmp` in VM 1 is absent in VM 2 — no shared state |

### Live demo via internal-ui

```sh
docker compose up
# Open http://localhost
```

Demo flow:
1. Submit a script with `{{PLACEHOLDER}}` tokens — show injection working
2. Add `echo $API_KEY` — show `[REDACTED]` scrubbing in output
3. Show engine stdout — audit events (`execution`, `secret_access`) as NDJSON
4. Submit two scripts that write to `/tmp` — show isolation (no shared state between VMs)
