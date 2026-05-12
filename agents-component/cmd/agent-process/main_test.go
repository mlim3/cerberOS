package main

import (
	"log/slog"
	"os"
	"testing"
)

// TestMain sets the global slog level to WARN before running any tests so that
// INFO-level diagnostics from production code do not pollute test output.
// Only WARN and ERROR lines appear, keeping failures fully pasteable.
func TestMain(m *testing.M) {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelError,
	})))
	os.Exit(m.Run())
}
