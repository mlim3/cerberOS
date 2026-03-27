# cerberOS Vault — Dev Notes

## Architecture Overview

Vault is a credential broker service. Agents submit scripts with `{{PLACEHOLDER}}` markers; vault resolves secrets, injects them, and returns the completed script. The agent runs the script in its own environment.

```
compose.yaml
  └─ vault (Go binary, :8000)
       ├─ SecretManager  (pluggable secret backend)
       ├─ Preprocessor   (placeholder resolution + injection)
       └─ Audit Logger   (structured event logging)
```

---

## Running Locally

```bash
docker compose build
docker compose up
```

The vault service starts on `:8000`. Use the CLI or POST to `/inject`:

```bash
vault inject -s 'echo {{API_KEY}}'
# or
curl -X POST http://localhost:8000/inject \
  -H 'Content-Type: application/json' \
  -d '{"agent":"test","script":"echo {{API_KEY}}"}'
```

---

## How It Works

### Secret injection flow

1. Agent submits script with `{{PLACEHOLDER}}` tokens
2. Preprocessor extracts all unique placeholder keys
3. SecretManager resolves all keys atomically (all-or-nothing)
4. Placeholders are substituted with resolved values
5. Completed script is returned to the agent

If any secret is missing or denied, the entire request fails — no partial injection.

### SecretManager interface

```go
type SecretManager interface {
    Resolve(keys []string) (map[string]string, error)
}
```

The current `MockSecretManager` provides dev secrets. Swap in HashiCorp Vault, AWS Secrets Manager, etc. without changing the rest of the codebase.

---

## Historical Notes

The following notes document issues encountered during the original QEMU-based execution engine (V0). They're preserved as context for anyone working with VM-based execution in the future.

### Alpine sh doesn't support brace expansion

Alpine's `/bin/sh` is busybox ash — POSIX only. Brace expansion like `{bin,dev,etc}` is a bashism and silently does the wrong thing.

```sh
# Wrong — creates one directory named "{bin,dev,etc}"
mkdir -p /rootfs/{bin,dev,etc}

# Right
mkdir -p /rootfs/bin /rootfs/dev /rootfs/etc
```

### `-cpu host` only works with kvm or hvf

When QEMU falls back to `tcg`, you must use a named CPU model instead:

```go
if accel == "tcg" {
    return "max"   // best emulated CPU
}
return "host"      // passthrough, requires kvm/hvf
```

### File permissions for non-root containers

Containers running as non-root need `--chown` at COPY time:

```dockerfile
COPY --chown=1001:1001 --from=artifacts /boot/vmlinuz-virt /vm/kernel
```

### ARM serial console is `ttyAMA0`, not `ttyS0`

On QEMU's `virt` machine (aarch64), the UART appears as `/dev/ttyAMA0`.

### Block device approach hit a kernel module wall

Alpine's `linux-virt` kernel compiles `virtio_blk` as a loadable module, not built-in. Without an initramfs to load it first, the kernel panics. Switching to initramfs avoids this entirely.
