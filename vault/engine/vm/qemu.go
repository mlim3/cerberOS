package vm

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"strings"
)

// QEMUConfig holds QEMU-specific options on top of the base Config.
type QEMUConfig struct {
	Config
	// QEMU accelerator override (kvm, hvf, tcg). Auto-detected if empty.
	Accel string
}

// DefaultQEMUConfig returns a QEMUConfig with sensible defaults for the current platform.
func DefaultQEMUConfig() QEMUConfig {
	// aarch64 QEMU virt machine: PL011 UART → ttyAMA0.
	// x86 microvm: 16550 UART → ttyS0.
	// Using initramfs: no root= or rw needed — kernel unpacks the cpio into memory.
	var kernelArgs string
	if runtime.GOARCH == "arm64" {
		kernelArgs = "console=ttyAMA0 reboot=k panic=1 init=/init"
	} else {
		kernelArgs = "console=ttyS0 pci=off reboot=k panic=1 init=/init"
	}
	return QEMUConfig{
		Config: Config{
			KernelArgs: kernelArgs,
			VCPUs:      1,
			MemoryMiB:  128,
		},
	}
}

// QEMU implements the VM interface using QEMU.
type QEMU struct {
	cfg     QEMUConfig
	cmd     *exec.Cmd
	stopped chan struct{}
}

// NewQEMU creates a new QEMU-backed VM instance from the given config.
func NewQEMU(cfg QEMUConfig) *QEMU {
	return &QEMU{cfg: cfg, stopped: make(chan struct{})}
}

// Start launches the QEMU process and boots the VM.
func (q *QEMU) Start(ctx context.Context) error {
	q.stopped = make(chan struct{})
	if q.cfg.KernelImagePath == "" {
		return fmt.Errorf("KERNEL_IMAGE_PATH is not set")
	}
	if q.cfg.InitrdPath == "" {
		return fmt.Errorf("INITRD_PATH is not set")
	}

	bin := q.binary()
	args := q.buildArgs()
	log.Printf("launching: %s %s", bin, strings.Join(args, " "))

	q.cmd = exec.CommandContext(ctx, bin, args...)
	q.cmd.Stdout = os.Stdout
	q.cmd.Stderr = os.Stderr

	if err := q.cmd.Start(); err != nil {
		return err
	}

	go func() {
		err := q.cmd.Wait()
		select {
		case <-q.stopped:
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
func (q *QEMU) Stop() error {
	close(q.stopped)
	if q.cmd != nil && q.cmd.Process != nil {
		_ = q.cmd.Process.Kill()
	}
	return nil
}

// binary returns the QEMU binary name for the current architecture.
func (q *QEMU) binary() string {
	if runtime.GOARCH == "arm64" {
		return "qemu-system-aarch64"
	}
	return "qemu-system-x86_64"
}

// machineType returns the QEMU machine type for the current architecture.
func (q *QEMU) machineType() string {
	if runtime.GOARCH == "arm64" {
		return "virt"
	}
	return "microvm"
}

// accel returns the accelerator to use, in priority order:
// 1. Explicit override from config
// 2. kvm if /dev/kvm is present (Linux with KVM)
// 3. tcg fallback (software emulation)
func (q *QEMU) accel() string {
	if q.cfg.Accel != "" {
		return q.cfg.Accel
	}
	if _, err := os.Stat("/dev/kvm"); err == nil {
		return "kvm"
	}
	return "tcg"
}

// cpu returns the CPU model. "host" requires kvm/hvf; tcg needs a concrete model.
func (q *QEMU) cpu() string {
	if q.accel() == "tcg" {
		return "max"
	}
	return "host"
}

// Run starts the VM, waits for it to exit, and returns captured output.
// The guest init script is expected to power off via sysrq-trigger when done,
// which causes QEMU to exit and cmd.Wait() to return.
func (q *QEMU) Run(ctx context.Context) (*RunResult, error) {
	if q.cfg.KernelImagePath == "" {
		return nil, fmt.Errorf("KERNEL_IMAGE_PATH is not set")
	}
	if q.cfg.InitrdPath == "" {
		return nil, fmt.Errorf("INITRD_PATH is not set")
	}

	bin := q.binary()
	args := q.buildArgs()
	log.Printf("launching (run): %s %s", bin, strings.Join(args, " "))

	q.cmd = exec.CommandContext(ctx, bin, args...)

	var buf bytes.Buffer
	q.cmd.Stdout = &buf
	q.cmd.Stderr = &buf

	if err := q.cmd.Start(); err != nil {
		return nil, err
	}

	// Wait for QEMU to exit (triggered by guest sysrq poweroff).
	// Non-zero exit is expected when guest powers off — not an error.
	_ = q.cmd.Wait()

	output := buf.String()
	return &RunResult{
		Output:   output,
		ExitCode: parseExitCode(output),
	}, nil
}

var exitCodeRe = regexp.MustCompile(`=== cerberOS job exit_code=(\d+) ===`)

func parseExitCode(output string) int {
	m := exitCodeRe.FindStringSubmatch(output)
	if len(m) < 2 {
		return -1
	}
	code, _ := strconv.Atoi(m[1])
	return code
}

// buildArgs constructs the full QEMU command-line argument list.
func (q *QEMU) buildArgs() []string {
	return []string{
		"-M", q.machineType(),
		"-accel", q.accel(),
		"-cpu", q.cpu(),
		"-smp", fmt.Sprintf("%d", q.cfg.VCPUs),
		"-m", fmt.Sprintf("%d", q.cfg.MemoryMiB),
		"-kernel", q.cfg.KernelImagePath,
		"-initrd", q.cfg.InitrdPath,
		"-append", q.cfg.KernelArgs,
		"-nographic",
	}
}
