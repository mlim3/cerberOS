package tests

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestSkillCacheSearch_ReturnsImplementationForStaticSkills(t *testing.T) {
	baseURL := strings.TrimRight(blackboxBaseURL(), "/")

	upsertStatus, upsertEnv := apiJSONRequest(t, http.MethodPost, baseURL+"/api/v1/skills/cache", map[string]any{
		"domain":      "e2e_test",
		"name":        "e2e_ping",
		"origin":      "static",
		"description": "Automated e2e connectivity probe for cross-domain skill discovery and delegation validation.",
		"payload": map[string]any{
			"name":                      "e2e_ping",
			"level":                     "command",
			"label":                     "E2E Test Ping",
			"description":               "Automated e2e connectivity probe for cross-domain skill discovery and delegation validation.",
			"required_credential_types": []string{},
			"timeout_seconds":           5,
			"implementation":            "e2e_ping",
		},
	}, nil)
	if upsertStatus != http.StatusCreated {
		t.Fatalf("upsert status = %d, want %d; body=%s", upsertStatus, http.StatusCreated, string(upsertEnv.Data))
	}
	assertSuccessEnvelope(t, upsertEnv)

	status, env := apiJSONRequest(t, http.MethodPost, baseURL+"/api/v1/skills/cache/search", map[string]any{
		"query":  "automated e2e connectivity probe",
		"domain": "e2e_test",
		"top_k":  1,
	}, nil)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d", status, http.StatusOK)
	}
	assertSuccessEnvelope(t, env)

	var payload struct {
		Results []map[string]any `json:"results"`
	}
	if err := json.Unmarshal(env.Data, &payload); err != nil {
		t.Fatalf("unmarshal search payload: %v", err)
	}
	if len(payload.Results) == 0 {
		t.Fatal("expected at least one search result")
	}

	top := payload.Results[0]
	if asString(top["name"]) != "e2e_ping" {
		t.Fatalf("top result name = %q, want e2e_ping; payload = %s", asString(top["name"]), string(env.Data))
	}
	if strings.TrimSpace(asString(top["implementation"])) == "" {
		t.Fatalf("top result implementation was empty; payload = %s", string(env.Data))
	}
	if asString(top["implementation"]) != "e2e_ping" {
		t.Fatalf("top result implementation = %q, want e2e_ping; payload = %s", asString(top["implementation"]), string(env.Data))
	}
}
