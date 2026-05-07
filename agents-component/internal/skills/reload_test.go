package skills_test

import (
	"sync"
	"sync/atomic"
	"testing"

	"github.com/cerberOS/agents-component/internal/skills"
	"github.com/cerberOS/agents-component/pkg/types"
)

// reloader casts mgr to skills.Reloader and fails the test if the assertion
// does not hold. All tests in this file expect the concrete hierarchyManager
// (returned by skills.New) to implement Reloader.
func reloader(t *testing.T, mgr skills.Manager) skills.Reloader {
	t.Helper()
	r, ok := mgr.(skills.Reloader)
	if !ok {
		t.Fatal("skills.Manager does not implement skills.Reloader")
	}
	return r
}

// dataDomain builds a minimal "data" domain with one local command.
func dataDomain() *types.SkillNode {
	return &types.SkillNode{
		Name:  "data",
		Level: "domain",
		Children: map[string]*types.SkillNode{
			"data.transform": {
				Name:           "data.transform",
				Level:          "command",
				Label:          "Data Transform",
				Description:    "Transform a local JSON value. Use to extract fields or measure length. Do NOT use for remote data sources.",
				TimeoutSeconds: 10,
				Spec: &types.SkillSpec{
					Parameters: map[string]types.ParameterDef{
						"data":      {Type: "string", Required: true, Description: "JSON value to transform."},
						"operation": {Type: "string", Required: true, Description: "Operation: extract, keys, or length."},
					},
				},
			},
		},
	}
}

// ---- Structural correctness tests ----

// TestReload_AddDomain verifies that a domain absent in the initial tree is
// discoverable after Reload injects it.
func TestReload_AddDomain(t *testing.T) {
	mgr := skills.New()
	if err := mgr.RegisterDomain(webDomain()); err != nil {
		t.Fatalf("setup: %v", err)
	}

	r := reloader(t, mgr)
	result, err := r.Reload([]*types.SkillNode{webDomain(), dataDomain()})
	if err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if len(result.Added) != 1 || result.Added[0] != "data" {
		t.Errorf("Added: want [data], got %v", result.Added)
	}

	if _, err := mgr.GetDomain("data"); err != nil {
		t.Errorf("GetDomain(data) after Reload: %v", err)
	}
	cmds, err := mgr.GetCommands("data")
	if err != nil {
		t.Fatalf("GetCommands(data): %v", err)
	}
	if len(cmds) != 1 {
		t.Errorf("want 1 command in data domain, got %d", len(cmds))
	}
}

// TestReload_RemoveDomain verifies that a domain present before Reload is no
// longer accessible after it is omitted from the new node list.
func TestReload_RemoveDomain(t *testing.T) {
	mgr := skills.New()
	if err := mgr.RegisterDomain(webDomain()); err != nil {
		t.Fatalf("setup web: %v", err)
	}
	if err := mgr.RegisterDomain(dataDomain()); err != nil {
		t.Fatalf("setup data: %v", err)
	}

	r := reloader(t, mgr)
	result, err := r.Reload([]*types.SkillNode{webDomain()}) // data omitted
	if err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if len(result.Removed) != 1 || result.Removed[0] != "data" {
		t.Errorf("Removed: want [data], got %v", result.Removed)
	}

	if _, err := mgr.GetDomain("data"); err == nil {
		t.Error("GetDomain(data) should return error after removal, got nil")
	}
}

// TestReload_ModifyDescription verifies that updating a command description is
// reflected immediately in GetCommands output.
func TestReload_ModifyDescription(t *testing.T) {
	mgr := skills.New()
	if err := mgr.RegisterDomain(webDomain()); err != nil {
		t.Fatalf("setup: %v", err)
	}

	updated := &types.SkillNode{
		Name:  "web",
		Level: "domain",
		Children: map[string]*types.SkillNode{
			"web.fetch": {
				Name:           "web.fetch",
				Level:          "command",
				Label:          "Web Fetch",
				Description:    "Fetch a web page via HTTP. UPDATED description for hot-reload test. Do NOT use for authenticated operations.",
				TimeoutSeconds: 30,
				Spec: &types.SkillSpec{
					Parameters: map[string]types.ParameterDef{
						"url":    {Type: "string", Required: true, Description: "URL to fetch."},
						"method": {Type: "string", Required: false, Description: "HTTP method."},
					},
				},
			},
		},
	}

	r := reloader(t, mgr)
	result, err := r.Reload([]*types.SkillNode{updated})
	if err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if len(result.Modified) != 1 || result.Modified[0] != "web" {
		t.Errorf("Modified: want [web], got %v", result.Modified)
	}

	cmds, err := mgr.GetCommands("web")
	if err != nil {
		t.Fatalf("GetCommands: %v", err)
	}
	if len(cmds) != 1 {
		t.Fatalf("want 1 command, got %d", len(cmds))
	}
	if cmds[0].Description != updated.Children["web.fetch"].Description {
		t.Errorf("description not updated: got %q", cmds[0].Description)
	}
}

// TestReload_AddCommandToExistingDomain verifies that a newly-added command is
// visible via GetCommands and GetSpec after Reload.
func TestReload_AddCommandToExistingDomain(t *testing.T) {
	mgr := skills.New()
	if err := mgr.RegisterDomain(webDomain()); err != nil {
		t.Fatalf("setup: %v", err)
	}

	webWithExtra := &types.SkillNode{
		Name:  "web",
		Level: "domain",
		Children: map[string]*types.SkillNode{
			"web.fetch": webDomain().Children["web.fetch"],
			"web.parse": {
				Name:           "web.parse",
				Level:          "command",
				Label:          "Web Parse",
				Description:    "Parse HTML content and extract structured data. Use after web_fetch. Do NOT use without fetched content.",
				TimeoutSeconds: 15,
				Spec: &types.SkillSpec{
					Parameters: map[string]types.ParameterDef{
						"html":     {Type: "string", Required: true, Description: "Raw HTML to parse."},
						"selector": {Type: "string", Required: true, Description: "CSS selector to extract."},
					},
				},
			},
		},
	}

	r := reloader(t, mgr)
	if _, err := r.Reload([]*types.SkillNode{webWithExtra}); err != nil {
		t.Fatalf("Reload: %v", err)
	}

	cmds, err := mgr.GetCommands("web")
	if err != nil {
		t.Fatalf("GetCommands: %v", err)
	}
	found := false
	for _, c := range cmds {
		if c.Name == "web.parse" {
			found = true
		}
	}
	if !found {
		t.Error("web.parse command not visible after Reload")
	}

	spec, err := mgr.GetSpec("web", "web.parse")
	if err != nil {
		t.Fatalf("GetSpec(web.parse): %v", err)
	}
	if _, ok := spec.Parameters["selector"]; !ok {
		t.Error("web.parse spec missing 'selector' parameter")
	}
}

// TestReload_RemovedCommandNotAccessible verifies that removing a command from
// a domain makes its spec inaccessible after Reload.
func TestReload_RemovedCommandNotAccessible(t *testing.T) {
	webWithExtra := &types.SkillNode{
		Name:  "web",
		Level: "domain",
		Children: map[string]*types.SkillNode{
			"web.fetch": webDomain().Children["web.fetch"],
			"web.parse": {
				Name:           "web.parse",
				Level:          "command",
				Label:          "Web Parse",
				Description:    "Parse HTML content. Use after fetching. Do NOT call without HTML input.",
				TimeoutSeconds: 15,
				Spec: &types.SkillSpec{
					Parameters: map[string]types.ParameterDef{
						"html": {Type: "string", Required: true, Description: "HTML to parse."},
					},
				},
			},
		},
	}

	mgr := skills.New()
	if err := mgr.RegisterDomain(webWithExtra); err != nil {
		t.Fatalf("setup: %v", err)
	}

	// Reload without web.parse — only web.fetch remains.
	r := reloader(t, mgr)
	if _, err := r.Reload([]*types.SkillNode{webDomain()}); err != nil {
		t.Fatalf("Reload: %v", err)
	}

	if _, err := mgr.GetSpec("web", "web.parse"); err == nil {
		t.Error("GetSpec(web.parse) should return error after removal, got nil")
	}
}

// ---- Validation / rejection tests ----

// TestReload_InvalidContractRejected verifies that a contract violation causes
// Reload to return an error and leaves the live tree intact.
func TestReload_InvalidContractRejected(t *testing.T) {
	mgr := skills.New()
	if err := mgr.RegisterDomain(webDomain()); err != nil {
		t.Fatalf("setup: %v", err)
	}

	// Build a domain with a command that violates the contract (missing label).
	bad := &types.SkillNode{
		Name:  "bad",
		Level: "domain",
		Children: map[string]*types.SkillNode{
			"bad.cmd": {
				Name:        "bad.cmd",
				Level:       "command",
				Label:       "", // violation: label required
				Description: "A bad command with no label. Do NOT use.",
			},
		},
	}

	r := reloader(t, mgr)
	_, err := r.Reload([]*types.SkillNode{bad})
	if err == nil {
		t.Fatal("Reload with invalid contract: expected error, got nil")
	}

	// Original tree must still be intact.
	if _, err := mgr.GetDomain("web"); err != nil {
		t.Errorf("GetDomain(web) after failed Reload: %v", err)
	}
	if _, err := mgr.GetDomain("bad"); err == nil {
		t.Error("GetDomain(bad) should be absent — Reload was rejected")
	}
}

// TestReload_EmptyDomainListClearsTree verifies that reloading with an empty
// list removes all domains.
func TestReload_EmptyDomainListClearsTree(t *testing.T) {
	mgr := skills.New()
	if err := mgr.RegisterDomain(webDomain()); err != nil {
		t.Fatalf("setup: %v", err)
	}

	r := reloader(t, mgr)
	result, err := r.Reload(nil)
	if err != nil {
		t.Fatalf("Reload(nil): %v", err)
	}
	if len(result.Removed) != 1 || result.Removed[0] != "web" {
		t.Errorf("Removed: want [web], got %v", result.Removed)
	}

	domains := mgr.ListDomains()
	if len(domains) != 0 {
		t.Errorf("ListDomains after clearing reload: want 0, got %v", domains)
	}
}

// TestReload_IdempotentReload verifies that reloading with the same tree
// produces an empty diff and leaves the tree accessible.
func TestReload_IdempotentReload(t *testing.T) {
	mgr := skills.New()
	if err := mgr.RegisterDomain(webDomain()); err != nil {
		t.Fatalf("setup: %v", err)
	}

	r := reloader(t, mgr)
	result, err := r.Reload([]*types.SkillNode{webDomain()})
	if err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if len(result.Added)+len(result.Removed)+len(result.Modified) != 0 {
		t.Errorf("idempotent reload: expected empty diff, got added=%v removed=%v modified=%v",
			result.Added, result.Removed, result.Modified)
	}

	// Tree still accessible after idempotent reload.
	if _, err := mgr.GetDomain("web"); err != nil {
		t.Errorf("GetDomain(web) after idempotent reload: %v", err)
	}
}

// ---- Embedding / search tests ----

// TestReload_SearchReflectsNewDescriptions verifies that the semantic search
// index is rebuilt after Reload so queries match new command descriptions.
func TestReload_SearchReflectsNewDescriptions(t *testing.T) {
	// Use a stub embedder that maps keyword presence to orthogonal vectors, so
	// ranking is deterministic regardless of hash collisions.
	stub := &stubEmbedder{
		dim: 4,
		vectors: map[string][]float64{
			"http":    {1, 0, 0, 0},
			"storage": {0, 1, 0, 0},
			"updated": {0, 0, 1, 0},
		},
	}
	mgr := skills.New(skills.WithEmbedder(stub))

	// Start with a web domain — description contains "http".
	if err := mgr.RegisterDomain(fetchDomain()); err != nil {
		t.Fatalf("setup: %v", err)
	}

	// Reload with updated description containing "updated" keyword.
	updatedWeb := &types.SkillNode{
		Name:  "web",
		Level: "domain",
		Children: map[string]*types.SkillNode{
			"web.fetch": {
				Name:           "web.fetch",
				Level:          "command",
				Label:          "Web Fetch",
				Description:    "Fetch content via HTTP. UPDATED for hot-reload. Do NOT use for authenticated calls.",
				TimeoutSeconds: 30,
				Spec: &types.SkillSpec{
					Parameters: map[string]types.ParameterDef{
						"url": {Type: "string", Required: true, Description: "URL to fetch."},
					},
				},
			},
		},
	}

	r := reloader(t, mgr)
	if _, err := r.Reload([]*types.SkillNode{updatedWeb}); err != nil {
		t.Fatalf("Reload: %v", err)
	}

	// Search for "updated" — the new description contains that word, so the
	// stub embedder maps it to [0,0,1,0]; the updated command maps to [0,0,1,0]
	// → score 1.0.
	results, err := mgr.Search("updated", 1)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("Search returned no results after reload with updated description")
	}
	if results[0].Name != "web.fetch" {
		t.Errorf("top result: want web.fetch, got %q", results[0].Name)
	}
}

// ---- Incremental embedding tests ----

// countingBatchEmbedder records how many individual texts have been embedded
// so tests can verify that unchanged commands are not re-embedded on Reload.
// It satisfies both skills.Embedder and skills.BatchEmbedder.
type countingBatchEmbedder struct {
	count int64 // atomic; incremented once per text embedded
	inner *stubEmbedder
}

func (c *countingBatchEmbedder) Embed(text string) ([]float64, error) {
	atomic.AddInt64(&c.count, 1)
	return c.inner.Embed(text)
}

func (c *countingBatchEmbedder) EmbedBatch(texts []string) ([][]float64, error) {
	atomic.AddInt64(&c.count, int64(len(texts)))
	vecs := make([][]float64, len(texts))
	for i, t := range texts {
		v, _ := c.inner.Embed(t)
		vecs[i] = v
	}
	return vecs, nil
}

func (c *countingBatchEmbedder) embedCount() int64 {
	return atomic.LoadInt64(&c.count)
}

// TestReload_IncrementalEmbedding verifies that reloading a tree where only
// one command changes calls the embedder exactly once — for the changed
// command only. Unchanged commands must reuse their existing vectors.
func TestReload_IncrementalEmbedding(t *testing.T) {
	ce := &countingBatchEmbedder{
		inner: &stubEmbedder{dim: 4, vectors: map[string][]float64{}},
	}
	mgr := skills.New(skills.WithEmbedder(ce))

	// Register a domain with two commands.
	twoCmd := &types.SkillNode{
		Name:  "web",
		Level: "domain",
		Children: map[string]*types.SkillNode{
			"web.fetch": webDomain().Children["web.fetch"],
			"web.parse": {
				Name:           "web.parse",
				Level:          "command",
				Label:          "Web Parse",
				Description:    "Parse HTML content and extract data. Use after web.fetch. Do NOT call without fetched HTML.",
				TimeoutSeconds: 15,
				Spec: &types.SkillSpec{
					Parameters: map[string]types.ParameterDef{
						"html": {Type: "string", Required: true, Description: "HTML to parse."},
					},
				},
			},
		},
	}
	if err := mgr.RegisterDomain(twoCmd); err != nil {
		t.Fatalf("RegisterDomain: %v", err)
	}

	afterRegister := ce.embedCount()
	if afterRegister != 2 {
		t.Fatalf("expected 2 embed calls after RegisterDomain, got %d", afterRegister)
	}

	// Reload with the same two commands plus one new command.
	// The embedder should be called exactly once — for the new command only.
	withNewCmd := &types.SkillNode{
		Name:  "web",
		Level: "domain",
		Children: map[string]*types.SkillNode{
			"web.fetch": twoCmd.Children["web.fetch"], // unchanged
			"web.parse": twoCmd.Children["web.parse"], // unchanged
			"web.search": {
				Name:           "web.search",
				Level:          "command",
				Label:          "Web Search",
				Description:    "Perform a web search and return result snippets. Use for discovery queries. Do NOT use to fetch full page content.",
				TimeoutSeconds: 20,
				Spec: &types.SkillSpec{
					Parameters: map[string]types.ParameterDef{
						"query": {Type: "string", Required: true, Description: "Search query string."},
					},
				},
			},
		},
	}

	r := reloader(t, mgr)
	if _, err := r.Reload([]*types.SkillNode{withNewCmd}); err != nil {
		t.Fatalf("Reload: %v", err)
	}

	afterReload := ce.embedCount() - afterRegister
	if afterReload != 1 {
		t.Errorf("expected 1 embed call during Reload (new command only), got %d", afterReload)
	}

	// All three commands must be discoverable after the reload.
	cmds, err := mgr.GetCommands("web")
	if err != nil {
		t.Fatalf("GetCommands: %v", err)
	}
	if len(cmds) != 3 {
		t.Errorf("expected 3 commands after Reload, got %d", len(cmds))
	}
}

// TestReload_IncrementalEmbedding_DescriptionChange verifies that modifying a
// command description causes exactly that command to be re-embedded.
func TestReload_IncrementalEmbedding_DescriptionChange(t *testing.T) {
	ce := &countingBatchEmbedder{
		inner: &stubEmbedder{dim: 4, vectors: map[string][]float64{}},
	}
	mgr := skills.New(skills.WithEmbedder(ce))

	if err := mgr.RegisterDomain(webDomain()); err != nil {
		t.Fatalf("RegisterDomain: %v", err)
	}
	afterRegister := ce.embedCount() // should be 1 (one command in webDomain)

	// Reload with an updated description for the same command.
	updatedWeb := &types.SkillNode{
		Name:  "web",
		Level: "domain",
		Children: map[string]*types.SkillNode{
			"web.fetch": {
				Name:           "web.fetch",
				Level:          "command",
				Label:          "Web Fetch",
				Description:    "Fetch a URL via HTTP. UPDATED. Do NOT use for authenticated operations.",
				TimeoutSeconds: 30,
				Spec:           webDomain().Children["web.fetch"].Spec,
			},
		},
	}

	r := reloader(t, mgr)
	if _, err := r.Reload([]*types.SkillNode{updatedWeb}); err != nil {
		t.Fatalf("Reload: %v", err)
	}

	afterReload := ce.embedCount() - afterRegister
	if afterReload != 1 {
		t.Errorf("expected 1 embed call for description change, got %d", afterReload)
	}
}

// TestReload_IncrementalEmbedding_Idempotent verifies that reloading with an
// identical tree calls the embedder zero times.
func TestReload_IncrementalEmbedding_Idempotent(t *testing.T) {
	ce := &countingBatchEmbedder{
		inner: &stubEmbedder{dim: 4, vectors: map[string][]float64{}},
	}
	mgr := skills.New(skills.WithEmbedder(ce))

	if err := mgr.RegisterDomain(webDomain()); err != nil {
		t.Fatalf("RegisterDomain: %v", err)
	}
	afterRegister := ce.embedCount()

	r := reloader(t, mgr)
	if _, err := r.Reload([]*types.SkillNode{webDomain()}); err != nil {
		t.Fatalf("Reload: %v", err)
	}

	afterReload := ce.embedCount() - afterRegister
	if afterReload != 0 {
		t.Errorf("idempotent reload: expected 0 embed calls, got %d", afterReload)
	}
}

// ---- Concurrency safety test ----

// TestReload_ConcurrentReadsDontRace verifies that concurrent GetDomain,
// GetCommands, and Search calls during a Reload do not produce a data race.
// Run with: go test -race ./internal/skills/...
func TestReload_ConcurrentReadsDontRace(t *testing.T) {
	mgr := skills.New()
	if err := mgr.RegisterDomain(webDomain()); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := mgr.RegisterDomain(dataDomain()); err != nil {
		t.Fatalf("setup: %v", err)
	}

	r := reloader(t, mgr)

	var wg sync.WaitGroup
	stop := make(chan struct{})

	// Launch concurrent readers.
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					_, _ = mgr.GetDomain("web")
					_, _ = mgr.GetCommands("web")
					_, _ = mgr.GetSpec("web", "web.fetch")
					_, _ = mgr.Search("fetch", 3)
					mgr.ListDomains()
				}
			}
		}()
	}

	// Perform several rapid reloads while readers are running.
	for i := 0; i < 20; i++ {
		nodes := []*types.SkillNode{webDomain(), dataDomain()}
		if i%2 == 0 {
			nodes = []*types.SkillNode{webDomain()} // alternately drop data domain
		}
		if _, err := r.Reload(nodes); err != nil {
			t.Errorf("Reload iteration %d: %v", i, err)
		}
	}

	close(stop)
	wg.Wait()
}
