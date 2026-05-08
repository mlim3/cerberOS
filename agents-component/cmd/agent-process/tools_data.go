// Package main — tools_data.go implements the "data" skill domain tools.
//
// The data domain provides:
//   - data_transform: local JSON extraction and inspection (no credentials).
//   - vault_data_read: authenticated data store read via Vault.
//   - vault_data_write: authenticated data store write/upsert via Vault.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/cerberOS/agents-component/internal/logfields"
	"github.com/cerberOS/agents-component/pkg/types"
)

// dataTransformTool inspects or extracts values from a locally-held JSON document.
// No credentials are required — the data is passed as a parameter.
func dataTransformTool() SkillTool {
	return SkillTool{
		Label:                   "Data Transform",
		RequiredCredentialTypes: nil,
		TimeoutSeconds:          10,
		Definition: anthropic.ToolParam{
			Name: "data_transform",
			Description: anthropic.String(
				"Transform or inspect a local JSON value. " +
					"Use to extract fields, list object keys, or measure array length. " +
					"Do NOT use when the data must be fetched from an authenticated source " +
					"— use vault_data_read for that."),
			InputSchema: anthropic.ToolInputSchemaParam{
				Properties: map[string]interface{}{
					"data": map[string]interface{}{
						"type":        "string",
						"description": "The JSON value to operate on, encoded as a string.",
					},
					"path": map[string]interface{}{
						"type": "string",
						"description": "Dot-notation path to navigate before applying the operation. " +
							"Examples: \".name\", \".users.0.email\", \".items.2\". " +
							"Omit or pass \".\" to operate on the root value.",
					},
					"operation": map[string]interface{}{
						"type": "string",
						"description": "What to do at the resolved path. " +
							"\"extract\": return the JSON value. " +
							"\"keys\": return sorted keys of a JSON object. " +
							"\"length\": return array length, object key count, or string byte length.",
						"enum": []string{"extract", "keys", "length"},
					},
				},
				Required: []string{"data", "operation"},
			},
		},
		Execute: executeDataTransform,
	}
}

func executeDataTransform(_ context.Context, raw json.RawMessage) ToolResult {
	var params struct {
		Data      string `json:"data"`
		Path      string `json:"path"`
		Operation string `json:"operation"`
	}
	if err := json.Unmarshal(raw, &params); err != nil {
		return ToolResult{
			Content: fmt.Sprintf("invalid parameters: %v", err),
			IsError: true,
			Details: map[string]interface{}{"error": err.Error()},
		}
	}

	var dataVal interface{}
	if err := json.Unmarshal([]byte(params.Data), &dataVal); err != nil {
		return ToolResult{
			Content: fmt.Sprintf("data field is not valid JSON: %v", err),
			IsError: true,
			Details: map[string]interface{}{"error": err.Error()},
		}
	}

	target, err := navigateJSONPath(dataVal, params.Path)
	if err != nil {
		return ToolResult{
			Content: fmt.Sprintf("path navigation failed: %v", err),
			IsError: true,
			Details: map[string]interface{}{"error": err.Error(), "path": params.Path},
		}
	}

	switch params.Operation {
	case "extract":
		out, err := json.Marshal(target)
		if err != nil {
			return ToolResult{
				Content: fmt.Sprintf("marshal result: %v", err),
				IsError: true,
				Details: map[string]interface{}{"error": err.Error()},
			}
		}
		return ToolResult{
			Content: string(out),
			Details: map[string]interface{}{"operation": "extract", "path": params.Path},
		}

	case "keys":
		m, ok := target.(map[string]interface{})
		if !ok {
			return ToolResult{
				Content: fmt.Sprintf("keys operation requires a JSON object; got %T at path %q", target, params.Path),
				IsError: true,
				Details: map[string]interface{}{"path": params.Path},
			}
		}
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		out, _ := json.Marshal(keys)
		return ToolResult{
			Content: string(out),
			Details: map[string]interface{}{"operation": "keys", "count": len(keys)},
		}

	case "length":
		switch v := target.(type) {
		case []interface{}:
			return ToolResult{
				Content: fmt.Sprintf("%d", len(v)),
				Details: map[string]interface{}{"operation": "length", "type": "array"},
			}
		case map[string]interface{}:
			return ToolResult{
				Content: fmt.Sprintf("%d", len(v)),
				Details: map[string]interface{}{"operation": "length", "type": "object"},
			}
		case string:
			return ToolResult{
				Content: fmt.Sprintf("%d", len(v)),
				Details: map[string]interface{}{"operation": "length", "type": "string"},
			}
		default:
			return ToolResult{
				Content: fmt.Sprintf("length: unsupported type %T", target),
				IsError: true,
			}
		}

	default:
		return ToolResult{
			Content: fmt.Sprintf("unknown operation %q; valid values: extract, keys, length", params.Operation),
			IsError: true,
		}
	}
}

// navigateJSONPath walks a decoded JSON value along a dot-notation path.
// An empty path or "." returns the root value unchanged.
// Array elements are addressed by numeric index: ".items.0".
func navigateJSONPath(data interface{}, path string) (interface{}, error) {
	if path == "" || path == "." {
		return data, nil
	}
	parts := strings.Split(strings.TrimPrefix(path, "."), ".")
	current := data
	for _, part := range parts {
		if part == "" {
			continue
		}
		switch v := current.(type) {
		case map[string]interface{}:
			val, ok := v[part]
			if !ok {
				return nil, fmt.Errorf("key %q not found in object", part)
			}
			current = val
		case []interface{}:
			idx, err := strconv.Atoi(part)
			if err != nil {
				return nil, fmt.Errorf("path segment %q is not a valid array index", part)
			}
			if idx < 0 || idx >= len(v) {
				return nil, fmt.Errorf("array index %d out of range (length %d)", idx, len(v))
			}
			current = v[idx]
		default:
			return nil, fmt.Errorf("cannot navigate into %T with path segment %q", current, part)
		}
	}
	return current, nil
}

// vaultDataReadTool reads records from an authenticated data store via the Vault.
func vaultDataReadTool(ve *VaultExecutor) SkillTool {
	return SkillTool{
		Label:                   "Vault Data Read",
		RequiredCredentialTypes: []string{"data_read_key"},
		TimeoutSeconds:          35,
		Definition: anthropic.ToolParam{
			Name: "vault_data_read",
			Description: anthropic.String(
				"Read records from an authenticated data store via the Vault. " +
					"Provide a collection name and query expression. " +
					"Do NOT use for unauthenticated HTTP sources — use web_fetch. " +
					"Do NOT include credential values in any parameter."),
			InputSchema: anthropic.ToolInputSchemaParam{
				Properties: map[string]interface{}{
					"collection": map[string]interface{}{
						"type":        "string",
						"description": "Name of the table, index, or collection to read from (e.g. \"users\", \"orders\").",
					},
					"query": map[string]interface{}{
						"type":        "string",
						"description": "Query expression or record ID. Interpretation is data-store specific (e.g. a SQL WHERE clause, a document ID, or a filter string).",
					},
				},
				Required: []string{"collection", "query"},
			},
		},
		Execute: func(ctx context.Context, raw json.RawMessage) ToolResult {
			return executeVaultDataRead(ctx, ve, raw)
		},
	}
}

func executeVaultDataRead(ctx context.Context, ve *VaultExecutor, raw json.RawMessage) ToolResult {
	var params struct {
		Collection string `json:"collection"`
		Query      string `json:"query"`
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
		ve.log.Info("vault forwarded a progress update for in-flight data_read operation",
			"operation_type", "data_read",
			"request_id", p.RequestID,
			"progress_type", p.ProgressType,
			"message_preview", logfields.PreviewWords(p.Message, 20, 140),
			"elapsed_ms", p.ElapsedMS,
		)
	}

	return ve.Execute(ctx, "data_read", "data_read_key", opParams, 30, onUpdate)
}

// vaultDataWriteTool writes or upserts a record into an authenticated data store via the Vault.
func vaultDataWriteTool(ve *VaultExecutor) SkillTool {
	return SkillTool{
		Label:                   "Vault Data Write",
		RequiredCredentialTypes: []string{"data_write_key"},
		TimeoutSeconds:          35,
		Definition: anthropic.ToolParam{
			Name: "vault_data_write",
			Description: anthropic.String(
				"Write or upsert a JSON record into an authenticated data store via the Vault. " +
					"Provide a collection name and a JSON-encoded record object. " +
					"Do NOT use for reading — use vault_data_read. " +
					"Do NOT include credential values in any parameter."),
			InputSchema: anthropic.ToolInputSchemaParam{
				Properties: map[string]interface{}{
					"collection": map[string]interface{}{
						"type":        "string",
						"description": "Name of the table, index, or collection to write to (e.g. \"users\", \"events\").",
					},
					"record": map[string]interface{}{
						"type":        "string",
						"description": "The record to write, encoded as a JSON object string. Must be a valid JSON object.",
					},
				},
				Required: []string{"collection", "record"},
			},
		},
		Execute: func(ctx context.Context, raw json.RawMessage) ToolResult {
			return executeVaultDataWrite(ctx, ve, raw)
		},
	}
}

func executeVaultDataWrite(ctx context.Context, ve *VaultExecutor, raw json.RawMessage) ToolResult {
	var params struct {
		Collection string `json:"collection"`
		Record     string `json:"record"`
	}
	if err := json.Unmarshal(raw, &params); err != nil {
		return ToolResult{
			Content: fmt.Sprintf("invalid parameters: %v", err),
			IsError: true,
			Details: map[string]interface{}{"error": err.Error()},
		}
	}

	// Validate that record is a JSON object before forwarding to the Vault.
	var recordCheck map[string]interface{}
	if err := json.Unmarshal([]byte(params.Record), &recordCheck); err != nil {
		return ToolResult{
			Content: fmt.Sprintf("record must be a valid JSON object: %v", err),
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
		ve.log.Info("vault forwarded a progress update for in-flight data_write operation",
			"operation_type", "data_write",
			"request_id", p.RequestID,
			"progress_type", p.ProgressType,
			"message_preview", logfields.PreviewWords(p.Message, 20, 140),
			"elapsed_ms", p.ElapsedMS,
		)
	}

	return ve.Execute(ctx, "data_write", "data_write_key", opParams, 30, onUpdate)
}
