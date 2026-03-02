package vm

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

// VMConfig holds configuration for launching a QEMU microVM.
type VMConfig struct {
	// Path to the Linux kernel image (e.g. vmlinuz or bzImage)
	KernelImagePath string
	// Path to the gzipped cpio initramfs
	InitrdPath string
	// Kernel boot arguments
	KernelArgs string
	// Number of vCPUs
	VCPUs int64
	// Memory in MiB
	MemoryMiB int64
	// QEMU accelerator override (kvm, hvf, tcg). Auto-detected if empty.
	Accel string
}

// DefaultVMConfig returns a VMConfig with sensible defaults for the current platform.
func DefaultVMConfig() VMConfig {
	// aarch64 QEMU virt machine: PL011 UART → ttyAMA0.
	// x86 microvm: 16550 UART → ttyS0.
	// Using initramfs: no root= or rw needed — kernel unpacks the cpio into memory.
	var kernelArgs string
	if runtime.GOARCH == "arm64" {
		kernelArgs = "console=ttyAMA0 reboot=k panic=1 init=/init"
	} else {
		kernelArgs = "console=ttyS0 pci=off reboot=k panic=1 init=/init"
	}
	return VMConfig{
		KernelArgs: kernelArgs,
		VCPUs:      1,
		MemoryMiB:  128,
	}
}

// VM manages the lifecycle of a single QEMU microVM.
type VM struct {
	cfg     VMConfig
	cmd     *exec.Cmd
	stopped chan struct{}
}

// NewVM creates a new VM instance from the given config.
func NewVM(cfg VMConfig) *VM {
	return &VM{cfg: cfg, stopped: make(chan struct{})}
}

// Start launches the QEMU process and boots the VM.
func (v *VM) Start(ctx context.Context) error {
	if v.cfg.KernelImagePath == "" {
		return fmt.Errorf("KERNEL_IMAGE_PATH is not set")
	}
	if v.cfg.InitrdPath == "" {
		return fmt.Errorf("INITRD_PATH is not set")
	}

	bin := v.binary()
	args := v.buildArgs()
	log.Printf("launching: %s %s", bin, strings.Join(args, " "))

	v.cmd = exec.CommandContext(ctx, bin, args...)
	v.cmd.Stdout = os.Stdout
	v.cmd.Stderr = os.Stderr

	if err := v.cmd.Start(); err != nil {
		return err
	}

	go func() {
		err := v.cmd.Wait()
		select {
		case <-v.stopped:
			// intentional kill — ignore
		default:
			if err != nil {
				log.Printf("vm exited unexpectedly: %v", err)
			} else {
				log.Printf("vm exited cleanly")
			}
		}
	}()

	return nil
}

// Stop kills the QEMU process.
func (v *VM) Stop() error {
	close(v.stopped)
	if v.cmd != nil && v.cmd.Process != nil {
		_ = v.cmd.Process.Kill()
	}
	return nil
}

// binary returns the QEMU binary name for the current architecture.
func (v *VM) binary() string {
	if runtime.GOARCH == "arm64" {
		return "qemu-system-aarch64"
	}
	return "qemu-system-x86_64"
}

// machineType returns the QEMU machine type for the current architecture.
func (v *VM) machineType() string {
	if runtime.GOARCH == "arm64" {
		return "virt"
	}
	return "microvm"
}

// accel returns the accelerator to use, in priority order:
// 1. Explicit override from config
// 2. kvm if /dev/kvm is present (Linux with KVM)
// 3. tcg fallback (software emulation)
func (v *VM) accel() string {
	if v.cfg.Accel != "" {
		return v.cfg.Accel
	}
	if _, err := os.Stat("/dev/kvm"); err == nil {
		return "kvm"
	}
	return "tcg"
}

// cpu returns the CPU model. "host" requires kvm/hvf; tcg needs a concrete model.
func (v *VM) cpu() string {
	if v.accel() == "tcg" {
		return "max"
	}
	return "host"
}

// buildArgs constructs the full QEMU command-line argument list.
func (v *VM) buildArgs() []string {
	return []string{
		"-M", v.machineType(),
		"-accel", v.accel(),
		"-cpu", v.cpu(),
		"-smp", fmt.Sprintf("%d", v.cfg.VCPUs),
		"-m", fmt.Sprintf("%d", v.cfg.MemoryMiB),
		"-kernel", v.cfg.KernelImagePath,
		"-initrd", v.cfg.InitrdPath,
		"-append", v.cfg.KernelArgs,
		"-nographic",
	}
}
