package policy

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/mlim3/cerberOS/orchestrator/internal/config"
	"github.com/mlim3/cerberOS/orchestrator/internal/mocks"
	"github.com/mlim3/cerberOS/orchestrator/internal/types"
)

type fakeDispatchRequest struct {
	TaskID               string
	OrchestratorTaskRef  string
	UserID               string
	RequiredSkillDomains []string
	TimeoutSeconds       int
}

// TestValidateAndScopeAllowWritesPolicyAllowEvent verifies the minimal happy path:
// the Policy Enforcer asks Vault to validate the requested domains, returns the
// derived PolicyScope to its caller, and writes a structured policy_event to
// Memory so the decision is auditable.
func TestValidateAndScopeAllowWritesPolicyAllowEvent(t *testing.T) {
	vault := &mocks.VaultMock{}
	mem := mocks.NewMemoryMock()
	enforcer := New(testConfig(), vault, mem)

	scope, err := enforcer.ValidateAndScope(context.Background(), "task-1", "orch-1", "user-1", []string{"web", "data"}, 60)
	if err != nil {
		t.Fatalf("ValidateAndScope() error = %v", err)
	}

	if len(scope.Domains) != 2 {
		t.Fatalf("scope.Domains len = %d, want 2", len(scope.Domains))
	}
	if len(mem.Records) != 1 {
		t.Fatalf("memory records = %d, want 1", len(mem.Records))
	}

	record := mem.Records[0]
	if record.DataType != types.DataTypePolicyEvent {
		t.Fatalf("record.DataType = %q, want %q", record.DataType, types.DataTypePolicyEvent)
	}
	if record.TaskID != "task-1" {
		t.Fatalf("record.TaskID = %q, want task-1", record.TaskID)
	}
	if record.OrchestratorTaskRef != "orch-1" {
		t.Fatalf("record.OrchestratorTaskRef = %q, want orch-1", record.OrchestratorTaskRef)
	}

	var event types.AuditEvent
	if err := json.Unmarshal(record.Payload, &event); err != nil {
		t.Fatalf("json.Unmarshal(record.Payload) error = %v", err)
	}
	if event.EventType != types.EventPolicyAllow {
		t.Fatalf("event.EventType = %q, want %q", event.EventType, types.EventPolicyAllow)
	}
	if event.Outcome != types.OutcomeSuccess {
		t.Fatalf("event.Outcome = %q, want %q", event.Outcome, types.OutcomeSuccess)
	}
	if event.UserID != "user-1" {
		t.Fatalf("event.UserID = %q, want user-1", event.UserID)
	}
	if event.TaskID != "task-1" {
		t.Fatalf("event.TaskID = %q, want task-1", event.TaskID)
	}
	if event.OrchestratorTaskRef != "orch-1" {
		t.Fatalf("event.OrchestratorTaskRef = %q, want orch-1", event.OrchestratorTaskRef)
	}
}

// TestValidateAndScopeDenyWritesPolicyDenyEvent verifies the deny path:
// when Vault rejects the task, the Policy Enforcer returns the error to its
// caller and still writes a structured denial record into Memory.
func TestValidateAndScopeDenyWritesPolicyDenyEvent(t *testing.T) {
	vault := &mocks.VaultMock{
		ShouldDeny: true,
		DenyReason: "domain storage not in user policy",
	}
	mem := mocks.NewMemoryMock()
	enforcer := New(testConfig(), vault, mem)

	_, err := enforcer.ValidateAndScope(context.Background(), "task-2", "orch-2", "user-2", []string{"storage"}, 60)
	if err == nil {
		t.Fatal("ValidateAndScope() error = nil, want denial error")
	}
	if !strings.Contains(err.Error(), "policy denied") {
		t.Fatalf("ValidateAndScope() error = %v, want policy denied", err)
	}
	if len(mem.Records) != 1 {
		t.Fatalf("memory records = %d, want 1", len(mem.Records))
	}

	record := mem.Records[0]
	if record.TaskID != "task-2" {
		t.Fatalf("record.TaskID = %q, want task-2", record.TaskID)
	}
	if record.OrchestratorTaskRef != "orch-2" {
		t.Fatalf("record.OrchestratorTaskRef = %q, want orch-2", record.OrchestratorTaskRef)
	}

	var event types.AuditEvent
	if err := json.Unmarshal(record.Payload, &event); err != nil {
		t.Fatalf("json.Unmarshal(record.Payload) error = %v", err)
	}
	if event.EventType != types.EventPolicyDeny {
		t.Fatalf("event.EventType = %q, want %q", event.EventType, types.EventPolicyDeny)
	}
	if event.Outcome != types.OutcomeDenied {
		t.Fatalf("event.Outcome = %q, want %q", event.Outcome, types.OutcomeDenied)
	}
	if event.TaskID != "task-2" {
		t.Fatalf("event.TaskID = %q, want task-2", event.TaskID)
	}
	if event.OrchestratorTaskRef != "orch-2" {
		t.Fatalf("event.OrchestratorTaskRef = %q, want orch-2", event.OrchestratorTaskRef)
	}
	if !strings.Contains(string(event.EventDetail), "domain storage not in user policy") {
		t.Fatalf("event.EventDetail = %s, want denial reason", string(event.EventDetail))
	}
}

// TestWriteAuditEventWritesPolicyEvent verifies the helper used by
// ValidateAndScope directly. This confirms the helper writes the expected
// data_type and module attribution even before the full Policy Enforcer is
// wired into Dispatcher.
func TestWriteAuditEventWritesPolicyEvent(t *testing.T) {
	vault := &mocks.VaultMock{}
	mem := mocks.NewMemoryMock()
	enforcer := New(testConfig(), vault, mem)

	if err := enforcer.writeAuditEvent(context.Background(), "task-1", "orch-1", "user-3", types.OutcomeDenied, "demo deny"); err != nil {
		t.Fatalf("writeAuditEvent() error = %v", err)
	}

	if len(mem.Records) != 1 {
		t.Fatalf("memory records = %d, want 1", len(mem.Records))
	}

	record := mem.Records[0]
	if record.DataType != types.DataTypePolicyEvent {
		t.Fatalf("record.DataType = %q, want %q", record.DataType, types.DataTypePolicyEvent)
	}
	if record.TaskID != "task-1" {
		t.Fatalf("record.TaskID = %q, want task-1", record.TaskID)
	}
	if record.OrchestratorTaskRef != "orch-1" {
		t.Fatalf("record.OrchestratorTaskRef = %q, want orch-1", record.OrchestratorTaskRef)
	}

	var event types.AuditEvent
	if err := json.Unmarshal(record.Payload, &event); err != nil {
		t.Fatalf("json.Unmarshal(record.Payload) error = %v", err)
	}
	if event.InitiatingModule != types.ModulePolicyEnforcer {
		t.Fatalf("event.InitiatingModule = %q, want %q", event.InitiatingModule, types.ModulePolicyEnforcer)
	}
	if event.TaskID != "task-1" {
		t.Fatalf("event.TaskID = %q, want task-1", event.TaskID)
	}
	if event.UserID != "user-3" {
		t.Fatalf("event.UserID = %q, want user-3", event.UserID)
	}
	if event.OrchestratorTaskRef != "orch-1" {
		t.Fatalf("event.OrchestratorTaskRef = %q, want orch-1", event.OrchestratorTaskRef)
	}
}

// TestWriteAuditEventReturnsErrorWhenMemoryWriteFails verifies that
// writeAuditEvent returns an error when the Memory backend fails to persist
// the policy_event audit record.
func TestWriteAuditEventReturnsErrorWhenMemoryWriteFails(t *testing.T) {
	vault := &mocks.VaultMock{}
	mem := mocks.NewMemoryMock()
	mem.ShouldFailWrites = true

	enforcer := New(testConfig(), vault, mem)

	err := enforcer.writeAuditEvent(context.Background(), "task-9", "orch-9", "user-9", types.OutcomeDenied, "memory down")
	if err == nil {
		t.Fatal("writeAuditEvent() error = nil, want non-nil")
	}
	if !strings.Contains(err.Error(), "write policy audit event") {
		t.Fatalf("writeAuditEvent() error = %v, want write policy audit event", err)
	}
	if mem.WriteCallCount != 1 {
		t.Fatalf("WriteCallCount = %d, want 1", mem.WriteCallCount)
	}
}

// TestValidateAndScopeCacheHitDoesNotCallVault verifies that once a policy
// decision is cached, the same request can be served from cache without
// calling Vault again. This is the core optimization path of M3 caching.
func TestValidateAndScopeCacheHitDoesNotCallVault(t *testing.T) {
	vault := &mocks.VaultMock{}
	mem := mocks.NewMemoryMock()
	enforcer := New(testConfig(), vault, mem)

	scope1, err := enforcer.ValidateAndScope(context.Background(), "task-1", "orch-1", "user-1", []string{"web", "data"}, 60)
	if err != nil {
		t.Fatalf("first ValidateAndScope() error = %v", err)
	}
	if len(vault.ValidatedTasks) != 1 {
		t.Fatalf("ValidatedTasks len after first call = %d, want 1", len(vault.ValidatedTasks))
	}

	vault.ShouldBeUnreachable = true

	scope2, err := enforcer.ValidateAndScope(context.Background(), "task-2", "orch-2", "user-1", []string{"data", "web"}, 60)
	if err != nil {
		t.Fatalf("second ValidateAndScope() error = %v, want cache hit success", err)
	}
	if len(vault.ValidatedTasks) != 1 {
		t.Fatalf("ValidatedTasks len after cache hit = %d, want 1", len(vault.ValidatedTasks))
	}
	if scope2.TokenRef != scope1.TokenRef {
		t.Fatalf("cache hit TokenRef = %q, want %q", scope2.TokenRef, scope1.TokenRef)
	}
	if len(mem.Records) != 2 {
		t.Fatalf("memory records = %d, want 2", len(mem.Records))
	}

	var event types.AuditEvent
	if err := json.Unmarshal(mem.Records[1].Payload, &event); err != nil {
		t.Fatalf("json.Unmarshal(cache hit record) error = %v", err)
	}
	if !strings.Contains(string(event.EventDetail), "cache_hit") {
		t.Fatalf("event.EventDetail = %s, want cache_hit", string(event.EventDetail))
	}
}

// TestValidateAndScopeVaultDownFailOpenUsesCache verifies the intended fail-open
// behavior: when Vault becomes unreachable but a valid cached decision exists,
// the enforcer still returns the cached scope instead of denying the task.
func TestValidateAndScopeVaultDownFailOpenUsesCache(t *testing.T) {
	cfg := testConfig()
	cfg.VaultFailureMode = config.VaultFailureModeOpen

	vault := &mocks.VaultMock{}
	mem := mocks.NewMemoryMock()
	enforcer := New(cfg, vault, mem)

	scope1, err := enforcer.ValidateAndScope(context.Background(), "task-10", "orch-10", "user-10", []string{"web"}, 60)
	if err != nil {
		t.Fatalf("initial ValidateAndScope() error = %v", err)
	}

	vault.ShouldBeUnreachable = true

	scope2, err := enforcer.ValidateAndScope(context.Background(), "task-11", "orch-11", "user-10", []string{"web"}, 60)
	if err != nil {
		t.Fatalf("fail-open ValidateAndScope() error = %v, want cached scope", err)
	}
	if scope2.TokenRef != scope1.TokenRef {
		t.Fatalf("fail-open TokenRef = %q, want %q", scope2.TokenRef, scope1.TokenRef)
	}
}

// TestInvalidateCacheForPolicyClearsCache verifies that the cache invalidation
// hook clears previously stored policy scopes. After invalidation, the same
// request should no longer succeed from cache when Vault is unavailable.
func TestInvalidateCacheForPolicyClearsCache(t *testing.T) {
	cfg := testConfig()
	cfg.VaultFailureMode = config.VaultFailureModeOpen

	vault := &mocks.VaultMock{}
	mem := mocks.NewMemoryMock()
	enforcer := New(cfg, vault, mem)

	if _, err := enforcer.ValidateAndScope(context.Background(), "task-20", "orch-20", "user-20", []string{"data"}, 60); err != nil {
		t.Fatalf("initial ValidateAndScope() error = %v", err)
	}

	enforcer.InvalidateCacheForPolicy("any-policy")
	vault.ShouldBeUnreachable = true

	_, err := enforcer.ValidateAndScope(context.Background(), "task-21", "orch-21", "user-20", []string{"data"}, 60)
	if err == nil {
		t.Fatal("ValidateAndScope() error = nil, want vault error after cache invalidation")
	}
	if !strings.Contains(err.Error(), "vault unreachable") {
		t.Fatalf("ValidateAndScope() error = %v, want vault unreachable", err)
	}
}

// TestPolicyWorkflowAllow simulates the end-to-end M3 policy workflow:
// a task arrives from the dispatcher/task manager, the Enforcer validates it
// against Vault, writes a policy_event to Memory, and returns the derived scope.
func TestPolicyWorkflowAllow(t *testing.T) {
	req := fakeDispatchRequest{
		TaskID:               "task-100",
		OrchestratorTaskRef:  "orch-100",
		UserID:               "user-100",
		RequiredSkillDomains: []string{"web", "data"},
		TimeoutSeconds:       60,
	}

	vault := &mocks.VaultMock{}
	mem := mocks.NewMemoryMock()
	enforcer := New(testConfig(), vault, mem)

	t.Log("=== POLICY WORKFLOW ALLOW DEMO START ===")
	t.Logf("Dispatcher received task: task_id=%s orch_ref=%s user_id=%s domains=%v timeout=%d",
		req.TaskID, req.OrchestratorTaskRef, req.UserID, req.RequiredSkillDomains, req.TimeoutSeconds)

	scope, err := simulateDispatcherPolicyStep(enforcer, req)
	if err != nil {
		t.Fatalf("policy workflow error = %v", err)
	}

	t.Logf("Vault validation succeeded. Returned scope token_ref=%s domains=%v",
		scope.TokenRef, scope.Domains)

	// Simulate what Dispatcher would do next: attach the scope to task_spec.
	taskSpec := struct {
		TaskID              string
		OrchestratorTaskRef string
		UserID              string
		PolicyScope         types.PolicyScope
	}{
		TaskID:              req.TaskID,
		OrchestratorTaskRef: req.OrchestratorTaskRef,
		UserID:              req.UserID,
		PolicyScope:         scope,
	}

	t.Logf("Dispatcher attached policy scope to task_spec: %+v", taskSpec)

	if taskSpec.PolicyScope.TokenRef == "" {
		t.Fatal("taskSpec.PolicyScope.TokenRef = empty, want non-empty")
	}
	if len(taskSpec.PolicyScope.Domains) != 2 {
		t.Fatalf("taskSpec.PolicyScope.Domains len = %d, want 2", len(taskSpec.PolicyScope.Domains))
	}
	if len(mem.Records) != 1 {
		t.Fatalf("memory records = %d, want 1", len(mem.Records))
	}

	var event types.AuditEvent
	if err := json.Unmarshal(mem.Records[0].Payload, &event); err != nil {
		t.Fatalf("json.Unmarshal(record.Payload) error = %v", err)
	}

	t.Logf("Memory wrote policy audit event: %s", mustPrettyJSON(t, event))

	if event.EventType != types.EventPolicyAllow {
		t.Fatalf("event.EventType = %q, want %q", event.EventType, types.EventPolicyAllow)
	}
	if event.TaskID != req.TaskID {
		t.Fatalf("event.TaskID = %q, want %q", event.TaskID, req.TaskID)
	}
	if event.OrchestratorTaskRef != req.OrchestratorTaskRef {
		t.Fatalf("event.OrchestratorTaskRef = %q, want %q", event.OrchestratorTaskRef, req.OrchestratorTaskRef)
	}

	t.Log("=== POLICY WORKFLOW ALLOW DEMO END ===")
}

// TestPolicyWorkflowDeny simulates the deny workflow:
// a task arrives, Vault rejects the requested skill domain, the Enforcer
// returns an error, and a policy_deny event is still written to Memory.
func TestPolicyWorkflowDeny(t *testing.T) {
	req := fakeDispatchRequest{
		TaskID:               "task-200",
		OrchestratorTaskRef:  "orch-200",
		UserID:               "user-200",
		RequiredSkillDomains: []string{"storage"},
		TimeoutSeconds:       60,
	}

	vault := &mocks.VaultMock{
		ShouldDeny: true,
		DenyReason: "domain storage not in user policy",
	}
	mem := mocks.NewMemoryMock()
	enforcer := New(testConfig(), vault, mem)

	t.Log("=== POLICY WORKFLOW DENY DEMO START ===")
	t.Logf("Dispatcher received task: task_id=%s orch_ref=%s user_id=%s domains=%v timeout=%d",
		req.TaskID, req.OrchestratorTaskRef, req.UserID, req.RequiredSkillDomains, req.TimeoutSeconds)

	_, err := simulateDispatcherPolicyStep(enforcer, req)
	if err == nil {
		t.Fatal("policy workflow error = nil, want denial")
	}

	t.Logf("Vault validation denied task: error=%v", err)

	if !strings.Contains(err.Error(), "policy denied") {
		t.Fatalf("policy workflow error = %v, want policy denied", err)
	}
	if len(mem.Records) != 1 {
		t.Fatalf("memory records = %d, want 1", len(mem.Records))
	}

	var event types.AuditEvent
	if err := json.Unmarshal(mem.Records[0].Payload, &event); err != nil {
		t.Fatalf("json.Unmarshal(record.Payload) error = %v", err)
	}

	t.Logf("Memory wrote policy deny audit event: %s", mustPrettyJSON(t, event))

	if event.EventType != types.EventPolicyDeny {
		t.Fatalf("event.EventType = %q, want %q", event.EventType, types.EventPolicyDeny)
	}
	if event.TaskID != req.TaskID {
		t.Fatalf("event.TaskID = %q, want %q", event.TaskID, req.TaskID)
	}
	if !strings.Contains(string(event.EventDetail), "domain storage not in user policy") {
		t.Fatalf("event.EventDetail = %s, want denial reason", string(event.EventDetail))
	}

	t.Log("=== POLICY WORKFLOW DENY DEMO END ===")
}

func simulateDispatcherPolicyStep(e *Enforcer, req fakeDispatchRequest) (types.PolicyScope, error) {
	return e.ValidateAndScope(
		context.Background(),
		req.TaskID,
		req.OrchestratorTaskRef,
		req.UserID,
		req.RequiredSkillDomains,
		req.TimeoutSeconds,
	)
}

// TestPolicyWorkflowFailOpenCacheFallback simulates a dispatcher request flow
// where Vault is unavailable, but the Enforcer is configured for FAIL_OPEN
// and can still authorize the task using a cached policy scope.
func TestPolicyWorkflowFailOpenCacheFallback(t *testing.T) {
	cfg := testConfig()
	cfg.VaultFailureMode = config.VaultFailureModeOpen

	vault := &mocks.VaultMock{}
	mem := mocks.NewMemoryMock()
	enforcer := New(cfg, vault, mem)

	seedReq := fakeDispatchRequest{
		TaskID:               "task-300",
		OrchestratorTaskRef:  "orch-300",
		UserID:               "user-300",
		RequiredSkillDomains: []string{"web"},
		TimeoutSeconds:       60,
	}

	t.Log("=== POLICY WORKFLOW FAIL-OPEN DEMO START ===")
	t.Logf("Step 1: seed cache with initial task: task_id=%s orch_ref=%s user_id=%s domains=%v",
		seedReq.TaskID, seedReq.OrchestratorTaskRef, seedReq.UserID, seedReq.RequiredSkillDomains)

	scope1, err := simulateDispatcherPolicyStep(enforcer, seedReq)
	if err != nil {
		t.Fatalf("seed workflow error = %v", err)
	}

	t.Logf("Initial Vault call succeeded. Cached scope token_ref=%s domains=%v", scope1.TokenRef, scope1.Domains)

	vault.ShouldBeUnreachable = true
	t.Log("Step 2: simulate Vault outage")

	failOpenReq := fakeDispatchRequest{
		TaskID:               "task-301",
		OrchestratorTaskRef:  "orch-301",
		UserID:               "user-300",
		RequiredSkillDomains: []string{"web"},
		TimeoutSeconds:       60,
	}

	t.Logf("Step 3: dispatcher retries with task: task_id=%s orch_ref=%s user_id=%s domains=%v",
		failOpenReq.TaskID, failOpenReq.OrchestratorTaskRef, failOpenReq.UserID, failOpenReq.RequiredSkillDomains)

	scope2, err := simulateDispatcherPolicyStep(enforcer, failOpenReq)
	if err != nil {
		t.Fatalf("fail-open workflow error = %v, want cached scope", err)
	}

	t.Logf("FAIL_OPEN succeeded using cached scope. token_ref=%s domains=%v", scope2.TokenRef, scope2.Domains)

	if scope2.TokenRef != scope1.TokenRef {
		t.Fatalf("fail-open TokenRef = %q, want %q", scope2.TokenRef, scope1.TokenRef)
	}
	if len(mem.Records) != 2 {
		t.Fatalf("memory records = %d, want 2", len(mem.Records))
	}

	var event types.AuditEvent
	if err := json.Unmarshal(mem.Records[1].Payload, &event); err != nil {
		t.Fatalf("json.Unmarshal(fallback record) error = %v", err)
	}

	t.Logf("Memory wrote fallback audit event: %s", mustPrettyJSON(t, event))

	if !strings.Contains(string(event.EventDetail), "cache_hit") &&
		!strings.Contains(string(event.EventDetail), "cache_fallback") {
		t.Fatalf("event.EventDetail = %s, want cache_hit or cache_fallback", string(event.EventDetail))
	}

	t.Log("=== POLICY WORKFLOW FAIL-OPEN DEMO END ===")
}

func mustPrettyJSON(t *testing.T, v any) string {
	t.Helper()

	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatalf("json.MarshalIndent() error = %v", err)
	}
	return fmt.Sprintf("\n%s", string(raw))
}

func testConfig() *config.OrchestratorConfig {
	return &config.OrchestratorConfig{
		VaultPolicyCacheTTL: 60,
		NodeID:              "test-node",
	}
}
