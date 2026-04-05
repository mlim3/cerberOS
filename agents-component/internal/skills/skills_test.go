package skills_test

import (
	"strings"
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

// ---- stubEmbedder ----
// Returns a controllable fixed vector per keyword match. Used to make search
// ranking tests deterministic and independent of the default hash embedder.

type stubEmbedder struct {
	// vectors maps a keyword substring to the vector returned when the input
	// contains that keyword.
	vectors map[string][]float64
	dim     int
}

func (s *stubEmbedder) Embed(text string) ([]float64, error) {
	text = strings.ToLower(text)
	for kw, v := range s.vectors {
		if strings.Contains(text, kw) {
			return v, nil
		}
	}
	return make([]float64, s.dim), nil
}

// normalised2D returns a unit-normalised [a,b] vector.
func normalised2D(a, b float64) []float64 {
	norm := a*a + b*b
	if norm == 0 {
		return []float64{0, 0}
	}
	n := 1.0
	// simple: caller already passes normalised values for test clarity
	_ = n
	// just return as-is — tests pass pre-normalised values
	return []float64{a, b}
}

func fetchDomain() *types.SkillNode {
	return &types.SkillNode{
		Name:  "web",
		Level: "domain",
		Children: map[string]*types.SkillNode{
			"web.fetch": {
				Name:           "web.fetch",
				Level:          "command",
				Label:          "Web Fetch",
				Description:    "Fetch web page content via HTTP GET. Use for reading public URLs. Do NOT use for authenticated requests.",
				TimeoutSeconds: 30,
				Spec: &types.SkillSpec{
					Parameters: map[string]types.ParameterDef{
						"url": {Type: "string", Required: true, Description: "URL to fetch."},
					},
				},
			},
		},
	}
}

func emailDomain() *types.SkillNode {
	return &types.SkillNode{
		Name:  "comms",
		Level: "domain",
		Children: map[string]*types.SkillNode{
			"comms.send_email": {
				Name:           "comms.send_email",
				Level:          "command",
				Label:          "Send Email",
				Description:    "Send an email message via an authenticated SMTP channel. Do NOT use for Slack or webhooks.",
				TimeoutSeconds: 30,
				Spec: &types.SkillSpec{
					Parameters: map[string]types.ParameterDef{
						"to":      {Type: "string", Required: true, Description: "Recipient address."},
						"subject": {Type: "string", Required: true, Description: "Email subject line."},
						"body":    {Type: "string", Required: true, Description: "Email body."},
					},
				},
			},
		},
	}
}

// ---- Search tests ----

func TestSearch_EmptyQuery(t *testing.T) {
	m := skills.New()
	if err := m.RegisterDomain(webDomain()); err != nil {
		t.Fatal(err)
	}
	_, err := m.Search("", 3)
	if err == nil {
		t.Error("empty query: expected error, got nil")
	}
}

func TestSearch_EmptyIndex(t *testing.T) {
	m := skills.New()
	results, err := m.Search("fetch a web page", 3)
	if err != nil {
		t.Fatalf("empty index: unexpected error: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("empty index: want 0 results, got %d", len(results))
	}
}

func TestSearch_ReturnsAtMostTopK(t *testing.T) {
	m := skills.New()
	if err := m.RegisterDomain(fetchDomain()); err != nil {
		t.Fatal(err)
	}
	if err := m.RegisterDomain(emailDomain()); err != nil {
		t.Fatal(err)
	}

	results, err := m.Search("send message", 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 {
		t.Errorf("topK=1: want 1 result, got %d", len(results))
	}
}

func TestSearch_DefaultTopKWhenZero(t *testing.T) {
	m := skills.New()
	// Register enough commands (two domains, one command each).
	if err := m.RegisterDomain(fetchDomain()); err != nil {
		t.Fatal(err)
	}
	if err := m.RegisterDomain(emailDomain()); err != nil {
		t.Fatal(err)
	}
	// topK=0 should use the default (3); corpus only has 2, so expect 2.
	results, err := m.Search("some query", 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 2 {
		t.Errorf("default topK with 2 commands: want 2, got %d", len(results))
	}
}

func TestSearch_TopKLargerThanCorpus(t *testing.T) {
	m := skills.New()
	if err := m.RegisterDomain(fetchDomain()); err != nil {
		t.Fatal(err)
	}
	// Corpus has 1 command; requesting topK=10 should return 1.
	results, err := m.Search("fetch page", 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 {
		t.Errorf("topK > corpus: want 1, got %d", len(results))
	}
}

func TestSearch_ProgressiveDisclosure_NoSpec(t *testing.T) {
	m := skills.New()
	if err := m.RegisterDomain(webDomain()); err != nil {
		t.Fatal(err)
	}
	results, err := m.Search("web fetch", 3)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected at least 1 result")
	}
	for _, r := range results {
		if r.Name == "" {
			t.Error("result must have non-empty Name")
		}
		if r.Description == "" {
			t.Error("result must have non-empty Description")
		}
		if r.Domain == "" {
			t.Error("result must have non-empty Domain")
		}
	}
	// SkillSearchResult has no Spec field — the type contract enforces this.
}

func TestSearch_OrderedByDescendingScore(t *testing.T) {
	// Use a deterministic stub embedder so ranking is controlled.
	// "fetch" commands map to [1, 0]; "email" commands map to [0, 1].
	// Query "fetch" → [1, 0]: fetch command should rank first.
	stub := &stubEmbedder{
		dim: 2,
		vectors: map[string][]float64{
			"fetch": {1, 0},
			"email": {0, 1},
		},
	}
	m := skills.New(skills.WithEmbedder(stub))

	if err := m.RegisterDomain(fetchDomain()); err != nil {
		t.Fatal(err)
	}
	if err := m.RegisterDomain(emailDomain()); err != nil {
		t.Fatal(err)
	}

	results, err := m.Search("fetch", 2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("want 2 results, got %d", len(results))
	}
	if !strings.Contains(results[0].Name, "fetch") {
		t.Errorf("top result should be fetch-related; got %q", results[0].Name)
	}
	if results[0].Score < results[1].Score {
		t.Errorf("results not ordered: scores[0]=%f < scores[1]=%f", results[0].Score, results[1].Score)
	}
}

func TestSearch_WithCustomEmbedderCalled(t *testing.T) {
	callCount := 0
	stub := &countingEmbedder{delegate: newHashEmbedderForTest(), onEmbed: func() { callCount++ }}

	m := skills.New(skills.WithEmbedder(stub))
	if err := m.RegisterDomain(webDomain()); err != nil {
		t.Fatal(err)
	}
	before := callCount // RegisterDomain embeds command descriptions
	if before == 0 {
		t.Error("custom embedder not called during RegisterDomain")
	}

	if _, err := m.Search("fetch page", 3); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if callCount == before {
		t.Error("custom embedder not called during Search (query embedding)")
	}
}

// countingEmbedder wraps another Embedder and counts Embed calls.
type countingEmbedder struct {
	delegate skills.Embedder
	onEmbed  func()
}

func (c *countingEmbedder) Embed(text string) ([]float64, error) {
	c.onEmbed()
	return c.delegate.Embed(text)
}

// newHashEmbedderForTest returns the default embedder for use in tests.
// Since hashEmbedder is unexported, we reach it through New() with no options
// and call Search to confirm it embeds — but for the counting test we need
// an Embedder value directly. Use a thin wrapper around the manager instead.
//
// Actually: just use a stubEmbedder with a large known vector.
func newHashEmbedderForTest() skills.Embedder {
	return &stubEmbedder{dim: 512, vectors: map[string][]float64{}}
}

func TestSearch_ResultContainsDomainPath(t *testing.T) {
	m := skills.New()
	if err := m.RegisterDomain(fetchDomain()); err != nil {
		t.Fatal(err)
	}
	results, err := m.Search("fetch", 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected at least 1 result")
	}
	if results[0].Domain != "web" {
		t.Errorf("domain path: want %q, got %q", "web", results[0].Domain)
	}
}
