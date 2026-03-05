package orchestrator

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/mlim3/cerberOS/vault/engine/initrd"
	"github.com/mlim3/cerberOS/vault/engine/preprocessor"
	"github.com/mlim3/cerberOS/vault/engine/vm"
)

// Request represents an incoming script execution request.
type Request struct {
	Script []byte            // raw script with {{PLACEHOLDER}} markers
	Env    map[string]string // additional env vars (reserved for future use)
}

// Response holds the execution result returned to the caller.
type Response struct {
	Output   string `json:"output"`
	ExitCode int    `json:"exit_code"`
}

// Orchestrator coordinates the full execution pipeline:
// preprocess → build initrd → boot VM → capture output → scrub secrets → return.
type Orchestrator struct {
	pp      *preprocessor.Preprocessor
	builder *initrd.Builder
	baseCfg vm.QEMUConfig
}

func New(pp *preprocessor.Preprocessor, builder *initrd.Builder, baseCfg vm.QEMUConfig) *Orchestrator {
	return &Orchestrator{
		pp:      pp,
		builder: builder,
		baseCfg: baseCfg,
	}
}

// Execute runs the full pipeline for a single script execution.
// Each call is stateless and safe for concurrent use.
func (o *Orchestrator) Execute(ctx context.Context, req Request) (*Response, error) {
	// 1. Preprocess: inject secrets into placeholders
	result, err := o.pp.Process(req.Script)
	if err != nil {
		return nil, fmt.Errorf("preprocess: %w", err)
	}

	// 2. Build a per-job initrd with the processed script embedded
	initrdPath, err := o.builder.Build(result.Script)
	if err != nil {
		return nil, fmt.Errorf("initrd build: %w", err)
	}
	defer os.Remove(initrdPath)

	// 3. Create an ephemeral VM with the custom initrd
	cfg := o.baseCfg
	cfg.InitrdPath = initrdPath
	machine := vm.NewQEMU(cfg)

	// 4. Boot, execute, and capture output
	runResult, err := machine.Run(ctx)
	if err != nil {
		return nil, fmt.Errorf("vm run: %w", err)
	}

	// 5. Extract job output from between sentinel markers
	output := extractJobOutput(runResult.Output)

	// 6. Scrub injected secret values from the output
	output = scrubSecrets(output, result.InjectedSecrets)

	return &Response{
		Output:   output,
		ExitCode: runResult.ExitCode,
	}, nil
}

// extractJobOutput pulls text between the cerberOS sentinel markers.
// If markers aren't found, returns the full output (useful for debugging).
func extractJobOutput(raw string) string {
	const startMarker = "=== cerberOS job start ==="
	const endPrefix = "=== cerberOS job exit_code="

	startIdx := strings.Index(raw, startMarker)
	if startIdx < 0 {
		return raw
	}
	startIdx += len(startMarker)
	// Skip the newline after the start marker
	if startIdx < len(raw) && raw[startIdx] == '\n' {
		startIdx++
	}

	endIdx := strings.Index(raw[startIdx:], endPrefix)
	if endIdx < 0 {
		return raw[startIdx:]
	}

	output := raw[startIdx : startIdx+endIdx]
	// Trim trailing newline before the end marker
	return strings.TrimRight(output, "\n")
}

// scrubSecrets replaces any occurrence of injected secret values with [REDACTED].
func scrubSecrets(output string, secrets []string) string {
	for _, secret := range secrets {
		if secret != "" {
			output = strings.ReplaceAll(output, secret, "[REDACTED]")
		}
	}
	return output
}
