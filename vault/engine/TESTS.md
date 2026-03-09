# vault/engine — Test Guide

## Overview

There are two test suites for the `vault` CLI (`engine/cmd/vault/`):

| Suite | File | Requires Docker |
|---|---|---|
| Unit | `main_test.go` | No |
| Integration | `integration_test.go` | Yes |

---

## Unit Tests

**File:** `engine/cmd/vault/main_test.go`

No external dependencies. A `httptest.Server` stands in for the engine, so tests run instantly and offline.

```sh
# from engine/
go test ./cmd/vault/

# verbose
go test -v ./cmd/vault/
```

### What is tested

**Script input sources**

| Test | What it checks |
|---|---|
| `TestInlineScript` | `-s` flag sends the correct script body to the engine |
| `TestFileScript` | `-f` flag reads a file and sends its contents |
| `TestStdinScript` | Piped stdin is read and forwarded when no flag is given |
| `TestFileTakesPriorityOverScript` | `-f` wins over `-s` when both are provided |

**Environment flags**

| Test | What it checks |
|---|---|
| `TestEnvFlags` | Multiple `-e KEY=VAL` pairs are all forwarded in the request |
| `TestEnvFlagInvalidFormat` | `-e NOEQUALS` (missing `=`) exits non-zero with a helpful hint |

**Exit code passthrough**

| Test | What it checks |
|---|---|
| `TestExitCodePassthrough` | Engine exit codes 0, 1, 2, 42 are all returned as the CLI's own exit code |

**Error handling**

| Test | What it checks |
|---|---|
| `TestNoScriptProvided` | No `-f`, `-s`, or stdin → exits non-zero with usage hint |
| `TestMissingFile` | `-f /nonexistent` → exits non-zero, mentions "reading file" |
| `TestEngineHTTPError` | Engine returns 500 → CLI exits non-zero, surfaces the status code |
| `TestEngineUnreachable` | Engine not running → CLI exits non-zero, mentions "connecting to engine" |

**CLI interface**

| Test | What it checks |
|---|---|
| `TestUnknownCommand` | Unknown subcommand → exits non-zero, mentions "unknown command" |
| `TestHelpFlag` | `-h` / `--help` → exits 0, prints usage |
| `TestNoArgs` | No arguments → exits 0, prints usage |

---

## Integration Tests

**File:** `engine/cmd/vault/integration_test.go`
**Build tag:** `integration`

`TestMain` runs `docker compose up --build --wait -d` against `vault/compose.yaml` before any test runs, then `docker compose down` on exit. Each test hits a real QEMU VM via the live engine HTTP server.

**Requires:** Docker with Compose v2 (`docker compose` subcommand).

```sh
# from engine/
go test -tags integration -timeout 5m ./cmd/vault/

# verbose — shows stdout/stderr/exit_code from every VM execution
go test -tags integration -v -timeout 5m ./cmd/vault/

# run a single test
go test -tags integration -v -timeout 5m -run TestIntegration_InlineEcho ./cmd/vault/
```

> The timeout is set to 5 minutes to cover the initial Docker build. Subsequent runs are faster if the image is already cached.

### What is tested

| Test | What it checks |
|---|---|
| `TestIntegration_InlineEcho` | `-s` flag executes a script in a real VM and returns output |
| `TestIntegration_FileScript` | `-f` flag reads a local file and executes it in the VM |
| `TestIntegration_StdinScript` | Piped stdin is executed in the VM |
| `TestIntegration_ExitCodePassthrough` | `exit 42` in the script propagates as the CLI's exit code |
| `TestIntegration_SecretPlaceholder` | `{{API_KEY}}` is injected from the MockStore and scrubbed to `[REDACTED]` in output |
| `TestIntegration_MultilineScript` | Multi-statement scripts run correctly inside the VM |
| `TestIntegration_IsolationBetweenRuns` | A file written to `/tmp` in one VM is absent in the next — no shared state |

### Compose setup

The engine is defined in `vault/compose.yaml`. It builds from `engine/Dockerfile.qemu` and exposes port `8000`. The `privileged: true` flag is required for QEMU to run inside the container without KVM passthrough.

To use hardware acceleration (Linux hosts with KVM):

```yaml
# in vault/compose.yaml, under services.engine:
devices:
  - /dev/kvm:/dev/kvm
```
