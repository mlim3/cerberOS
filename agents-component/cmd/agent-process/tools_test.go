package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// ---- toolsForDomain tests ----

func TestToolsForDomain_Web(t *testing.T) {
	tools := toolsForDomain("web", nil)
	names := toolNames(tools)
	mustContain(t, names, "web_fetch")
	mustContain(t, names, "task_complete")
	mustNotContain(t, names, "vault_web_fetch") // ve == nil
}

func TestToolsForDomain_Data(t *testing.T) {
	tools := toolsForDomain("data", nil)
	names := toolNames(tools)
	mustContain(t, names, "data_transform")
	mustContain(t, names, "task_complete")
	mustNotContain(t, names, "vault_data_read") // ve == nil
	mustNotContain(t, names, "vault_data_write")
}

func TestToolsForDomain_Comms(t *testing.T) {
	tools := toolsForDomain("comms", nil)
	names := toolNames(tools)
	mustContain(t, names, "comms_format")
	mustContain(t, names, "task_complete")
	mustNotContain(t, names, "vault_comms_send") // ve == nil
}

func TestToolsForDomain_Storage(t *testing.T) {
	// storage has no local tools; with nil ve only task_complete is present.
	tools := toolsForDomain("storage", nil)
	names := toolNames(tools)
	mustContain(t, names, "task_complete")
	mustNotContain(t, names, "vault_storage_read")
	mustNotContain(t, names, "vault_storage_write")
	mustNotContain(t, names, "vault_storage_list")
}

func TestToolsForDomain_Unknown(t *testing.T) {
	tools := toolsForDomain("unknown-domain", nil)
	names := toolNames(tools)
	mustContain(t, names, "task_complete")
	if len(tools) != 1 {
		t.Errorf("unknown domain: want 1 tool (task_complete), got %d", len(tools))
	}
}

// ---- data_transform: navigateJSONPath tests ----

func TestNavigateJSONPath_Root(t *testing.T) {
	data := map[string]interface{}{"a": 1.0}
	got, err := navigateJSONPath(data, "")
	if err != nil {
		t.Fatal(err)
	}
	if got.(map[string]interface{})["a"] != 1.0 {
		t.Error("root path: unexpected value")
	}
}

func TestNavigateJSONPath_DotOnly(t *testing.T) {
	data := "hello"
	got, err := navigateJSONPath(data, ".")
	if err != nil || got != "hello" {
		t.Errorf("dot path: want %q, got %v (err=%v)", "hello", got, err)
	}
}

func TestNavigateJSONPath_NestedKey(t *testing.T) {
	data := map[string]interface{}{
		"user": map[string]interface{}{"name": "alice"},
	}
	got, err := navigateJSONPath(data, ".user.name")
	if err != nil {
		t.Fatal(err)
	}
	if got != "alice" {
		t.Errorf("nested key: want %q, got %v", "alice", got)
	}
}

func TestNavigateJSONPath_ArrayIndex(t *testing.T) {
	data := []interface{}{"x", "y", "z"}
	got, err := navigateJSONPath(data, ".1")
	if err != nil {
		t.Fatal(err)
	}
	if got != "y" {
		t.Errorf("array index: want %q, got %v", "y", got)
	}
}

func TestNavigateJSONPath_MissingKey(t *testing.T) {
	data := map[string]interface{}{"a": 1.0}
	_, err := navigateJSONPath(data, ".b")
	if err == nil {
		t.Error("missing key: expected error, got nil")
	}
}

func TestNavigateJSONPath_OutOfRange(t *testing.T) {
	data := []interface{}{"a"}
	_, err := navigateJSONPath(data, ".5")
	if err == nil {
		t.Error("out-of-range index: expected error, got nil")
	}
}

func TestNavigateJSONPath_NonNumericIndexOnArray(t *testing.T) {
	data := []interface{}{"a", "b"}
	_, err := navigateJSONPath(data, ".name")
	if err == nil {
		t.Error("non-numeric index on array: expected error, got nil")
	}
}

// ---- data_transform: executeDataTransform tests ----

func marshal(t *testing.T, v interface{}) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

func dataTransformParams(data, path, op string) json.RawMessage {
	p := map[string]string{"data": data, "operation": op}
	if path != "" {
		p["path"] = path
	}
	b, _ := json.Marshal(p)
	return b
}

func TestDataTransform_ExtractRoot(t *testing.T) {
	raw := dataTransformParams(`{"key":"value"}`, "", "extract")
	result := executeDataTransform(context.Background(), raw)
	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Content)
	}
	if !strings.Contains(result.Content, "value") {
		t.Errorf("extract root: want content containing %q, got %q", "value", result.Content)
	}
}

func TestDataTransform_ExtractNestedField(t *testing.T) {
	raw := dataTransformParams(`{"user":{"name":"bob"}}`, ".user.name", "extract")
	result := executeDataTransform(context.Background(), raw)
	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Content)
	}
	if result.Content != `"bob"` {
		t.Errorf("extract nested field: want %q, got %q", `"bob"`, result.Content)
	}
}

func TestDataTransform_Keys(t *testing.T) {
	raw := dataTransformParams(`{"b":2,"a":1,"c":3}`, "", "keys")
	result := executeDataTransform(context.Background(), raw)
	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Content)
	}
	// Keys must be sorted.
	if result.Content != `["a","b","c"]` {
		t.Errorf("keys: want %q, got %q", `["a","b","c"]`, result.Content)
	}
}

func TestDataTransform_KeysOnNonObject(t *testing.T) {
	raw := dataTransformParams(`[1,2,3]`, "", "keys")
	result := executeDataTransform(context.Background(), raw)
	if !result.IsError {
		t.Error("keys on array: expected IsError=true")
	}
}

func TestDataTransform_LengthArray(t *testing.T) {
	raw := dataTransformParams(`[1,2,3,4]`, "", "length")
	result := executeDataTransform(context.Background(), raw)
	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Content)
	}
	if result.Content != "4" {
		t.Errorf("length array: want %q, got %q", "4", result.Content)
	}
}

func TestDataTransform_LengthObject(t *testing.T) {
	raw := dataTransformParams(`{"x":1,"y":2}`, "", "length")
	result := executeDataTransform(context.Background(), raw)
	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Content)
	}
	if result.Content != "2" {
		t.Errorf("length object: want %q, got %q", "2", result.Content)
	}
}

func TestDataTransform_LengthString(t *testing.T) {
	raw := dataTransformParams(`"hello"`, "", "length")
	result := executeDataTransform(context.Background(), raw)
	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Content)
	}
	if result.Content != "5" {
		t.Errorf("length string: want %q, got %q", "5", result.Content)
	}
}

func TestDataTransform_InvalidJSON(t *testing.T) {
	raw := dataTransformParams(`{not json}`, "", "extract")
	result := executeDataTransform(context.Background(), raw)
	if !result.IsError {
		t.Error("invalid JSON in data: expected IsError=true")
	}
}

func TestDataTransform_UnknownOperation(t *testing.T) {
	raw := dataTransformParams(`{}`, "", "frobnicate")
	result := executeDataTransform(context.Background(), raw)
	if !result.IsError {
		t.Error("unknown operation: expected IsError=true")
	}
}

func TestDataTransform_InvalidParams(t *testing.T) {
	result := executeDataTransform(context.Background(), json.RawMessage(`not-json`))
	if !result.IsError {
		t.Error("invalid params: expected IsError=true")
	}
}

// ---- comms_format: executeCommsFormat tests ----

func commsFormatRaw(template string, vars map[string]string) json.RawMessage {
	p := map[string]interface{}{"template": template}
	if vars != nil {
		p["variables"] = vars
	}
	b, _ := json.Marshal(p)
	return b
}

func TestCommsFormat_BasicSubstitution(t *testing.T) {
	raw := commsFormatRaw("Hello, {{name}}!", map[string]string{"name": "Alice"})
	result := executeCommsFormat(context.Background(), raw)
	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Content)
	}
	if result.Content != "Hello, Alice!" {
		t.Errorf("basic substitution: want %q, got %q", "Hello, Alice!", result.Content)
	}
}

func TestCommsFormat_MultipleVariables(t *testing.T) {
	raw := commsFormatRaw(
		"Dear {{first}} {{last}}, your order {{id}} is ready.",
		map[string]string{"first": "Bob", "last": "Smith", "id": "ORD-123"},
	)
	result := executeCommsFormat(context.Background(), raw)
	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Content)
	}
	want := "Dear Bob Smith, your order ORD-123 is ready."
	if result.Content != want {
		t.Errorf("multiple vars: want %q, got %q", want, result.Content)
	}
}

func TestCommsFormat_NoVariables(t *testing.T) {
	raw := commsFormatRaw("Static message.", nil)
	result := executeCommsFormat(context.Background(), raw)
	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Content)
	}
	if result.Content != "Static message." {
		t.Errorf("no vars: want %q, got %q", "Static message.", result.Content)
	}
}

func TestCommsFormat_UnresolvedPlaceholderWarning(t *testing.T) {
	raw := commsFormatRaw("Hello, {{name}} and {{missing}}!", map[string]string{"name": "Alice"})
	result := executeCommsFormat(context.Background(), raw)
	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Content)
	}
	// Should still contain the unresolved placeholder.
	if !strings.Contains(result.Content, "{{missing}}") {
		t.Error("unresolved placeholder: should remain in output")
	}
	// Details should contain a warning.
	if _, ok := result.Details["warning"]; !ok {
		t.Error("unresolved placeholder: expected warning in Details")
	}
}

func TestCommsFormat_InvalidParams(t *testing.T) {
	result := executeCommsFormat(context.Background(), json.RawMessage(`not-json`))
	if !result.IsError {
		t.Error("invalid params: expected IsError=true")
	}
}

// ---- helpers ----

func toolNames(tools []SkillTool) []string {
	names := make([]string, len(tools))
	for i, t := range tools {
		names[i] = t.Definition.Name
	}
	return names
}

func mustContain(t *testing.T, names []string, want string) {
	t.Helper()
	for _, n := range names {
		if n == want {
			return
		}
	}
	t.Errorf("tool %q missing from domain registry; got: %v", want, names)
}

func mustNotContain(t *testing.T, names []string, unwanted string) {
	t.Helper()
	for _, n := range names {
		if n == unwanted {
			t.Errorf("tool %q should not be in registry (ve==nil), but was found", unwanted)
			return
		}
	}
}
