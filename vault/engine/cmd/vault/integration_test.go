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

// composeFile is the path to vault/compose.yaml relative to the package directory.
// Go sets the test working directory to the package being tested (engine/cmd/vault/),
// so three levels up reaches the vault root.
const composeFile = "../../../compose.yaml"

// engineURL is the engine's base URL used by all integration tests.
const engineURL = "http://localhost:8000"

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

	// Extra readiness poll — wait until /execute responds (even with 400).
	if err := waitForEngine(engineURL, 60*time.Second); err != nil {
		fmt.Fprintf(os.Stderr, "engine did not become ready: %v\n", err)
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

func waitForEngine(url string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := http.Post(url+"/execute", "application/json", strings.NewReader("{}"))
		if err == nil {
			resp.Body.Close()
			return nil // server is up (any status code is fine)
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("engine at %s not reachable after %s", url, timeout)
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

func TestIntegration_InlineEcho(t *testing.T) {
	stdout, stderr, code := runCLI(t,
		[]string{"execute", "--host", engineURL, "-s", "#!/bin/sh\necho hello from vm"},
		"",
	)
	logResult(t, stdout, stderr, code)
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	if !strings.Contains(stdout, "hello from vm") {
		t.Errorf("expected 'hello from vm' in output, got: %q", stdout)
	}
}

func TestIntegration_FileScript(t *testing.T) {
	script := "#!/bin/sh\necho file executed\necho line two\n"
	f := filepath.Join(t.TempDir(), "run.sh")
	if err := os.WriteFile(f, []byte(script), 0644); err != nil {
		t.Fatal(err)
	}

	stdout, stderr, code := runCLI(t,
		[]string{"execute", "--host", engineURL, "-f", f},
		"",
	)
	logResult(t, stdout, stderr, code)
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	if !strings.Contains(stdout, "file executed") || !strings.Contains(stdout, "line two") {
		t.Errorf("unexpected output: %q", stdout)
	}
}

func TestIntegration_StdinScript(t *testing.T) {
	stdout, stderr, code := runCLI(t,
		[]string{"execute", "--host", engineURL},
		"#!/bin/sh\necho from stdin\n",
	)
	logResult(t, stdout, stderr, code)
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	if !strings.Contains(stdout, "from stdin") {
		t.Errorf("unexpected output: %q", stdout)
	}
}

func TestIntegration_ExitCodePassthrough(t *testing.T) {
	stdout, stderr, code := runCLI(t,
		[]string{"execute", "--host", engineURL, "-s", "#!/bin/sh\nexit 42"},
		"",
	)
	logResult(t, stdout, stderr, code)
	if code != 42 {
		t.Errorf("expected exit 42, got %d", code)
	}
}

func TestIntegration_SecretPlaceholder(t *testing.T) {
	// API_KEY is a mock secret defined in the engine's MockStore.
	// The output should show [REDACTED], not the raw secret value.
	stdout, stderr, code := runCLI(t,
		[]string{"execute", "--host", engineURL, "-s", "#!/bin/sh\necho {{API_KEY}}"},
		"",
	)
	logResult(t, stdout, stderr, code)
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	if strings.Contains(stdout, "mock-api-key-12345") {
		t.Errorf("raw secret leaked into output: %q", stdout)
	}
	if !strings.Contains(stdout, "[REDACTED]") {
		t.Errorf("expected [REDACTED] in output, got: %q", stdout)
	}
}

func TestIntegration_MultilineScript(t *testing.T) {
	script := "#!/bin/sh\nA=hello\nB=world\necho $A $B\n"
	stdout, stderr, code := runCLI(t,
		[]string{"execute", "--host", engineURL, "-s", script},
		"",
	)
	logResult(t, stdout, stderr, code)
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	if !strings.Contains(stdout, "hello world") {
		t.Errorf("unexpected output: %q", stdout)
	}
}

func TestIntegration_IsolationBetweenRuns(t *testing.T) {
	// Write a file in run 1, confirm it is absent in run 2.
	stdout1, stderr1, code1 := runCLI(t,
		[]string{"execute", "--host", engineURL, "-s", "#!/bin/sh\necho canary > /tmp/canary.txt"},
		"",
	)
	t.Log("--- run 1")
	logResult(t, stdout1, stderr1, code1)
	if code1 != 0 {
		t.Fatalf("run 1 failed with exit %d", code1)
	}

	stdout2, stderr2, code2 := runCLI(t,
		[]string{"execute", "--host", engineURL, "-s", "#!/bin/sh\ncat /tmp/canary.txt 2>/dev/null || echo absent"},
		"",
	)
	t.Log("--- run 2")
	logResult(t, stdout2, stderr2, code2)
	if code2 != 0 {
		t.Fatalf("run 2 failed with exit %d", code2)
	}
	if !strings.Contains(stdout2, "absent") {
		t.Errorf("state leaked between VM runs; output: %q", stdout2)
	}
}
