// Package main — tools_storage.go implements the "storage" skill domain tools.
//
// All storage operations require vault credentials — there are no local-only
// storage tools. The domain provides:
//   - vault_storage_read:  read an object from authenticated cloud storage.
//   - vault_storage_write: write or overwrite an object in authenticated cloud storage.
//   - vault_storage_list:  list objects (keys + metadata) in authenticated cloud storage.
package main

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/cerberOS/agents-component/pkg/types"
)

// vaultStorageReadTool reads an object from authenticated cloud storage via the Vault.
// TimeoutSeconds = 65: vault deadline = 60s (objects can be large) + 5s buffer.
func vaultStorageReadTool(ve *VaultExecutor) SkillTool {
	return SkillTool{
		Label:                   "Vault Storage Read",
		RequiredCredentialTypes: []string{"storage_credential"},
		TimeoutSeconds:          65,
		Definition: anthropic.ToolParam{
			Name: "vault_storage_read",
			Description: anthropic.String(
				"Read a file or object from authenticated cloud storage via the Vault. " +
					"Specify bucket and object key. " +
					"Do NOT use for public URLs — use web_fetch instead. " +
					"Do NOT include credential values in any parameter."),
			InputSchema: anthropic.ToolInputSchemaParam{
				Properties: map[string]interface{}{
					"bucket": map[string]interface{}{
						"type":        "string",
						"description": "Bucket or container name as configured in the Vault storage credential (e.g. \"prod-assets\", \"reports-archive\").",
					},
					"key": map[string]interface{}{
						"type":        "string",
						"description": "Object key or file path within the bucket (e.g. \"reports/2024-01.csv\", \"images/logo.png\").",
					},
				},
				Required: []string{"bucket", "key"},
			},
		},
		Execute: func(ctx context.Context, raw json.RawMessage) ToolResult {
			return executeVaultStorageRead(ctx, ve, raw)
		},
	}
}

func executeVaultStorageRead(ctx context.Context, ve *VaultExecutor, raw json.RawMessage) ToolResult {
	var params struct {
		Bucket string `json:"bucket"`
		Key    string `json:"key"`
	}
	if err := json.Unmarshal(raw, &params); err != nil {
		return ToolResult{
			Content: fmt.Sprintf("invalid parameters: %v", err),
			IsError: true,
			Details: map[string]interface{}{"error": err.Error()},
		}
	}

	opParams, err := json.Marshal(params)
	if err != nil {
		return ToolResult{
			Content: fmt.Sprintf("failed to encode operation params: %v", err),
			IsError: true,
			Details: map[string]interface{}{"error": err.Error()},
		}
	}

	onUpdate := func(p types.VaultOperationProgress) {
		ve.log.Info("vault storage_read: progress",
			"request_id", p.RequestID,
			"progress_type", p.ProgressType,
			"message", p.Message,
			"elapsed_ms", p.ElapsedMS,
		)
	}

	return ve.Execute(ctx, "storage_read", "storage_credential", opParams, 60, onUpdate)
}

// vaultStorageWriteTool writes or overwrites an object in authenticated cloud storage via the Vault.
// TimeoutSeconds = 65: vault deadline = 60s (uploads can be slow) + 5s buffer.
func vaultStorageWriteTool(ve *VaultExecutor) SkillTool {
	return SkillTool{
		Label:                   "Vault Storage Write",
		RequiredCredentialTypes: []string{"storage_credential"},
		TimeoutSeconds:          65,
		Definition: anthropic.ToolParam{
			Name: "vault_storage_write",
			Description: anthropic.String(
				"Write or overwrite an object in authenticated cloud storage via the Vault. " +
					"Specify bucket, key, and content. " +
					"Do NOT use for reading — use vault_storage_read. " +
					"Do NOT include credential values in any parameter."),
			InputSchema: anthropic.ToolInputSchemaParam{
				Properties: map[string]interface{}{
					"bucket": map[string]interface{}{
						"type":        "string",
						"description": "Bucket or container name as configured in the Vault storage credential (e.g. \"prod-assets\", \"reports-archive\").",
					},
					"key": map[string]interface{}{
						"type":        "string",
						"description": "Object key or file path to write within the bucket (e.g. \"reports/2024-01.csv\"). Existing objects are overwritten.",
					},
					"content": map[string]interface{}{
						"type":        "string",
						"description": "Content to write to the object. For binary data, use base64 encoding and set content_type accordingly.",
					},
					"content_type": map[string]interface{}{
						"type":        "string",
						"description": "MIME type of the content (e.g. \"text/plain\", \"application/json\", \"image/png\"). Defaults to \"application/octet-stream\" when omitted.",
					},
				},
				Required: []string{"bucket", "key", "content"},
			},
		},
		Execute: func(ctx context.Context, raw json.RawMessage) ToolResult {
			return executeVaultStorageWrite(ctx, ve, raw)
		},
	}
}

func executeVaultStorageWrite(ctx context.Context, ve *VaultExecutor, raw json.RawMessage) ToolResult {
	var params struct {
		Bucket      string `json:"bucket"`
		Key         string `json:"key"`
		Content     string `json:"content"`
		ContentType string `json:"content_type,omitempty"`
	}
	if err := json.Unmarshal(raw, &params); err != nil {
		return ToolResult{
			Content: fmt.Sprintf("invalid parameters: %v", err),
			IsError: true,
			Details: map[string]interface{}{"error": err.Error()},
		}
	}
	if params.ContentType == "" {
		params.ContentType = "application/octet-stream"
	}

	opParams, err := json.Marshal(params)
	if err != nil {
		return ToolResult{
			Content: fmt.Sprintf("failed to encode operation params: %v", err),
			IsError: true,
			Details: map[string]interface{}{"error": err.Error()},
		}
	}

	onUpdate := func(p types.VaultOperationProgress) {
		ve.log.Info("vault storage_write: progress",
			"request_id", p.RequestID,
			"progress_type", p.ProgressType,
			"message", p.Message,
			"elapsed_ms", p.ElapsedMS,
		)
	}

	return ve.Execute(ctx, "storage_write", "storage_credential", opParams, 60, onUpdate)
}

// vaultStorageListTool lists objects (keys and metadata) in authenticated cloud storage via the Vault.
// TimeoutSeconds = 35: listing is metadata-only, expected to be fast.
func vaultStorageListTool(ve *VaultExecutor) SkillTool {
	return SkillTool{
		Label:                   "Vault Storage List",
		RequiredCredentialTypes: []string{"storage_credential"},
		TimeoutSeconds:          35,
		Definition: anthropic.ToolParam{
			Name: "vault_storage_list",
			Description: anthropic.String(
				"List objects in an authenticated cloud storage bucket via the Vault. " +
					"Returns object keys and metadata. Use an optional prefix to narrow results. " +
					"Do NOT use to read object content — use vault_storage_read. " +
					"Do NOT include credential values in any parameter."),
			InputSchema: anthropic.ToolInputSchemaParam{
				Properties: map[string]interface{}{
					"bucket": map[string]interface{}{
						"type":        "string",
						"description": "Bucket or container name as configured in the Vault storage credential (e.g. \"prod-assets\", \"reports-archive\").",
					},
					"prefix": map[string]interface{}{
						"type":        "string",
						"description": "Optional key prefix to filter results (e.g. \"reports/2024-\"). Omit to list all objects in the bucket.",
					},
					"max_results": map[string]interface{}{
						"type":        "integer",
						"description": "Maximum number of objects to return. Defaults to 100 when omitted; hard cap enforced by the Vault.",
					},
				},
				Required: []string{"bucket"},
			},
		},
		Execute: func(ctx context.Context, raw json.RawMessage) ToolResult {
			return executeVaultStorageList(ctx, ve, raw)
		},
	}
}

func executeVaultStorageList(ctx context.Context, ve *VaultExecutor, raw json.RawMessage) ToolResult {
	var params struct {
		Bucket     string `json:"bucket"`
		Prefix     string `json:"prefix,omitempty"`
		MaxResults int    `json:"max_results,omitempty"`
	}
	if err := json.Unmarshal(raw, &params); err != nil {
		return ToolResult{
			Content: fmt.Sprintf("invalid parameters: %v", err),
			IsError: true,
			Details: map[string]interface{}{"error": err.Error()},
		}
	}
	if params.MaxResults == 0 {
		params.MaxResults = 100
	}

	opParams, err := json.Marshal(params)
	if err != nil {
		return ToolResult{
			Content: fmt.Sprintf("failed to encode operation params: %v", err),
			IsError: true,
			Details: map[string]interface{}{"error": err.Error()},
		}
	}

	onUpdate := func(p types.VaultOperationProgress) {
		ve.log.Info("vault storage_list: progress",
			"request_id", p.RequestID,
			"progress_type", p.ProgressType,
			"message", p.Message,
			"elapsed_ms", p.ElapsedMS,
		)
	}

	return ve.Execute(ctx, "storage_list", "storage_credential", opParams, 30, onUpdate)
}
