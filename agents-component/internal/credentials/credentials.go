// Package credentials is M5 — the Credential Broker. It is a request builder only.
// It formats and tags credential request payloads scoped to task requirements,
// then hands them to internal/comms for delivery to the Orchestrator. The
// Orchestrator proxies those requests to the Credential Vault (OpenBao). This
// package does NOT call OpenBao or any external API directly — that is a
// security violation and must not be introduced.
package credentials

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/cerberOS/agents-component/internal/comms"
	"github.com/cerberOS/agents-component/pkg/types"
)

// Broker is the interface for the two-phase credential lifecycle.
type Broker interface {
	// PreAuthorize registers the permission set for an agent at spawn time and
	// returns an opaque permission_token reference from the Vault. The token is
	// stored internally; the agent receives only a pointer, not the token itself.
	//
	// skillDomains are the required skill domains passed to the Vault for scope
	// resolution (e.g. ["web", "data"]). The Vault registers a scoped policy and
	// returns an opaque permission_token — never a raw credential value.
	PreAuthorize(agentID, taskID string, skillDomains []string) (permissionToken string, err error)

	// GetCredential delivers a single credential value to an agent. It validates
	// that the requested key is within the pre-authorized permission set before
	// querying the vault.
	GetCredential(agentID, credentialKey string) (value string, err error)

	// Revoke invalidates the vault token and removes all pre-authorized state
	// for an agent. Called at agent termination.
	Revoke(agentID string) error
}

// agentAuth holds pre-authorized state for a single agent.
type agentAuth struct {
	vaultToken    string
	permissionSet map[string]struct{}
}

// — Stub broker (in-process, for unit tests) ———————————————————————————————

// stubBroker is the default implementation backed by in-process maps.
// Used in unit tests only. The production implementation is natsBroker.
type stubBroker struct {
	mu          sync.RWMutex
	agents      map[string]*agentAuth
	stubSecrets map[string]string
}

// New returns a Credential Broker backed by an in-process stub vault.
// Seed stubSecrets with test credentials as needed in tests.
func New(stubSecrets map[string]string) Broker {
	if stubSecrets == nil {
		stubSecrets = make(map[string]string)
	}
	return &stubBroker{
		agents:      make(map[string]*agentAuth),
		stubSecrets: stubSecrets,
	}
}

func (b *stubBroker) PreAuthorize(agentID, taskID string, skillDomains []string) (string, error) {
	if agentID == "" {
		return "", fmt.Errorf("credentials: agentID must not be empty")
	}
	if len(skillDomains) == 0 {
		return "", fmt.Errorf("credentials: skillDomains must not be empty")
	}

	token := "stub-token-" + agentID // deterministic for tests; production token comes from Orchestrator credential_response

	perms := make(map[string]struct{}, len(skillDomains))
	for _, p := range skillDomains {
		perms[p] = struct{}{}
	}

	b.mu.Lock()
	b.agents[agentID] = &agentAuth{vaultToken: token, permissionSet: perms}
	b.mu.Unlock()

	return token, nil
}

func (b *stubBroker) GetCredential(agentID, credentialKey string) (string, error) {
	b.mu.RLock()
	auth, ok := b.agents[agentID]
	b.mu.RUnlock()

	if !ok {
		return "", fmt.Errorf("credentials: agent %q has no pre-authorized permission set", agentID)
	}
	if _, allowed := auth.permissionSet[credentialKey]; !allowed {
		return "", fmt.Errorf("credentials: key %q not in permission set for agent %q", credentialKey, agentID)
	}

	val, found := b.stubSecrets[credentialKey]
	if !found {
		return "", fmt.Errorf("credentials: key %q not found in vault", credentialKey)
	}
	return val, nil
}

func (b *stubBroker) Revoke(agentID string) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	if _, ok := b.agents[agentID]; !ok {
		return fmt.Errorf("credentials: agent %q not found", agentID)
	}
	delete(b.agents, agentID)
	return nil
}

// — NATS broker (production) ———————————————————————————————————————————————

// credResult carries the outcome of a single credential.response.
type credResult struct {
	token string
	err   error
}

const (
	credAuthMaxAttempts = 3
	credAuthTimeout     = 5 * time.Second
	credAuthBaseBackoff = time.Second
	credAuthDefaultTTL  = 3600
)

// natsBroker is the production implementation. It performs the full round-trip:
// publishes credential.request (operation: authorize) to the Orchestrator via
// NATS JetStream and awaits a credential.response filtered by request_id, with
// exponential backoff retries before declaring VAULT_UNREACHABLE.
//
// It never calls OpenBao directly. All communication routes through the
// Orchestrator via the comms.Client interface.
type natsBroker struct {
	comms comms.Client

	mu     sync.RWMutex
	agents map[string]*agentAuth // agentID → stored permission token reference

	pendingMu sync.Mutex
	pending   map[string]chan credResult // requestID → waiting PreAuthorize goroutine
}

// NewNATSBroker returns a Credential Broker that performs the real credential
// pre-authorization round-trip over NATS. It subscribes to
// aegis.agents.credential.response with a durable consumer at construction time
// so responses are never missed, then routes each response to the waiting
// PreAuthorize goroutine by request_id.
func NewNATSBroker(c comms.Client) (Broker, error) {
	b := &natsBroker{
		comms:   c,
		agents:  make(map[string]*agentAuth),
		pending: make(map[string]chan credResult),
	}
	if err := c.SubscribeDurable(
		comms.SubjectCredentialResponse,
		comms.ConsumerCredentialResponse,
		b.handleResponse,
	); err != nil {
		return nil, fmt.Errorf("credentials: subscribe %q: %w", comms.SubjectCredentialResponse, err)
	}
	return b, nil
}

// PreAuthorize publishes a credential.request (operation: authorize) to the
// Orchestrator and blocks until a credential.response arrives or the retry
// budget is exhausted.
//
// Retry policy: up to credAuthMaxAttempts attempts, each with a credAuthTimeout
// deadline and exponential backoff (1s, 2s, 4s, …) between attempts.
// Returns VAULT_UNREACHABLE after all attempts fail.
func (b *natsBroker) PreAuthorize(agentID, taskID string, skillDomains []string) (string, error) {
	if agentID == "" {
		return "", fmt.Errorf("credentials: agentID must not be empty")
	}
	if taskID == "" {
		return "", fmt.Errorf("credentials: taskID must not be empty")
	}
	if len(skillDomains) == 0 {
		return "", fmt.Errorf("credentials: skillDomains must not be empty")
	}

	requestID := newCredentialID()

	req := types.CredentialRequest{
		RequestID:    requestID,
		AgentID:      agentID,
		TaskID:       taskID,
		Operation:    "authorize",
		SkillDomains: skillDomains,
		TTLSeconds:   credAuthDefaultTTL,
	}

	// Register the result channel BEFORE the first publish so a fast response
	// from the Orchestrator is never lost in the window between publish and listen.
	resultCh := make(chan credResult, 1)
	b.pendingMu.Lock()
	b.pending[requestID] = resultCh
	b.pendingMu.Unlock()
	defer func() {
		b.pendingMu.Lock()
		delete(b.pending, requestID)
		b.pendingMu.Unlock()
	}()

	var lastErr error
	for attempt := 0; attempt < credAuthMaxAttempts; attempt++ {
		if attempt > 0 {
			// Exponential backoff: 1s, 2s, 4s, … between retries.
			backoff := credAuthBaseBackoff * time.Duration(1<<uint(attempt-1))
			time.Sleep(backoff)
		}

		if err := b.comms.Publish(
			comms.SubjectCredentialRequest,
			comms.PublishOptions{
				MessageType:   comms.MsgTypeCredentialRequest,
				CorrelationID: requestID,
			},
			req,
		); err != nil {
			lastErr = fmt.Errorf("credentials: publish credential.request (attempt %d/%d): %w",
				attempt+1, credAuthMaxAttempts, err)
			continue
		}

		timer := time.NewTimer(credAuthTimeout)
		select {
		case result := <-resultCh:
			timer.Stop()
			if result.err != nil {
				// Vault returned denied or error — do not retry; propagate immediately.
				return "", result.err
			}
			// Granted: store permission token reference for this agent.
			b.mu.Lock()
			b.agents[agentID] = &agentAuth{
				vaultToken:    result.token,
				permissionSet: domainsToPermSet(skillDomains),
			}
			b.mu.Unlock()
			return result.token, nil

		case <-timer.C:
			lastErr = fmt.Errorf("credentials: credential.response timeout (attempt %d/%d)",
				attempt+1, credAuthMaxAttempts)
		}
	}

	return "", fmt.Errorf("credentials: VAULT_UNREACHABLE after %d attempts: %w",
		credAuthMaxAttempts, lastErr)
}

// GetCredential is not supported in the NATS broker. Credential access is
// exclusively via vault.execute.request (ADR-004, vault-delegated execution).
func (b *natsBroker) GetCredential(agentID, credentialKey string) (string, error) {
	return "", fmt.Errorf("credentials: GetCredential is not supported; use vault.execute.request for credentialed operations (ADR-004)")
}

// Revoke publishes a credential.request (operation: revoke) to the Orchestrator
// as fire-and-forget and removes the agent's local state. No response is expected.
func (b *natsBroker) Revoke(agentID string) error {
	b.mu.Lock()
	delete(b.agents, agentID)
	b.mu.Unlock()

	req := types.CredentialRequest{
		RequestID: newCredentialID(),
		AgentID:   agentID,
		Operation: "revoke",
	}
	return b.comms.Publish(
		comms.SubjectCredentialRequest,
		comms.PublishOptions{
			MessageType:   comms.MsgTypeCredentialRequest,
			CorrelationID: req.RequestID,
		},
		req,
	)
}

// handleResponse is the durable subscription handler for aegis.agents.credential.response.
// It routes the response to the goroutine blocked in PreAuthorize by matching on
// the envelope correlation_id (primary) or the payload request_id (fallback).
func (b *natsBroker) handleResponse(msg *comms.Message) {
	var resp types.CredentialResponse
	if err := json.Unmarshal(msg.Data, &resp); err != nil {
		// Malformed response — ack to avoid redelivery loop; nothing we can do.
		_ = msg.Ack()
		return
	}

	// Prefer the envelope-level correlation_id; fall back to the payload field so
	// the broker works correctly even when the stub client does not populate it.
	requestID := msg.CorrelationID
	if requestID == "" {
		requestID = resp.RequestID
	}
	if requestID == "" {
		_ = msg.Ack()
		return
	}

	b.pendingMu.Lock()
	ch, ok := b.pending[requestID]
	b.pendingMu.Unlock()

	if !ok {
		// Response arrived after the deadline or for an unknown request — ack and drop.
		_ = msg.Ack()
		return
	}

	var result credResult
	if resp.Status == "granted" {
		result.token = resp.PermissionToken
	} else {
		errMsg := resp.ErrorMessage
		if errMsg == "" {
			errMsg = resp.Status
		}
		result.err = fmt.Errorf("credentials: authorization %s (code: %s): %s",
			resp.Status, resp.ErrorCode, errMsg)
	}

	// Non-blocking send: if the channel already has a result (duplicate delivery),
	// drop the duplicate and ack.
	select {
	case ch <- result:
	default:
	}
	_ = msg.Ack()
}

// domainsToPermSet converts skill domain names to the internal permission-set map
// used for scope validation.
func domainsToPermSet(domains []string) map[string]struct{} {
	perms := make(map[string]struct{}, len(domains))
	for _, d := range domains {
		perms[d] = struct{}{}
	}
	return perms
}

// newCredentialID returns a UUID v4 string using crypto/rand.
func newCredentialID() string {
	var b [16]byte
	_, _ = io.ReadFull(rand.Reader, b[:])
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant bits
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}
