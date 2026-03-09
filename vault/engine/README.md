# vault/engine

The engine is a secure, ephemeral script execution service. It accepts arbitrary shell scripts over HTTP, injects secrets, runs them inside isolated QEMU virtual machines, and returns the output with secrets scrubbed.

Each execution gets a fresh VM that boots, runs the script, and powers off — no shared state between requests.

## How it works

```
POST /execute
      │
      ▼
 Preprocessor          Replaces {{PLACEHOLDER}} tokens with secrets from the SecretStore
      │
      ▼
 InitRD Builder        Packs the script into a per-job initramfs alongside a custom /init
      │
      ▼
 QEMU VM               Boots the kernel with the custom initrd; init executes the script
      │
      ▼
 Orchestrator          Extracts output between sentinel markers, scrubs injected secrets
      │
      ▼
 JSON response         { "output": "...", "exit_code": 0 }
```

### 1. Preprocessor (`preprocessor/`)

Scans the script for `{{KEY_NAME}}` placeholders and replaces them with values fetched from a `SecretStore`. Returns the processed script and a list of the injected values (used later for scrubbing).

The `SecretStore` interface is pluggable — the current implementation is a mock. Drop in HashiCorp Vault, AWS KMS, etc. without changing anything else.

### 2. InitRD Builder (`initrd/`)

Builds a per-job initramfs on top of the base `/initrd.gz` that ships with the container:

1. Decompresses the base cpio archive
2. Strips the `TRAILER!!!` end marker
3. Appends three new entries in newc (ASCII hex) cpio format:
   - `/job/` — directory
   - `/job/script.sh` — the processed script (executable)
   - `/init` — replacement init that mounts `/proc`, `/sys`, `/dev`, runs the script, and powers off the VM via `echo o > /proc/sysrq-trigger`
4. Re-appends `TRAILER!!!`, gzips, and writes to a temp file

The init script wraps execution in sentinel markers:

```
=== cerberOS job start ===
<script output>
=== cerberOS job exit_code=N ===
```

### 3. QEMU VM (`vm/`)

A `VM` interface with a QEMU backend. Platform defaults are chosen automatically:

| Architecture | QEMU binary           | Machine type | Console   |
| ------------ | --------------------- | ------------ | --------- |
| `aarch64`    | `qemu-system-aarch64` | `virt`       | `ttyAMA0` |
| `x86_64`     | `qemu-system-x86_64`  | `microvm`    | `ttyS0`   |

Accelerator priority: KVM (if `/dev/kvm` exists) → TCG software emulation.

The `Run` method starts the VM, captures all output to a buffer, and waits for it to exit. The VM powers itself off at the end of the init script — no external signal needed.

### 4. Orchestrator (`orchestrator/`)

Ties the pipeline together. For each request:

1. Calls the preprocessor
2. Builds the initrd (written to a temp file, deleted on completion)
3. Creates an ephemeral QEMU VM pointing at that initrd
4. Calls `vm.Run()` and extracts the output between the sentinel markers
5. Replaces every injected secret value with `[REDACTED]`
6. Returns `Response{Output, ExitCode}`

### 5. Audit Logger (`audit/`)

Structured audit logging for agent interactions. Every `POST /execute` call produces two events in order:

| Event | `kind` | What it records |
| --- | --- | --- |
| Script submitted | `execution` | agent identifier |
| Secrets resolved | `secret_access` | agent identifier + secret **names** (never values) |

Events are written as newline-delimited JSON to stdout by default:

```json
{"time":"2026-03-08T12:00:00Z","kind":"execution","agent":"my-agent","message":"agent submitted script for execution"}
{"time":"2026-03-08T12:00:00Z","kind":"secret_access","agent":"my-agent","keys":["API_KEY","DB_PASS"],"message":"agent requested secrets"}
```

The logger is pluggable — ship events anywhere by implementing `audit.Exporter`:

```go
type Exporter interface {
    Export(e Event) error
}
```

Pass one or more exporters to `audit.New` in `main.go`:

```go
audit.New(
    audit.NewJSONExporter(os.Stdout), // stdout (default)
    &PostgresExporter{db: db},        // database
    &WebhookExporter{url: "..."},     // HTTP ingest, Splunk, etc.
)
```

No changes to `Orchestrator`, `Preprocessor`, or any other package are needed when adding a new destination.

### 6. HTTP Server (`main.go`)

Listens on `:8000`. Manages a single long-lived VM instance (for `start`/`stop`) separately from ephemeral execute requests.

| Endpoint   | Method | Description                         |
| ---------- | ------ | ----------------------------------- |
| `/start`   | POST   | Boot the persistent VM (debug)      |
| `/stop`    | POST   | Shut down the persistent VM (debug) |
| `/execute` | POST   | Run a script in an ephemeral VM     |

`/execute` request body:

```json
{
  "agent": "my-agent",
  "script": "#!/bin/sh\necho {{API_KEY}}"
}
```

## Building

The `Dockerfile.qemu` is a three-stage build:

1. **Build** — compiles the Go binary (static, no CGO)
2. **Artifacts** — builds a minimal Alpine rootfs (busybox only), compiles the `linux-virt` kernel, and creates the base `initrd.gz`
3. **Production** — assembles the final image with QEMU, kernel, base initrd, and engine binary; runs as non-root `altuser` (UID 1001)

```sh
docker build -f Dockerfile.qemu -t vault-engine .
docker run -p 8000:8000 vault-engine
```

KVM passthrough for hardware acceleration:

```sh
docker run --device /dev/kvm -p 8000:8000 vault-engine
```

## Dependencies

None. The entire engine is standard library Go (`go 1.24`).

## Security properties

- **VM isolation** — each script runs in its own kernel instance with no shared filesystem or network
- **Ephemeral** — VMs boot from a read-only initramfs; nothing persists between executions
- **Secret scrubbing** — injected secret values are replaced with `[REDACTED]` before output is returned
- **Minimal rootfs** — only busybox in the guest; no package manager, no shell history, no network stack
- **Non-root container** — engine process runs as UID 1001
- **Audit trail** — every execution and secret access is logged with agent identity and secret names (never values); exporters are pluggable
- **No external dependencies** — reduced supply chain surface
