# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

cerberOS Vault is a secure, ephemeral script execution service. AI agents submit shell scripts; the vault injects secrets at runtime inside isolated QEMU VMs and scrubs secret values from output before returning results. The core security invariant: **agents never see raw secret values**.

## Commands

### Build & Run

```bash
docker compose build
docker compose up
```

Services: engine (:8000), secretstore (:8001), internal-ui (:80), swagger (:8080).

### Tests

Unit tests (no Docker required):
```bash
cd engine && go test ./cmd/vault/
cd engine && go test -v ./cmd/vault/
```

Integration tests (requires running Docker stack):
```bash
cd engine && go test -tags integration -timeout 5m ./cmd/vault/
# Run a single integration test:
cd engine && go test -tags integration -v -timeout 5m -run TestIntegration_InlineEcho ./cmd/vault/
```

### UI Development

```bash
cd internal-ui && npm run dev    # Dev server
cd internal-ui && npm run lint   # ESLint
cd internal-ui && npm run build  # Production build
```

## Architecture

### Three Services

```
Agent → POST /execute → engine (:8000, Go)
                            │ POST /secrets/resolve (X-Engine-Token header)
                            ▼
                        secretstore (:8001, Go)

internal-ui (:80, Next.js) — browser UI proxying to engine
```

### Execution Pipeline (engine/orchestrator)

The orchestrator runs 5 sequential steps — order encodes the security invariant:

1. **Preprocessor** — finds `{{PLACEHOLDER}}` patterns, batch-resolves from secretstore, returns processed script + injected key list
2. **InitRD Builder** — packs processed script into per-job cpio initramfs as `/job/script.sh`
3. **QEMU VM** — boots Alpine linux-virt kernel with ephemeral initramfs; custom `/init` wraps execution
4. **extractJobOutput()** — extracts output between sentinel markers (`=== cerberOS job start ===` / `=== cerberOS job exit_code=N ===`) to strip kernel boot noise
5. **scrubSecrets()** — replaces all injected secret values with `[REDACTED]`

### Design Patterns

- **Strategy** — `VM` interface (`engine/vm/vm.go`) allows swapping QEMU for Firecracker etc.; `SecretManager` interface in secretstore allows swapping the mock for real backends (Vault, KMS)
- **Pipeline** — orchestrator's 5 steps are strictly linear; each step's output feeds the next
- **Observer** — `audit.Exporter` interface; `JSONExporter` and `MultiExporter` compose audit outputs
- **Template Method** — preprocessor's placeholder collection → batch resolve → audit → substitute flow

See `DESIGN_PATTERNS.md` for deep-dive rationale on each pattern.

## Key Implementation Notes

### Zero External Go Dependencies

Both `engine/` and `secretstore/` use Go 1.24 stdlib only. This is intentional to minimize supply chain risk. Do not add external dependencies without strong justification.

### QEMU Architecture Gotchas (from DOCS.md)

- ARM serial console is `ttyAMA0`, not `ttyS0` — engine auto-detects per arch
- Virtio modules are not built-in to `linux-virt` — copied and depmod'd during Docker build
- DHCP doesn't work (no `AF_PACKET`) — VMs use static IP `10.0.2.15/24`
- `-cpu host` only works with KVM/HVF; engine uses `max` for `tcg` fallback
- All `COPY` commands use `--chown=1001:1001` — engine runs as UID 1001 (`altuser`)
- Alpine sh doesn't support brace expansion — create directories explicitly

### Secret Safety

- `scrubSecrets()` in orchestrator runs on all output before returning
- Secretstore's `engineOnly` middleware validates `X-Engine-Token` before reading the request body
- Audit events record key *names* only, never values — enforced in preprocessor and secretstore audit

### initramfs Construction

`engine/initrd/builder.go` writes a cpio newc archive programmatically (no `cpio` binary required). The custom `/init` script prints the sentinel markers and manages the script execution lifecycle.

### Environment Variables

| Variable | Description |
|----------|-------------|
| `KERNEL_IMAGE_PATH` | Path to kernel inside container (default: `/vm/kernel`) |
| `INITRD_PATH` | Path to base initramfs (default: `/vm/initrd.gz`) |
| `QEMU_ACCEL` | Force accelerator: `kvm`, `hvf`, or `tcg` (auto-detected by default) |
| `SECRET_STORE_TOKEN` | Shared token for engine → secretstore auth |
| `SECRET_STORE_URL` | Secretstore base URL (default: `http://localhost:8001`) |
| `ENGINE_TOKEN` | Token secretstore expects in `X-Engine-Token` header |

## Testing Approach

- **Unit tests** (`engine/cmd/vault/main_test.go`) use `httptest.Server` to mock the engine HTTP API — fast, no Docker
- **Integration tests** (`engine/cmd/vault/integration_test.go`, build tag `integration`) spin up the full Docker stack and test real VM execution including secret injection and output scrubbing
- The CLI tool in `engine/cmd/vault/` is the primary agent interface and the main target of both test suites
