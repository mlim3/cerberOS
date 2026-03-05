# Build stage
FROM golang:1.25.5-alpine AS build

# Install build dependencies for CGO
RUN apk add --no-cache gcc musl-dev

WORKDIR /app

# Copy dependency files first for better layer caching
#COPY go.mod go.sum ./
COPY go.mod ./

# Cache mounts speed up the installation of existing dependencies
RUN --mount=type=cache,target=/go/pkg/mod \
  --mount=type=cache,target=/root/.cache/go-build \
  go mod download

# Add non-root user (Alpine uses adduser instead of useradd)
RUN adduser -D -u 1001 altuser

COPY . .

# Compile the application as a statically-linked binary
RUN CGO_ENABLED=0 go build \
  -ldflags="-s -w" \
  -o engine

# Artifacts stage - builds the kernel and initramfs that the VM boots
FROM alpine:3.23.3 AS artifacts

# linux-virt: Alpine's paravirtualized kernel
# busybox-static: statically linked — no shared libs needed in the initramfs
# cpio: packs the rootfs directory into a cpio archive (no loop device, no disk driver needed)
RUN apk add --no-cache linux-virt busybox-static

# Build minimal rootfs directory (no brace expansion — alpine sh is POSIX only)
RUN mkdir -p /rootfs/bin /rootfs/dev /rootfs/etc /rootfs/proc /rootfs/sys /rootfs/tmp /rootfs/run && \
  cp /bin/busybox.static /rootfs/bin/busybox && \
  ln -sf busybox /rootfs/bin/sh && \
  ln -sf busybox /rootfs/bin/ash && \
  ln -sf busybox /rootfs/bin/ls && \
  ln -sf busybox /rootfs/bin/cat && \
  ln -sf busybox /rootfs/bin/echo && \
  ln -sf busybox /rootfs/bin/mount && \
  ln -sf busybox /rootfs/bin/umount && \
  ln -sf busybox /rootfs/bin/mkdir && \
  ln -sf busybox /rootfs/bin/env && \
  ln -sf busybox /rootfs/bin/sleep && \
  ln -sf busybox /rootfs/bin/poweroff

# Init script: mounts essential pseudo-filesystems then drops to a shell
RUN printf '#!/bin/sh\n\
  mount -t proc proc /proc\n\
  mount -t sysfs sysfs /sys\n\
  mount -t devtmpfs devtmpfs /dev 2>/dev/null || true\n\
  echo "=== cerberOS VM ready ==="\n\
  exec /bin/sh\n' > /rootfs/init && \
  chmod +x /rootfs/init

# Pack rootfs into a gzipped cpio initramfs.
# The kernel loads this directly into memory — no disk driver or block device needed.
RUN cd /rootfs && find . | cpio -o -H newc | gzip > /initrd.gz

# Production stage
FROM alpine:3.23.3

WORKDIR /

# Install QEMU for the current platform (aarch64 or x86_64)
RUN case "$(uname -m)" in \
  aarch64) apk add --no-cache qemu-system-aarch64 ;; \
  x86_64)  apk add --no-cache qemu-system-x86_64  ;; \
  esac

# Copy kernel and initramfs from artifacts stage, owned by altuser (1001) so QEMU can open them
COPY --chown=1001:1001 --from=artifacts /boot/vmlinuz-virt /vm/kernel
COPY --chown=1001:1001 --from=artifacts /initrd.gz /vm/initrd.gz

# Default paths — override via env vars if needed
ENV KERNEL_IMAGE_PATH=/vm/kernel
ENV INITRD_PATH=/vm/initrd.gz

COPY --from=build /app/engine engine
RUN chmod +x /engine && mkdir -p /tmp && chown 1001:1001 /tmp

# Use non-root user
COPY --from=build /etc/passwd /etc/passwd
USER altuser

EXPOSE 8000
CMD ["/engine"]
