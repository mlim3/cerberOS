package envelope

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestValidate_Valid(t *testing.T) {
	valid := []byte(`{"specversion":"1.0","id":"x","source":"a","type":"b","data":{}}`)
	if err := Validate(valid); err != nil {
		t.Errorf("Validate: want nil, got %v", err)
	}

	minimal := []byte(`{"specversion":"1.0","id":"x","source":"a","type":"t"}`)
	if err := Validate(minimal); err != nil {
		t.Errorf("Validate minimal: want nil, got %v", err)
	}
}

func TestValidate_Invalid(t *testing.T) {
	tests := []struct {
		name string
		json []byte
	}{
		{"missing id", []byte(`{"specversion":"1.0","source":"a","type":"b"}`)},
		{"missing source", []byte(`{"specversion":"1.0","id":"x","type":"b"}`)},
		{"missing type", []byte(`{"specversion":"1.0","id":"x","source":"a"}`)},
		{"invalid specversion", []byte(`{"specversion":"0.9","id":"x","source":"a","type":"b"}`)},
		{"wrong specversion", []byte(`{"specversion":"2.0","id":"x","source":"a","type":"b"}`)},
		{"not json", []byte(`{invalid`)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := Validate(tt.json); err == nil {
				t.Error("Validate: want error, got nil")
			}
		})
	}
}

func TestExtractID(t *testing.T) {
	got := ExtractID([]byte(`{"id":"evt-123","source":"a","type":"b"}`))
	if got != "evt-123" {
		t.Errorf("ExtractID: got %q, want evt-123", got)
	}
	got = ExtractID([]byte(`{}`))
	if got != "" {
		t.Errorf("ExtractID empty: got %q", got)
	}
	got = ExtractID([]byte(`{invalid`))
	if got != "" {
		t.Errorf("ExtractID invalid: got %q", got)
	}
}

func TestParseMetadata(t *testing.T) {
	json := []byte(`{"source":"aegis/io","correlationid":"c123","traceid":"t456"}`)
	source, corrID, traceID := ParseMetadata(json)
	if source != "aegis/io" || corrID != "c123" || traceID != "t456" {
		t.Errorf("ParseMetadata: got source=%q corrID=%q traceID=%q", source, corrID, traceID)
	}

	empty, c, tr := ParseMetadata([]byte(`{}`))
	if empty != "" || c != "" || tr != "" {
		t.Errorf("ParseMetadata empty: got source=%q corrID=%q traceID=%q", empty, c, tr)
	}

	_, _, _ = ParseMetadata([]byte(`{invalid`))
	// Invalid JSON returns empty strings; no panic
}

func TestBuild(t *testing.T) {
	ev := Build("aegis/test", "aegis.tasks.routed", map[string]string{"taskId": "t1"})
	if ev.SpecVersion != "1.0" || ev.Source != "aegis/test" || ev.Type != "aegis.tasks.routed" {
		t.Errorf("Build: unexpected result %+v", ev)
	}
	if ev.ID == "" || ev.CorrelationID == "" {
		t.Error("Build: id and correlationid should be set")
	}
	data := ev.MustMarshal()
	if err := Validate(data); err != nil {
		t.Errorf("Build output invalid: %v", err)
	}
}

func TestNewID(t *testing.T) {
	id1 := NewID()
	id2 := NewID()
	if id1 == id2 {
		t.Error("NewID: expected unique IDs")
	}
	if len(id1) != 36 || !strings.Contains(id1, "-") {
		t.Errorf("NewID: expected UUID format, got %q", id1)
	}
}

func TestSetTraceID_SetCorrelationID(t *testing.T) {
	ev := Build("src", "type", nil)
	ev.SetTraceID("trace-1")
	ev.SetCorrelationID("corr-1")
	if ev.TraceID != "trace-1" || ev.CorrelationID != "corr-1" {
		t.Errorf("SetTraceID/SetCorrelationID: got traceID=%q corrID=%q", ev.TraceID, ev.CorrelationID)
	}
	data := ev.MustMarshal()
	var m map[string]interface{}
	json.Unmarshal(data, &m)
	if m["traceid"] != "trace-1" || m["correlationid"] != "corr-1" {
		t.Errorf("MustMarshal: traceid/correlationid not in JSON")
	}
}
