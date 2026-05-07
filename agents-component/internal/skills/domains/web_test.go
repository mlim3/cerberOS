package domains_test

import (
	"hash/fnv"
	"math"
	"strings"
	"testing"
	"unicode"

	"github.com/cerberOS/agents-component/internal/skills"
	"github.com/cerberOS/agents-component/internal/skills/domains"
)

func TestWebDomain_PassesToolContract(t *testing.T) {
	domain := domains.WebDomain()

	if domain.Name != "web" {
		t.Fatalf("expected domain name 'web', got %q", domain.Name)
	}
	if domain.Level != "domain" {
		t.Fatalf("expected level 'domain', got %q", domain.Level)
	}

	expectedCommands := []string{"web.fetch", "web.search", "web.extract"}
	for _, name := range expectedCommands {
		cmd, ok := domain.Children[name]
		if !ok {
			t.Errorf("missing expected command %q", name)
			continue
		}
		if err := skills.ValidateCommandContract(cmd); err != nil {
			t.Errorf("command %q failed Tool Contract: %v", name, err)
		}
	}
}

func TestWebDomain_RegistersWithManager(t *testing.T) {
	mgr := skills.New()
	if err := mgr.RegisterDomain(domains.WebDomain()); err != nil {
		t.Fatalf("RegisterDomain failed: %v", err)
	}

	cmds, err := mgr.GetCommands("web")
	if err != nil {
		t.Fatalf("GetCommands('web') failed: %v", err)
	}
	if len(cmds) != 3 {
		t.Fatalf("expected 3 commands, got %d", len(cmds))
	}

	// Specs must be withheld at command level (progressive disclosure).
	for _, cmd := range cmds {
		if cmd.Spec != nil {
			t.Errorf("command %q: Spec should be withheld by GetCommands", cmd.Name)
		}
	}
}

func TestWebDomain_CredentialRouting(t *testing.T) {
	mgr := skills.New()
	if err := mgr.RegisterDomain(domains.WebDomain()); err != nil {
		t.Fatalf("RegisterDomain failed: %v", err)
	}

	cases := []struct {
		command         string
		wantVaultRouted bool // true == requires vault-delegated execution
	}{
		{"web.fetch", false},
		{"web.search", true},
		{"web.extract", false},
	}

	// Re-check directly from the domain node since GetCommands strips Spec.
	domain := domains.WebDomain()
	for _, tc := range cases {
		cmd, ok := domain.Children[tc.command]
		if !ok {
			t.Errorf("command %q not found", tc.command)
			continue
		}
		isVaultRouted := len(cmd.RequiredCredentialTypes) > 0
		if isVaultRouted != tc.wantVaultRouted {
			t.Errorf("command %q: wantVaultRouted=%v, got RequiredCredentialTypes=%v",
				tc.command, tc.wantVaultRouted, cmd.RequiredCredentialTypes)
		}
	}
}

func TestWebDomain_WebSearch_CredentialType(t *testing.T) {
	domain := domains.WebDomain()
	cmd, ok := domain.Children["web.search"]
	if !ok {
		t.Fatal("web.search not found")
	}
	if len(cmd.RequiredCredentialTypes) != 1 || cmd.RequiredCredentialTypes[0] != "search_api_key" {
		t.Errorf("web.search should require credential_type 'search_api_key', got %v", cmd.RequiredCredentialTypes)
	}
}

func TestWebDomain_GetSpecReturnsFullParams(t *testing.T) {
	mgr := skills.New()
	if err := mgr.RegisterDomain(domains.WebDomain()); err != nil {
		t.Fatalf("RegisterDomain failed: %v", err)
	}

	spec, err := mgr.GetSpec("web", "web.search")
	if err != nil {
		t.Fatalf("GetSpec failed: %v", err)
	}
	required := []string{"query", "max_results", "include_domains", "exclude_domains"}
	for _, p := range required {
		if _, ok := spec.Parameters[p]; !ok {
			t.Errorf("expected parameter %q in web.search spec", p)
		}
	}
	if !spec.Parameters["query"].Required {
		t.Error("web.search 'query' parameter should be required")
	}
	if spec.Parameters["max_results"].Required {
		t.Error("web.search 'max_results' parameter should not be required")
	}
}

func TestWebDomain_SearchFindsCorrectCommand(t *testing.T) {
	mgr := newSearchManager()
	if err := mgr.RegisterDomain(domains.WebDomain()); err != nil {
		t.Fatalf("RegisterDomain failed: %v", err)
	}

	tests := []struct {
		query string
		want  string
	}{
		{"search the web for news", "web.search"},
		{"fetch the content of a URL", "web.fetch"},
		{"extract text from a webpage", "web.extract"},
	}

	for _, tc := range tests {
		results, err := mgr.Search(tc.query, 3)
		if err != nil {
			t.Errorf("Search(%q) error: %v", tc.query, err)
			continue
		}
		if len(results) == 0 {
			t.Errorf("Search(%q) returned no results", tc.query)
			continue
		}
		if results[0].Name != tc.want {
			t.Errorf("Search(%q): expected top result %q, got %q (score %.3f)",
				tc.query, tc.want, results[0].Name, results[0].Score)
		}
	}
}

func TestWebDomain_TimeoutBoundsValid(t *testing.T) {
	domain := domains.WebDomain()
	for name, cmd := range domain.Children {
		if cmd.TimeoutSeconds <= 0 || cmd.TimeoutSeconds > 300 {
			t.Errorf("command %q has out-of-range TimeoutSeconds: %d", name, cmd.TimeoutSeconds)
		}
	}
}

func TestBothDomains_CanCoexistInManager(t *testing.T) {
	mgr := newSearchManager()
	if err := mgr.RegisterDomain(domains.LogsDomain()); err != nil {
		t.Fatalf("register logs domain: %v", err)
	}
	if err := mgr.RegisterDomain(domains.WebDomain()); err != nil {
		t.Fatalf("register web domain: %v", err)
	}

	domainNames := mgr.ListDomains()
	found := map[string]bool{}
	for _, d := range domainNames {
		found[d] = true
	}
	if !found["logs"] {
		t.Error("'logs' domain not listed after registration")
	}
	if !found["web"] {
		t.Error("'web' domain not listed after registration")
	}

	// Cross-domain search should return results from both domains.
	results, err := mgr.Search("search and fetch information", 6)
	if err != nil {
		t.Fatalf("cross-domain search failed: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected results from cross-domain search")
	}
}

func newSearchManager() skills.Manager {
	return skills.New(skills.WithEmbedder(&lexicalHashEmbedder{dim: 512}))
}

type lexicalHashEmbedder struct {
	dim int
}

func (e *lexicalHashEmbedder) Embed(text string) ([]float64, error) {
	tokens := tokenizeForTest(text)
	vec := make([]float64, e.dim)
	for _, token := range tokens {
		idx := int(hashTokenForTest(token) % uint32(e.dim))
		vec[idx]++
	}
	normalizeForTest(vec)
	return vec, nil
}

func tokenizeForTest(text string) []string {
	text = strings.ToLower(text)
	var unigrams []string
	var cur strings.Builder

	flush := func() {
		if cur.Len() >= 2 {
			unigrams = append(unigrams, cur.String())
		}
		cur.Reset()
	}

	for _, r := range text {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' {
			cur.WriteRune(r)
		} else {
			flush()
		}
	}
	flush()

	tokens := make([]string, 0, len(unigrams)*2)
	tokens = append(tokens, unigrams...)
	for i := 0; i < len(unigrams)-1; i++ {
		tokens = append(tokens, unigrams[i]+"_"+unigrams[i+1])
	}
	return tokens
}

func hashTokenForTest(token string) uint32 {
	h := fnv.New32a()
	_, _ = h.Write([]byte(token))
	return h.Sum32()
}

func normalizeForTest(vec []float64) {
	sum := 0.0
	for _, v := range vec {
		sum += v * v
	}
	if sum == 0 {
		return
	}
	norm := math.Sqrt(sum)
	for i := range vec {
		vec[i] /= norm
	}
}
