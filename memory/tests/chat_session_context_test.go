package tests

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// sessionHistoryTurn mirrors the subset of fields the Orchestrator/Agents
// consume from GET /api/v1/chat/{conversationId}/history.
type sessionHistoryTurn struct {
	MessageID  string    `json:"messageId"`
	Role       string    `json:"role"`
	Content    string    `json:"content"`
	TokenCount *int32    `json:"tokenCount,omitempty"`
	CreatedAt  time.Time `json:"createdAt"`
}

type sessionHistoryResponse struct {
	ConversationID string               `json:"conversationId"`
	Turns          []sessionHistoryTurn `json:"turns"`
	TotalTokens    int                  `json:"totalTokens"`
	Truncated      bool                 `json:"truncated"`
	TokenBudget    int                  `json:"tokenBudget"`
	MaxTurns       int                  `json:"maxTurns"`
}

// postMessage is a local helper that seeds the chat conversation table via the
// public chat API; it lets each test compose conversation transcripts without
// reaching into the DB directly.
func postMessage(t *testing.T, baseURL, conversationID, userID, role, content string, tokenCount *int32) {
	t.Helper()
	body := map[string]any{
		"userId":  userID,
		"role":    role,
		"content": content,
	}
	if tokenCount != nil {
		body["tokenCount"] = *tokenCount
	}
	status, env := apiJSONRequest(t, http.MethodPost, baseURL+"/api/v1/chat/"+conversationID+"/messages", body, nil)
	if status != http.StatusCreated {
		t.Fatalf("seed message %q status = %d (env=%+v)", role, status, env)
	}
}

func newConversationID() string {
	return uuid.NewString()
}

func int32Ptr(v int32) *int32 { return &v }

func decodeSessionHistory(t *testing.T, raw json.RawMessage) sessionHistoryResponse {
	t.Helper()
	var out sessionHistoryResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode history payload: %v\nraw=%s", err, string(raw))
	}
	return out
}

// TestSessionHistory_ChronologicalOrder verifies that the endpoint returns the
// turns in the order they were posted (oldest first). The Orchestrator relies
// on this ordering when it folds the transcript into the worker-agent's first
// user turn.
func TestSessionHistory_ChronologicalOrder(t *testing.T) {
	baseURL := strings.TrimRight(blackboxBaseURL(), "/")
	userID := validUserFixture(t)
	conversationID := newConversationID()

	postMessage(t, baseURL, conversationID, userID, "user", "How to become a hairdresser?", int32Ptr(8))
	postMessage(t, baseURL, conversationID, userID, "assistant", "Step 1: research...", int32Ptr(12))
	postMessage(t, baseURL, conversationID, userID, "user", "How many of those can you do?", int32Ptr(9))

	status, env := apiJSONRequest(t, http.MethodGet,
		baseURL+"/api/v1/chat/"+conversationID+"/history?userId="+userID, nil, nil)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d (env=%+v)", status, http.StatusOK, env)
	}
	assertSuccessEnvelope(t, env)

	hist := decodeSessionHistory(t, env.Data)
	if hist.ConversationID != conversationID {
		t.Fatalf("conversationId = %q, want %q", hist.ConversationID, conversationID)
	}
	if len(hist.Turns) != 3 {
		t.Fatalf("len(turns) = %d, want 3", len(hist.Turns))
	}
	if hist.Turns[0].Role != "user" || !strings.Contains(hist.Turns[0].Content, "hairdresser") {
		t.Fatalf("first turn = %+v, want user hairdresser prompt", hist.Turns[0])
	}
	if hist.Turns[1].Role != "assistant" {
		t.Fatalf("second turn role = %q, want assistant", hist.Turns[1].Role)
	}
	if hist.Turns[2].Role != "user" || !strings.Contains(hist.Turns[2].Content, "How many") {
		t.Fatalf("third turn = %+v, want follow-up user prompt", hist.Turns[2])
	}
	for i := 1; i < len(hist.Turns); i++ {
		if hist.Turns[i].CreatedAt.Before(hist.Turns[i-1].CreatedAt) {
			t.Fatalf("turns not chronological at i=%d: %v before %v", i, hist.Turns[i].CreatedAt, hist.Turns[i-1].CreatedAt)
		}
	}
}

// TestSessionHistory_TokenBudgetTrimsOldest proves that when the accumulated
// token_count exceeds the requested budget, the oldest turns are dropped and
// truncated=true is reported. This is the key acceptance criterion in the
// multi-team issue (bounded + logged token budget for injected history).
func TestSessionHistory_TokenBudgetTrimsOldest(t *testing.T) {
	baseURL := strings.TrimRight(blackboxBaseURL(), "/")
	userID := validUserFixture(t)
	conversationID := newConversationID()

	postMessage(t, baseURL, conversationID, userID, "user", "first turn", int32Ptr(100))
	postMessage(t, baseURL, conversationID, userID, "assistant", "second turn", int32Ptr(100))
	postMessage(t, baseURL, conversationID, userID, "user", "third turn", int32Ptr(100))
	postMessage(t, baseURL, conversationID, userID, "assistant", "fourth turn", int32Ptr(100))

	// Budget=250 should retain only the newest two turns (the third is 100,
	// the fourth is 100 — total 200 ≤ 250; the second would push it to 300).
	url := fmt.Sprintf("%s/api/v1/chat/%s/history?userId=%s&token_budget=250", baseURL, conversationID, userID)
	status, env := apiJSONRequest(t, http.MethodGet, url, nil, nil)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d (env=%+v)", status, http.StatusOK, env)
	}

	hist := decodeSessionHistory(t, env.Data)
	if !hist.Truncated {
		t.Fatalf("truncated = false, want true when budget < total tokens")
	}
	if len(hist.Turns) != 2 {
		t.Fatalf("len(turns) = %d, want 2 (budget=250 with 100-token turns)", len(hist.Turns))
	}
	if hist.Turns[0].Content != "third turn" || hist.Turns[1].Content != "fourth turn" {
		t.Fatalf("retained turns = [%q, %q], want [third, fourth]",
			hist.Turns[0].Content, hist.Turns[1].Content)
	}
	if hist.TotalTokens != 200 {
		t.Fatalf("totalTokens = %d, want 200", hist.TotalTokens)
	}
	if hist.TokenBudget != 250 {
		t.Fatalf("tokenBudget echoed = %d, want 250", hist.TokenBudget)
	}
}

// TestSessionHistory_MaxTurnsCap verifies the max_turns hard cap fires
// independently of the token budget.
func TestSessionHistory_MaxTurnsCap(t *testing.T) {
	baseURL := strings.TrimRight(blackboxBaseURL(), "/")
	userID := validUserFixture(t)
	conversationID := newConversationID()

	for i := 0; i < 6; i++ {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		postMessage(t, baseURL, conversationID, userID, role, fmt.Sprintf("turn %d", i), int32Ptr(1))
	}

	url := fmt.Sprintf("%s/api/v1/chat/%s/history?userId=%s&max_turns=3&token_budget=0", baseURL, conversationID, userID)
	status, env := apiJSONRequest(t, http.MethodGet, url, nil, nil)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d (env=%+v)", status, http.StatusOK, env)
	}

	hist := decodeSessionHistory(t, env.Data)
	if len(hist.Turns) != 3 {
		t.Fatalf("len(turns) = %d, want 3 (max_turns cap)", len(hist.Turns))
	}
	if !hist.Truncated {
		t.Fatalf("truncated = false, want true when max_turns drops older turns")
	}
	// Should be the last three posted (turns 3, 4, 5).
	wantTail := []string{"turn 3", "turn 4", "turn 5"}
	for i, w := range wantTail {
		if hist.Turns[i].Content != w {
			t.Fatalf("turns[%d].content = %q, want %q", i, hist.Turns[i].Content, w)
		}
	}
}

// TestSessionHistory_RoleFilter verifies system messages are excluded by
// default but can be opted-in, matching the Agents system-prompt builder's
// expectation that it controls the system role itself.
func TestSessionHistory_RoleFilter(t *testing.T) {
	baseURL := strings.TrimRight(blackboxBaseURL(), "/")
	userID := validUserFixture(t)
	conversationID := newConversationID()

	postMessage(t, baseURL, conversationID, userID, "system", "hidden system note", int32Ptr(1))
	postMessage(t, baseURL, conversationID, userID, "user", "visible user turn", int32Ptr(1))
	postMessage(t, baseURL, conversationID, userID, "assistant", "visible assistant turn", int32Ptr(1))

	// Default (no include_roles) must exclude system.
	status, env := apiJSONRequest(t, http.MethodGet,
		baseURL+"/api/v1/chat/"+conversationID+"/history?userId="+userID, nil, nil)
	if status != http.StatusOK {
		t.Fatalf("default status = %d, want 200", status)
	}
	hist := decodeSessionHistory(t, env.Data)
	for _, turn := range hist.Turns {
		if turn.Role == "system" {
			t.Fatalf("default include_roles leaked a system message: %+v", turn)
		}
	}
	if len(hist.Turns) != 2 {
		t.Fatalf("default len(turns) = %d, want 2 (system excluded)", len(hist.Turns))
	}

	// Opt-in to system messages.
	status, env = apiJSONRequest(t, http.MethodGet,
		baseURL+"/api/v1/chat/"+conversationID+"/history?userId="+userID+"&include_roles=user,assistant,system", nil, nil)
	if status != http.StatusOK {
		t.Fatalf("opt-in status = %d, want 200", status)
	}
	hist = decodeSessionHistory(t, env.Data)
	if len(hist.Turns) != 3 {
		t.Fatalf("opt-in len(turns) = %d, want 3", len(hist.Turns))
	}
	if hist.Turns[0].Role != "system" {
		t.Fatalf("opt-in first role = %q, want system", hist.Turns[0].Role)
	}
}

// TestSessionHistory_Ownership verifies a different user cannot read
// another user's conversation transcript — the same ownership rule
// that protects the existing messages endpoint.
func TestSessionHistory_Ownership(t *testing.T) {
	baseURL := strings.TrimRight(blackboxBaseURL(), "/")
	ownerID := validUserFixture(t)
	intruderID := generateSeededUserFixture(t)
	conversationID := newConversationID()

	postMessage(t, baseURL, conversationID, ownerID, "user", "private", int32Ptr(1))

	status, env := apiJSONRequest(t, http.MethodGet,
		baseURL+"/api/v1/chat/"+conversationID+"/history?userId="+intruderID, nil, nil)
	if status != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", status, http.StatusNotFound)
	}
	assertErrorCode(t, env, "not_found")
}

// TestSessionHistory_EmptyConversationReturnsEmptyArray asserts that a
// conversation with no visible turns returns an empty array rather than
// 404 — so Orchestrator can treat "no prior context" as a normal case.
func TestSessionHistory_EmptyConversationReturnsEmptyArray(t *testing.T) {
	baseURL := strings.TrimRight(blackboxBaseURL(), "/")
	userID := validUserFixture(t)
	conversationID := newConversationID()

	// Seed only a system message; default role filter should exclude it.
	postMessage(t, baseURL, conversationID, userID, "system", "just a system note", int32Ptr(1))

	status, env := apiJSONRequest(t, http.MethodGet,
		baseURL+"/api/v1/chat/"+conversationID+"/history?userId="+userID, nil, nil)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	hist := decodeSessionHistory(t, env.Data)
	if len(hist.Turns) != 0 {
		t.Fatalf("len(turns) = %d, want 0", len(hist.Turns))
	}
	if hist.Truncated {
		t.Fatalf("truncated = true, want false when no turns exist")
	}
	if hist.TotalTokens != 0 {
		t.Fatalf("totalTokens = %d, want 0", hist.TotalTokens)
	}
}

// TestSessionHistory_MissingTokenCountFallsBackToContentLength proves that a
// missing token_count is estimated from content length so the budget trimmer
// still works for legacy messages that were persisted before the Agents team
// started emitting token counts.
func TestSessionHistory_MissingTokenCountFallsBackToContentLength(t *testing.T) {
	baseURL := strings.TrimRight(blackboxBaseURL(), "/")
	userID := validUserFixture(t)
	conversationID := newConversationID()

	// No token_count — the content is ~400 chars → ≈100 tokens.
	longContent := strings.Repeat("abcd ", 80)
	postMessage(t, baseURL, conversationID, userID, "user", longContent, nil)
	postMessage(t, baseURL, conversationID, userID, "assistant", "short reply", nil)

	url := fmt.Sprintf("%s/api/v1/chat/%s/history?userId=%s&token_budget=10", baseURL, conversationID, userID)
	status, env := apiJSONRequest(t, http.MethodGet, url, nil, nil)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	hist := decodeSessionHistory(t, env.Data)
	// The long message should be dropped by the budget; the short reply (~3
	// tokens) should remain.
	if !hist.Truncated {
		t.Fatalf("truncated = false, want true when estimated tokens exceed budget")
	}
	if len(hist.Turns) != 1 || hist.Turns[0].Content != "short reply" {
		t.Fatalf("turns = %+v, want only the short assistant reply", hist.Turns)
	}
}
