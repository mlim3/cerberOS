package skills_test

import (
	"testing"

	"github.com/cerberOS/agents-component/internal/skills"
	"github.com/cerberOS/agents-component/pkg/types"
)

func webDomain() *types.SkillNode {
	return &types.SkillNode{
		Name:  "web",
		Level: "domain",
		Children: map[string]*types.SkillNode{
			"web.fetch": {
				Name:           "web.fetch",
				Level:          "command",
				Label:          "Web Fetch",
				Description:    "Fetch the content of a URL via HTTP. Use for web pages and APIs without authentication. Do NOT use for authenticated operations.",
				TimeoutSeconds: 30,
				Spec: &types.SkillSpec{
					Parameters: map[string]types.ParameterDef{
						"url":    {Type: "string", Required: true, Description: "The fully-qualified URL to fetch."},
						"method": {Type: "string", Required: false, Description: "HTTP method: GET or POST. Defaults to GET."},
					},
				},
			},
		},
	}
}

func TestRegisterAndGetDomain(t *testing.T) {
	m := skills.New()
	if err := m.RegisterDomain(webDomain()); err != nil {
		t.Fatalf("RegisterDomain: %v", err)
	}

	d, err := m.GetDomain("web")
	if err != nil {
		t.Fatalf("GetDomain: %v", err)
	}
	if d.Name != "web" {
		t.Errorf("got name %q, want %q", d.Name, "web")
	}
	if len(d.Children) != 0 {
		t.Error("GetDomain must not expose children")
	}
}

func TestGetCommands(t *testing.T) {
	m := skills.New()
	m.RegisterDomain(webDomain())

	cmds, err := m.GetCommands("web")
	if err != nil {
		t.Fatalf("GetCommands: %v", err)
	}
	if len(cmds) != 1 || cmds[0].Name != "web.fetch" {
		t.Errorf("unexpected commands: %+v", cmds)
	}
	if cmds[0].Spec != nil {
		t.Error("GetCommands must not expose specs")
	}
}

func TestGetSpec(t *testing.T) {
	m := skills.New()
	m.RegisterDomain(webDomain())

	spec, err := m.GetSpec("web", "web.fetch")
	if err != nil {
		t.Fatalf("GetSpec: %v", err)
	}
	if _, ok := spec.Parameters["url"]; !ok {
		t.Error("expected 'url' parameter in spec")
	}
}

func TestRegisterDomainWrongLevel(t *testing.T) {
	m := skills.New()
	node := &types.SkillNode{Name: "web", Level: "command"}
	if err := m.RegisterDomain(node); err == nil {
		t.Error("expected error for wrong level, got nil")
	}
}

// --- Tool Contract enforcement tests (EDD §13.2) ---

func commandWithout(field string) *types.SkillNode {
	cmd := &types.SkillNode{
		Name:           "web.fetch",
		Level:          "command",
		Label:          "Web Fetch",
		Description:    "Fetch a URL. Use for unauthenticated HTTP. Do NOT use for authenticated operations.",
		TimeoutSeconds: 30,
		Spec: &types.SkillSpec{
			Parameters: map[string]types.ParameterDef{
				"url": {Type: "string", Required: true, Description: "URL to fetch."},
			},
		},
	}
	switch field {
	case "label":
		cmd.Label = ""
	case "description":
		cmd.Description = ""
	case "param_description":
		cmd.Spec.Parameters["url"] = types.ParameterDef{Type: "string", Required: true} // no Description
	case "timeout_invalid":
		cmd.TimeoutSeconds = 999
	}
	return cmd
}

func domainWith(cmd *types.SkillNode) *types.SkillNode {
	return &types.SkillNode{
		Name:     "web",
		Level:    "domain",
		Children: map[string]*types.SkillNode{cmd.Name: cmd},
	}
}

func TestContractMissingLabel(t *testing.T) {
	m := skills.New()
	if err := m.RegisterDomain(domainWith(commandWithout("label"))); err == nil {
		t.Error("expected error for missing label, got nil")
	}
}

func TestContractMissingDescription(t *testing.T) {
	m := skills.New()
	if err := m.RegisterDomain(domainWith(commandWithout("description"))); err == nil {
		t.Error("expected error for missing description, got nil")
	}
}

func TestContractMissingParameterDescription(t *testing.T) {
	m := skills.New()
	if err := m.RegisterDomain(domainWith(commandWithout("param_description"))); err == nil {
		t.Error("expected error for parameter with no description, got nil")
	}
}

func TestContractTimeoutOutOfRange(t *testing.T) {
	m := skills.New()
	if err := m.RegisterDomain(domainWith(commandWithout("timeout_invalid"))); err == nil {
		t.Error("expected error for timeout > 300, got nil")
	}
}

// --- Search tests (EDD §13.5) ---

func multiCommandDomain() *types.SkillNode {
	return &types.SkillNode{
		Name:  "web",
		Level: "domain",
		Children: map[string]*types.SkillNode{
			"web_fetch": {
				Name:        "web_fetch",
				Level:       "command",
				Label:       "Web Fetch",
				Description: "Fetch the content of a URL via HTTP. Use for public web pages and unauthenticated APIs. Do NOT use for authenticated operations.",
				Spec: &types.SkillSpec{
					Parameters: map[string]types.ParameterDef{
						"url": {Type: "string", Required: true, Description: "URL to fetch."},
					},
				},
			},
			"vault_web_fetch": {
				Name:                    "vault_web_fetch",
				Level:                   "command",
				Label:                   "Vault Web Fetch",
				RequiredCredentialTypes: []string{"web_api_key"},
				Description:             "Fetch a URL using a stored API credential via the Vault. Use for authenticated HTTP requests requiring an API key. Do NOT use for public URLs.",
				Spec: &types.SkillSpec{
					Parameters: map[string]types.ParameterDef{
						"url": {Type: "string", Required: true, Description: "URL to fetch."},
					},
				},
			},
		},
	}
}

func storageDomain() *types.SkillNode {
	return &types.SkillNode{
		Name:  "storage",
		Level: "domain",
		Children: map[string]*types.SkillNode{
			"storage_read": {
				Name:        "storage_read",
				Level:       "command",
				Label:       "Storage Read",
				Description: "Read a file or object from persistent storage. Use for retrieving saved data. Do NOT use for web requests.",
			},
		},
	}
}

func TestSearch_EmptyQueryReturnsError(t *testing.T) {
	m := skills.New()
	m.RegisterDomain(multiCommandDomain())

	_, err := m.Search("", 3)
	if err == nil {
		t.Error("expected error for empty query, got nil")
	}
}

func TestSearch_EmptyIndexReturnsEmptySlice(t *testing.T) {
	m := skills.New()

	results, err := m.Search("fetch a URL", 3)
	if err != nil {
		t.Fatalf("Search on empty index: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected empty results, got %d", len(results))
	}
}

func TestSearch_ResultsAreDescendingScore(t *testing.T) {
	m := skills.New()
	if err := m.RegisterDomain(multiCommandDomain()); err != nil {
		t.Fatalf("RegisterDomain: %v", err)
	}

	results, err := m.Search("HTTP fetch URL", 5)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	for i := 1; i < len(results); i++ {
		if results[i].Score > results[i-1].Score {
			t.Errorf("results not sorted: score[%d]=%.4f > score[%d]=%.4f",
				i, results[i].Score, i-1, results[i-1].Score)
		}
	}
}

func TestSearch_TopKLimitsResults(t *testing.T) {
	m := skills.New()
	if err := m.RegisterDomain(multiCommandDomain()); err != nil {
		t.Fatalf("RegisterDomain: %v", err)
	}

	results, err := m.Search("fetch URL", 1)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 1 {
		t.Errorf("expected 1 result with topK=1, got %d", len(results))
	}
}

func TestSearch_ZeroTopKUsesDefault(t *testing.T) {
	m := skills.New()
	// Register two domains to get more than 3 commands total.
	if err := m.RegisterDomain(multiCommandDomain()); err != nil {
		t.Fatalf("RegisterDomain web: %v", err)
	}
	if err := m.RegisterDomain(storageDomain()); err != nil {
		t.Fatalf("RegisterDomain storage: %v", err)
	}

	results, err := m.Search("fetch URL", 0)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	// 3 commands total — default topK=3, so expect all 3.
	if len(results) != 3 {
		t.Errorf("expected 3 results with topK=0 (default), got %d", len(results))
	}
}

func TestSearch_TopKLargerThanIndexReturnsAll(t *testing.T) {
	m := skills.New()
	if err := m.RegisterDomain(multiCommandDomain()); err != nil {
		t.Fatalf("RegisterDomain: %v", err)
	}

	results, err := m.Search("fetch URL", 100)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 2 {
		t.Errorf("expected 2 results (all commands), got %d", len(results))
	}
}

func TestSearch_ProgressiveDisclosure_NoSpec(t *testing.T) {
	m := skills.New()
	if err := m.RegisterDomain(multiCommandDomain()); err != nil {
		t.Fatalf("RegisterDomain: %v", err)
	}

	results, err := m.Search("fetch URL", 5)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	for _, r := range results {
		if r.Domain == "" {
			t.Error("result must have Domain")
		}
		if r.Name == "" {
			t.Error("result must have Name")
		}
		if r.Description == "" {
			t.Error("result must have Description")
		}
		// SkillSearchResult has no Spec field by design — nothing to check here,
		// but confirm the result fields are the only ones present.
	}
}

func TestSearch_CrossDomain_MatchesCorrectDomain(t *testing.T) {
	m := skills.New()
	if err := m.RegisterDomain(multiCommandDomain()); err != nil {
		t.Fatalf("RegisterDomain web: %v", err)
	}
	if err := m.RegisterDomain(storageDomain()); err != nil {
		t.Fatalf("RegisterDomain storage: %v", err)
	}

	// "storage_read" contains the unique token "storage" — it should appear in results.
	results, err := m.Search("storage read persistent data", 3)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	found := false
	for _, r := range results {
		if r.Domain == "storage" && r.Name == "storage_read" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected storage.storage_read in results for storage query; got %+v", results)
	}
}

func TestSearch_AuthenticatedQueryRanksVaultToolFirst(t *testing.T) {
	m := skills.New()
	if err := m.RegisterDomain(multiCommandDomain()); err != nil {
		t.Fatalf("RegisterDomain: %v", err)
	}

	// "authenticated API key" shares tokens with vault_web_fetch's description.
	results, err := m.Search("authenticated API key credential", 2)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected results, got none")
	}
	if results[0].Name != "vault_web_fetch" {
		t.Errorf("expected vault_web_fetch as top result for authenticated query, got %q (score=%.4f)",
			results[0].Name, results[0].Score)
	}
}

func TestSearch_ResultDomainMatchesRegistration(t *testing.T) {
	m := skills.New()
	if err := m.RegisterDomain(storageDomain()); err != nil {
		t.Fatalf("RegisterDomain: %v", err)
	}

	results, err := m.Search("read file data", 1)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Domain != "storage" {
		t.Errorf("expected domain %q, got %q", "storage", results[0].Domain)
	}
}

func TestGetCommandsExposesContractFields(t *testing.T) {
	m := skills.New()
	if err := m.RegisterDomain(webDomain()); err != nil {
		t.Fatalf("RegisterDomain: %v", err)
	}
	cmds, err := m.GetCommands("web")
	if err != nil {
		t.Fatalf("GetCommands: %v", err)
	}
	if len(cmds) != 1 {
		t.Fatalf("expected 1 command, got %d", len(cmds))
	}
	cmd := cmds[0]
	if cmd.Label == "" {
		t.Error("GetCommands must expose Label")
	}
	if cmd.Description == "" {
		t.Error("GetCommands must expose Description")
	}
	if cmd.Spec != nil {
		t.Error("GetCommands must not expose Spec")
	}
}
