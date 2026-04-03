// Package skills is M4 — the Skill Hierarchy Manager. It owns the three-level
// skill tree (domain → command → spec) and enforces progressive disclosure:
// agents receive only domain names at spawn and drill down on demand.
//
// The manager also enforces the Tool Contract (EDD §13.2) at registration time:
// every command node must have a label, a description (≤300 chars), and all
// parameters must carry descriptions. Registrations that do not conform are
// rejected with a descriptive error.
package skills

import (
	"fmt"
	"sort"
	"sync"

	"github.com/cerberOS/agents-component/pkg/types"
)

const (
	maxToolNameLen   = 64  // §13.2: name max length
	maxDescLen       = 300 // §13.2: description max length
	maxTimeout       = 300 // §13.2: timeout_seconds hard max
	defaultSearchTop = 3   // §13.5: default number of search results
)

// Manager is the interface for skill tree operations.
type Manager interface {
	// RegisterDomain adds a top-level domain node (and its full subtree) to the
	// tree. Rejects any command-level child that does not satisfy the Tool Contract.
	// Pre-computes embeddings for all command descriptions at registration time.
	RegisterDomain(node *types.SkillNode) error

	// GetDomain returns the domain node by name (level=domain, no children exposed).
	GetDomain(name string) (*types.SkillNode, error)

	// GetCommands returns command-level children of a domain. Each node includes
	// label, description, required_credential_types, and timeout_seconds, but not
	// the parameter spec (progressive disclosure).
	GetCommands(domain string) ([]*types.SkillNode, error)

	// GetSpec returns the full parameter spec for a specific command.
	GetSpec(domain, command string) (*types.SkillSpec, error)

	// ListDomains returns the names of all registered domains.
	ListDomains() []string

	// Search performs semantic similarity search across all registered command
	// descriptions using pre-computed embeddings (EDD §13.5). Returns the top
	// topK matching commands. Each result contains only domain path, name, and
	// description — parameters are withheld per the progressive disclosure
	// contract. Call GetSpec to retrieve the full parameter schema for a command.
	// If topK is zero or negative the default (3) is used.
	// Returns an error when query is empty or the embedder fails.
	Search(query string, topK int) ([]types.SkillSearchResult, error)
}

// InvocationHook is called after each successful GetSpec call. It receives the
// domain and command names. Implementations must be non-blocking.
type InvocationHook func(domain, command string)

// Option configures a hierarchyManager.
type Option func(*hierarchyManager)

// WithEmbedder replaces the default feature-hashing embedder with a custom one.
// Use this to inject a high-quality remote model (e.g. voyage-3-lite) from a
// cmd/ binary where network calls are permitted. The embedder must be safe for
// concurrent use.
func WithEmbedder(e Embedder) Option {
	return func(m *hierarchyManager) {
		m.embedder = e
	}
}

// WithGetSpecHook registers a callback fired after every successful GetSpec call.
// Pass metrics.Recorder.ObserveSkillInvocation here to drive the
// skill_invocations_total Prometheus counter.
func WithGetSpecHook(h InvocationHook) Option {
	return func(m *hierarchyManager) {
		m.onGetSpec = h
	}
}

// commandEmbedding is a single entry in the in-memory embedding index.
type commandEmbedding struct {
	domain      string
	name        string
	description string
	vector      []float64
}

// hierarchyManager is the default in-memory implementation.
type hierarchyManager struct {
	mu         sync.RWMutex
	domains    map[string]*types.SkillNode
	embedder   Embedder
	embeddings []commandEmbedding // rebuilt incrementally as domains are registered
	onGetSpec  InvocationHook     // optional; called after each successful GetSpec
}

// New returns a ready-to-use Skill Hierarchy Manager.
// Zero or more Option values can be passed to override defaults (e.g. WithEmbedder).
func New(opts ...Option) Manager {
	m := &hierarchyManager{
		domains:  make(map[string]*types.SkillNode),
		embedder: newHashEmbedder(defaultHashDim),
	}
	for _, o := range opts {
		o(m)
	}
	return m
}

// RegisterDomain adds a domain and its command subtree. Every command-level child
// must pass validateCommandContract or the entire registration is rejected.
// Embeddings for all command descriptions are pre-computed and added to the
// in-memory search index (EDD §13.5).
func (m *hierarchyManager) RegisterDomain(node *types.SkillNode) error {
	if node == nil || node.Name == "" {
		return fmt.Errorf("skills: domain node must have a non-empty name")
	}
	if node.Level != "domain" {
		return fmt.Errorf("skills: expected level=domain, got %q", node.Level)
	}

	// Validate the Tool Contract for every command-level child before accepting
	// the registration. This is the enforcement point described in EDD §13.2.
	for _, child := range node.Children {
		if child.Level == "command" {
			if err := validateCommandContract(child); err != nil {
				return fmt.Errorf("skills: domain %q registration rejected: %w", node.Name, err)
			}
		}
	}

	// Pre-compute embeddings BEFORE taking the write lock so the (potentially
	// slow) embedding call does not block concurrent readers.
	newEntries := m.buildEmbeddings(node)

	m.mu.Lock()
	defer m.mu.Unlock()

	if _, exists := m.domains[node.Name]; exists {
		return fmt.Errorf("skills: domain %q already registered", node.Name)
	}
	m.domains[node.Name] = node
	m.embeddings = append(m.embeddings, newEntries...)
	return nil
}

// buildEmbeddings computes commandEmbedding entries for all command-level
// children of the domain. Called without holding the mutex.
// Uses embedTexts for batch embedding when the configured embedder supports it.
func (m *hierarchyManager) buildEmbeddings(domain *types.SkillNode) []commandEmbedding {
	type cmdMeta struct {
		name string
		desc string
	}
	var cmds []cmdMeta
	for _, c := range domain.Children {
		if c.Level == "command" {
			cmds = append(cmds, cmdMeta{name: c.Name, desc: c.Description})
		}
	}
	if len(cmds) == 0 {
		return nil
	}

	// Embed all command texts in a single batch call when possible.
	texts := make([]string, len(cmds))
	for i, c := range cmds {
		// Embed the command name and description together so both technical
		// identifiers and natural-language intent are captured.
		texts[i] = c.name + " " + c.desc
	}
	vecs := m.embedTexts(texts)

	var entries []commandEmbedding
	for i, c := range cmds {
		if i < len(vecs) && vecs[i] != nil {
			entries = append(entries, commandEmbedding{
				domain:      domain.Name,
				name:        c.name,
				description: c.desc,
				vector:      vecs[i],
			})
		}
		// Non-fatal: commands with nil vectors are excluded from search results
		// but structural queries (GetSpec etc.) still work.
	}
	return entries
}

// GetDomain returns a shallow copy of the domain node with no children exposed.
// Agents receive only the domain name at spawn — not its commands.
func (m *hierarchyManager) GetDomain(name string) (*types.SkillNode, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	d, ok := m.domains[name]
	if !ok {
		return nil, fmt.Errorf("skills: domain %q not found", name)
	}
	return &types.SkillNode{Name: d.Name, Level: d.Level}, nil
}

// GetCommands returns command-level metadata for the domain. Each returned node
// includes label, description, required_credential_types, and timeout_seconds so
// callers can present the command manifest — but the parameter spec is withheld
// until GetSpec is called (progressive disclosure).
func (m *hierarchyManager) GetCommands(domain string) ([]*types.SkillNode, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	d, ok := m.domains[domain]
	if !ok {
		return nil, fmt.Errorf("skills: domain %q not found", domain)
	}

	commands := make([]*types.SkillNode, 0, len(d.Children))
	for _, cmd := range d.Children {
		commands = append(commands, &types.SkillNode{
			Name:                    cmd.Name,
			Level:                   cmd.Level,
			Label:                   cmd.Label,
			Description:             cmd.Description,
			RequiredCredentialTypes: cmd.RequiredCredentialTypes,
			TimeoutSeconds:          cmd.TimeoutSeconds,
			// Spec intentionally withheld — call GetSpec for parameter detail.
		})
	}
	return commands, nil
}

// GetSpec returns the full parameter spec for a command. This is the deepest
// level of disclosure and is only served when the agent is constructing a call.
func (m *hierarchyManager) GetSpec(domain, command string) (*types.SkillSpec, error) {
	m.mu.RLock()

	d, ok := m.domains[domain]
	if !ok {
		m.mu.RUnlock()
		return nil, fmt.Errorf("skills: domain %q not found", domain)
	}
	cmd, ok := d.Children[command]
	if !ok {
		m.mu.RUnlock()
		return nil, fmt.Errorf("skills: command %q not found in domain %q", command, domain)
	}
	if cmd.Spec == nil {
		m.mu.RUnlock()
		return nil, fmt.Errorf("skills: no spec defined for %q.%q", domain, command)
	}
	spec := *cmd.Spec
	hook := m.onGetSpec
	m.mu.RUnlock()

	if hook != nil {
		hook(domain, command)
	}
	return &spec, nil
}

func (m *hierarchyManager) ListDomains() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	names := make([]string, 0, len(m.domains))
	for n := range m.domains {
		names = append(names, n)
	}
	return names
}

// Search finds the topK most semantically relevant commands across all registered
// domains and returns them ordered by descending similarity score (EDD §13.5).
//
// The query embedding is computed outside the read lock so a slow remote embedder
// does not block concurrent RegisterDomain or GetCommands calls. Results include
// only domain, name, and description — no parameter specs (progressive disclosure).
func (m *hierarchyManager) Search(query string, topK int) ([]types.SkillSearchResult, error) {
	if query == "" {
		return nil, fmt.Errorf("skills: search query must not be empty")
	}
	if topK <= 0 {
		topK = defaultSearchTop
	}

	// Embed the query outside the lock — may be slow for remote models.
	queryVec, err := m.embedder.Embed(query)
	if err != nil {
		return nil, fmt.Errorf("skills: embed query: %w", err)
	}

	m.mu.RLock()
	embeddings := m.embeddings // read slice header under lock; entries are immutable
	m.mu.RUnlock()

	if len(embeddings) == 0 {
		return []types.SkillSearchResult{}, nil
	}

	type scored struct {
		idx   int
		score float64
	}
	scores := make([]scored, len(embeddings))
	for i, emb := range embeddings {
		scores[i] = scored{idx: i, score: cosineSimilarity(queryVec, emb.vector)}
	}

	sort.Slice(scores, func(i, j int) bool {
		return scores[i].score > scores[j].score
	})

	if topK > len(scores) {
		topK = len(scores)
	}

	results := make([]types.SkillSearchResult, topK)
	for i := 0; i < topK; i++ {
		emb := embeddings[scores[i].idx]
		results[i] = types.SkillSearchResult{
			Domain:      emb.domain,
			Name:        emb.name,
			Description: emb.description,
			Score:       scores[i].score,
		}
	}
	return results, nil
}

// validateCommandContract enforces the Tool Contract from EDD §13.2 for a single
// command-level SkillNode. Returns a descriptive error for every violation found.
func validateCommandContract(node *types.SkillNode) error {
	// name: required, max 64 chars.
	if node.Name == "" {
		return fmt.Errorf("contract violation: command name is required")
	}
	if len(node.Name) > maxToolNameLen {
		return fmt.Errorf("contract violation: command name %q exceeds %d characters (%d)",
			node.Name, maxToolNameLen, len(node.Name))
	}

	// label: required — used in monitoring and audit logs, never shown to the LLM.
	if node.Label == "" {
		return fmt.Errorf("contract violation: label is required for command %q", node.Name)
	}

	// description: required, max 300 chars.
	if node.Description == "" {
		return fmt.Errorf("contract violation: description is required for command %q", node.Name)
	}
	if len(node.Description) > maxDescLen {
		return fmt.Errorf("contract violation: description for command %q exceeds %d characters (%d)",
			node.Name, maxDescLen, len(node.Description))
	}

	// parameters: every parameter must have a description.
	// Parameters without descriptions cause LLM hallucination (§13.2).
	if node.Spec != nil {
		for paramName, param := range node.Spec.Parameters {
			if param.Description == "" {
				return fmt.Errorf("contract violation: parameter %q in command %q has no description",
					paramName, node.Name)
			}
		}
	}

	// timeout_seconds: optional, but if set must be within the allowed range.
	if node.TimeoutSeconds < 0 || node.TimeoutSeconds > maxTimeout {
		return fmt.Errorf("contract violation: timeout_seconds for command %q must be 0–%d, got %d",
			node.Name, maxTimeout, node.TimeoutSeconds)
	}

	return nil
}
