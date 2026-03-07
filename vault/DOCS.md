# cerberOS Vault — Dev Notes

## Architecture Overview

The vault engine is a Go service that spins up QEMU microVMs on demand. Each VM boots a minimal Linux kernel with a self-contained initramfs (in-memory root filesystem) — no persistent disk, no state between runs.

```
compose.yaml
  └─ engine (Go binary)
       └─ QEMU (qemu-system-aarch64 / qemu-system-x86_64)
            ├─ kernel:  Alpine linux-virt (/vm/kernel)
            └─ initrd:  busybox-static cpio.gz (/vm/initrd.gz)
```

---

## Running Locally

```bash
docker compose build
docker compose up
```

You should see kernel boot output followed by:

```
=== cerberOS VM ready ===
/bin/sh: can't access tty; job control turned off
```

That's the VM shell running. The TTY message is expected — the shell is headless by design.

---

## How It Works

### Multi-stage Docker build

The `engine/Dockerfile` has three stages:

| Stage       | What it does                                                  |
| ----------- | ------------------------------------------------------------- |
| `build`     | Compiles the Go engine binary                                 |
| `artifacts` | Downloads Alpine's `linux-virt` kernel + builds the initramfs |
| production  | Combines QEMU + kernel + initrd + engine into the final image |

### Initramfs instead of a block device

We use a **cpio initramfs** rather than an ext2/ext4 disk image. The kernel decompresses it directly into memory at boot — no disk driver, no device mounting needed. This sidesteps a class of issues with virtio modules not being available before the filesystem is accessible.

```dockerfile
# In the artifacts stage:
RUN cd /rootfs && find . | cpio -o -H newc | gzip > /initrd.gz
```

QEMU loads it with `-initrd /vm/initrd.gz`. No `-drive` or `-device` flags needed.

### Accelerator detection

The engine auto-detects the right QEMU accelerator at runtime:

| Environment                   | Accelerator | Notes                                    |
| ----------------------------- | ----------- | ---------------------------------------- |
| Linux with `/dev/kvm` present | `kvm`       | Fast — used in CI and production         |
| macOS Docker / no KVM         | `tcg`       | Software emulation — slow but functional |

Override with `QEMU_ACCEL=kvm|hvf|tcg` if needed.

---

## macOS Limitations

Docker containers on macOS run inside a Linux VM (Docker Desktop or OrbStack). **Apple Silicon does not support nested KVM virtualization** — Apple's Hypervisor.framework doesn't expose it. Neither Docker Desktop nor OrbStack can work around this.

This means on Mac, QEMU falls back to `tcg` (full software emulation). The VM still boots and works correctly, just slowly (seconds instead of milliseconds). For latency-sensitive work, use a Linux host.

For production and CI, use a Linux host with `/dev/kvm` — `compose.yaml` already has `privileged: true` and `/dev/kvm` device passthrough configured.

---

## Things We Learned the Hard Way

These are real issues hit during setup, documented so you don't hit them again.

### Alpine sh doesn't support brace expansion

Alpine's `/bin/sh` is busybox ash — POSIX only. Brace expansion like `{bin,dev,etc}` is a bashism and silently does the wrong thing (creates a literal directory named `{bin,dev,etc}`).

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

The engine container runs as `altuser` (UID 1001). Files copied into the image default to root ownership and QEMU can't open them. Fix with `--chown` at copy time:

```dockerfile
COPY --chown=1001:1001 --from=artifacts /boot/vmlinuz-virt /vm/kernel
COPY --chown=1001:1001 --from=artifacts /initrd.gz /vm/initrd.gz
```

### ARM serial console is `ttyAMA0`, not `ttyS0`

On QEMU's `virt` machine (used for aarch64), the UART is a PL011 — which appears as `/dev/ttyAMA0` in the guest kernel, not `ttyS0`. Using the wrong device means the kernel boots silently with no output.

```
# Wrong for ARM
console=ttyS0

# Correct for ARM QEMU virt
console=ttyAMA0
```

`ttyS0` is correct for x86 (`microvm` machine type).

### `hvc0` console requires an explicit virtio-serial device

`-nographic` auto-wires the first serial port (UART) to stdio. It does **not** wire `hvc0`. Using `console=hvc0` without adding `-device virtio-serial-device -device virtconsole,...` means the kernel outputs nothing.

Stick to `console=ttyAMA0` (ARM) or `console=ttyS0` (x86) — both work with `-nographic` out of the box.

### Block device approach hit a kernel module wall

The original approach used an ext2 disk image attached via `virtio-blk-device`. Alpine's `linux-virt` kernel compiles `virtio_blk` as a loadable module, not built-in. Without an initramfs to load it first, the kernel panics trying to mount the root device:

```
VFS: Unable to mount root fs on unknown-block(0,0)
Kernel panic - not syncing: VFS: Unable to mount root fs
```

Switching to an initramfs entirely avoids this — the kernel loads it from memory before any drivers are needed.

---

## Environment Variables

| Variable            | Default         | Description                                          |
| ------------------- | --------------- | ---------------------------------------------------- |
| `KERNEL_IMAGE_PATH` | `/vm/kernel`    | Path to the kernel image inside the container        |
| `INITRD_PATH`       | `/vm/initrd.gz` | Path to the gzipped cpio initramfs                   |
| `QEMU_ACCEL`        | auto-detected   | Force a specific accelerator: `kvm`, `hvf`, or `tcg` |
