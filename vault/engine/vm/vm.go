package vm

import "context"

// Config holds hypervisor-agnostic VM configuration.
type Config struct {
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
}

// RunResult holds the output from a completed VM execution.
type RunResult struct {
	Output   string // combined stdout/stderr from the VM console
	ExitCode int    // exit code from the job script (-1 if unknown)
}

// VM is the interface that all hypervisor backends must implement.
type VM interface {
	Start(ctx context.Context) error
	Stop() error
	// Run starts the VM, waits for it to exit, and returns captured output.
	Run(ctx context.Context) (*RunResult, error)
}
