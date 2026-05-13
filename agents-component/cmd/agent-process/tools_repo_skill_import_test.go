package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cerberOS/agents-component/pkg/types"
)

type fakeRepoSkillPersister struct {
	persisted []persistedSkillCall
	reloads   []reloadCall
}

type persistedSkillCall struct {
	domain      string
	ownerUserID string
	scope       string
	name        string
}

type reloadCall struct {
	domain    string
	skillName string
	scope     string
}

func (f *fakeRepoSkillPersister) PersistSkillWithScope(domain string, node *types.SkillNode, ownerUserID, scope string) error {
	f.persisted = append(f.persisted, persistedSkillCall{
		domain: domain, ownerUserID: ownerUserID, scope: scope, name: node.Name,
	})
	return nil
}

func (f *fakeRepoSkillPersister) PublishSkillReload(domain, skillName, scope string) error {
	f.reloads = append(f.reloads, reloadCall{domain: domain, skillName: skillName, scope: scope})
	return nil
}

func TestImportRepoSkills_HappyPath(t *testing.T) {
	originalAPI := githubAPIBase
	originalRaw := rawGitHubBase
	defer func() {
		githubAPIBase = originalAPI
		rawGitHubBase = originalRaw
	}()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/obra/superpowers":
			_, _ = fmt.Fprint(w, `{"default_branch":"main"}`)
		case r.URL.Path == "/repos/obra/superpowers/git/trees/main":
			_, _ = fmt.Fprint(w, `{"tree":[
				{"path":"skills/using-superpowers/SKILL.md","type":"blob"},
				{"path":"skills/writing-skills/guide.md","type":"blob"},
				{"path":"docs/notes.md","type":"blob"},
				{"path":"README.md","type":"blob"}
			]}`)
		case r.URL.Path == "/obra/superpowers/main/skills/using-superpowers/SKILL.md":
			_, _ = fmt.Fprint(w, `---
name: using_superpowers
display_name: Using Superpowers
description: "Use Superpowers for skill-heavy tasks. Do NOT use when a simple direct answer is enough."
---

# Using Superpowers

Follow the Superpowers workflow carefully.
`)
		case r.URL.Path == "/obra/superpowers/main/skills/writing-skills/guide.md":
			_, _ = fmt.Fprint(w, `---
name: writing_skills
description: "Write skills carefully. Do NOT use for execution tasks."
---

## Writing Skills

Observe candidate files and decide whether they are skills.
`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	githubAPIBase = srv.URL
	rawGitHubBase = srv.URL

	result, err := importRepoSkills(context.Background(), "obra/superpowers")
	if err != nil {
		t.Fatalf("importRepoSkills: %v", err)
	}
	if result.FallbackUsed {
		t.Fatal("expected fallbackUsed=false")
	}
	if len(result.Skills) != 2 {
		t.Fatalf("expected 2 imported skills, got %d", len(result.Skills))
	}
	if result.Skills[0].Node.Name != "using_superpowers" {
		t.Fatalf("unexpected first skill name %q", result.Skills[0].Node.Name)
	}
	if result.Skills[1].Node.Name != "writing_skills" {
		t.Fatalf("unexpected second skill name %q", result.Skills[1].Node.Name)
	}
}

func TestImportRepoSkills_FallbackWhenNoSkillLikeFiles(t *testing.T) {
	originalAPI := githubAPIBase
	originalRaw := rawGitHubBase
	defer func() {
		githubAPIBase = originalAPI
		rawGitHubBase = originalRaw
	}()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/acme/notes":
			_, _ = fmt.Fprint(w, `{"default_branch":"main"}`)
		case r.URL.Path == "/repos/acme/notes/git/trees/main":
			_, _ = fmt.Fprint(w, `{"tree":[
				{"path":"docs/overview.md","type":"blob"},
				{"path":"README.md","type":"blob"}
			]}`)
		case r.URL.Path == "/acme/notes/main/README.md":
			_, _ = fmt.Fprint(w, "# Notes\nNothing here looks like a skill.")
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	githubAPIBase = srv.URL
	rawGitHubBase = srv.URL

	result, err := importRepoSkills(context.Background(), "acme/notes")
	if err != nil {
		t.Fatalf("importRepoSkills: %v", err)
	}
	if !result.FallbackUsed {
		t.Fatal("expected fallbackUsed=true")
	}
	if len(result.Skills) != 1 {
		t.Fatalf("expected 1 fallback skill, got %d", len(result.Skills))
	}
	if result.Skills[0].Node.Name == "" {
		t.Fatal("fallback skill name must not be empty")
	}
}

func TestExecuteExtractSkillsFromRepo_PersistsAndReloads(t *testing.T) {
	originalAPI := githubAPIBase
	originalRaw := rawGitHubBase
	defer func() {
		githubAPIBase = originalAPI
		rawGitHubBase = originalRaw
	}()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/obra/superpowers":
			_, _ = fmt.Fprint(w, `{"default_branch":"main"}`)
		case r.URL.Path == "/repos/obra/superpowers/git/trees/main":
			_, _ = fmt.Fprint(w, `{"tree":[{"path":"skills/using-superpowers/SKILL.md","type":"blob"}]}`)
		case r.URL.Path == "/obra/superpowers/main/skills/using-superpowers/SKILL.md":
			_, _ = fmt.Fprint(w, `---
name: using_superpowers
display_name: Using Superpowers
description: "Use Superpowers when you need to explore the project. Do NOT use for unrelated tasks."
---

# Using Superpowers

Apply the Superpowers workflow.
`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	githubAPIBase = srv.URL
	rawGitHubBase = srv.URL

	persister := &fakeRepoSkillPersister{}
	spawnCtx := &SpawnContext{SkillDomain: "general", UserContextID: "user-123"}
	raw, _ := json.Marshal(map[string]string{"repo": "https://github.com/obra/superpowers"})
	result := executeExtractSkillsFromRepo(context.Background(), spawnCtx, persister, raw)
	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Content)
	}
	if len(persister.persisted) != 1 {
		t.Fatalf("expected one persisted skill, got %d", len(persister.persisted))
	}
	if persister.persisted[0].name != "using_superpowers" {
		t.Fatalf("unexpected persisted skill name %q", persister.persisted[0].name)
	}
	if len(persister.reloads) != 1 {
		t.Fatalf("expected one reload signal, got %d", len(persister.reloads))
	}
	if !strings.Contains(result.Content, "using_superpowers") {
		t.Fatalf("tool result should mention imported skill name, got %q", result.Content)
	}
}
