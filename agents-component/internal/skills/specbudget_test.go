package skills_test

import (
	"testing"

	"github.com/cerberOS/agents-component/internal/skills"
	"github.com/cerberOS/agents-component/pkg/types"
)

// ---- helpers ----

func newBudget(t *testing.T, ceiling int) *skills.SpecBudget {
	t.Helper()
	b, err := skills.NewSpecBudget(ceiling)
	if err != nil {
		t.Fatalf("NewSpecBudget(%d): %v", ceiling, err)
	}
	return b
}

// ---- constructor tests ----

func TestNewSpecBudget_Valid(t *testing.T) {
	b := newBudget(t, 1000)
	if b.Ceiling() != 1000 {
		t.Errorf("Ceiling: want 1000, got %d", b.Ceiling())
	}
	if b.Used() != 0 {
		t.Errorf("Used initially: want 0, got %d", b.Used())
	}
	if b.Remaining() != 1000 {
		t.Errorf("Remaining initially: want 1000, got %d", b.Remaining())
	}
}

func TestNewSpecBudget_ZeroCeiling(t *testing.T) {
	if _, err := skills.NewSpecBudget(0); err == nil {
		t.Error("NewSpecBudget(0): expected error, got nil")
	}
}

func TestNewSpecBudget_NegativeCeiling(t *testing.T) {
	if _, err := skills.NewSpecBudget(-1); err == nil {
		t.Error("NewSpecBudget(-1): expected error, got nil")
	}
}

// ---- LoadSpec basic tests ----

func TestLoadSpec_SingleSpec(t *testing.T) {
	b := newBudget(t, 100)
	evicted, err := b.LoadSpec("web", "web.fetch", 40)
	if err != nil {
		t.Fatalf("LoadSpec: %v", err)
	}
	if len(evicted) != 0 {
		t.Errorf("evicted: want 0, got %v", evicted)
	}
	if b.Used() != 40 {
		t.Errorf("Used: want 40, got %d", b.Used())
	}
	if b.Remaining() != 60 {
		t.Errorf("Remaining: want 60, got %d", b.Remaining())
	}
	if !b.IsLoaded("web", "web.fetch") {
		t.Error("IsLoaded(web, web.fetch): want true, got false")
	}
}

func TestLoadSpec_ExactlyFillsCeiling(t *testing.T) {
	b := newBudget(t, 100)
	_, err := b.LoadSpec("web", "web.fetch", 100)
	if err != nil {
		t.Fatalf("LoadSpec at ceiling: %v", err)
	}
	if b.Remaining() != 0 {
		t.Errorf("Remaining: want 0, got %d", b.Remaining())
	}
}

func TestLoadSpec_ZeroTokenSpec(t *testing.T) {
	b := newBudget(t, 100)
	evicted, err := b.LoadSpec("web", "web.fetch", 0)
	if err != nil {
		t.Fatalf("LoadSpec(0 tokens): %v", err)
	}
	if len(evicted) != 0 {
		t.Errorf("evicted: want 0, got %v", evicted)
	}
	if b.Used() != 0 {
		t.Errorf("Used after zero-cost spec: want 0, got %d", b.Used())
	}
	if !b.IsLoaded("web", "web.fetch") {
		t.Error("IsLoaded: want true for zero-cost spec")
	}
}

func TestLoadSpec_NegativeTokens_Error(t *testing.T) {
	b := newBudget(t, 100)
	if _, err := b.LoadSpec("web", "web.fetch", -1); err == nil {
		t.Error("LoadSpec(-1): expected error, got nil")
	}
}

func TestLoadSpec_ExceedsCeiling_Error(t *testing.T) {
	b := newBudget(t, 50)
	if _, err := b.LoadSpec("web", "web.fetch", 51); err == nil {
		t.Error("LoadSpec(51) with ceiling 50: expected error, got nil")
	}
	// Budget must remain empty — no partial state.
	if b.Used() != 0 {
		t.Errorf("Used after rejected LoadSpec: want 0, got %d", b.Used())
	}
}

// ---- LRU eviction tests ----

func TestLoadSpec_EvictsLRU_SingleEviction(t *testing.T) {
	b := newBudget(t, 100)

	// Load A (cost 60) then B (cost 60). Loading B must evict A (LRU).
	if _, err := b.LoadSpec("web", "fetch", 60); err != nil {
		t.Fatalf("LoadSpec A: %v", err)
	}
	evicted, err := b.LoadSpec("web", "parse", 60)
	if err != nil {
		t.Fatalf("LoadSpec B: %v", err)
	}

	if len(evicted) != 1 {
		t.Fatalf("evicted: want 1, got %d", len(evicted))
	}
	if evicted[0].Command != "fetch" {
		t.Errorf("evicted command: want fetch, got %q", evicted[0].Command)
	}
	if evicted[0].Tokens != 60 {
		t.Errorf("evicted tokens: want 60, got %d", evicted[0].Tokens)
	}

	if b.IsLoaded("web", "fetch") {
		t.Error("fetch should be evicted")
	}
	if !b.IsLoaded("web", "parse") {
		t.Error("parse should be loaded")
	}
	if b.Used() != 60 {
		t.Errorf("Used: want 60, got %d", b.Used())
	}
}

func TestLoadSpec_EvictsMultipleLRU(t *testing.T) {
	b := newBudget(t, 100)

	// Load three specs that together cost 90 tokens.
	b.LoadSpec("web", "a", 30)
	b.LoadSpec("web", "b", 30)
	b.LoadSpec("web", "c", 30)

	// Loading a 90-token spec must evict all three (a, b, c in LRU order).
	evicted, err := b.LoadSpec("data", "big", 90)
	if err != nil {
		t.Fatalf("LoadSpec big: %v", err)
	}
	if len(evicted) != 3 {
		t.Fatalf("evicted: want 3, got %d", len(evicted))
	}
	// LRU order: a was loaded first → evicted first.
	if evicted[0].Command != "a" || evicted[1].Command != "b" || evicted[2].Command != "c" {
		t.Errorf("eviction order: want [a, b, c], got %v", evicted)
	}
}

func TestLoadSpec_LRUOrder_RespectsInsertionOrder(t *testing.T) {
	// ceiling=100; load a(50)+b(50)=100 (full). Load d(40): must evict only a
	// (LRU) since 50-40=10 tokens freed is enough (50+40=90 ≤ 100).
	b := newBudget(t, 100)

	b.LoadSpec("web", "a", 50)
	b.LoadSpec("web", "b", 50)

	// a is LRU. Loading a 40-token spec must evict only a.
	evicted, err := b.LoadSpec("web", "d", 40)
	if err != nil {
		t.Fatalf("LoadSpec d: %v", err)
	}
	if len(evicted) != 1 || evicted[0].Command != "a" {
		t.Errorf("expected eviction of a, got %v", evicted)
	}
}

// ---- Touch tests ----

func TestTouch_PromotesToMRU(t *testing.T) {
	// ceiling=100; load a(50)+b(50)=100 (full). Touch a → b becomes LRU.
	// Load c(40): must evict b (LRU, not a) since 50-40=10 freed is enough.
	b := newBudget(t, 100)

	b.LoadSpec("web", "a", 50)
	b.LoadSpec("web", "b", 50)

	// Touch a → a moves to MRU; b becomes LRU.
	b.Touch("web", "a")

	// Loading c(40) evicts the LRU (b, not a).
	evicted, err := b.LoadSpec("web", "c", 40)
	if err != nil {
		t.Fatalf("LoadSpec c: %v", err)
	}
	if len(evicted) != 1 || evicted[0].Command != "b" {
		t.Errorf("expected eviction of b after touching a, got %v", evicted)
	}
	if b.IsLoaded("web", "a") == false {
		t.Error("a should still be loaded (it was MRU)")
	}
}

func TestTouch_UnloadedSpecIsNoop(t *testing.T) {
	b := newBudget(t, 100)
	// Touch on something not loaded should not panic or modify state.
	b.Touch("web", "nonexistent")
	if b.Used() != 0 {
		t.Errorf("Used after Touch on unloaded spec: want 0, got %d", b.Used())
	}
}

// ---- Idempotent re-load tests ----

func TestLoadSpec_Idempotent_ReturnsNoEvictions(t *testing.T) {
	b := newBudget(t, 100)

	if _, err := b.LoadSpec("web", "fetch", 40); err != nil {
		t.Fatalf("first LoadSpec: %v", err)
	}

	evicted, err := b.LoadSpec("web", "fetch", 40)
	if err != nil {
		t.Fatalf("second LoadSpec: %v", err)
	}
	if len(evicted) != 0 {
		t.Errorf("idempotent re-load: expected 0 evictions, got %v", evicted)
	}
	// Used must not double-count.
	if b.Used() != 40 {
		t.Errorf("Used after idempotent re-load: want 40, got %d", b.Used())
	}
}

func TestLoadSpec_Idempotent_PromotesToMRU(t *testing.T) {
	b := newBudget(t, 150)

	b.LoadSpec("web", "a", 50)
	b.LoadSpec("web", "b", 50)

	// Re-load a → a becomes MRU; b becomes LRU.
	b.LoadSpec("web", "a", 50)

	// Load c (cost 60) — must evict b (LRU), not a (MRU).
	evicted, _ := b.LoadSpec("web", "c", 60)
	if len(evicted) != 1 || evicted[0].Command != "b" {
		t.Errorf("expected eviction of b after re-loading a, got %v", evicted)
	}
}

// ---- Remove tests ----

func TestRemove_FreesTokenBudget(t *testing.T) {
	b := newBudget(t, 100)
	b.LoadSpec("web", "fetch", 60)

	b.Remove("web", "fetch")

	if b.IsLoaded("web", "fetch") {
		t.Error("fetch should be removed")
	}
	if b.Used() != 0 {
		t.Errorf("Used after Remove: want 0, got %d", b.Used())
	}
	if b.Remaining() != 100 {
		t.Errorf("Remaining after Remove: want 100, got %d", b.Remaining())
	}
}

func TestRemove_AllowsLoadingAfterFree(t *testing.T) {
	b := newBudget(t, 100)
	b.LoadSpec("web", "a", 60)
	b.LoadSpec("web", "b", 40) // fills to ceiling

	b.Remove("web", "a")

	// Now 60 tokens freed — c (cost 60) should load without evicting b.
	evicted, err := b.LoadSpec("web", "c", 60)
	if err != nil {
		t.Fatalf("LoadSpec after Remove: %v", err)
	}
	if len(evicted) != 0 {
		t.Errorf("expected no evictions after Remove freed space, got %v", evicted)
	}
}

func TestRemove_UnloadedSpecIsNoop(t *testing.T) {
	b := newBudget(t, 100)
	b.Remove("web", "nonexistent") // must not panic
	if b.Used() != 0 {
		t.Errorf("Used after Remove on unloaded spec: want 0, got %d", b.Used())
	}
}

// ---- Loaded ordering tests ----

func TestLoaded_MRUFirst(t *testing.T) {
	b := newBudget(t, 300)

	b.LoadSpec("web", "a", 50)
	b.LoadSpec("web", "b", 50)
	b.LoadSpec("web", "c", 50)

	// c is MRU, then b, then a (LRU).
	loaded := b.Loaded()
	if len(loaded) != 3 {
		t.Fatalf("Loaded: want 3, got %d", len(loaded))
	}
	if loaded[0].Command != "c" || loaded[1].Command != "b" || loaded[2].Command != "a" {
		t.Errorf("Loaded order: want [c, b, a], got %v", loaded)
	}
}

func TestLoaded_EmptyBudget(t *testing.T) {
	b := newBudget(t, 100)
	if loaded := b.Loaded(); len(loaded) != 0 {
		t.Errorf("Loaded on empty budget: want [], got %v", loaded)
	}
}

func TestLoaded_AfterEviction(t *testing.T) {
	b := newBudget(t, 100)

	b.LoadSpec("web", "a", 60)
	b.LoadSpec("web", "b", 60) // evicts a

	loaded := b.Loaded()
	if len(loaded) != 1 || loaded[0].Command != "b" {
		t.Errorf("Loaded after eviction: want [b], got %v", loaded)
	}
}

// ---- Multi-domain tests ----

func TestLoadSpec_MultiDomain(t *testing.T) {
	b := newBudget(t, 200)

	b.LoadSpec("web", "fetch", 50)
	b.LoadSpec("data", "transform", 50)
	b.LoadSpec("comms", "send", 50)

	if b.Used() != 150 {
		t.Errorf("Used: want 150, got %d", b.Used())
	}

	loaded := b.Loaded()
	if len(loaded) != 3 {
		t.Fatalf("Loaded: want 3, got %d", len(loaded))
	}
	// comms.send is MRU.
	if loaded[0].Domain != "comms" || loaded[0].Command != "send" {
		t.Errorf("loaded[0]: want comms.send, got %+v", loaded[0])
	}
}

func TestLoadSpec_SameCommandDifferentDomains(t *testing.T) {
	b := newBudget(t, 200)

	// "fetch" in two domains — treated as distinct keys.
	b.LoadSpec("web", "fetch", 40)
	b.LoadSpec("data", "fetch", 40)

	if b.Used() != 80 {
		t.Errorf("Used: want 80, got %d", b.Used())
	}
	if !b.IsLoaded("web", "fetch") || !b.IsLoaded("data", "fetch") {
		t.Error("both domain-qualified keys should be loaded")
	}
}

// ---- EstimateSpecTokens tests ----

func TestEstimateSpecTokens_Nil(t *testing.T) {
	if cost := skills.EstimateSpecTokens(nil); cost < 1 {
		t.Errorf("EstimateSpecTokens(nil): want ≥ 1, got %d", cost)
	}
}

func TestEstimateSpecTokens_NonNil(t *testing.T) {
	spec := &types.SkillSpec{
		Parameters: map[string]types.ParameterDef{
			"url":    {Type: "string", Required: true, Description: "The URL to fetch."},
			"method": {Type: "string", Required: false, Description: "HTTP method."},
		},
	}
	cost := skills.EstimateSpecTokens(spec)
	if cost < 1 {
		t.Errorf("EstimateSpecTokens: want ≥ 1, got %d", cost)
	}
	// Sanity: non-trivial spec should cost at least a few tokens.
	if cost < 5 {
		t.Errorf("EstimateSpecTokens: expected ≥ 5 tokens for a spec with 2 params, got %d", cost)
	}
}

func TestEstimateSpecTokens_LargerSpecCostsMore(t *testing.T) {
	small := &types.SkillSpec{
		Parameters: map[string]types.ParameterDef{
			"x": {Type: "string", Required: true, Description: "Short."},
		},
	}
	large := &types.SkillSpec{
		Parameters: map[string]types.ParameterDef{
			"a": {Type: "string", Required: true, Description: "A parameter with a long description that explains its purpose."},
			"b": {Type: "string", Required: true, Description: "Another parameter with a long description for testing purposes."},
			"c": {Type: "integer", Required: false, Description: "Yet another parameter with a long description."},
		},
	}
	if skills.EstimateSpecTokens(small) >= skills.EstimateSpecTokens(large) {
		t.Error("larger spec should cost more tokens than smaller spec")
	}
}

// ---- Budget accounting invariant test ----

// TestBudget_AccountingInvariant verifies that Used() + Remaining() == Ceiling()
// holds across a mix of LoadSpec, Touch, and Remove operations.
func TestBudget_AccountingInvariant(t *testing.T) {
	b := newBudget(t, 200)
	check := func(label string) {
		t.Helper()
		if b.Used()+b.Remaining() != b.Ceiling() {
			t.Errorf("%s: Used(%d) + Remaining(%d) != Ceiling(%d)",
				label, b.Used(), b.Remaining(), b.Ceiling())
		}
	}

	check("initial")
	b.LoadSpec("web", "a", 50)
	check("after load a")
	b.LoadSpec("web", "b", 70)
	check("after load b")
	b.Touch("web", "a")
	check("after touch a")
	b.LoadSpec("web", "c", 90) // may evict b (LRU after touch a)
	check("after load c (possible eviction)")
	b.Remove("web", "a")
	check("after remove a")
	b.LoadSpec("web", "d", 200) // fills to ceiling, evicts all
	check("after fill to ceiling")
	b.Remove("web", "d")
	check("after remove all")
}
