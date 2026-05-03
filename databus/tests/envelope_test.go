package tests

import (
	"encoding/json"
	"testing"

	"aegis-databus/pkg/envelope"
)

func TestCloudEventsValidate(t *testing.T) {
	valid := []byte(`{"specversion":"1.0","id":"x","source":"a","type":"b","datacontenttype":"application/json","data":{}}`)
	if err := envelope.Validate(valid); err != nil {
		t.Errorf("valid envelope rejected: %v", err)
	}

	missingID := []byte(`{"specversion":"1.0","source":"a","type":"b"}`)
	if err := envelope.Validate(missingID); err == nil {
		t.Error("expected error for missing id")
	}

	missingSource := []byte(`{"specversion":"1.0","id":"x","type":"b"}`)
	if err := envelope.Validate(missingSource); err == nil {
		t.Error("expected error for missing source")
	}

	invalidSpec := []byte(`{"specversion":"0.9","id":"x","source":"a","type":"b"}`)
	if err := envelope.Validate(invalidSpec); err == nil {
		t.Error("expected error for invalid specversion")
	}
}

func TestCloudEventsBuild(t *testing.T) {
	ev := envelope.Build("aegis/test", "aegis.tasks.routed", map[string]string{"taskId": "t1"})
	if ev.SpecVersion != "1.0" || ev.Source != "aegis/test" || ev.Type != "aegis.tasks.routed" {
		t.Errorf("unexpected Build result: %+v", ev)
	}
	data := ev.MustMarshal()
	if err := envelope.Validate(data); err != nil {
		t.Errorf("built envelope invalid: %v", err)
	}
	var m map[string]interface{}
	json.Unmarshal(data, &m)
	if _, ok := m["id"]; !ok {
		t.Error("missing id in built envelope")
	}
}
