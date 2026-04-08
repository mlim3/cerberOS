//go:build integration

package main

import (
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// composeFile is the path to the root docker-compose.yml relative to the package
// directory. Go sets the test working directory to the package being tested
// (engine/cmd/vault/), so four levels up reaches the repo root.
const composeFile = "../../../../docker-compose.yml"

// vaultURL is the vault service's base URL used by all integration tests.
const vaultURL = "http://localhost:8000"

// TestMain brings compose up before any test and tears it down after.
func TestMain(m *testing.M) {
	compose, err := filepath.Abs(composeFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "resolving compose path: %v\n", err)
		os.Exit(1)
	}

	up := exec.Command("docker", "compose", "-f", compose, "up", "--build", "--wait", "-d")
	up.Stdout = os.Stdout
	up.Stderr = os.Stderr
	fmt.Println("--- integration: docker compose up --build --wait")
	if err := up.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "docker compose up failed: %v\n", err)
		os.Exit(1)
	}

	// Wait until /inject responds (even with 400).
	if err := waitForVault(vaultURL, 60*time.Second); err != nil {
		fmt.Fprintf(os.Stderr, "vault did not become ready: %v\n", err)
		dockerComposeDown(compose)
		os.Exit(1)
	}

	code := m.Run()

	fmt.Println("--- integration: docker compose down")
	dockerComposeDown(compose)
	os.Exit(code)
}

func dockerComposeDown(compose string) {
	down := exec.Command("docker", "compose", "-f", compose, "down")
	down.Stdout = os.Stdout
	down.Stderr = os.Stderr
	_ = down.Run()
}

func waitForVault(url string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := http.Post(url+"/inject", "application/json", strings.NewReader("{}"))
		if err == nil {
			resp.Body.Close()
			return nil // server is up (any status code is fine)
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("vault at %s not reachable after %s", url, timeout)
}

// logResult prints stdout/stderr/exit_code via t.Log — only visible with -v.
func logResult(t *testing.T, stdout, stderr string, code int) {
	t.Helper()
	t.Logf("exit_code: %d", code)
	if stdout != "" {
		t.Logf("stdout:\n%s", stdout)
	}
	if stderr != "" {
		t.Logf("stderr:\n%s", stderr)
	}
}

// --- integration tests ---

func TestIntegration_InlineInject(t *testing.T) {
	stdout, stderr, code := runCLI(t,
		[]string{"inject", "--host", vaultURL, "-s", "#!/bin/sh\necho {{API_KEY}}"},
		"",
	)
	logResult(t, stdout, stderr, code)
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	// The injected script should contain the resolved secret value
	if !strings.Contains(stdout, "mock-api-key-12345") {
		t.Errorf("expected secret value in injected script, got: %q", stdout)
	}
	// Original placeholder should be replaced
	if strings.Contains(stdout, "{{API_KEY}}") {
		t.Errorf("placeholder was not replaced: %q", stdout)
	}
}

func TestIntegration_FileInject(t *testing.T) {
	script := "#!/bin/sh\necho {{DB_PASS}}\necho {{SECRET_KEY}}\n"
	f := filepath.Join(t.TempDir(), "run.sh")
	if err := os.WriteFile(f, []byte(script), 0644); err != nil {
		t.Fatal(err)
	}

	stdout, stderr, code := runCLI(t,
		[]string{"inject", "--host", vaultURL, "-f", f},
		"",
	)
	logResult(t, stdout, stderr, code)
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	if !strings.Contains(stdout, "mock-db-password") {
		t.Errorf("expected DB_PASS value in output, got: %q", stdout)
	}
	if !strings.Contains(stdout, "mock-secret-key") {
		t.Errorf("expected SECRET_KEY value in output, got: %q", stdout)
	}
}

func TestIntegration_StdinInject(t *testing.T) {
	stdout, stderr, code := runCLI(t,
		[]string{"inject", "--host", vaultURL},
		"#!/bin/sh\necho {{TEST_SECRET}}\n",
	)
	logResult(t, stdout, stderr, code)
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	if !strings.Contains(stdout, "hello-from-secretstore") {
		t.Errorf("expected TEST_SECRET value in output, got: %q", stdout)
	}
}

func TestIntegration_NoPlaceholders(t *testing.T) {
	// A script with no placeholders should be returned unchanged.
	script := "#!/bin/sh\necho no secrets here\n"
	stdout, stderr, code := runCLI(t,
		[]string{"inject", "--host", vaultURL, "-s", script},
		"",
	)
	logResult(t, stdout, stderr, code)
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	if stdout != script {
		t.Errorf("expected script unchanged, got: %q", stdout)
	}
}

func TestIntegration_UnknownSecretFails(t *testing.T) {
	// Requesting a secret that doesn't exist should fail the entire injection.
	_, stderr, code := runCLI(t,
		[]string{"inject", "--host", vaultURL, "-s", "echo {{NONEXISTENT_SECRET}}"},
		"",
	)
	if code == 0 {
		t.Fatal("expected non-zero exit for unknown secret")
	}
	if !strings.Contains(stderr, "secret not found") {
		t.Errorf("expected denial message, got: %q", stderr)
	}
}

func TestIntegration_AtomicFailure(t *testing.T) {
	// If one secret exists and another doesn't, the entire request must fail.
	// No partial injection — API_KEY should NOT appear in any output.
	_, stderr, code := runCLI(t,
		[]string{"inject", "--host", vaultURL, "-s", "echo {{API_KEY}} {{DOES_NOT_EXIST}}"},
		"",
	)
	if code == 0 {
		t.Fatal("expected non-zero exit for partial secret access")
	}
	if !strings.Contains(stderr, "secret not found") {
		t.Errorf("expected denial message, got: %q", stderr)
	}
}

func TestIntegration_OutputToFile(t *testing.T) {
	outFile := filepath.Join(t.TempDir(), "injected.sh")
	_, stderr, code := runCLI(t,
		[]string{"inject", "--host", vaultURL, "-s", "#!/bin/sh\necho {{API_KEY}}", "-o", outFile},
		"",
	)
	logResult(t, "", stderr, code)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}

	content, err := os.ReadFile(outFile)
	if err != nil {
		t.Fatalf("reading output file: %v", err)
	}
	if !strings.Contains(string(content), "mock-api-key-12345") {
		t.Errorf("expected secret in output file, got: %q", string(content))
	}
}
