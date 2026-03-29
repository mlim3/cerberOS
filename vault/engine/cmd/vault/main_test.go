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

// mockVault starts a fake /inject server.
// handler receives the decoded injectRequest and returns status + response body.
func mockVault(t *testing.T, handler func(injectRequest) (int, any)) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/inject" || r.Method != http.MethodPost {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		var req injectRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		status, resp := handler(req)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
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
	srv := mockVault(t, func(req injectRequest) (int, any) {
		if req.Script != "echo {{API_KEY}}" {
			t.Errorf("unexpected script: %q", req.Script)
		}
		return http.StatusOK, injectResponse{Script: "echo mock-api-key-12345"}
	})

	stdout, _, code := runCLI(t, []string{"inject", "--host", srv.URL, "-s", "echo {{API_KEY}}"}, "")
	if code != 0 {
		t.Fatalf("expected exit 0, got %d", code)
	}
	if stdout != "echo mock-api-key-12345" {
		t.Errorf("unexpected output: %q", stdout)
	}
}

func TestFileScript(t *testing.T) {
	script := "#!/bin/sh\necho {{DB_PASS}}\n"
	f := filepath.Join(t.TempDir(), "test.sh")
	if err := os.WriteFile(f, []byte(script), 0644); err != nil {
		t.Fatal(err)
	}

	srv := mockVault(t, func(req injectRequest) (int, any) {
		if req.Script != script {
			t.Errorf("unexpected script: %q", req.Script)
		}
		return http.StatusOK, injectResponse{Script: "#!/bin/sh\necho mock-db-password\n"}
	})

	stdout, _, code := runCLI(t, []string{"inject", "--host", srv.URL, "-f", f}, "")
	if code != 0 {
		t.Fatalf("expected exit 0, got %d", code)
	}
	if !strings.Contains(stdout, "mock-db-password") {
		t.Errorf("unexpected output: %q", stdout)
	}
}

func TestStdinScript(t *testing.T) {
	script := "echo {{SECRET_KEY}}\n"
	srv := mockVault(t, func(req injectRequest) (int, any) {
		if req.Script != script {
			t.Errorf("unexpected script: %q", req.Script)
		}
		return http.StatusOK, injectResponse{Script: "echo mock-secret-key\n"}
	})

	stdout, _, code := runCLI(t, []string{"inject", "--host", srv.URL}, script)
	if code != 0 {
		t.Fatalf("expected exit 0, got %d", code)
	}
	if !strings.Contains(stdout, "mock-secret-key") {
		t.Errorf("unexpected output: %q", stdout)
	}
}

// -f takes priority over -s
func TestFileTakesPriorityOverScript(t *testing.T) {
	fileContent := "echo file wins\n"
	f := filepath.Join(t.TempDir(), "winner.sh")
	os.WriteFile(f, []byte(fileContent), 0644)

	srv := mockVault(t, func(req injectRequest) (int, any) {
		if req.Script != fileContent {
			t.Errorf("expected file content, got: %q", req.Script)
		}
		return http.StatusOK, injectResponse{Script: fileContent}
	})

	runCLI(t, []string{"inject", "--host", srv.URL, "-f", f, "-s", "echo ignored"}, "")
}

func TestOutputToFile(t *testing.T) {
	srv := mockVault(t, func(req injectRequest) (int, any) {
		return http.StatusOK, injectResponse{Script: "#!/bin/sh\necho injected\n"}
	})

	outFile := filepath.Join(t.TempDir(), "out.sh")
	_, _, code := runCLI(t, []string{"inject", "--host", srv.URL, "-s", "echo test", "-o", outFile}, "")
	if code != 0 {
		t.Fatalf("expected exit 0, got %d", code)
	}

	content, err := os.ReadFile(outFile)
	if err != nil {
		t.Fatalf("reading output file: %v", err)
	}
	if !strings.Contains(string(content), "echo injected") {
		t.Errorf("unexpected file content: %q", string(content))
	}
}

// --- Authorization failure ---

func TestAuthorizationFailure(t *testing.T) {
	srv := mockVault(t, func(req injectRequest) (int, any) {
		return http.StatusForbidden, errorResponse{Error: "secret not found: NONEXISTENT"}
	})

	_, stderr, code := runCLI(t, []string{"inject", "--host", srv.URL, "-s", "echo {{NONEXISTENT}}"}, "")
	if code == 0 {
		t.Fatal("expected non-zero exit for denied secret")
	}
	if !strings.Contains(stderr, "secret not found") {
		t.Errorf("expected denial message in stderr, got: %q", stderr)
	}
}

// --- Error cases ---

func TestNoScriptProvided(t *testing.T) {
	var outBuf, errBuf strings.Builder
	code := run([]string{"inject", "--host", "http://unused"}, nil, &outBuf, &errBuf)
	if code == 0 {
		t.Fatal("expected non-zero exit")
	}
	if !strings.Contains(errBuf.String(), "no script provided") {
		t.Errorf("expected hint in stderr, got: %q", errBuf.String())
	}
}

func TestMissingFile(t *testing.T) {
	_, stderr, code := runCLI(t, []string{"inject", "--host", "http://unused", "-f", "/no/such/file.sh"}, "")
	if code == 0 {
		t.Fatal("expected non-zero exit")
	}
	if !strings.Contains(stderr, "reading file") {
		t.Errorf("unexpected stderr: %q", stderr)
	}
}

func TestVaultHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "internal server error", http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)

	_, stderr, code := runCLI(t, []string{"inject", "--host", srv.URL, "-s", "true"}, "")
	if code == 0 {
		t.Fatal("expected non-zero exit")
	}
	if !strings.Contains(stderr, "500") {
		t.Errorf("expected 500 in stderr, got: %q", stderr)
	}
}

func TestVaultUnreachable(t *testing.T) {
	_, stderr, code := runCLI(t, []string{"inject", "--host", "http://127.0.0.1:19999", "-s", "true"}, "")
	if code == 0 {
		t.Fatal("expected non-zero exit")
	}
	if !strings.Contains(stderr, "connecting to vault") {
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
		if !strings.Contains(stdout, "vault inject") {
			t.Errorf("%s: expected usage in stdout, got: %q", flag, stdout)
		}
	}
}

func TestNoArgs(t *testing.T) {
	stdout, _, code := runCLI(t, []string{}, "")
	if code != 0 {
		t.Errorf("expected exit 0 (shows help), got %d", code)
	}
	if !strings.Contains(stdout, "vault inject") {
		t.Errorf("expected usage output, got: %q", stdout)
	}
}
