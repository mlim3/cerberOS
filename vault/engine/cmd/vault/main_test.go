package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// mockEngine starts a fake /execute server.
// handler receives the decoded executeRequest and returns an executeResponse.
func mockEngine(t *testing.T, handler func(executeRequest) executeResponse) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/execute" || r.Method != http.MethodPost {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		var req executeRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		resp := handler(req)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// helper: run CLI and capture output
func runCLI(t *testing.T, args []string, stdinData string) (stdout, stderr string, exitCode int) {
	t.Helper()
	var outBuf, errBuf strings.Builder
	var stdinReader *strings.Reader
	if stdinData != "" {
		stdinReader = strings.NewReader(stdinData)
	}
	exitCode = run(args, stdinReader, &outBuf, &errBuf)
	return outBuf.String(), errBuf.String(), exitCode
}

// --- Script source tests ---

func TestInlineScript(t *testing.T) {
	srv := mockEngine(t, func(req executeRequest) executeResponse {
		if req.Script != "echo hello" {
			t.Errorf("unexpected script: %q", req.Script)
		}
		return executeResponse{Response: executeResult{Output: "hello\n", ExitCode: 0}}
	})

	stdout, _, code := runCLI(t, []string{"execute", "--host", srv.URL, "-s", "echo hello"}, "")
	if code != 0 {
		t.Fatalf("expected exit 0, got %d", code)
	}
	if stdout != "hello\n" {
		t.Errorf("unexpected output: %q", stdout)
	}
}

func TestFileScript(t *testing.T) {
	script := "#!/bin/sh\necho from file\n"
	f := filepath.Join(t.TempDir(), "test.sh")
	if err := os.WriteFile(f, []byte(script), 0644); err != nil {
		t.Fatal(err)
	}

	srv := mockEngine(t, func(req executeRequest) executeResponse {
		if req.Script != script {
			t.Errorf("unexpected script: %q", req.Script)
		}
		return executeResponse{Response: executeResult{Output: "from file\n", ExitCode: 0}}
	})

	stdout, _, code := runCLI(t, []string{"execute", "--host", srv.URL, "-f", f}, "")
	if code != 0 {
		t.Fatalf("expected exit 0, got %d", code)
	}
	if stdout != "from file\n" {
		t.Errorf("unexpected output: %q", stdout)
	}
}

func TestStdinScript(t *testing.T) {
	script := "echo from stdin\n"
	srv := mockEngine(t, func(req executeRequest) executeResponse {
		if req.Script != script {
			t.Errorf("unexpected script: %q", req.Script)
		}
		return executeResponse{Response: executeResult{Output: "from stdin\n", ExitCode: 0}}
	})

	stdout, _, code := runCLI(t, []string{"execute", "--host", srv.URL}, script)
	if code != 0 {
		t.Fatalf("expected exit 0, got %d", code)
	}
	if stdout != "from stdin\n" {
		t.Errorf("unexpected output: %q", stdout)
	}
}

func TestFileScriptEcho(t *testing.T) {
	// Verifies that -f reads a real script file and sends its contents to the engine,
	// and that the engine's output ("testing from test script\n") is printed to stdout.
	script := "echo testing from test script\n"
	f := filepath.Join(t.TempDir(), "echo_test.sh")
	if err := os.WriteFile(f, []byte(script), 0644); err != nil {
		t.Fatal(err)
	}

	srv := mockEngine(t, func(req executeRequest) executeResponse {
		if req.Script != script {
			t.Errorf("unexpected script: %q", req.Script)
		}
		return executeResponse{Response: executeResult{Output: "testing from test script\n", ExitCode: 0}}
	})

	stdout, _, code := runCLI(t, []string{"execute", "--host", srv.URL, "-f", f}, "")
	if code != 0 {
		t.Fatalf("expected exit 0, got %d", code)
	}
	if stdout != "testing from test script\n" {
		t.Errorf("unexpected output: %q", stdout)
	}
}

// -f takes priority over -s
func TestFileTakesPriorityOverScript(t *testing.T) {
	fileContent := "echo file wins\n"
	f := filepath.Join(t.TempDir(), "winner.sh")
	os.WriteFile(f, []byte(fileContent), 0644)

	srv := mockEngine(t, func(req executeRequest) executeResponse {
		if req.Script != fileContent {
			t.Errorf("expected file content, got: %q", req.Script)
		}
		return executeResponse{Response: executeResult{Output: "file wins\n", ExitCode: 0}}
	})

	runCLI(t, []string{"execute", "--host", srv.URL, "-f", f, "-s", "echo ignored"}, "")
}

// --- Env flag tests ---

func TestEnvFlags(t *testing.T) {
	srv := mockEngine(t, func(req executeRequest) executeResponse {
		if req.Env["FOO"] != "bar" || req.Env["BAZ"] != "qux" {
			t.Errorf("unexpected env: %v", req.Env)
		}
		return executeResponse{Response: executeResult{Output: "", ExitCode: 0}}
	})

	runCLI(t, []string{"execute", "--host", srv.URL, "-s", "true", "-e", "FOO=bar", "-e", "BAZ=qux"}, "")
}

func TestEnvFlagInvalidFormat(t *testing.T) {
	srv := mockEngine(t, func(req executeRequest) executeResponse {
		return executeResponse{}
	})

	_, stderr, code := runCLI(t, []string{"execute", "--host", srv.URL, "-s", "true", "-e", "NOEQUALS"}, "")
	if code == 0 {
		t.Fatal("expected non-zero exit")
	}
	if !strings.Contains(stderr, "KEY=VAL") {
		t.Errorf("expected KEY=VAL hint in stderr, got: %q", stderr)
	}
}

// --- Exit code passthrough ---

func TestExitCodePassthrough(t *testing.T) {
	for _, want := range []int{0, 1, 2, 42} {
		want := want
		t.Run("exit"+strings.TrimSpace(strings.Repeat(" ", 0)), func(t *testing.T) {
			srv := mockEngine(t, func(req executeRequest) executeResponse {
				return executeResponse{Response: executeResult{Output: "", ExitCode: want}}
			})
			_, _, got := runCLI(t, []string{"execute", "--host", srv.URL, "-s", "true"}, "")
			if got != want {
				t.Errorf("exit code: want %d got %d", want, got)
			}
		})
	}
}

// --- Error cases ---

func TestNoScriptProvided(t *testing.T) {
	// nil stdin, no -f or -s
	var outBuf, errBuf strings.Builder
	code := run([]string{"execute", "--host", "http://unused"}, nil, &outBuf, &errBuf)
	if code == 0 {
		t.Fatal("expected non-zero exit")
	}
	if !strings.Contains(errBuf.String(), "no script provided") {
		t.Errorf("expected hint in stderr, got: %q", errBuf.String())
	}
}

func TestMissingFile(t *testing.T) {
	_, stderr, code := runCLI(t, []string{"execute", "--host", "http://unused", "-f", "/no/such/file.sh"}, "")
	if code == 0 {
		t.Fatal("expected non-zero exit")
	}
	if !strings.Contains(stderr, "reading file") {
		t.Errorf("unexpected stderr: %q", stderr)
	}
}

func TestEngineHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "internal server error", http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)

	_, stderr, code := runCLI(t, []string{"execute", "--host", srv.URL, "-s", "true"}, "")
	if code == 0 {
		t.Fatal("expected non-zero exit")
	}
	if !strings.Contains(stderr, "500") {
		t.Errorf("expected 500 in stderr, got: %q", stderr)
	}
}

func TestEngineUnreachable(t *testing.T) {
	_, stderr, code := runCLI(t, []string{"execute", "--host", "http://127.0.0.1:19999", "-s", "true"}, "")
	if code == 0 {
		t.Fatal("expected non-zero exit")
	}
	if !strings.Contains(stderr, "connecting to engine") {
		t.Errorf("unexpected stderr: %q", stderr)
	}
}

func TestUnknownCommand(t *testing.T) {
	_, stderr, code := runCLI(t, []string{"notacommand"}, "")
	if code == 0 {
		t.Fatal("expected non-zero exit")
	}
	if !strings.Contains(stderr, "unknown command") {
		t.Errorf("unexpected stderr: %q", stderr)
	}
}

func TestHelpFlag(t *testing.T) {
	for _, flag := range []string{"-h", "--help"} {
		stdout, _, code := runCLI(t, []string{flag}, "")
		if code != 0 {
			t.Errorf("%s: expected exit 0", flag)
		}
		if !strings.Contains(stdout, "vault execute") {
			t.Errorf("%s: expected usage in stdout, got: %q", flag, stdout)
		}
	}
}

func TestNoArgs(t *testing.T) {
	stdout, _, code := runCLI(t, []string{}, "")
	if code != 0 {
		t.Errorf("expected exit 0 (shows help), got %d", code)
	}
	if !strings.Contains(stdout, "vault execute") {
		t.Errorf("expected usage output, got: %q", stdout)
	}
}
