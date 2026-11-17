// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package otelgenai

import (
	"go.opentelemetry.io/otel/attribute"

	anthropicschema "github.com/envoyproxy/ai-gateway/internal/apischema/anthropic"
	"github.com/envoyproxy/ai-gateway/internal/apischema/openai"
	"github.com/envoyproxy/ai-gateway/internal/json"
)

// gen_ai.tool.definitions is opt-in content: tool descriptions and parameter
// schemas are authored by the caller and can carry proprietary detail.

// toolDefinition is one entry of the gen_ai.tool.definitions array.
type toolDefinition struct {
	Type        string `json:"type"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	// Parameters is the JSON schema describing the tool's arguments.
	Parameters any `json:"parameters,omitempty"`
}

// toolDefinitionsAttr marshals tool definitions, omitting the attribute when
// there are none.
func toolDefinitionsAttr(defs []toolDefinition) []attribute.KeyValue {
	if len(defs) == 0 {
		return nil
	}
	encoded, err := json.Marshal(defs)
	if err != nil {
		return nil
	}
	return []attribute.KeyValue{attribute.String(ToolDefinitions, string(encoded))}
}

// chatToolDefinitions extracts the tools offered on an OpenAI chat request.
//
// Only function tools carry a name and schema; provider-native tools such as
// google_search are recorded by type alone.
func chatToolDefinitions(req *openai.ChatCompletionRequest) []toolDefinition {
	defs := make([]toolDefinition, 0, len(req.Tools))
	for i := range req.Tools {
		tool := &req.Tools[i]
		def := toolDefinition{Type: string(tool.Type)}
		if fn := tool.Function; fn != nil {
			def.Name = fn.Name
			def.Description = fn.Description
			def.Parameters = fn.Parameters
		}
		if def.Type == "" && def.Name == "" {
			continue
		}
		defs = append(defs, def)
	}
	return defs
}

// anthropicToolDefinitions extracts the tools offered on a messages request.
//
// Custom tools carry a schema. The provider-native tools (bash, text editor,
// web search) are fixed capabilities identified by type and name only.
func anthropicToolDefinitions(req *anthropicschema.MessagesRequest) []toolDefinition {
	return anthropicTools(req.Tools)
}

// anthropicTools extracts tool definitions from the tools slice, shared by the
// endpoints that accept one.
func anthropicTools(in []anthropicschema.ToolUnion) []toolDefinition {
	defs := make([]toolDefinition, 0, len(in))
	for i := range in {
		switch tool := &in[i]; {
		case tool.Tool != nil:
			def := toolDefinition{
				Type:        tool.Tool.Type,
				Name:        tool.Tool.Name,
				Description: tool.Tool.Description,
			}
			// Assigning an absent schema would emit "parameters":null, because
			// a nil json.RawMessage held in an any field is a non-nil
			// interface and omitempty does not drop it. A null there reads as
			// "takes no arguments" rather than "no schema was declared".
			if len(tool.Tool.InputSchema) > 0 {
				def.Parameters = tool.Tool.InputSchema
			}
			defs = append(defs, def)
		case tool.BashTool != nil:
			defs = append(defs, toolDefinition{Type: tool.BashTool.Type, Name: tool.BashTool.Name})
		case tool.WebSearchTool != nil:
			defs = append(defs, toolDefinition{Type: tool.WebSearchTool.Type, Name: tool.WebSearchTool.Name})
		case tool.TextEditorTool20250124 != nil:
			defs = append(defs, toolDefinition{Type: tool.TextEditorTool20250124.Type, Name: tool.TextEditorTool20250124.Name})
		case tool.TextEditorTool20250429 != nil:
			defs = append(defs, toolDefinition{Type: tool.TextEditorTool20250429.Type, Name: tool.TextEditorTool20250429.Name})
		case tool.TextEditorTool20250728 != nil:
			defs = append(defs, toolDefinition{Type: tool.TextEditorTool20250728.Type, Name: tool.TextEditorTool20250728.Name})
		}
	}
	return defs
}
