package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/cerberOS/agents-component/pkg/types"
	"go.yaml.in/yaml/v2"
)

const (
	repoSkillImportToolName = "extract_skills_from_repo"
	repoSkillImportMaxRead  = 256 * 1024
)

var globalRepoImportIntentPhrases = []string{
	"for all users",
	"for everyone",
	"for the whole team",
	"for the team",
	"global install",
	"install globally",
	"install for all users",
	"install for everyone",
	"available for all users",
	"share with everyone",
	"available to everyone",
	"make it available for all users",
	"make them available for all users",
}

var githubAPIBase = "https://api.github.com"

type repoSkillPersister interface {
	PersistSkillWithScope(domain string, node *types.SkillNode, ownerUserID, scope string) error
	PublishSkillReload(domain, skillName, scope string) error
}

type repoSkillImportResult struct {
	RepoSlug     string
	Ref          string
	Skills       []importedRepoSkill
	FallbackUsed bool
}

type importedRepoSkill struct {
	Path string
	Node *types.SkillNode
}

type repoTreeEntry struct {
	Path string `json:"path"`
	Type string `json:"type"`
}

func extractSkillsFromRepoTool(spawnCtx *SpawnContext, sl repoSkillPersister) SkillTool {
	return SkillTool{
		Label:                   "Extract Skills From Repo",
		RequiredCredentialTypes: nil,
		TimeoutSeconds:          120,
		Definition: anthropic.ToolParam{
			Name: repoSkillImportToolName,
			Description: anthropic.String(
				"Extract all skill-like files from a public GitHub repository, convert them into reusable skills, persist them, and reload once. " +
					"Use when the user gives a GitHub repo link and asks to extract, import, or install skills. " +
					"Do NOT use to execute a task. " +
					"Do NOT use for private repositories or credentialed content."),
			InputSchema: anthropic.ToolInputSchemaParam{
				Properties: map[string]interface{}{
					"repo": map[string]interface{}{
						"type":        "string",
						"description": "GitHub repository URL or owner/repo slug to scan for skill-like files.",
					},
				},
				Required: []string{"repo"},
			},
		},
		Execute: func(ctx context.Context, raw json.RawMessage) ToolResult {
			return executeExtractSkillsFromRepo(ctx, spawnCtx, sl, raw)
		},
	}
}

func executeExtractSkillsFromRepo(ctx context.Context, spawnCtx *SpawnContext, persister repoSkillPersister, raw json.RawMessage) ToolResult {
	if ctx == nil {
		ctx = context.Background()
	}
	var params struct {
		Repo string `json:"repo"`
	}
	if err := json.Unmarshal(raw, &params); err != nil {
		return ToolResult{Content: fmt.Sprintf("invalid parameters: %v", err), IsError: true}
	}

	repoSlug, err := normalizeRepoSlug(params.Repo)
	if err != nil {
		return ToolResult{Content: err.Error(), IsError: true}
	}

	imported, err := importRepoSkills(ctx, repoSlug)
	if err != nil {
		return ToolResult{Content: fmt.Sprintf("repo skill extraction failed: %v", err), IsError: true}
	}
	if persister == nil {
		return ToolResult{Content: "repo skill extraction unavailable: persistence layer is nil", IsError: true}
	}

	domain := "general"
	if spawnCtx != nil && strings.TrimSpace(spawnCtx.SkillDomain) != "" {
		domain = spawnCtx.SkillDomain
	}
	ownerUserID := ""
	if spawnCtx != nil {
		ownerUserID = spawnCtx.UserContextID
	}
	scope, scopeNote := inferRepoImportScope(spawnCtx)

	persisted := 0
	persistedNames := make([]string, 0, len(imported.Skills))
	for _, item := range imported.Skills {
		if item.Node == nil {
			continue
		}
		if err := persister.PersistSkillWithScope(domain, item.Node, ownerUserID, scope); err != nil {
			continue
		}
		persisted++
		persistedNames = append(persistedNames, item.Node.Name)
	}
	if persisted == 0 {
		return ToolResult{Content: "no skills were persisted from the repository", IsError: true}
	}
	_ = persister.PublishSkillReload(domain, persistedNames[0], scope)

	var summary strings.Builder
	fmt.Fprintf(&summary, "Imported %d skills from %s@%s.\n", persisted, imported.RepoSlug, imported.Ref)
	for _, item := range imported.Skills {
		if item.Node == nil {
			continue
		}
		fmt.Fprintf(&summary, "- %s (%s)\n", item.Node.Name, item.Path)
	}
	if scope == "all" {
		summary.WriteString("Imported skills are available to all users.\n")
	} else if scopeNote != "" {
		summary.WriteString(scopeNote + "\n")
	}
	if imported.FallbackUsed {
		summary.WriteString("Fallback mode was used because no strong skill-like files were found.\n")
	}

	return ToolResult{
		Content: summary.String(),
		Details: map[string]interface{}{
			"repo":          imported.RepoSlug,
			"ref":           imported.Ref,
			"persisted":     persisted,
			"fallback_used": imported.FallbackUsed,
			"skills":        persistedNames,
			"scope":         scope,
		},
	}
}

func inferRepoImportScope(spawnCtx *SpawnContext) (string, string) {
	if spawnCtx == nil {
		return "user", ""
	}

	text := strings.ToLower(strings.TrimSpace(spawnCtx.OriginalUserMessage + "\n" + spawnCtx.Instructions))
	if !containsGlobalRepoImportIntent(text) {
		return "user", ""
	}

	role := strings.ToLower(strings.TrimSpace(spawnCtx.UserRole))
	if role == "root" {
		return "all", ""
	}
	return "user", "global install requests are limited to root; imported for the current user only"
}

func containsGlobalRepoImportIntent(text string) bool {
	for _, phrase := range globalRepoImportIntentPhrases {
		if strings.Contains(text, phrase) {
			return true
		}
	}
	return false
}

func importRepoSkills(ctx context.Context, repoSlug string) (repoSkillImportResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	ref, candidatePaths := discoverSkillCandidates(ctx, repoSlug)
	if len(candidatePaths) == 0 {
		node, err := buildFallbackRepoSkillNode(ctx, repoSlug)
		if err != nil {
			return repoSkillImportResult{}, err
		}
		return repoSkillImportResult{
			RepoSlug:     repoSlug,
			Ref:          ref,
			Skills:       []importedRepoSkill{{Path: "README.md", Node: node}},
			FallbackUsed: true,
		}, nil
	}

	imported := make([]importedRepoSkill, 0, len(candidatePaths))
	for _, filePath := range candidatePaths {
		content, err := fetchRepoFile(ctx, repoSlug, ref, filePath)
		if err != nil || strings.TrimSpace(content) == "" {
			continue
		}
		parsed := parseSkillDocument(filePath, content)
		if !looksLikeSkillDocument(filePath, parsed) {
			continue
		}
		node, err := buildRepoSkillNode(repoSlug, filePath, parsed)
		if err != nil {
			continue
		}
		imported = append(imported, importedRepoSkill{Path: filePath, Node: node})
	}

	if len(imported) == 0 {
		node, err := buildFallbackRepoSkillNode(ctx, repoSlug)
		if err != nil {
			return repoSkillImportResult{}, err
		}
		return repoSkillImportResult{
			RepoSlug:     repoSlug,
			Ref:          ref,
			Skills:       []importedRepoSkill{{Path: "README.md", Node: node}},
			FallbackUsed: true,
		}, nil
	}

	return repoSkillImportResult{
		RepoSlug: repoSlug,
		Ref:      ref,
		Skills:   imported,
	}, nil
}

func discoverSkillCandidates(ctx context.Context, repoSlug string) (string, []string) {
	ref := getDefaultBranch(ctx, repoSlug)
	tree, err := fetchRepoTree(ctx, repoSlug, ref)
	if err != nil || len(tree) == 0 {
		return ref, nil
	}

	candidatePaths := make([]string, 0, len(tree))
	for _, entry := range tree {
		if entry.Type != "blob" {
			continue
		}
		if isPotentialSkillFile(entry.Path) {
			candidatePaths = append(candidatePaths, entry.Path)
		}
	}
	sort.Strings(candidatePaths)
	return ref, candidatePaths
}

func getDefaultBranch(ctx context.Context, repoSlug string) string {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("%s/repos/%s", githubAPIBase, repoSlug), nil)
	if err != nil {
		return "main"
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "cerberos-agent")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "main"
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return "main"
	}
	var body struct {
		DefaultBranch string `json:"default_branch"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "main"
	}
	if strings.TrimSpace(body.DefaultBranch) == "" {
		return "main"
	}
	return body.DefaultBranch
}

func fetchRepoTree(ctx context.Context, repoSlug, ref string) ([]repoTreeEntry, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("%s/repos/%s/git/trees/%s?recursive=1", githubAPIBase, repoSlug, ref), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "cerberos-agent")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("github tree fetch failed: HTTP %d", resp.StatusCode)
	}
	var body struct {
		Tree []repoTreeEntry `json:"tree"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, err
	}
	return body.Tree, nil
}

func fetchRepoFile(ctx context.Context, repoSlug, ref, filePath string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("%s/%s/%s/%s", rawGitHubBase, repoSlug, ref, path.Clean(filePath)), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "cerberos-agent")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("github raw fetch failed: HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, repoSkillImportMaxRead))
	if err != nil {
		return "", err
	}
	return string(body), nil
}

func buildFallbackRepoSkillNode(ctx context.Context, repoSlug string) (*types.SkillNode, error) {
	summary := fmt.Sprintf("Imported from %s", repoSlug)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("%s/repos/%s/readme", githubAPIBase, repoSlug), nil)
	if err == nil {
		req.Header.Set("Accept", "application/vnd.github.raw")
		req.Header.Set("User-Agent", "cerberos-agent")
		if resp, err := http.DefaultClient.Do(req); err == nil {
			defer resp.Body.Close()
			if resp.StatusCode < 400 {
				if body, err := io.ReadAll(io.LimitReader(resp.Body, repoSkillImportMaxRead)); err == nil {
					lines := strings.Split(string(body), "\n")
					parts := make([]string, 0, 5)
					for _, line := range lines {
						line = strings.TrimSpace(line)
						if line == "" {
							continue
						}
						parts = append(parts, line)
						if len(parts) == 5 {
							break
						}
					}
					if len(parts) > 0 {
						summary = strings.Join(parts, " ")
					}
				}
			}
		}
	}

	name := sanitizeSkillName(strings.ReplaceAll(repoSlug, "/", "_"))
	if name == "" {
		name = "imported_skill"
	}
	description := ensureNegativeGuidance(fmt.Sprintf("From %s: %s", repoSlug, summary))
	description = limitDescription(description)
	now := time.Now().UTC()
	node := &types.SkillNode{
		Name:          name,
		Level:         "command",
		Label:         humanizeLabel(repoSlug),
		Description:   description,
		Recipe:        fmt.Sprintf("1. Treat %s as the source of a reusable skill.\n2. Apply the guidance in the repository README.\n3. Return a concise result.", repoSlug),
		Spec:          &types.SkillSpec{Parameters: map[string]types.ParameterDef{}},
		Origin:        "synthesized",
		SynthesizedAt: &now,
	}
	if err := validateGeneratedSkill(node); err != nil {
		return nil, err
	}
	return node, nil
}

type skillDocument struct {
	frontmatter map[string]string
	body        string
}

func parseSkillDocument(filePath, content string) skillDocument {
	source := strings.ReplaceAll(strings.TrimPrefix(content, "\uFEFF"), "\r\n", "\n")
	if strings.HasSuffix(strings.ToLower(filePath), ".yaml") || strings.HasSuffix(strings.ToLower(filePath), ".yml") {
		return skillDocument{frontmatter: parseYAMLMap(source), body: ""}
	}
	if !strings.HasPrefix(source, "---\n") {
		return skillDocument{frontmatter: map[string]string{}, body: strings.TrimSpace(source)}
	}

	lines := strings.Split(source, "\n")
	var fmLines []string
	idx := 1
	for ; idx < len(lines); idx++ {
		if strings.TrimSpace(lines[idx]) == "---" {
			idx++
			break
		}
		fmLines = append(fmLines, lines[idx])
	}
	return skillDocument{
		frontmatter: parseYAMLMap(strings.Join(fmLines, "\n")),
		body:        strings.TrimSpace(strings.Join(lines[idx:], "\n")),
	}
}

func parseYAMLMap(src string) map[string]string {
	out := map[string]string{}
	if strings.TrimSpace(src) == "" {
		return out
	}
	var raw map[string]interface{}
	if err := yaml.Unmarshal([]byte(src), &raw); err != nil {
		return out
	}
	for key, val := range raw {
		out[key] = fmt.Sprint(val)
	}
	return out
}

func looksLikeSkillDocument(filePath string, doc skillDocument) bool {
	score := 0
	fm := doc.frontmatter
	body := strings.ToLower(doc.body)
	if strings.TrimSpace(fm["name"]) != "" {
		score += 2
	}
	if strings.TrimSpace(fm["description"]) != "" {
		score += 2
	}
	if strings.TrimSpace(fm["when_to_use"]) != "" {
		score += 2
	}
	if strings.TrimSpace(fm["version"]) != "" {
		score++
	}
	if strings.TrimSpace(fm["languages"]) != "" {
		score++
	}
	if strings.Contains(body, "do not use") || strings.Contains(body, "when to use") || strings.Contains(body, "steps") || strings.Contains(body, "usage") {
		score++
	}
	if strings.Contains(body, "#") {
		score++
	}
	if strings.Contains(strings.ToLower(filePath), "/skill") || strings.Contains(strings.ToLower(filePath), "skill") || strings.Contains(strings.ToLower(filePath), "workflow") || strings.Contains(strings.ToLower(filePath), "guide") || strings.Contains(strings.ToLower(filePath), "playbook") {
		score++
	}
	return score >= 3 || (strings.TrimSpace(fm["name"]) != "" && strings.TrimSpace(fm["description"]) != "")
}

func buildRepoSkillNode(repoSlug, filePath string, doc skillDocument) (*types.SkillNode, error) {
	name := sanitizeSkillName(doc.frontmatter["name"])
	if name == "" {
		name = sanitizeSkillName(pathToSkillName(filePath))
	}
	if name == "" {
		name = deriveSkillName(filePath)
	}

	labelSource := firstNonEmpty(doc.frontmatter["display_name"], doc.frontmatter["label"], doc.frontmatter["name"], pathBaseLabel(filePath))
	label := humanizeLabel(labelSource)
	description := firstNonEmpty(doc.frontmatter["description"], doc.frontmatter["when_to_use"], summarizeMarkdown(doc.body))
	if description == "" {
		description = limitDescription(ensureNegativeGuidance(fmt.Sprintf("Imported from %s/%s", repoSlug, filePath)))
	} else {
		description = limitDescription(ensureNegativeGuidance(description))
	}
	recipe := strings.TrimSpace(doc.body)
	if recipe == "" {
		recipe = fmt.Sprintf("1. Treat %s/%s as a reusable skill document.\n2. Follow the guidance in the file.\n3. Return a concise result.", repoSlug, filePath)
	}
	now := time.Now().UTC()
	node := &types.SkillNode{
		Name:          name,
		Level:         "command",
		Label:         label,
		Description:   description,
		Recipe:        recipe,
		Spec:          &types.SkillSpec{Parameters: map[string]types.ParameterDef{}},
		Origin:        "synthesized",
		SynthesizedAt: &now,
	}
	if err := validateGeneratedSkill(node); err != nil {
		return nil, err
	}
	return node, nil
}

func pathToSkillName(filePath string) string {
	parts := strings.Split(strings.Trim(filePath, "/"), "/")
	if len(parts) == 0 {
		return ""
	}
	base := strings.TrimSuffix(parts[len(parts)-1], path.Ext(parts[len(parts)-1]))
	base = strings.ToLower(strings.TrimSpace(base))
	if base == "skill" || base == "skills" || base == "readme" || base == "index" {
		if len(parts) >= 2 {
			base = parts[len(parts)-2]
		}
	}
	return strings.ReplaceAll(base, "-", "_")
}

func pathBaseLabel(filePath string) string {
	parts := strings.Split(strings.Trim(filePath, "/"), "/")
	if len(parts) == 0 {
		return "Imported Skill"
	}
	base := strings.TrimSuffix(parts[len(parts)-1], path.Ext(parts[len(parts)-1]))
	if base == "SKILL" || base == "skill" || base == "README" || base == "readme" {
		if len(parts) >= 2 {
			base = parts[len(parts)-2]
		}
	}
	return base
}

func summarizeMarkdown(body string) string {
	lines := strings.Split(body, "\n")
	parts := make([]string, 0, 5)
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts = append(parts, line)
		if len(parts) == 5 {
			break
		}
	}
	return strings.Join(parts, " ")
}

func limitDescription(text string) string {
	trimmed := strings.TrimSpace(text)
	if len(trimmed) <= 300 {
		return trimmed
	}
	return trimmed[:297] + "..."
}

func humanizeLabel(text string) string {
	base := strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(text, "_", " "), "-", " "))
	if base == "" {
		return "Imported Skill"
	}
	parts := strings.Fields(base)
	for i, part := range parts {
		if len(part) == 0 {
			continue
		}
		parts[i] = strings.ToUpper(part[:1]) + strings.ToLower(part[1:])
	}
	return strings.Join(parts, " ")
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func normalizeRepoSlug(repo string) (string, error) {
	r := strings.TrimSpace(repo)
	if r == "" {
		return "", fmt.Errorf("repo is required")
	}
	r = strings.TrimPrefix(r, "https://")
	r = strings.TrimPrefix(r, "http://")
	r = strings.TrimPrefix(r, "www.")
	r = strings.TrimPrefix(r, "github.com/")
	r = strings.TrimPrefix(r, "github.com:")
	r = strings.TrimSuffix(r, ".git")
	r = strings.SplitN(r, "?", 2)[0]
	r = strings.SplitN(r, "#", 2)[0]
	r = strings.Trim(r, "/")
	parts := strings.Split(r, "/")
	if len(parts) < 2 {
		return "", fmt.Errorf("repo must look like owner/repo or https://github.com/owner/repo")
	}
	return parts[0] + "/" + parts[1], nil
}

func isPotentialSkillFile(filePath string) bool {
	lower := strings.ToLower(filePath)
	base := path.Base(lower)
	if strings.Contains(lower, "/skills/") {
		return true
	}
	if strings.Contains(lower, "/skill/") {
		return true
	}
	if strings.Contains(base, "skill") || strings.Contains(base, "guide") || strings.Contains(base, "workflow") || strings.Contains(base, "playbook") || strings.Contains(base, "instruction") {
		return true
	}
	return strings.HasSuffix(base, ".md") || strings.HasSuffix(base, ".markdown") || strings.HasSuffix(base, ".mdx") || strings.HasSuffix(base, ".txt") || strings.HasSuffix(base, ".yaml") || strings.HasSuffix(base, ".yml")
}
