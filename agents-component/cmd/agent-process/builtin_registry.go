// Package main — builtin_registry.go maps implementation names to SkillTool
// factories for config-driven tool dispatch.
//
// Each entry in builtinRegistry corresponds to the "implementation" field in
// default_skills.yaml (and any custom skills config). When toolsForDomain builds
// the tool list from config, it looks up each command's implementation name here
// to obtain the concrete SkillTool (including its Execute function).
//
// To add a new built-in tool:
//  1. Implement the execute function and tool constructor in a tools_*.go file.
//  2. Add an entry here mapping the implementation name to a ToolFactory.
//  3. Add the command definition to default_skills.yaml (or a custom config file).
package main

// ToolFactory constructs a SkillTool for the given VaultExecutor.
// ve may be nil for local-only tools that do not require vault execution.
// Factories for vault tools must only be called when ve is non-nil (toolsForDomain
// enforces this by checking RequiredCredentialTypes before calling the factory).
type ToolFactory func(ve *VaultExecutor) SkillTool

// builtinRegistry maps implementation names to their ToolFactory.
// Keys must match the "implementation" fields in the skills config YAML/JSON.
var builtinRegistry = map[string]ToolFactory{
	// web domain
	"web_fetch":       func(_ *VaultExecutor) SkillTool { return webFetchTool() },
	"vault_web_fetch": func(ve *VaultExecutor) SkillTool { return vaultWebFetchTool(ve) },
	"web_search":      func(ve *VaultExecutor) SkillTool { return webSearchTool(ve) },
	"web_extract":     func(_ *VaultExecutor) SkillTool { return webExtractTool() },

	// logs domain
	"logs_query":  func(_ *VaultExecutor) SkillTool { return logsQueryTool(nil) },
	"logs_search": func(_ *VaultExecutor) SkillTool { return logsSearchTool(nil) },
	"logs_tail":   func(_ *VaultExecutor) SkillTool { return logsTailTool(nil) },
	"logs_agent":  func(_ *VaultExecutor) SkillTool { return logsAgentTool(nil) },

	// data domain
	"data_transform":   func(_ *VaultExecutor) SkillTool { return dataTransformTool() },
	"vault_data_read":  func(ve *VaultExecutor) SkillTool { return vaultDataReadTool(ve) },
	"vault_data_write": func(ve *VaultExecutor) SkillTool { return vaultDataWriteTool(ve) },

	// comms domain
	"comms_format":     func(_ *VaultExecutor) SkillTool { return commsFormatTool() },
	"vault_comms_send": func(ve *VaultExecutor) SkillTool { return vaultCommsSendTool(ve) },

	// storage domain
	"vault_storage_read":  func(ve *VaultExecutor) SkillTool { return vaultStorageReadTool(ve) },
	"vault_storage_write": func(ve *VaultExecutor) SkillTool { return vaultStorageWriteTool(ve) },
	"vault_storage_list":  func(ve *VaultExecutor) SkillTool { return vaultStorageListTool(ve) },
}
