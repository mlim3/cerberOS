package tests

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

type apiEnvelope struct {
	OK    bool            `json:"ok"`
	Data  json.RawMessage `json:"data"`
	Error *apiError       `json:"error"`
}

type apiError struct {
	Code    string          `json:"code"`
	Message string          `json:"message"`
	Details json.RawMessage `json:"details"`
}

func apiJSONRequest(t *testing.T, method, url string, body any, headers map[string]string) (int, apiEnvelope) {
	t.Helper()

	var reqBody *bytes.Reader
	if body == nil {
		reqBody = bytes.NewReader(nil)
	} else {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal request body: %v", err)
		}
		reqBody = bytes.NewReader(raw)
	}

	req, err := http.NewRequestWithContext(testContext(t), method, url, reqBody)
	if err != nil {
		t.Fatalf("create request: %v", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("perform request %s %s: %v", method, url, err)
	}
	defer resp.Body.Close()

	rawBody, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response body from %s %s: %v", method, url, err)
	}

	var env apiEnvelope
	if err := json.Unmarshal(rawBody, &env); err != nil {
		t.Fatalf("decode response envelope from %s %s: %v\nraw body:\n%s", method, url, err, string(rawBody))
	}

	return resp.StatusCode, env
}

func runCLI(t *testing.T, cliPath string, args ...string) (string, string, error) {
	t.Helper()

	cmd := exec.CommandContext(testContext(t), cliPath, args...)
	cmd.Env = append(os.Environ(), getBaseEnv()...)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	return stdout.String(), stderr.String(), err
}

func blackboxCLIPath(t *testing.T) string {
	t.Helper()

	path := envOrDefault("MEMORY_CLI_PATH", "../memory-cli")
	if !filepath.IsAbs(path) {
		path = filepath.Clean(path)
	}

	if _, err := os.Stat(path); err != nil {
		t.Skipf("CLI binary not available at %q; set MEMORY_CLI_PATH to run CLI contract tests", path)
	}
	return path
}

func validUserFixture(t *testing.T) string {
	t.Helper()
	userID := envOrDefault("MEMORY_TEST_VALID_USER_ID", "11111111-1111-1111-1111-111111111111")
	seedUser(t, userID)
	return userID
}

func generateSeededUserFixture(t *testing.T) string {
	t.Helper()
	userID := uuid.NewString()
	seedUser(t, userID)
	return userID
}

func requiredEnv(t *testing.T, key string) string {
	t.Helper()
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		t.Skipf("%s is required for this black-box contract test", key)
	}
	return value
}

func envOrDefault(key, fallback string) string {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	return value
}

func blackboxBaseURL() string {
	if strings.TrimSpace(os.Getenv("MEMORY_API_BASE_URL")) != "" {
		return os.Getenv("MEMORY_API_BASE_URL")
	}
	if testServer != nil {
		return testServer.URL
	}
	return "http://127.0.0.1:8080"
}

func assertSuccessEnvelope(t *testing.T, env apiEnvelope) {
	t.Helper()
	if !env.OK {
		t.Fatalf("ok = false, want true; error = %#v", env.Error)
	}
	if env.Error != nil {
		t.Fatalf("error = %#v, want nil", env.Error)
	}
	if len(env.Data) == 0 || string(env.Data) == "null" {
		t.Fatalf("data = %s, want non-null success payload", string(env.Data))
	}
}

func assertErrorEnvelope(t *testing.T, env apiEnvelope, context string) {
	t.Helper()
	if env.OK {
		t.Fatalf("%s: ok = true, want false", context)
	}
	if string(env.Data) != "null" {
		t.Fatalf("%s: data = %s, want null", context, string(env.Data))
	}
	if env.Error == nil {
		t.Fatalf("%s: error = nil, want populated error envelope", context)
	}
	if strings.TrimSpace(env.Error.Code) == "" {
		t.Fatalf("%s: error.code was empty", context)
	}
	if strings.TrimSpace(env.Error.Message) == "" {
		t.Fatalf("%s: error.message was empty", context)
	}
}

func assertErrorCode(t *testing.T, env apiEnvelope, want string) {
	t.Helper()
	assertErrorEnvelope(t, env, "error response")
	if env.Error.Code != want {
		t.Fatalf("error.code = %q, want %q", env.Error.Code, want)
	}
}

func assertJSONContainsStringField(t *testing.T, raw json.RawMessage, field, want string) {
	t.Helper()

	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil {
		t.Fatalf("unmarshal success data: %v", err)
	}

	got, _ := obj[field].(string)
	if got != want {
		t.Fatalf("%s = %q, want %q; data = %s", field, got, want, string(raw))
	}
}

func assertJSONHasNonEmptyStringField(t *testing.T, raw json.RawMessage, field string) {
	t.Helper()

	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil {
		t.Fatalf("unmarshal success data: %v", err)
	}

	got, _ := obj[field].(string)
	if strings.TrimSpace(got) == "" {
		t.Fatalf("%s was empty; data = %s", field, string(raw))
	}
}

func assertJSONContainsBoolField(t *testing.T, raw json.RawMessage, field string, want bool) {
	t.Helper()

	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil {
		t.Fatalf("unmarshal success data: %v", err)
	}

	got, _ := obj[field].(bool)
	if got != want {
		t.Fatalf("%s = %v, want %v; data = %s", field, got, want, string(raw))
	}
}

func assertJSONArrayOutput(t *testing.T, stdout string) {
	t.Helper()

	var items []map[string]any
	if err := json.Unmarshal([]byte(stdout), &items); err != nil {
		t.Fatalf("stdout was not a JSON array: %v\nstdout:\n%s", err, stdout)
	}
	if items == nil {
		t.Fatalf("stdout decoded to nil slice; want [] for empty results\nstdout:\n%s", stdout)
	}
}

func assertAndExtractNonEmptyStringField(t *testing.T, raw json.RawMessage, field string) string {
	t.Helper()

	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil {
		t.Fatalf("unmarshal success data: %v", err)
	}

	got, _ := obj[field].(string)
	if strings.TrimSpace(got) == "" {
		t.Fatalf("%s was empty; data = %s", field, string(raw))
	}
	return got
}

func assertAndExtractFirstFactID(t *testing.T, raw json.RawMessage) string {
	t.Helper()

	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("unmarshal facts payload: %v", err)
	}

	facts, ok := payload["facts"].([]any)
	if !ok {
		t.Fatalf("facts missing or not an array; data = %s", string(raw))
	}
	if len(facts) == 0 {
		t.Fatalf("facts array was empty; data = %s", string(raw))
	}

	firstFact, ok := facts[0].(map[string]any)
	if !ok {
		t.Fatalf("first fact was not an object: %#v", facts[0])
	}

	factID := asString(firstFact["factId"])
	if strings.TrimSpace(factID) == "" {
		t.Fatalf("first factId was empty: %#v", firstFact)
	}
	return factID
}

func assertJSONHasArrayField(t *testing.T, raw json.RawMessage, field string) {
	t.Helper()

	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil {
		t.Fatalf("unmarshal success data: %v", err)
	}

	arr, ok := obj[field].([]any)
	if !ok {
		t.Fatalf("%s missing or not an array; data = %s", field, string(raw))
	}
	if arr == nil {
		t.Fatalf("%s decoded to nil array; data = %s", field, string(raw))
	}
}

func assertFactsArrayDoesNotContainFactID(t *testing.T, raw json.RawMessage, bannedFactID string) {
	t.Helper()

	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("unmarshal facts payload: %v", err)
	}

	facts, ok := payload["facts"].([]any)
	if !ok {
		t.Fatalf("facts missing or not an array; data = %s", string(raw))
	}
	for _, factAny := range facts {
		fact, ok := factAny.(map[string]any)
		if ok && asString(fact["factId"]) == bannedFactID {
			t.Fatalf("unexpected factId %q present in %s", bannedFactID, string(raw))
		}
	}
}

func assertFactsArrayContainsFactID(t *testing.T, raw json.RawMessage, wantFactID string) {
	t.Helper()

	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("unmarshal facts payload: %v", err)
	}

	facts, ok := payload["facts"].([]any)
	if !ok {
		t.Fatalf("facts missing or not an array; data = %s", string(raw))
	}
	for _, factAny := range facts {
		fact, ok := factAny.(map[string]any)
		if ok && asString(fact["factId"]) == wantFactID {
			return
		}
	}
	t.Fatalf("expected factId %q in %s", wantFactID, string(raw))
}

func assertArchivedFactHasReason(t *testing.T, raw json.RawMessage, factID, wantReason string) {
	t.Helper()

	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("unmarshal archived facts payload: %v", err)
	}

	facts, ok := payload["facts"].([]any)
	if !ok {
		t.Fatalf("facts missing or not an array; data = %s", string(raw))
	}
	for _, factAny := range facts {
		fact, ok := factAny.(map[string]any)
		if !ok {
			continue
		}
		if asString(fact["factId"]) == factID && asString(fact["archiveReason"]) == wantReason {
			return
		}
	}
	t.Fatalf("could not find archived fact %q with reason %q in %s", factID, wantReason, string(raw))
}

func assertArchivedFactSupersededBy(t *testing.T, raw json.RawMessage, oldFactID, newFactID string) {
	t.Helper()

	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("unmarshal archived facts payload: %v", err)
	}

	facts, ok := payload["facts"].([]any)
	if !ok {
		t.Fatalf("facts missing or not an array; data = %s", string(raw))
	}
	for _, factAny := range facts {
		fact, ok := factAny.(map[string]any)
		if !ok {
			continue
		}
		if asString(fact["factId"]) == oldFactID &&
			asString(fact["archiveReason"]) == "superseded" &&
			asString(fact["supersededByFactId"]) == newFactID {
			return
		}
	}
	t.Fatalf("could not find archived superseded relationship old=%q new=%q in %s", oldFactID, newFactID, string(raw))
}

func asString(v any) string {
	s, _ := v.(string)
	return s
}

func testContext(t *testing.T) context.Context {
	t.Helper()
	ctx, _ := context.WithTimeout(context.Background(), 20*time.Second)
	return ctx
}

func unknownUserID() string {
	return "99999999-9999-9999-9999-999999999999"
}

func vaultKeyName() string {
	return fmt.Sprintf("BLACKBOX_TEST_KEY_%d", time.Now().UnixNano())
}
