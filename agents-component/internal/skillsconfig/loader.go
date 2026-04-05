// Package skillsconfig loads skill definitions from YAML or JSON configuration
// files and converts them into types usable by the Skill Hierarchy Manager (M4)
// and the agent-process tool registry.
//
// The package embeds a default_skills.yaml that covers all built-in skill
// domains. When AEGIS_SKILLS_CONFIG_PATH is unset the embedded default is used
// automatically, so the component works correctly with zero configuration.
//
// To extend the built-in skill set at deployment time, set
// AEGIS_SKILLS_CONFIG_PATH to a YAML or JSON file that follows the same schema
// as default_skills.yaml. The file replaces (not merges with) the embedded
// default, so it must include every domain the component should serve.
package skillsconfig

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"go.yaml.in/yaml/v2"

	"github.com/cerberOS/agents-component/pkg/types"
)

//go:embed default_skills.yaml
var defaultSkillsYAML []byte

// Config is the top-level structure of a skill definition file.
type Config struct {
	Domains []DomainDef `yaml:"domains" json:"domains"`
}

// DomainDef describes one skill domain (e.g. "web", "data").
type DomainDef struct {
	Name     string       `yaml:"name" json:"name"`
	Commands []CommandDef `yaml:"commands" json:"commands"`
}

// CommandDef describes a single command within a domain.
//
// Implementation is resolved at agent-process startup against the builtin
// registry (cmd/agent-process/builtin_registry.go). It is not stored in
// types.SkillNode — M4 has no need for it.
type CommandDef struct {
	Name                    string     `yaml:"name" json:"name"`
	Label                   string     `yaml:"label" json:"label"`
	Description             string     `yaml:"description" json:"description"`
	Implementation          string     `yaml:"implementation" json:"implementation"`
	RequiredCredentialTypes []string   `yaml:"required_credential_types" json:"required_credential_types"`
	TimeoutSeconds          int        `yaml:"timeout_seconds" json:"timeout_seconds"`
	Parameters              []ParamDef `yaml:"parameters" json:"parameters"`
}

// ParamDef describes a single parameter in a command's input schema.
//
// AdditionalPropertiesType, when set, adds an "additionalProperties" entry to
// the JSON Schema for object-typed parameters (e.g. the comms_format variables
// map where every value is a string).
type ParamDef struct {
	Name                     string   `yaml:"name" json:"name"`
	Type                     string   `yaml:"type" json:"type"`
	Required                 bool     `yaml:"required" json:"required"`
	Description              string   `yaml:"description" json:"description"`
	Enum                     []string `yaml:"enum,omitempty" json:"enum,omitempty"`
	AdditionalPropertiesType string   `yaml:"additional_properties_type,omitempty" json:"additional_properties_type,omitempty"`
}

// Load reads a skill configuration from the file at path.
//
// Files with a .json extension are parsed as JSON; all other paths are parsed
// as YAML. When path is empty, the embedded default_skills.yaml is used.
//
// Returns an error if the file cannot be read or if parsing fails. The returned
// Config is always non-nil on success.
func Load(path string) (*Config, error) {
	if path == "" {
		return parseYAML(defaultSkillsYAML)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("skillsconfig: read %q: %w", path, err)
	}

	if strings.ToLower(filepath.Ext(path)) == ".json" {
		return parseJSON(data)
	}
	return parseYAML(data)
}

func parseYAML(data []byte) (*Config, error) {
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("skillsconfig: YAML parse: %w", err)
	}
	return &cfg, nil
}

func parseJSON(data []byte) (*Config, error) {
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("skillsconfig: JSON parse: %w", err)
	}
	return &cfg, nil
}

// ToSkillNodes converts the config into SkillNode trees for M4 registration.
//
// Each DomainDef becomes a domain-level SkillNode. Each CommandDef becomes a
// command-level child with Label, Description, RequiredCredentialTypes,
// TimeoutSeconds, and a full SkillSpec (parameters without Enum or
// AdditionalPropertiesType — those fields are used only in the agent-process
// Anthropic tool schema).
func (c *Config) ToSkillNodes() []*types.SkillNode {
	nodes := make([]*types.SkillNode, 0, len(c.Domains))
	for _, d := range c.Domains {
		domain := &types.SkillNode{
			Name:     d.Name,
			Level:    "domain",
			Children: make(map[string]*types.SkillNode),
		}
		for _, cmd := range d.Commands {
			params := make(map[string]types.ParameterDef, len(cmd.Parameters))
			for _, p := range cmd.Parameters {
				params[p.Name] = types.ParameterDef{
					Type:        p.Type,
					Required:    p.Required,
					Description: p.Description,
				}
			}
			var spec *types.SkillSpec
			if len(params) > 0 {
				spec = &types.SkillSpec{Parameters: params}
			}
			domain.Children[cmd.Name] = &types.SkillNode{
				Name:                    cmd.Name,
				Level:                   "command",
				Label:                   cmd.Label,
				Description:             cmd.Description,
				RequiredCredentialTypes: cmd.RequiredCredentialTypes,
				TimeoutSeconds:          cmd.TimeoutSeconds,
				Spec:                    spec,
			}
		}
		nodes = append(nodes, domain)
	}
	return nodes
}
