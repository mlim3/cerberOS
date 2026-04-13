package tests

import (
	"os/exec"
	"testing"
)

func TestCLIVaultList(t *testing.T) {
	ctx, cancel := cliTestContext()
	defer cancel()

	cmd := exec.CommandContext(ctx, cliPath, "-db", "env", "vault", "list", "--user", "11111111-1111-1111-1111-111111111111")
	cmd.Env = getBaseEnv()

	_, err := cmd.Output()
	if err == nil {
		t.Fatalf("Expected CLI command to fail for vault in DB mode, but it succeeded")
	}
}
