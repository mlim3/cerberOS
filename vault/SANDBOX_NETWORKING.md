# Sandbox VM Networking — Issues & Solutions

A full record of every problem hit getting curl working inside a QEMU initramfs VM, in the order they were encountered.

---

## 1. `bash-static` doesn't exist on Alpine

**Symptom:** `apk add bash-static` → `ERROR: unable to select packages: bash-static (no such package)`

**Cause:** Alpine Linux does not ship a statically-linked bash package. The package is simply called `bash` and it is dynamically linked against `musl` and `libreadline`.

**Fix:** Install `bash` (dynamic) and manually copy the binary and its shared libraries into the initramfs rootfs.

```dockerfile
RUN apk add --no-cache bash

RUN cp /bin/bash /rootfs/bin/bash && \
    cp /lib/ld-musl-*.so.1 /rootfs/lib/ && \
    # ... copy libreadline, libncursesw, etc.
```

---

## 2. Bash is at `/bin/bash`, not `/usr/bin/bash`

**Symptom:** `cp: can't stat '/usr/bin/bash': No such file or directory`

**Cause:** On Alpine, bash installs to `/bin/bash`. The assumption that it was under `/usr/bin/` (common on glibc distros) was wrong.

**Fix:** Use `/bin/bash` as the source path.

---

## 3. `/rootfs/lib/` didn't exist yet when copying shared libs

**Symptom:** `cp: can't create '/rootfs/lib/': Is a directory` (confusingly worded — actually means the directory didn't exist)

**Cause:** The `mkdir -p` that created the rootfs skeleton didn't include `/rootfs/lib` or `/rootfs/usr/lib`.

**Fix:** Add them to the initial `mkdir -p`:

```dockerfile
RUN mkdir -p /rootfs/bin /rootfs/dev /rootfs/etc /rootfs/proc \
             /rootfs/sys /rootfs/tmp /rootfs/run /rootfs/lib /rootfs/usr/lib
```

---

## 4. No network device in the VM (`eth0` missing entirely)

**Symptom:** `ip: ioctl 0x8913 failed: No such device` — `eth0` never appeared, only `lo`.

**Cause:** The QEMU launch args had no `-netdev` or `-device` options. QEMU defaults to no networking.

**Fix:** Add a user-mode network backend and a virtio NIC to `buildArgs()` in [qemu.go](engine/vm/qemu.go):

```go
"-netdev", "user,id=net0",
"-device", "virtio-net-pci,netdev=net0",
```

Also removed `pci=off` from the x86 kernel args, which would have prevented `virtio-pci` from initialising.

---

## 5. `linux-virt` ships virtio as loadable modules, not built-in

**Symptom:** Even after adding the QEMU NIC args, `eth0` still didn't appear. No virtio kernel messages in dmesg.

**Cause:** Alpine's `linux-virt` kernel does not build virtio drivers into the kernel image — they are loadable `.ko` modules. Without loading them, the PCI device QEMU presents is invisible to the guest.

**Fix:** Copy the virtio module tree into the initramfs and call `modprobe virtio_net` at boot.

---

## 6. `bash-static` package doesn't exist → reframe: module copy glob was wrong

**Symptom:** After adding a module copy step, the VM still logged `module virtio not found`. The modules directory in the initramfs contained only `modules.dep` metadata, no `.ko` files.

**Cause:** The `for mod in drivers/virtio/virtio.ko* ...` loop used `[ -f "$KMOD/$mod" ]` with a glob in the path, but the shell does not expand globs in variable-prefixed paths like that inside `[ -f ... ]`. The copy silently did nothing.

**Fix:** Use `find` with `-name` patterns and copy everything it finds, preserving directory structure:

```dockerfile
RUN set -e && \
  KVER=$(ls /lib/modules) && \
  cd / && \
  find lib/modules/$KVER/kernel \( \
    -name "virtio*.ko*" -o \
    -name "failover.ko*" -o \
    -name "net_failover.ko*" \
  \) | while read f; do \
    mkdir -p "/rootfs/$(dirname "$f")" && \
    # decompress or copy
  done && \
  depmod -b /rootfs $KVER
```

---

## 7. Modules are gzip-compressed (`.ko.gz`) — `insmod` can't load them

**Symptom:** `insmod: Invalid ELF header magic` — the `.ko.gz` files are gzip-compressed. Plain `insmod` (busybox version) does not decompress them.

**Cause:** Alpine compresses kernel modules with gzip to save space. The busybox `insmod` applet does not support compressed modules.

**Fix:** Decompress `.ko.gz` files to plain `.ko` during the Docker build step, so `modprobe` (which reads `modules.dep`) can load them normally at boot:

```dockerfile
case "$f" in
  *.gz)  gunzip -c "$f" > "/rootfs/${f%.gz}" ;;
  *.zst) zstd -dq "$f" -o "/rootfs/${f%.zst}" ;;
  *)     cp "$f" "/rootfs/$f" ;;
esac
```

Then re-run `depmod -b /rootfs $KVER` so `modules.dep` points to the decompressed filenames.

---

## 8. Dependency chain for `virtio_net`

**Symptom:** `virtio_net: Unknown symbol net_failover_create` — loading `virtio_net.ko` alone fails because it depends on other modules.

**Cause:** `virtio_net` has a dependency chain:

```
failover → net_failover → virtio_net
virtio → virtio_ring → virtio_pci → virtio_net
```

**Fix:** Don't use `insmod` (no dep resolution). Use `modprobe`, which reads `modules.dep` and loads the full chain automatically:

```sh
modprobe virtio_net
```

This loads all deps in the correct order automatically.

---

## 9. `udhcpc` fails — `AF_PACKET` not supported

**Symptom:** `udhcpc: socket(AF_PACKET,2,8): Address family not supported by protocol`

**Cause:** `udhcpc` (busybox DHCP client) uses raw `AF_PACKET` sockets to send DHCP broadcast frames. `CONFIG_PACKET` (the kernel module that provides `AF_PACKET`) is not built into `linux-virt` and wasn't included in our module copy.

**Fix:** Skip DHCP entirely. QEMU's user-mode networking (`-netdev user`) always assigns the same static addresses:

| Address       | Role              |
|---------------|-------------------|
| `10.0.2.15`   | Guest IP          |
| `10.0.2.2`    | Default gateway   |
| `10.0.2.3`    | DNS forwarder     |

Configure statically at boot:

```sh
ip link set eth0 up
ip addr add 10.0.2.15/24 dev eth0
ip route add default via 10.0.2.2
echo "nameserver 10.0.2.3" > /etc/resolv.conf
```

QEMU's DNS forwarder at `10.0.2.3` proxies to the host's resolver, so full DNS resolution works.

---

## Final Working Network Init Sequence

```sh
# Load the virtio NIC driver (and its deps via depmod metadata)
modprobe virtio_net

# Configure with QEMU user-mode static addresses
ip link set eth0 up
ip addr add 10.0.2.15/24 dev eth0
ip route add default via 10.0.2.2
echo "nameserver 10.0.2.3" > /etc/resolv.conf
```

---

## What Goes in the Initramfs for Networking

| Component | Where it comes from |
|-----------|-------------------|
| `virtio*.ko` + deps, decompressed | Copied from `/lib/modules/$KVER/kernel/` during Docker build |
| `modules.dep` (rebuilt) | `depmod -b /rootfs $KVER` during Docker build |
| `curl` binary | `apk add curl` + copied with `ldd`-resolved shared libs |
| `ca-certificates.crt` | `apk add ca-certificates` + copied to `/etc/ssl/certs/` |
| musl dynamic linker | `/lib/ld-musl-*.so.1` copied to `/rootfs/lib/` |
| busybox applets | `ip`, `modprobe`, `insmod` as symlinks to `busybox.static` |
