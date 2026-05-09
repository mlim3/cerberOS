// Package main — tools_local_files.go implements the "local_files" skill domain.
//
// Two tools are exposed:
//   - local_file_write: append or overwrite a file under the per-user data dir
//   - local_file_read: read a file from the per-user data dir
//
// Files are scoped to /data/users/<user_id>/ on disk, where <user_id> is the
// AEGIS_USER_CONTEXT_ID env var passed to the agent process at spawn. The
// directory is bind-mounted into the aegis-agents container (and inherited by
// agent-process subprocesses) at ./data/agents on the host. A user-supplied
// path beginning with "~/" is rewritten to that base; absolute paths and
// path-traversal sequences ("..") are rejected so an agent cannot escape its
// per-user sandbox.
//
// FP-Stefan: this is the demo path (Docker subprocess spawn). Production
// Firecracker agents would need /data/users/<id>/ mounted into the VM via the
// rootfs build; called out in plans/multitenancy and in the README so it is
// not a surprise post-presentation.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
)

const (
	// localFilesBase is the on-container root for per-user files. The compose
	// file bind-mounts ./data/agents -> /data on the aegis-agents container
	// so the host filesystem reflects writes immediately.
	localFilesBase = "/data/users"

	// maxLocalFileBytes caps reads at 256KB. Writes are bounded by the
	// agent's overall context window, not this limit.
	maxLocalFileBytes = 256 * 1024
)

// userScopedDir resolves the on-disk root for the current agent's user. When
// AEGIS_USER_CONTEXT_ID is empty (e.g. unit tests, headless mode), falls back
// to a shared "shared" directory so the tool still works in dev.
func userScopedDir() string {
	uid := strings.TrimSpace(os.Getenv("AEGIS_USER_CONTEXT_ID"))
	if uid == "" {
		uid = "shared"
	}
	return filepath.Join(localFilesBase, uid)
}

// resolveUserPath maps a user-supplied path to an absolute path within the
// per-user dir. Returns an error if the result would escape the dir (e.g.
// "../etc/passwd") or if the input is an absolute path outside the sandbox.
func resolveUserPath(input string) (string, error) {
	if input == "" {
		return "", fmt.Errorf("path is required")
	}
	clean := strings.TrimSpace(input)
	// Tilde expansion: "~/foo.txt" -> "<base>/foo.txt".
	if strings.HasPrefix(clean, "~/") || clean == "~" {
		clean = strings.TrimPrefix(clean, "~/")
		clean = strings.TrimPrefix(clean, "~")
	}
	if filepath.IsAbs(clean) {
		// Allow paths already inside the sandbox; reject all others.
		base := userScopedDir()
		if !strings.HasPrefix(filepath.Clean(clean), filepath.Clean(base)+string(os.PathSeparator)) &&
			filepath.Clean(clean) != filepath.Clean(base) {
			return "", fmt.Errorf("absolute paths outside %s are not allowed", base)
		}
		return filepath.Clean(clean), nil
	}
	resolved := filepath.Join(userScopedDir(), clean)
	resolvedClean := filepath.Clean(resolved)
	base := filepath.Clean(userScopedDir())
	if !strings.HasPrefix(resolvedClean, base+string(os.PathSeparator)) && resolvedClean != base {
		return "", fmt.Errorf("path escapes the per-user sandbox")
	}
	return resolvedClean, nil
}

func localFileWriteTool() SkillTool {
	return SkillTool{
		Label:                   "Local File Write",
		RequiredCredentialTypes: nil,
		TimeoutSeconds:          15,
		Definition: anthropic.ToolParam{
			Name: "local_file_write",
			Description: anthropic.String(
				"Write text to a file in the user's local data directory (e.g. ~/notes.txt). " +
					"Use mode='append' to add to an existing file, 'overwrite' to replace contents. " +
					"Do NOT use for binary data. " +
					"Do NOT use to write outside the user's data directory — paths are sandboxed."),
			InputSchema: anthropic.ToolInputSchemaParam{
				Properties: map[string]interface{}{
					"path": map[string]interface{}{
						"type":        "string",
						"description": "Relative path within the user's data directory (e.g. 'notes.txt'). The prefix '~/' is treated as the user's home dir. Absolute paths outside the sandbox are rejected.",
					},
					"content": map[string]interface{}{
						"type":        "string",
						"description": "Text content to write. Will be UTF-8 encoded.",
					},
					"mode": map[string]interface{}{
						"type":        "string",
						"description": "Write mode: 'append' (default — adds to file end with newline if absent) or 'overwrite' (replaces file contents).",
						"enum":        []string{"append", "overwrite"},
					},
				},
				Required: []string{"path", "content"},
			},
		},
		Execute: executeLocalFileWrite,
	}
}

func executeLocalFileWrite(ctx context.Context, raw json.RawMessage) ToolResult {
	var p struct {
		Path    string `json:"path"`
		Content string `json:"content"`
		Mode    string `json:"mode"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return ToolResult{Content: fmt.Sprintf("invalid parameters: %v", err), IsError: true}
	}
	full, err := resolveUserPath(p.Path)
	if err != nil {
		return ToolResult{Content: err.Error(), IsError: true}
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return ToolResult{Content: fmt.Sprintf("failed to create dir: %v", err), IsError: true}
	}
	if p.Mode == "" {
		p.Mode = "append"
	}
	var content string
	switch p.Mode {
	case "overwrite":
		content = p.Content
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			return ToolResult{Content: fmt.Sprintf("write failed: %v", err), IsError: true}
		}
	case "append":
		content = p.Content
		// Ensure separation: if file already exists and doesn't end with a
		// newline, prepend one to avoid concatenated lines.
		f, err := os.OpenFile(full, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			return ToolResult{Content: fmt.Sprintf("open failed: %v", err), IsError: true}
		}
		defer f.Close()
		if info, statErr := f.Stat(); statErr == nil && info.Size() > 0 {
			// Best-effort newline injection between appends.
			if !strings.HasPrefix(content, "\n") {
				content = "\n" + content
			}
		}
		if _, err := f.WriteString(content); err != nil {
			return ToolResult{Content: fmt.Sprintf("write failed: %v", err), IsError: true}
		}
	default:
		return ToolResult{Content: fmt.Sprintf("unknown mode: %q (use append or overwrite)", p.Mode), IsError: true}
	}
	info, _ := os.Stat(full)
	var size int64
	if info != nil {
		size = info.Size()
	}
	return ToolResult{
		Content: fmt.Sprintf("Wrote to %s (mode=%s, size=%d bytes)", full, p.Mode, size),
		Details: map[string]interface{}{
			"path":  full,
			"mode":  p.Mode,
			"bytes": size,
		},
	}
}

func localFileReadTool() SkillTool {
	return SkillTool{
		Label:                   "Local File Read",
		RequiredCredentialTypes: nil,
		TimeoutSeconds:          10,
		Definition: anthropic.ToolParam{
			Name: "local_file_read",
			Description: anthropic.String(
				"Read text from a file in the user's local data directory. " +
					"Returns up to 256KB; longer files are truncated. " +
					"Do NOT use for binary data. " +
					"Do NOT use to read outside the user's data directory — paths are sandboxed."),
			InputSchema: anthropic.ToolInputSchemaParam{
				Properties: map[string]interface{}{
					"path": map[string]interface{}{
						"type":        "string",
						"description": "Relative path within the user's data directory. The prefix '~/' is treated as the user's home dir.",
					},
				},
				Required: []string{"path"},
			},
		},
		Execute: executeLocalFileRead,
	}
}

func executeLocalFileRead(ctx context.Context, raw json.RawMessage) ToolResult {
	var p struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return ToolResult{Content: fmt.Sprintf("invalid parameters: %v", err), IsError: true}
	}
	full, err := resolveUserPath(p.Path)
	if err != nil {
		return ToolResult{Content: err.Error(), IsError: true}
	}
	data, err := os.ReadFile(full)
	if err != nil {
		if os.IsNotExist(err) {
			return ToolResult{
				Content: fmt.Sprintf("File not found: %s", full),
				IsError: true,
				Details: map[string]interface{}{"path": full, "exists": false},
			}
		}
		return ToolResult{Content: fmt.Sprintf("read failed: %v", err), IsError: true}
	}
	body := data
	truncated := false
	if len(body) > maxLocalFileBytes {
		body = body[:maxLocalFileBytes]
		truncated = true
	}
	return ToolResult{
		Content: string(body),
		Details: map[string]interface{}{
			"path":      full,
			"bytes":     len(body),
			"truncated": truncated,
		},
	}
}
