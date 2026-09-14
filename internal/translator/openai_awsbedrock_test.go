// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package translator

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/shared/constant"
	"github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream"
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	openaigo "github.com/openai/openai-go/v3"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"k8s.io/utils/ptr"

	"github.com/envoyproxy/ai-gateway/internal/apischema/awsbedrock"
	"github.com/envoyproxy/ai-gateway/internal/apischema/openai"
	"github.com/envoyproxy/ai-gateway/internal/internalapi"
	"github.com/envoyproxy/ai-gateway/internal/json"
	"github.com/envoyproxy/ai-gateway/internal/metrics"
)

const releaseDateUnix = 1731679200 // Represents 2024-11-15 09:00:00 UTC

func getChatCompletionResponseChunk(body []byte) []openai.ChatCompletionResponseChunk {
	lines := bytes.Split(body, []byte("\n\n"))

	chunks := []openai.ChatCompletionResponseChunk{}
	for _, line := range lines {
		// Remove "data: " prefix from SSE format if present.
		line = bytes.TrimPrefix(line, []byte("data: "))

		// Try to parse as JSON.
		var chunk openai.ChatCompletionResponseChunk
		if err := json.Unmarshal(line, &chunk); err == nil {
			chunks = append(chunks, chunk)
		}
	}
	return chunks
}

func TestOpenAIToAWSBedrockTranslatorV1ChatCompletion_RequestBody(t *testing.T) {
	tests := []struct {
		name   string
		output awsbedrock.ConverseInput
		input  openai.ChatCompletionRequest
	}{
		{
			name: "basic test",
			input: openai.ChatCompletionRequest{
				Stream: false,
				Model:  "gpt-4o",
				Messages: []openai.ChatCompletionMessageParamUnion{
					{
						OfSystem: &openai.ChatCompletionSystemMessageParam{
							Content: openai.ContentUnion{
								Value: "from-system",
							},
							Role: openai.ChatMessageRoleSystem,
						},
					},
					{
						OfDeveloper: &openai.ChatCompletionDeveloperMessageParam{
							Content: openai.ContentUnion{
								Value: "from-developer",
							},
							Role: openai.ChatMessageRoleDeveloper,
						},
					},
					{
						OfUser: &openai.ChatCompletionUserMessageParam{
							Content: openai.StringOrUserRoleContentUnion{
								Value: "from-user",
							},
							Role: openai.ChatMessageRoleUser,
						},
					},
					{
						OfUser: &openai.ChatCompletionUserMessageParam{
							Content: openai.StringOrUserRoleContentUnion{
								Value: "part1",
							},
							Role: openai.ChatMessageRoleUser,
						},
					},
					{
						OfUser: &openai.ChatCompletionUserMessageParam{
							Content: openai.StringOrUserRoleContentUnion{
								Value: "part2",
							},
							Role: openai.ChatMessageRoleUser,
						},
					},
					{
						OfTool: &openai.ChatCompletionToolMessageParam{
							Content: openai.ContentUnion{
								Value: "Weather in Queens, NY is 70F and clear skies.",
							},
							ToolCallID: "call_6g7a",
							Role:       openai.ChatMessageRoleTool,
						},
					},
					{
						OfAssistant: &openai.ChatCompletionAssistantMessageParam{
							Content: openai.StringOrAssistantRoleContentUnion{
								Value: openai.ChatCompletionAssistantMessageParamContent{
									Type: openai.ChatCompletionAssistantMessageParamContentTypeText,
									Text: ptr.To("I dunno"),
								},
							},
							ToolCalls: []openai.ChatCompletionMessageToolCallParam{
								{
									ID: ptr.To("call_6g7a"),
									Function: openai.ChatCompletionMessageToolCallFunctionParam{
										Arguments: "{\"code_block\":\"from playwright.sync_api import sync_playwright\\n\"}",
										Name:      "exec_python_code",
									},
									Type: openai.ChatCompletionMessageToolCallTypeFunction,
								},
							},
							Role: openai.ChatMessageRoleAssistant,
						},
					},
					{
						OfAssistant: &openai.ChatCompletionAssistantMessageParam{
							Content: openai.StringOrAssistantRoleContentUnion{
								Value: "I also dunno",
							},
							Role: openai.ChatMessageRoleAssistant,
						},
					},
					{
						OfAssistant: &openai.ChatCompletionAssistantMessageParam{
							Content: openai.StringOrAssistantRoleContentUnion{
								Value: "",
							},
							Role: openai.ChatMessageRoleAssistant,
						},
					},
				},
			},
			output: awsbedrock.ConverseInput{
				InferenceConfig: &awsbedrock.InferenceConfiguration{},
				System: []*awsbedrock.SystemContentBlock{
					{
						Text: ptr.To("from-system"),
					},
					{
						Text: ptr.To("from-developer"),
					},
				},
				Messages: []*awsbedrock.Message{
					{
						Role: openai.ChatMessageRoleUser,
						Content: []*awsbedrock.ContentBlock{
							{
								Text: ptr.To("from-user"),
							},
						},
					},
					{
						Role: openai.ChatMessageRoleUser,
						Content: []*awsbedrock.ContentBlock{
							{
								Text: ptr.To("part1"),
							},
						},
					},
					{
						Role: openai.ChatMessageRoleUser,
						Content: []*awsbedrock.ContentBlock{
							{
								Text: ptr.To("part2"),
							},
						},
					},
					{
						Role: openai.ChatMessageRoleUser,
						Content: []*awsbedrock.ContentBlock{
							{
								ToolResult: &awsbedrock.ToolResultBlock{
									ToolUseID: ptr.To("call_6g7a"),
									Content: []*awsbedrock.ToolResultContentBlock{
										{
											Text: ptr.To("Weather in Queens, NY is 70F and clear skies."),
										},
									},
								},
							},
						},
					},
					{
						Role: openai.ChatMessageRoleAssistant,
						Content: []*awsbedrock.ContentBlock{
							{
								Text: ptr.To("I dunno"),
							},
							{
								ToolUse: &awsbedrock.ToolUseBlock{
									Name:      "exec_python_code",
									ToolUseID: "call_6g7a",
									Input:     map[string]any{"code_block": "from playwright.sync_api import sync_playwright\n"},
								},
							},
						},
					},
					{
						Role: openai.ChatMessageRoleAssistant,
						Content: []*awsbedrock.ContentBlock{
							{
								Text: ptr.To("I also dunno"),
							},
						},
					},
				},
			},
		},
		{
			name: "test content array",
			input: openai.ChatCompletionRequest{
				Stream: false,
				Model:  "gpt-4o",
				Messages: []openai.ChatCompletionMessageParamUnion{
					{
						OfSystem: &openai.ChatCompletionSystemMessageParam{
							Content: openai.ContentUnion{
								Value: []openai.ChatCompletionContentPartTextParam{
									{Text: "from-system"},
								},
							},
							Role: openai.ChatMessageRoleSystem,
						},
					},
					{
						OfDeveloper: &openai.ChatCompletionDeveloperMessageParam{
							Content: openai.ContentUnion{
								Value: []openai.ChatCompletionContentPartTextParam{
									{Text: "from-developer"},
								},
							},
							Role: openai.ChatMessageRoleDeveloper,
						},
					},
					{
						OfUser: &openai.ChatCompletionUserMessageParam{
							Content: openai.StringOrUserRoleContentUnion{
								Value: []openai.ChatCompletionContentPartUserUnionParam{
									{OfText: &openai.ChatCompletionContentPartTextParam{Text: "from-user"}},
								},
							},
							Role: openai.ChatMessageRoleUser,
						},
					},
					{
						OfUser: &openai.ChatCompletionUserMessageParam{
							Content: openai.StringOrUserRoleContentUnion{
								Value: []openai.ChatCompletionContentPartUserUnionParam{
									{OfText: &openai.ChatCompletionContentPartTextParam{Text: "user1"}},
								},
							},
							Role: openai.ChatMessageRoleUser,
						},
					},
					{
						OfUser: &openai.ChatCompletionUserMessageParam{
							Content: openai.StringOrUserRoleContentUnion{
								Value: []openai.ChatCompletionContentPartUserUnionParam{
									{OfText: &openai.ChatCompletionContentPartTextParam{Text: "user2"}},
								},
							},
							Role: openai.ChatMessageRoleUser,
						},
					},
				},
			},
			output: awsbedrock.ConverseInput{
				InferenceConfig: &awsbedrock.InferenceConfiguration{},
				System: []*awsbedrock.SystemContentBlock{
					{
						Text: ptr.To("from-system"),
					},
					{
						Text: ptr.To("from-developer"),
					},
				},
				Messages: []*awsbedrock.Message{
					{
						Role: openai.ChatMessageRoleUser,
						Content: []*awsbedrock.ContentBlock{
							{
								Text: ptr.To("from-user"),
							},
						},
					},
					{
						Role: openai.ChatMessageRoleUser,
						Content: []*awsbedrock.ContentBlock{
							{
								Text: ptr.To("user1"),
							},
						},
					},
					{
						Role: openai.ChatMessageRoleUser,
						Content: []*awsbedrock.ContentBlock{
							{
								Text: ptr.To("user2"),
							},
						},
					},
				},
			},
		},
		{
			name: "test image",
			input: openai.ChatCompletionRequest{
				Stream: false,
				Model:  "gpt-4o",
				Messages: []openai.ChatCompletionMessageParamUnion{
					{
						OfSystem: &openai.ChatCompletionSystemMessageParam{
							Content: openai.ContentUnion{
								Value: []openai.ChatCompletionContentPartTextParam{
									{Text: "from-system"},
								},
							},
							Role: openai.ChatMessageRoleSystem,
						},
					},
					{
						OfUser: &openai.ChatCompletionUserMessageParam{
							Content: openai.StringOrUserRoleContentUnion{
								Value: []openai.ChatCompletionContentPartUserUnionParam{
									{OfImageURL: &openai.ChatCompletionContentPartImageParam{
										ImageURL: openai.ChatCompletionContentPartImageImageURLParam{
											URL: "data:image/jpeg;base64,dGVzdA==",
										},
									}},
								},
							},
							Role: openai.ChatMessageRoleUser,
						},
					},
				},
			},
			output: awsbedrock.ConverseInput{
				InferenceConfig: &awsbedrock.InferenceConfiguration{},
				System: []*awsbedrock.SystemContentBlock{
					{
						Text: ptr.To("from-system"),
					},
				},
				Messages: []*awsbedrock.Message{
					{
						Role: openai.ChatMessageRoleUser,
						Content: []*awsbedrock.ContentBlock{
							{
								Image: &awsbedrock.ImageBlock{
									Source: awsbedrock.ImageSource{
										Bytes: []byte("test"),
									},
									Format: "jpeg",
								},
							},
						},
					},
				},
			},
		},
		{
			name: "test parameters",
			input: openai.ChatCompletionRequest{
				Stream:      false,
				Model:       "gpt-4o",
				MaxTokens:   ptr.To(int64(10)),
				TopP:        ptr.To(float64(1)),
				Temperature: ptr.To(0.7),
				Messages: []openai.ChatCompletionMessageParamUnion{
					{
						OfUser: &openai.ChatCompletionUserMessageParam{
							Content: openai.StringOrUserRoleContentUnion{
								Value: "from-user",
							},
							Role: openai.ChatMessageRoleUser,
						},
					},
				},
			},
			output: awsbedrock.ConverseInput{
				InferenceConfig: &awsbedrock.InferenceConfiguration{
					MaxTokens:   ptr.To(int64(10)),
					TopP:        ptr.To(float64(1)),
					Temperature: ptr.To(0.7),
				},
				Messages: []*awsbedrock.Message{
					{
						Role: openai.ChatMessageRoleUser,
						Content: []*awsbedrock.ContentBlock{
							{
								Text: ptr.To("from-user"),
							},
						},
					},
				},
			},
		},
		{
			name: "test tools function calling with empty tool choice",
			input: openai.ChatCompletionRequest{
				Stream:      false,
				Model:       "gpt-4o",
				MaxTokens:   ptr.To(int64(10)),
				TopP:        ptr.To(float64(1)),
				ServiceTier: "reserved",
				Temperature: ptr.To(0.7),
				Messages: []openai.ChatCompletionMessageParamUnion{
					{
						OfUser: &openai.ChatCompletionUserMessageParam{
							Content: openai.StringOrUserRoleContentUnion{
								Value: "from-user",
							},
							Role: openai.ChatMessageRoleUser,
						},
					},
				},
				Tools: []openai.Tool{
					{
						Type: "function",
						Function: &openai.FunctionDefinition{
							Name:        "get_current_weather",
							Description: "Get the current weather in a given location",
							Parameters: map[string]any{
								"type": "object",
								"properties": map[string]any{
									"location": map[string]any{
										"type":        "string",
										"description": "The city and state, e.g. San Francisco, CA",
									},
									"unit": map[string]any{
										"type": "string",
										"enum": []string{"celsius", "fahrenheit"},
									},
								},
								"required": []string{"location"},
							},
						},
					},
				},
			},
			output: awsbedrock.ConverseInput{
				InferenceConfig: &awsbedrock.InferenceConfiguration{
					MaxTokens:   ptr.To(int64(10)),
					TopP:        ptr.To(float64(1)),
					Temperature: ptr.To(0.7),
				},
				ServiceTier: &awsbedrock.ServiceTier{Type: "reserved"},
				Messages: []*awsbedrock.Message{
					{
						Role: openai.ChatMessageRoleUser,
						Content: []*awsbedrock.ContentBlock{
							{
								Text: ptr.To("from-user"),
							},
						},
					},
				},
				ToolConfig: &awsbedrock.ToolConfiguration{
					Tools: []*awsbedrock.Tool{
						{
							ToolSpec: &awsbedrock.ToolSpecification{
								Name:        ptr.To("get_current_weather"),
								Description: ptr.To("Get the current weather in a given location"),
								InputSchema: &awsbedrock.ToolInputSchema{
									JSON: map[string]any{
										"type": "object",
										"properties": map[string]any{
											"location": map[string]any{
												"type":        "string",
												"description": "The city and state, e.g. San Francisco, CA",
											},
											"unit": map[string]any{
												"type": "string",
												"enum": []any{"celsius", "fahrenheit"},
											},
										},
										"required": []any{"location"},
									},
								},
							},
						},
					},
				},
			},
		},
		{
			name: "test auto tool choice",
			input: openai.ChatCompletionRequest{
				Model:       "gpt-4o",
				ServiceTier: "default",
				Messages: []openai.ChatCompletionMessageParamUnion{
					{
						OfUser: &openai.ChatCompletionUserMessageParam{
							Content: openai.StringOrUserRoleContentUnion{
								Value: "from-user",
							},
							Role: openai.ChatMessageRoleUser,
						},
					},
				},
				Tools: []openai.Tool{
					{
						Type: "function",
						Function: &openai.FunctionDefinition{
							Name:        "get_current_weather",
							Description: "Get the current weather in a given location",
						},
					},
				},
				ToolChoice: &openai.ChatCompletionToolChoiceUnion{Value: "auto"},
			},
			output: awsbedrock.ConverseInput{
				InferenceConfig: &awsbedrock.InferenceConfiguration{},
				ServiceTier:     &awsbedrock.ServiceTier{Type: "default"},
				Messages: []*awsbedrock.Message{
					{
						Role: openai.ChatMessageRoleUser,
						Content: []*awsbedrock.ContentBlock{
							{
								Text: ptr.To("from-user"),
							},
						},
					},
				},
				ToolConfig: &awsbedrock.ToolConfiguration{
					Tools: []*awsbedrock.Tool{
						{
							ToolSpec: &awsbedrock.ToolSpecification{
								Name:        ptr.To("get_current_weather"),
								Description: ptr.To("Get the current weather in a given location"),
								InputSchema: &awsbedrock.ToolInputSchema{},
							},
						},
					},
					ToolChoice: &awsbedrock.ToolChoice{Auto: &awsbedrock.AutoToolChoice{}},
				},
			},
		},
		{
			name: "test required tool choice",
			input: openai.ChatCompletionRequest{
				Model: "gpt-4o",
				Messages: []openai.ChatCompletionMessageParamUnion{
					{
						OfUser: &openai.ChatCompletionUserMessageParam{
							Content: openai.StringOrUserRoleContentUnion{
								Value: "from-user",
							},
							Role: openai.ChatMessageRoleUser,
						},
					},
				},
				Tools: []openai.Tool{
					{
						Type: "function",
						Function: &openai.FunctionDefinition{
							Name:        "get_current_weather",
							Description: "Get the current weather in a given location",
						},
					},
				},
				ToolChoice: &openai.ChatCompletionToolChoiceUnion{Value: "required"},
			},
			output: awsbedrock.ConverseInput{
				InferenceConfig: &awsbedrock.InferenceConfiguration{},
				Messages: []*awsbedrock.Message{
					{
						Role: openai.ChatMessageRoleUser,
						Content: []*awsbedrock.ContentBlock{
							{
								Text: ptr.To("from-user"),
							},
						},
					},
				},
				ToolConfig: &awsbedrock.ToolConfiguration{
					Tools: []*awsbedrock.Tool{
						{
							ToolSpec: &awsbedrock.ToolSpecification{
								Name:        ptr.To("get_current_weather"),
								Description: ptr.To("Get the current weather in a given location"),
								InputSchema: &awsbedrock.ToolInputSchema{},
							},
						},
					},
					ToolChoice: &awsbedrock.ToolChoice{Any: &awsbedrock.AnyToolChoice{}},
				},
			},
		},
		{
			name: "test tool choice for anthropic claude model",
			input: openai.ChatCompletionRequest{
				Model: "bedrock.anthropic.claude-3-5-sonnet-20240620-v1:0",
				Messages: []openai.ChatCompletionMessageParamUnion{
					{
						OfUser: &openai.ChatCompletionUserMessageParam{
							Content: openai.StringOrUserRoleContentUnion{
								Value: "from-user",
							},
							Role: openai.ChatMessageRoleUser,
						},
					},
				},
				Tools: []openai.Tool{
					{
						Type: "function",
						Function: &openai.FunctionDefinition{
							Name:        "get_current_weather",
							Description: "Get the current weather in a given location",
						},
					},
				},
				ToolChoice: &openai.ChatCompletionToolChoiceUnion{Value: "some-tools"},
			},
			output: awsbedrock.ConverseInput{
				InferenceConfig: &awsbedrock.InferenceConfiguration{},
				Messages: []*awsbedrock.Message{
					{
						Role: openai.ChatMessageRoleUser,
						Content: []*awsbedrock.ContentBlock{
							{
								Text: ptr.To("from-user"),
							},
						},
					},
				},
				ToolConfig: &awsbedrock.ToolConfiguration{
					Tools: []*awsbedrock.Tool{
						{
							ToolSpec: &awsbedrock.ToolSpecification{
								Name:        ptr.To("get_current_weather"),
								Description: ptr.To("Get the current weather in a given location"),
								InputSchema: &awsbedrock.ToolInputSchema{},
							},
						},
					},
					ToolChoice: &awsbedrock.ToolChoice{
						Tool: &awsbedrock.SpecificToolChoice{
							Name: ptr.To("some-tools"),
						},
					},
				},
			},
		},
		{
			name: "test tool choices for anthropic claude model",
			input: openai.ChatCompletionRequest{
				Model: "bedrock.anthropic.claude-3-5-sonnet-20240620-v1:0",
				Messages: []openai.ChatCompletionMessageParamUnion{
					{
						OfUser: &openai.ChatCompletionUserMessageParam{
							Content: openai.StringOrUserRoleContentUnion{
								Value: "from-user",
							},
							Role: openai.ChatMessageRoleUser,
						},
					},
				},
				Tools: []openai.Tool{
					{
						Type: "function",
						Function: &openai.FunctionDefinition{
							Name:        "get_current_weather",
							Description: "Get the current weather in a given location",
						},
					},
				},
				ToolChoice: &openai.ChatCompletionToolChoiceUnion{
					Value: openai.ChatCompletionNamedToolChoice{
						Type: openai.ToolType("function"),
						Function: openai.ChatCompletionNamedToolChoiceFunction{
							Name: "my_function",
						},
					},
				},
			},
			output: awsbedrock.ConverseInput{
				InferenceConfig: &awsbedrock.InferenceConfiguration{},
				Messages: []*awsbedrock.Message{
					{
						Role: openai.ChatMessageRoleUser,
						Content: []*awsbedrock.ContentBlock{
							{
								Text: ptr.To("from-user"),
							},
						},
					},
				},
				ToolConfig: &awsbedrock.ToolConfiguration{
					Tools: []*awsbedrock.Tool{
						{
							ToolSpec: &awsbedrock.ToolSpecification{
								Name:        ptr.To("get_current_weather"),
								Description: ptr.To("Get the current weather in a given location"),
								InputSchema: &awsbedrock.ToolInputSchema{},
							},
						},
					},
					ToolChoice: &awsbedrock.ToolChoice{
						Tool: &awsbedrock.SpecificToolChoice{
							Name: ptr.To("my_function"),
						},
					},
				},
			},
		},
		{
			name: "test single stop word",
			input: openai.ChatCompletionRequest{
				Model: "gpt-4o",
				Messages: []openai.ChatCompletionMessageParamUnion{
					{
						OfUser: &openai.ChatCompletionUserMessageParam{
							Content: openai.StringOrUserRoleContentUnion{
								Value: "from-user",
							},
							Role: openai.ChatMessageRoleUser,
						},
					},
				},
				Stop: openaigo.ChatCompletionNewParamsStopUnion{
					OfString: openaigo.Opt[string]("stop_only"),
				},
			},
			output: awsbedrock.ConverseInput{
				InferenceConfig: &awsbedrock.InferenceConfiguration{
					StopSequences: []string{"stop_only"},
				},
				Messages: []*awsbedrock.Message{
					{
						Role: openai.ChatMessageRoleUser,
						Content: []*awsbedrock.ContentBlock{
							{
								Text: ptr.To("from-user"),
							},
						},
					},
				},
			},
		},
		{
			name: "test stop sequence",
			input: openai.ChatCompletionRequest{
				Model: "gpt-4o",
				Messages: []openai.ChatCompletionMessageParamUnion{
					{
						OfUser: &openai.ChatCompletionUserMessageParam{
							Content: openai.StringOrUserRoleContentUnion{
								Value: "from-user",
							},
							Role: openai.ChatMessageRoleUser,
						},
					},
				},
				Stop: openaigo.ChatCompletionNewParamsStopUnion{
					OfStringArray: []string{"stop1", "stop2"},
				},
			},
			output: awsbedrock.ConverseInput{
				InferenceConfig: &awsbedrock.InferenceConfiguration{
					StopSequences: []string{"stop1", "stop2"},
				},
				Messages: []*awsbedrock.Message{
					{
						Role: openai.ChatMessageRoleUser,
						Content: []*awsbedrock.ContentBlock{
							{
								Text: ptr.To("from-user"),
							},
						},
					},
				},
			},
		},
		{
			name: "test parallel tool calls for anthropic claude model",
			input: openai.ChatCompletionRequest{
				Model: "bedrock.anthropic.claude-3-5-sonnet-20240620-v1:0",
				Messages: []openai.ChatCompletionMessageParamUnion{
					{
						OfUser: &openai.ChatCompletionUserMessageParam{
							Role: openai.ChatMessageRoleUser,
							Content: openai.StringOrUserRoleContentUnion{
								Value: "What is the weather in Dallas, Texas and Orlando, Florida in Fahrenheit?",
							},
						},
					},
					{
						OfAssistant: &openai.ChatCompletionAssistantMessageParam{
							Role: openai.ChatMessageRoleAssistant,
							ToolCalls: []openai.ChatCompletionMessageToolCallParam{
								{
									ID: ptr.To("tool-1"),
									Function: openai.ChatCompletionMessageToolCallFunctionParam{
										Name:      "get_current_weather",
										Arguments: "{\"city\": \"Dallas\", \"state\": \"TX\", \"unit\": \"fahrenheit\"}",
									},
									Type: openai.ChatCompletionMessageToolCallType(openai.ToolTypeFunction),
								},
								{
									ID: ptr.To("tool-2"),
									Function: openai.ChatCompletionMessageToolCallFunctionParam{
										Name:      "get_current_weather",
										Arguments: "{\"city\": \"Orlando\", \"state\": \"FL\", \"unit\": \"fahrenheit\"}",
									},
									Type: openai.ChatCompletionMessageToolCallType(openai.ToolTypeFunction),
								},
							},
						},
					},
					{
						OfTool: &openai.ChatCompletionToolMessageParam{
							Content: openai.ContentUnion{
								Value: "The weather in Dallas TX is 98 degrees fahrenheit with mostly cloudy skies and a change of rain in the evening.",
							},
							Role:       openai.ChatMessageRoleTool,
							ToolCallID: "tool-1",
						},
					},
					{
						OfTool: &openai.ChatCompletionToolMessageParam{
							Content: openai.ContentUnion{
								Value: "The weather in Orlando FL is 78 degrees fahrenheit with clear skies.",
							},
							Role:       openai.ChatMessageRoleTool,
							ToolCallID: "tool-2",
						},
					},
				},
			},
			output: awsbedrock.ConverseInput{
				InferenceConfig: &awsbedrock.InferenceConfiguration{},
				Messages: []*awsbedrock.Message{
					{
						Role: openai.ChatMessageRoleUser,
						Content: []*awsbedrock.ContentBlock{
							{
								Text: ptr.To("What is the weather in Dallas, Texas and Orlando, Florida in Fahrenheit?"),
							},
						},
					},
					{
						Role: openai.ChatMessageRoleAssistant,
						Content: []*awsbedrock.ContentBlock{
							{
								ToolUse: &awsbedrock.ToolUseBlock{
									Name:      "get_current_weather",
									ToolUseID: "tool-1",
									Input:     map[string]any{"city": "Dallas", "state": "TX", "unit": "fahrenheit"},
								},
							},
							{
								ToolUse: &awsbedrock.ToolUseBlock{
									Name:      "get_current_weather",
									ToolUseID: "tool-2",
									Input:     map[string]any{"city": "Orlando", "state": "FL", "unit": "fahrenheit"},
								},
							},
						},
					},
					{
						Role: awsbedrock.ConversationRoleUser,
						Content: []*awsbedrock.ContentBlock{
							{
								ToolResult: &awsbedrock.ToolResultBlock{
									Content: []*awsbedrock.ToolResultContentBlock{
										{Text: ptr.To("The weather in Dallas TX is 98 degrees fahrenheit with mostly cloudy skies and a change of rain in the evening.")},
									},
									ToolUseID: ptr.To("tool-1"),
								},
							},
							{
								ToolResult: &awsbedrock.ToolResultBlock{
									Content: []*awsbedrock.ToolResultContentBlock{
										{Text: ptr.To("The weather in Orlando FL is 78 degrees fahrenheit with clear skies.")},
									},
									ToolUseID: ptr.To("tool-2"),
								},
							},
						},
					},
				},
			},
		},
		{
			name: "test thinking parameter for anthropic claude model",
			input: openai.ChatCompletionRequest{
				Model: "anthropic.claude-3-sonnet-20240229-v1:0",
				Messages: []openai.ChatCompletionMessageParamUnion{
					{
						OfUser: &openai.ChatCompletionUserMessageParam{
							Content: openai.StringOrUserRoleContentUnion{
								Value: "Hello",
							},
							Role: openai.ChatMessageRoleUser,
						},
					},
				},
				Thinking: &openai.ThinkingUnion{
					OfEnabled: &openai.ThinkingEnabled{
						BudgetTokens: int64(1024),
						Type:         "enabled",
					},
				},
			},
			output: awsbedrock.ConverseInput{
				AdditionalModelRequestFields: map[string]interface{}{
					"thinking": map[string]interface{}{"type": "enabled", "budget_tokens": float64(1024)},
				},
				InferenceConfig: &awsbedrock.InferenceConfiguration{},
				Messages: []*awsbedrock.Message{
					{
						Role: openai.ChatMessageRoleUser,
						Content: []*awsbedrock.ContentBlock{
							{
								Text: ptr.To("Hello"),
							},
						},
					},
				},
			},
		},
		{
			name: "test assistant message with thinking content",
			input: openai.ChatCompletionRequest{
				Model: "unit-test-model",
				Messages: []openai.ChatCompletionMessageParamUnion{
					{
						OfUser: &openai.ChatCompletionUserMessageParam{
							Role:    openai.ChatMessageRoleUser,
							Content: openai.StringOrUserRoleContentUnion{Value: "How do I list prime numbers?"},
						},
					},
					{
						OfAssistant: &openai.ChatCompletionAssistantMessageParam{
							Role: openai.ChatMessageRoleAssistant,
							Content: openai.StringOrAssistantRoleContentUnion{
								Value: []openai.ChatCompletionAssistantMessageParamContent{
									{
										Type: openai.ChatCompletionAssistantMessageParamContentTypeThinking,
										Text: ptr.To("Let me think"),
									},
								},
							},
						},
					},
				},
			},
			output: awsbedrock.ConverseInput{
				InferenceConfig: &awsbedrock.InferenceConfiguration{},
				Messages: []*awsbedrock.Message{
					{
						Role:    openai.ChatMessageRoleUser,
						Content: []*awsbedrock.ContentBlock{{Text: ptr.To("How do I list prime numbers?")}},
					},
					{
						Role: openai.ChatMessageRoleAssistant,
						Content: []*awsbedrock.ContentBlock{
							{
								ReasoningContent: &awsbedrock.ReasoningContentBlock{
									ReasoningText: &awsbedrock.ReasoningTextBlock{
										Text: "Let me think",
									},
								},
							},
						},
					},
				},
			},
		},
		{
			name: "test assistant message with redacted thinking content",
			input: openai.ChatCompletionRequest{
				Model: "unit-test-model",
				Messages: []openai.ChatCompletionMessageParamUnion{
					{
						OfUser: &openai.ChatCompletionUserMessageParam{
							Role:    openai.ChatMessageRoleUser,
							Content: openai.StringOrUserRoleContentUnion{Value: "How do I list prime numbers?"},
						},
					},
					{
						OfAssistant: &openai.ChatCompletionAssistantMessageParam{
							Role: openai.ChatMessageRoleAssistant,
							Content: openai.StringOrAssistantRoleContentUnion{
								Value: []openai.ChatCompletionAssistantMessageParamContent{
									{
										Type:            openai.ChatCompletionAssistantMessageParamContentTypeRedactedThinking,
										RedactedContent: &openai.RedactedContentUnion{Value: []byte{104, 101, 108, 108, 111}},
									},
								},
							},
						},
					},
				},
			},
			output: awsbedrock.ConverseInput{
				InferenceConfig: &awsbedrock.InferenceConfiguration{},
				Messages: []*awsbedrock.Message{
					{
						Role:    openai.ChatMessageRoleUser,
						Content: []*awsbedrock.ContentBlock{{Text: ptr.To("How do I list prime numbers?")}},
					},
					{
						Role: openai.ChatMessageRoleAssistant,
						Content: []*awsbedrock.ContentBlock{
							{
								ReasoningContent: &awsbedrock.ReasoningContentBlock{
									RedactedContent: []byte{104, 101, 108, 108, 111},
								},
							},
						},
					},
				},
			},
		},
		{
			name: "test multi-turn with tool use and thinking content",
			input: openai.ChatCompletionRequest{
				Model: "unit-test-model",
				Messages: []openai.ChatCompletionMessageParamUnion{
					{
						OfUser: &openai.ChatCompletionUserMessageParam{
							Role:    openai.ChatMessageRoleUser,
							Content: openai.StringOrUserRoleContentUnion{Value: "What's the weather in Paris?"},
						},
					},
					{
						OfAssistant: &openai.ChatCompletionAssistantMessageParam{
							Role: openai.ChatMessageRoleAssistant,
							ToolCalls: []openai.ChatCompletionMessageToolCallParam{
								{
									ID:   ptr.To("tool_call_123"),
									Type: openai.ChatCompletionMessageToolCallTypeFunction,
									Function: openai.ChatCompletionMessageToolCallFunctionParam{
										Name:      "get_weather",
										Arguments: "{\"location\":\"Paris\"}",
									},
								},
							},
							Content: openai.StringOrAssistantRoleContentUnion{
								Value: []openai.ChatCompletionAssistantMessageParamContent{
									{
										Type:      openai.ChatCompletionAssistantMessageParamContentTypeThinking,
										Text:      ptr.To("I need to call the get_weather tool for Paris."),
										Signature: ptr.To("sig_12345"),
									},
								},
							},
						},
					},
					{
						OfTool: &openai.ChatCompletionToolMessageParam{
							Role:       openai.ChatMessageRoleTool,
							ToolCallID: "tool_call_123",
							Content:    openai.ContentUnion{Value: "{\"temperature\": 88}"},
						},
					},
				},
			},
			output: awsbedrock.ConverseInput{
				InferenceConfig: &awsbedrock.InferenceConfiguration{},
				Messages: []*awsbedrock.Message{
					{
						Role:    openai.ChatMessageRoleUser,
						Content: []*awsbedrock.ContentBlock{{Text: ptr.To("What's the weather in Paris?")}},
					},
					{
						Role: openai.ChatMessageRoleAssistant,
						Content: []*awsbedrock.ContentBlock{
							{
								ReasoningContent: &awsbedrock.ReasoningContentBlock{
									ReasoningText: &awsbedrock.ReasoningTextBlock{
										Text:      "I need to call the get_weather tool for Paris.",
										Signature: "sig_12345",
									},
								},
							},
							{
								ToolUse: &awsbedrock.ToolUseBlock{
									ToolUseID: "tool_call_123",
									Name:      "get_weather",
									Input:     map[string]any{"location": "Paris"},
								},
							},
						},
					},
					{
						Role: awsbedrock.ConversationRoleUser,
						Content: []*awsbedrock.ContentBlock{
							{
								ToolResult: &awsbedrock.ToolResultBlock{
									ToolUseID: ptr.To("tool_call_123"),
									Content: []*awsbedrock.ToolResultContentBlock{
										{Text: ptr.To("{\"temperature\": 88}")},
									},
								},
							},
						},
					},
				},
			},
		},
		{
			name: "test thinking enabled config",
			input: openai.ChatCompletionRequest{
				Model: "bedrock.unit-test-model",
				Messages: []openai.ChatCompletionMessageParamUnion{
					{
						OfUser: &openai.ChatCompletionUserMessageParam{
							Role:    openai.ChatMessageRoleUser,
							Content: openai.StringOrUserRoleContentUnion{Value: "Hello"},
						},
					},
				},
				Thinking: &openai.ThinkingUnion{
					OfEnabled: &openai.ThinkingEnabled{
						Type:         "enabled",
						BudgetTokens: 1024,
					},
				},
			},
			output: awsbedrock.ConverseInput{
				AdditionalModelRequestFields: map[string]interface{}{
					"thinking": map[string]interface{}{"type": "enabled", "budget_tokens": float64(1024)},
				},
				InferenceConfig: &awsbedrock.InferenceConfiguration{},
				Messages: []*awsbedrock.Message{
					{
						Role:    openai.ChatMessageRoleUser,
						Content: []*awsbedrock.ContentBlock{{Text: ptr.To("Hello")}},
					},
				},
			},
		},
		{
			name: "test thinking enabled config with display",
			input: openai.ChatCompletionRequest{
				Model: "bedrock.unit-test-model",
				Messages: []openai.ChatCompletionMessageParamUnion{
					{
						OfUser: &openai.ChatCompletionUserMessageParam{
							Role:    openai.ChatMessageRoleUser,
							Content: openai.StringOrUserRoleContentUnion{Value: "Hello"},
						},
					},
				},
				Thinking: &openai.ThinkingUnion{
					OfEnabled: &openai.ThinkingEnabled{
						Type:         "enabled",
						BudgetTokens: 1024,
						Display:      "omitted",
					},
				},
			},
			output: awsbedrock.ConverseInput{
				AdditionalModelRequestFields: map[string]interface{}{
					"thinking": map[string]interface{}{"type": "enabled", "budget_tokens": float64(1024), "display": "omitted"},
				},
				InferenceConfig: &awsbedrock.InferenceConfiguration{},
				Messages: []*awsbedrock.Message{
					{
						Role:    openai.ChatMessageRoleUser,
						Content: []*awsbedrock.ContentBlock{{Text: ptr.To("Hello")}},
					},
				},
			},
		},
		{
			name: "test thinking adaptive config with display",
			input: openai.ChatCompletionRequest{
				Model: "bedrock.unit-test-model",
				Messages: []openai.ChatCompletionMessageParamUnion{
					{
						OfUser: &openai.ChatCompletionUserMessageParam{
							Role:    openai.ChatMessageRoleUser,
							Content: openai.StringOrUserRoleContentUnion{Value: "Hello"},
						},
					},
				},
				Thinking: &openai.ThinkingUnion{
					OfAdaptive: &openai.ThinkingAdaptive{
						Type:    "adaptive",
						Display: "summarized",
					},
				},
			},
			output: awsbedrock.ConverseInput{
				AdditionalModelRequestFields: map[string]interface{}{
					"thinking": map[string]interface{}{"type": "adaptive", "display": "summarized"},
				},
				InferenceConfig: &awsbedrock.InferenceConfiguration{},
				Messages: []*awsbedrock.Message{
					{
						Role:    openai.ChatMessageRoleUser,
						Content: []*awsbedrock.ContentBlock{{Text: ptr.To("Hello")}},
					},
				},
			},
		},
		{
			name: "test thinking disabled config",
			input: openai.ChatCompletionRequest{
				Model: "bedrock.unit-test-model",
				Messages: []openai.ChatCompletionMessageParamUnion{
					{
						OfUser: &openai.ChatCompletionUserMessageParam{
							Role:    openai.ChatMessageRoleUser,
							Content: openai.StringOrUserRoleContentUnion{Value: "Hello"},
						},
					},
				},
				Thinking: &openai.ThinkingUnion{
					OfDisabled: &openai.ThinkingDisabled{
						Type: "disabled",
					},
				},
			},
			output: awsbedrock.ConverseInput{
				AdditionalModelRequestFields: map[string]interface{}{
					"thinking": map[string]interface{}{"type": "disabled"},
				},
				InferenceConfig: &awsbedrock.InferenceConfiguration{},
				Messages: []*awsbedrock.Message{
					{
						Role:    openai.ChatMessageRoleUser,
						Content: []*awsbedrock.ContentBlock{{Text: ptr.To("Hello")}},
					},
				},
			},
		},
		{
			name: "test assistant message with mixed text and thinking content",
			input: openai.ChatCompletionRequest{
				Model: "unit-test-model",
				Messages: []openai.ChatCompletionMessageParamUnion{
					{
						OfAssistant: &openai.ChatCompletionAssistantMessageParam{
							Role: openai.ChatMessageRoleAssistant,
							Content: openai.StringOrAssistantRoleContentUnion{
								Value: []openai.ChatCompletionAssistantMessageParamContent{
									{
										Type: openai.ChatCompletionAssistantMessageParamContentTypeText,
										Text: ptr.To("This is a standard text part."),
									},
									{
										Type: openai.ChatCompletionAssistantMessageParamContentTypeThinking,
										Text: ptr.To("This is a thinking part."),
									},
								},
							},
						},
					},
				},
			},
			output: awsbedrock.ConverseInput{
				InferenceConfig: &awsbedrock.InferenceConfiguration{},
				Messages: []*awsbedrock.Message{
					{
						Role: openai.ChatMessageRoleAssistant,
						Content: []*awsbedrock.ContentBlock{
							{
								Text: ptr.To("This is a standard text part."),
							},
							{
								ReasoningContent: &awsbedrock.ReasoningContentBlock{
									ReasoningText: &awsbedrock.ReasoningTextBlock{
										Text: "This is a thinking part.",
									},
								},
							},
						},
					},
				},
			},
		},
		{
			name: "test reasoning_effort forwarded as reasoning_config",
			input: openai.ChatCompletionRequest{
				Model: "some-reasoning-model",
				Messages: []openai.ChatCompletionMessageParamUnion{
					{
						OfUser: &openai.ChatCompletionUserMessageParam{
							Role:    openai.ChatMessageRoleUser,
							Content: openai.StringOrUserRoleContentUnion{Value: "Hello"},
						},
					},
				},
				ReasoningEffort: "high",
			},
			output: awsbedrock.ConverseInput{
				AdditionalModelRequestFields: map[string]interface{}{
					"reasoning_config": "high",
				},
				InferenceConfig: &awsbedrock.InferenceConfiguration{},
				Messages: []*awsbedrock.Message{
					{
						Role:    openai.ChatMessageRoleUser,
						Content: []*awsbedrock.ContentBlock{{Text: ptr.To("Hello")}},
					},
				},
			},
		},
		{
			name: "test reasoning_effort coexists with thinking",
			input: openai.ChatCompletionRequest{
				Model: "some-reasoning-model",
				Messages: []openai.ChatCompletionMessageParamUnion{
					{
						OfUser: &openai.ChatCompletionUserMessageParam{
							Role:    openai.ChatMessageRoleUser,
							Content: openai.StringOrUserRoleContentUnion{Value: "Hello"},
						},
					},
				},
				Thinking: &openai.ThinkingUnion{
					OfEnabled: &openai.ThinkingEnabled{
						Type:         "enabled",
						BudgetTokens: 2048,
					},
				},
				ReasoningEffort: "medium",
			},
			output: awsbedrock.ConverseInput{
				AdditionalModelRequestFields: map[string]interface{}{
					"thinking":         map[string]interface{}{"type": "enabled", "budget_tokens": float64(2048)},
					"reasoning_config": "medium",
				},
				InferenceConfig: &awsbedrock.InferenceConfiguration{},
				Messages: []*awsbedrock.Message{
					{
						Role:    openai.ChatMessageRoleUser,
						Content: []*awsbedrock.ContentBlock{{Text: ptr.To("Hello")}},
					},
				},
			},
		},
		{
			name: "test empty reasoning_effort is not forwarded",
			input: openai.ChatCompletionRequest{
				Model: "some-model",
				Messages: []openai.ChatCompletionMessageParamUnion{
					{
						OfUser: &openai.ChatCompletionUserMessageParam{
							Role:    openai.ChatMessageRoleUser,
							Content: openai.StringOrUserRoleContentUnion{Value: "Hello"},
						},
					},
				},
			},
			output: awsbedrock.ConverseInput{
				InferenceConfig: &awsbedrock.InferenceConfiguration{},
				Messages: []*awsbedrock.Message{
					{
						Role:    openai.ChatMessageRoleUser,
						Content: []*awsbedrock.ContentBlock{{Text: ptr.To("Hello")}},
					},
				},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o := &openAIToAWSBedrockTranslatorV1ChatCompletion{}
			originalReq := tt.input
			hm, newBody, err := o.RequestBody(nil, &originalReq, false)
			var expPath string
			require.Equal(t, tt.input.Stream, o.stream)
			encodedModel := url.PathEscape(tt.input.Model)
			if tt.input.Stream {
				expPath = fmt.Sprintf("/model/%s/converse-stream", encodedModel)
			} else {
				expPath = fmt.Sprintf("/model/%s/converse", encodedModel)
			}
			require.NoError(t, err)
			require.NotNil(t, hm)
			require.NotNil(t, hm)
			require.Len(t, hm, 2)
			require.Equal(t, pathHeaderName, hm[0].Key())
			require.Equal(t, expPath, hm[0].Value())
			require.Equal(t, contentLengthHeaderName, hm[1].Key())
			require.Equal(t, strconv.Itoa(len(newBody)), hm[1].Value())

			var awsReq awsbedrock.ConverseInput
			err = json.Unmarshal(newBody, &awsReq)
			require.NoError(t, err)
			if !cmp.Equal(awsReq, tt.output) {
				t.Errorf("ConvertOpenAIToBedrock(), diff(got, expected) = %s\n", cmp.Diff(awsReq, tt.output))
			}
		})
	}

	t.Run("model override", func(t *testing.T) {
		modelNameOverride := "bedrock.anthropic.claude-3-5-sonnet-20240620-v1:0"
		o := &openAIToAWSBedrockTranslatorV1ChatCompletion{modelNameOverride: modelNameOverride}
		originalReq := openai.ChatCompletionRequest{
			Model:    "claude-3-5-sonnet",
			Messages: []openai.ChatCompletionMessageParamUnion{},
		}
		hm, _, err := o.RequestBody(nil, &originalReq, false)
		require.NoError(t, err)
		require.NotNil(t, hm)
		require.NotNil(t, hm)
		require.Len(t, hm, 2)
		require.Equal(t, pathHeaderName, hm[0].Key())
		require.Equal(t, "/model/"+url.PathEscape(modelNameOverride)+"/converse", hm[0].Value())
	})
}

func TestOpenAIToAWSBedrockTranslatorV1ChatCompletion_ResponseHeaders(t *testing.T) {
	t.Run("streaming", func(t *testing.T) {
		o := &openAIToAWSBedrockTranslatorV1ChatCompletion{stream: true}
		hm, err := o.ResponseHeaders(map[string]string{
			"content-type": "application/vnd.amazon.eventstream",
		})
		require.NoError(t, err)
		require.NotNil(t, hm)
		require.NotNil(t, hm)
		require.Len(t, hm, 1)
		require.Equal(t, "content-type", hm[0].Key())
		require.Equal(t, "text/event-stream", hm[0].Value())
	})
	t.Run("non-streaming", func(t *testing.T) {
		o := &openAIToAWSBedrockTranslatorV1ChatCompletion{}
		hm, err := o.ResponseHeaders(nil)
		require.NoError(t, err)
		require.Nil(t, hm)
	})
}

func TestOpenAIToAWSBedrockTranslatorV1ChatCompletion_Streaming_ResponseBody(t *testing.T) {
	t.Run("streaming", func(t *testing.T) {
		o := &openAIToAWSBedrockTranslatorV1ChatCompletion{stream: true, requestModel: "claude-sonnet-4"}
		buf, err := base64.StdEncoding.DecodeString(base64RealStreamingEvents)
		require.NoError(t, err)

		var results []byte
		for i := range buf {
			hm, newBody, tokenUsage, _, err := o.ResponseBody(nil, bytes.NewBuffer([]byte{buf[i]}), i == len(buf)-1, nil)
			require.NoError(t, err)
			require.Nil(t, hm)
			if len(newBody) > 0 {
				results = append(results, newBody...)
			}
			if outputTokens, ok := tokenUsage.OutputTokens(); ok && outputTokens > 0 {
				require.Equal(t, uint32(75), outputTokens)
			}
		}

		// normalize Created and ID
		openaiChunks := getChatCompletionResponseChunk(results)

		var normalizedResults []byte
		for i := range openaiChunks {
			oaichunk := &openaiChunks[i]
			oaichunk.ID = "123"
			oaichunk.Created = openai.JSONUNIXTime(time.Unix(releaseDateUnix, 0)) // 0 nanoseconds
			// oaichunk.Model = "claude-sonnet-4"
			err := serializeOpenAIChatCompletionChunk(oaichunk, &normalizedResults)
			require.NoError(t, err)
		}
		normalizedResults = append(normalizedResults, []byte("data: [DONE]\n")...)

		require.Equal(t,
			`data: {"id":"123","choices":[{"index":0,"delta":{"content":"","role":"assistant"}}],"created":1731679200,"model":"claude-sonnet-4","object":"chat.completion.chunk"}

data: {"id":"123","choices":[{"index":0,"delta":{"content":"To","role":"assistant"}}],"created":1731679200,"model":"claude-sonnet-4","object":"chat.completion.chunk"}

data: {"id":"123","choices":[{"index":0,"delta":{"content":" calculate the cosine","role":"assistant"}}],"created":1731679200,"model":"claude-sonnet-4","object":"chat.completion.chunk"}

data: {"id":"123","choices":[{"index":0,"delta":{"content":" of 7,","role":"assistant"}}],"created":1731679200,"model":"claude-sonnet-4","object":"chat.completion.chunk"}

data: {"id":"123","choices":[{"index":0,"delta":{"content":" we can use the","role":"assistant"}}],"created":1731679200,"model":"claude-sonnet-4","object":"chat.completion.chunk"}

data: {"id":"123","choices":[{"index":0,"delta":{"content":" \"","role":"assistant"}}],"created":1731679200,"model":"claude-sonnet-4","object":"chat.completion.chunk"}

data: {"id":"123","choices":[{"index":0,"delta":{"content":"cosine\" function","role":"assistant"}}],"created":1731679200,"model":"claude-sonnet-4","object":"chat.completion.chunk"}

data: {"id":"123","choices":[{"index":0,"delta":{"content":" that","role":"assistant"}}],"created":1731679200,"model":"claude-sonnet-4","object":"chat.completion.chunk"}

data: {"id":"123","choices":[{"index":0,"delta":{"content":" is","role":"assistant"}}],"created":1731679200,"model":"claude-sonnet-4","object":"chat.completion.chunk"}

data: {"id":"123","choices":[{"index":0,"delta":{"content":" available to","role":"assistant"}}],"created":1731679200,"model":"claude-sonnet-4","object":"chat.completion.chunk"}

data: {"id":"123","choices":[{"index":0,"delta":{"content":" us.","role":"assistant"}}],"created":1731679200,"model":"claude-sonnet-4","object":"chat.completion.chunk"}

data: {"id":"123","choices":[{"index":0,"delta":{"content":" Let","role":"assistant"}}],"created":1731679200,"model":"claude-sonnet-4","object":"chat.completion.chunk"}

data: {"id":"123","choices":[{"index":0,"delta":{"content":"'s use","role":"assistant"}}],"created":1731679200,"model":"claude-sonnet-4","object":"chat.completion.chunk"}

data: {"id":"123","choices":[{"index":0,"delta":{"content":" this","role":"assistant"}}],"created":1731679200,"model":"claude-sonnet-4","object":"chat.completion.chunk"}

data: {"id":"123","choices":[{"index":0,"delta":{"content":" function to","role":"assistant"}}],"created":1731679200,"model":"claude-sonnet-4","object":"chat.completion.chunk"}

data: {"id":"123","choices":[{"index":0,"delta":{"content":" get","role":"assistant"}}],"created":1731679200,"model":"claude-sonnet-4","object":"chat.completion.chunk"}

data: {"id":"123","choices":[{"index":0,"delta":{"content":" the result","role":"assistant"}}],"created":1731679200,"model":"claude-sonnet-4","object":"chat.completion.chunk"}

data: {"id":"123","choices":[{"index":0,"delta":{"content":".","role":"assistant"}}],"created":1731679200,"model":"claude-sonnet-4","object":"chat.completion.chunk"}

data: {"id":"123","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"tooluse_QklrEHKjRu6Oc4BQUfy7ZQ","function":{"arguments":"","name":"cosine"},"type":"function"}]}}],"created":1731679200,"model":"claude-sonnet-4","object":"chat.completion.chunk"}

data: {"id":"123","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":null,"function":{"arguments":"","name":""},"type":"function"}]}}],"created":1731679200,"model":"claude-sonnet-4","object":"chat.completion.chunk"}

data: {"id":"123","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":null,"function":{"arguments":"{\"x\": 7}","name":""},"type":"function"}]}}],"created":1731679200,"model":"claude-sonnet-4","object":"chat.completion.chunk"}

data: {"id":"123","choices":[{"index":0,"delta":{"content":"","role":"assistant"},"finish_reason":"tool_calls"}],"created":1731679200,"model":"claude-sonnet-4","object":"chat.completion.chunk"}

data: {"id":"123","choices":[],"created":1731679200,"model":"claude-sonnet-4","service_tier":"default","object":"chat.completion.chunk","usage":{"prompt_tokens":386,"completion_tokens":75,"total_tokens":461}}

data: [DONE]
`, string(normalizedResults))
	})
}

func TestOpenAIToAWSBedrockTranslator_ResponseError(t *testing.T) {
	tests := []struct {
		name            string
		responseHeaders map[string]string
		input           io.Reader
		output          openai.Error
	}{
		{
			name: "test unhealthy upstream",
			responseHeaders: map[string]string{
				":status":      "503",
				"content-type": "text/plain",
			},
			input: bytes.NewBuffer([]byte("service not available")),
			output: openai.Error{
				Type: "error",
				Error: openai.ErrorType{
					Type:    awsBedrockBackendError,
					Code:    ptr.To("503"),
					Message: "service not available",
				},
			},
		},
		{
			name: "test AWS throttled error response",
			responseHeaders: map[string]string{
				":status":              "429",
				"content-type":         "application/json",
				awsErrorTypeHeaderName: "ThrottledException",
			},
			input: bytes.NewBuffer([]byte(`{"message": "aws bedrock rate limit exceeded"}`)),
			output: openai.Error{
				Type: "error",
				Error: openai.ErrorType{
					Type:    "ThrottledException",
					Code:    ptr.To("429"),
					Message: "aws bedrock rate limit exceeded",
				},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := json.Marshal(tt.input)
			require.NoError(t, err)

			o := &openAIToAWSBedrockTranslatorV1ChatCompletion{}
			hm, newBody, err := o.ResponseError(tt.responseHeaders, tt.input)
			require.NoError(t, err)
			require.NotNil(t, newBody)
			require.NotNil(t, hm)
			require.Len(t, hm, 2)
			require.Equal(t, contentTypeHeaderName, hm[0].Key())
			require.Equal(t, jsonContentType, hm[0].Value()) //nolint:testifylint
			require.Equal(t, contentLengthHeaderName, hm[1].Key())
			require.Equal(t, strconv.Itoa(len(newBody)), hm[1].Value())

			var openAIError openai.Error
			err = json.Unmarshal(newBody, &openAIError)
			require.NoError(t, err)
			if !cmp.Equal(openAIError, tt.output) {
				t.Errorf("ConvertAWSBedrockErrorResp(), diff(got, expected) = %s\n", cmp.Diff(openAIError, tt.output))
			}
		})
	}
}

func TestOpenAIToAWSBedrockTranslatorV1ChatCompletion_ResponseBody(t *testing.T) {
	t.Run("invalid body", func(t *testing.T) {
		o := &openAIToAWSBedrockTranslatorV1ChatCompletion{}
		_, _, _, _, err := o.ResponseBody(nil, bytes.NewBuffer([]byte("invalid")), false, nil)
		require.Error(t, err)
	})
	tests := []struct {
		name   string
		input  awsbedrock.ConverseResponse
		output openai.ChatCompletionResponse
	}{
		{
			name: "basic_testing",
			input: awsbedrock.ConverseResponse{
				Usage: &awsbedrock.TokenUsage{
					InputTokens:           10,
					OutputTokens:          20,
					TotalTokens:           30,
					CacheWriteInputTokens: ptr.To[int64](7),
					CacheReadInputTokens:  ptr.To[int64](5),
				},
				ServiceTier: &awsbedrock.ServiceTier{Type: "default"},
				Output: &awsbedrock.ConverseOutput{
					Message: awsbedrock.Message{
						Role: "assistant",
						Content: []*awsbedrock.ContentBlock{
							{Text: ptr.To("response")},
							{Text: ptr.To("from")},
							{Text: ptr.To("assistant")},
						},
					},
				},
			},
			output: openai.ChatCompletionResponse{
				ID:          "123",
				Model:       "claude-sonnet-4",
				Created:     openai.JSONUNIXTime(time.Unix(releaseDateUnix, 0)),
				Object:      "chat.completion",
				ServiceTier: "default",
				Usage: openai.Usage{
					TotalTokens:      42,
					PromptTokens:     22,
					CompletionTokens: 20,
					PromptTokensDetails: &openai.PromptTokensDetails{
						CachedTokens:        5,
						CacheCreationTokens: 7,
					},
				},
				Choices: []openai.ChatCompletionResponseChoice{
					{
						Index: 0,
						Message: openai.ChatCompletionResponseChoiceMessage{
							Content: ptr.To("response"),
							Role:    awsbedrock.ConversationRoleAssistant,
						},
						FinishReason: openai.ChatCompletionChoicesFinishReasonStop,
					},
				},
			},
		},
		{
			name: "test stop reason",
			input: awsbedrock.ConverseResponse{
				Usage: &awsbedrock.TokenUsage{
					InputTokens:  10,
					OutputTokens: 20,
					TotalTokens:  30,
				},
				ServiceTier: &awsbedrock.ServiceTier{Type: "reserved"},
				StopReason:  ptr.To("stop_sequence"),
				Output: &awsbedrock.ConverseOutput{
					Message: awsbedrock.Message{
						Role: awsbedrock.ConversationRoleAssistant,
						Content: []*awsbedrock.ContentBlock{
							{Text: ptr.To("response")},
						},
					},
				},
			},
			output: openai.ChatCompletionResponse{
				ID:      "123",
				Model:   "claude-sonnet-4",
				Created: openai.JSONUNIXTime(time.Unix(releaseDateUnix, 0)),
				Object:  "chat.completion",
				Usage: openai.Usage{
					TotalTokens:      30,
					PromptTokens:     10,
					CompletionTokens: 20,
				},
				ServiceTier: "reserved",
				Choices: []openai.ChatCompletionResponseChoice{
					{
						Index:        0,
						FinishReason: openai.ChatCompletionChoicesFinishReasonStop,
						Message: openai.ChatCompletionResponseChoiceMessage{
							Content: ptr.To("response"),
							Role:    awsbedrock.ConversationRoleAssistant,
						},
					},
				},
			},
		},
		{
			name: "test tool use",
			input: awsbedrock.ConverseResponse{
				StopReason: ptr.To(awsbedrock.StopReasonToolUse),
				Output: &awsbedrock.ConverseOutput{
					Message: awsbedrock.Message{
						Role: awsbedrock.ConversationRoleAssistant,
						// Text and ToolUse are sent in two different content blocks for AWS Bedrock, OpenAI merges them in one message.
						Content: []*awsbedrock.ContentBlock{
							{
								Text: ptr.To("response"),
							},
							{
								ToolUse: &awsbedrock.ToolUseBlock{
									Name:      "exec_python_code",
									ToolUseID: "call_6g7a",
									Input:     map[string]any{"code_block": "from playwright.sync_api import sync_playwright\n"},
								},
							},
						},
					},
				},
			},
			output: openai.ChatCompletionResponse{
				ID:      "123",
				Model:   "claude-sonnet-4",
				Created: openai.JSONUNIXTime(time.Unix(releaseDateUnix, 0)),
				Object:  "chat.completion",
				Choices: []openai.ChatCompletionResponseChoice{
					{
						Index:        0,
						FinishReason: openai.ChatCompletionChoicesFinishReasonToolCalls,
						Message: openai.ChatCompletionResponseChoiceMessage{
							Content: ptr.To("response"),
							Role:    awsbedrock.ConversationRoleAssistant,
							ToolCalls: []openai.ChatCompletionMessageToolCallParam{
								{
									ID: ptr.To("call_6g7a"),
									Function: openai.ChatCompletionMessageToolCallFunctionParam{
										Name:      "exec_python_code",
										Arguments: "{\"code_block\":\"from playwright.sync_api import sync_playwright\\n\"}",
									},
									Type: openai.ChatCompletionMessageToolCallTypeFunction,
								},
							},
						},
					},
				},
			},
		},
		{
			name: "merge content",
			input: awsbedrock.ConverseResponse{
				Usage: &awsbedrock.TokenUsage{
					InputTokens:  10,
					OutputTokens: 20,
					TotalTokens:  30,
				},
				Output: &awsbedrock.ConverseOutput{
					Message: awsbedrock.Message{
						Role: awsbedrock.ConversationRoleAssistant,
						Content: []*awsbedrock.ContentBlock{
							{Text: ptr.To("response")},
							{ToolUse: &awsbedrock.ToolUseBlock{
								Name:      "exec_python_code",
								ToolUseID: "call_6g7a",
								Input:     map[string]any{"code_block": "from playwright.sync_api import sync_playwright\n"},
							}},
						},
					},
				},
			},
			output: openai.ChatCompletionResponse{
				ID:      "123",
				Model:   "claude-sonnet-4",
				Created: openai.JSONUNIXTime(time.Unix(releaseDateUnix, 0)),
				Object:  "chat.completion",
				Usage: openai.Usage{
					TotalTokens:      30,
					PromptTokens:     10,
					CompletionTokens: 20,
				},
				Choices: []openai.ChatCompletionResponseChoice{
					{
						Index: 0,
						Message: openai.ChatCompletionResponseChoiceMessage{
							Content: ptr.To("response"),
							Role:    awsbedrock.ConversationRoleAssistant,
							ToolCalls: []openai.ChatCompletionMessageToolCallParam{
								{
									ID: ptr.To("call_6g7a"),
									Function: openai.ChatCompletionMessageToolCallFunctionParam{
										Name:      "exec_python_code",
										Arguments: "{\"code_block\":\"from playwright.sync_api import sync_playwright\\n\"}",
									},
									Type: openai.ChatCompletionMessageToolCallTypeFunction,
								},
							},
						},
						FinishReason: openai.ChatCompletionChoicesFinishReasonStop,
					},
				},
			},
		},
		{
			name: "response with reasoning content",
			input: awsbedrock.ConverseResponse{
				StopReason: ptr.To(awsbedrock.StopReasonEndTurn),
				Output: &awsbedrock.ConverseOutput{
					Message: awsbedrock.Message{
						Role: awsbedrock.ConversationRoleAssistant,
						Content: []*awsbedrock.ContentBlock{
							{
								ReasoningContent: &awsbedrock.ReasoningContentBlock{
									ReasoningText: &awsbedrock.ReasoningTextBlock{
										Text: "This is the model's thought process.",
									},
								},
							},
							{
								Text: ptr.To("This is the final answer."),
							},
						},
					},
				},
			},
			output: openai.ChatCompletionResponse{
				ID:      "123",
				Model:   "claude-sonnet-4",
				Created: openai.JSONUNIXTime(time.Unix(releaseDateUnix, 0)),
				Object:  "chat.completion",
				Choices: []openai.ChatCompletionResponseChoice{
					{
						Index:        0,
						FinishReason: openai.ChatCompletionChoicesFinishReasonStop,
						Message: openai.ChatCompletionResponseChoiceMessage{
							Role:             awsbedrock.ConversationRoleAssistant,
							Content:          ptr.To("This is the final answer."),
							ReasoningContent: &openai.ReasoningContentUnion{Value: "This is the model's thought process."},
							ThinkingBlocks: []openai.ThinkingBlock{
								{Type: "thinking", Thinking: "This is the model's thought process."},
							},
						},
					},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body, err := json.Marshal(tt.input)
			require.NoError(t, err)

			o := &openAIToAWSBedrockTranslatorV1ChatCompletion{}
			hm, newBody, usedToken, _, err := o.ResponseBody(nil, bytes.NewBuffer(body), false, nil)
			require.NoError(t, err)
			require.NotNil(t, newBody)
			require.NotNil(t, hm)
			require.Len(t, hm, 1)
			require.Equal(t, contentLengthHeaderName, hm[0].Key())
			require.Equal(t, strconv.Itoa(len(newBody)), hm[0].Value())

			// normalize newBody
			var chatCompletion openai.ChatCompletionResponse
			err = json.Unmarshal(newBody, &chatCompletion)
			require.NoError(t, err)
			chatCompletion.ID = "123"
			chatCompletion.Model = "claude-sonnet-4"
			chatCompletion.Created = openai.JSONUNIXTime(time.Unix(releaseDateUnix, 0))
			normalizedBody, normalizedErr := json.Marshal(chatCompletion)
			require.NoError(t, normalizedErr)

			expectedBody, err := json.Marshal(tt.output)
			require.NoError(t, err)
			require.JSONEq(t, string(expectedBody), string(normalizedBody))

			var expectedUsage metrics.TokenUsage
			if tt.input.Usage != nil {
				expectedUsage = tokenUsageFrom(
					int32(tt.output.Usage.PromptTokens), // nolint:gosec
					-1,
					-1,
					int32(tt.output.Usage.CompletionTokens), // nolint:gosec
					int32(tt.output.Usage.TotalTokens),      // nolint:gosec
					-1,
				)
				if tt.input.Usage.CacheReadInputTokens != nil {
					expectedUsage.SetCachedInputTokens(uint32(tt.output.Usage.PromptTokensDetails.CachedTokens)) //nolint:gosec
				}
				if tt.input.Usage.CacheWriteInputTokens != nil {
					expectedUsage.SetCacheCreationInputTokens(uint32(tt.output.Usage.PromptTokensDetails.CacheCreationTokens)) //nolint:gosec
				}
			} else {
				expectedUsage = tokenUsageFrom(-1, -1, -1, -1, -1, -1)
			}
			require.Equal(t, expectedUsage, usedToken)
		})
	}
}

// TestOpenAIToAWSBedrockTranslatorV1ChatCompletion_RequestBodyURLEncoding tests URL encoding of ARNs in paths
func TestOpenAIToAWSBedrockTranslatorV1ChatCompletion_RequestBodyURLEncoding(t *testing.T) {
	tests := []struct {
		name         string
		modelName    string
		stream       bool
		expectedPath string
	}{
		{
			name:         "ARN with slashes",
			modelName:    "arn:aws:bedrock:us-east-1:123456789012:inference-profile/us.anthropic.claude-3-5-sonnet-20241022-v2:0",
			stream:       false,
			expectedPath: "/model/arn:aws:bedrock:us-east-1:123456789012:inference-profile%2Fus.anthropic.claude-3-5-sonnet-20241022-v2:0/converse",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o := &openAIToAWSBedrockTranslatorV1ChatCompletion{}
			req := openai.ChatCompletionRequest{
				Model:  tt.modelName,
				Stream: tt.stream,
				Messages: []openai.ChatCompletionMessageParamUnion{
					{
						OfUser: &openai.ChatCompletionUserMessageParam{
							Content: openai.StringOrUserRoleContentUnion{
								Value: "test message",
							},
							Role: openai.ChatMessageRoleUser,
						},
					},
				},
			}

			hm, _, err := o.RequestBody(nil, &req, false)
			require.NoError(t, err)
			require.NotNil(t, hm)
			require.NotNil(t, hm)
			require.Len(t, hm, 2)
			require.Equal(t, pathHeaderName, hm[0].Key())
			require.Equal(t, tt.expectedPath, hm[0].Value())
		})
	}
}

func TestOpenAIToAWSBedrockTranslatorV1ChatCompletion_RequestBodyErr(t *testing.T) {
	tests := []struct {
		name  string
		input openai.ChatCompletionRequest
		err   error
	}{
		{
			name: "test unexpected  tool choice type",
			input: openai.ChatCompletionRequest{
				Model: "gpt-4o",
				Messages: []openai.ChatCompletionMessageParamUnion{
					{
						OfUser: &openai.ChatCompletionUserMessageParam{
							Content: openai.StringOrUserRoleContentUnion{
								Value: "from-user",
							},
							Role: openai.ChatMessageRoleUser,
						},
					},
				},
				Tools: []openai.Tool{
					{
						Type: "function",
						Function: &openai.FunctionDefinition{
							Name:        "get_current_weather",
							Description: "Get the current weather in a given location",
						},
					},
				},
				ToolChoice: &openai.ChatCompletionToolChoiceUnion{Value: 123},
			},
			err: internalapi.ErrInvalidRequestBody,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o := &openAIToAWSBedrockTranslatorV1ChatCompletion{}
			originalReq := tt.input
			_, _, err := o.RequestBody(nil, &originalReq, false)
			require.ErrorIs(t, err, tt.err)
		})
	}
}

// base64RealStreamingEvents is the base64 encoded raw binary response from bedrock anthropic.claude model.
// The request is to find the cosine of number 7 with a tool configuration.
const base64RealStreamingEvents = "AAAAmwAAAFJGkfmwCzpldmVudC10eXBlBwAMbWVzc2FnZVN0YXJ0DTpjb250ZW50LXR5cGUHABBhcHBsaWNhdGlvbi9qc29uDTptZXNzYWdlLXR5cGUHAAVldmVudHsicCI6ImFiY2RlZmdoaWprbG1ub3BxcnN0dXZ3eHl6QUJDRCIsInJvbGUiOiJhc3Npc3RhbnQifbCidJ0AAACpAAAAV+0a5tkLOmV2ZW50LXR5cGUHABFjb250ZW50QmxvY2tEZWx0YQ06Y29udGVudC10eXBlBwAQYXBwbGljYXRpb24vanNvbg06bWVzc2FnZS10eXBlBwAFZXZlbnR7ImNvbnRlbnRCbG9ja0luZGV4IjowLCJkZWx0YSI6eyJ0ZXh0IjoiVG8ifSwicCI6ImFiY2RlZmdoaWprbG1uIn0rY75JAAAAsQAAAFe9ijqaCzpldmVudC10eXBlBwARY29udGVudEJsb2NrRGVsdGENOmNvbnRlbnQtdHlwZQcAEGFwcGxpY2F0aW9uL2pzb24NOm1lc3NhZ2UtdHlwZQcABWV2ZW50eyJjb250ZW50QmxvY2tJbmRleCI6MCwiZGVsdGEiOnsidGV4dCI6IiBjYWxjdWxhdGUgdGhlIGNvc2luZSJ9LCJwIjoiYWJjIn3hywqfAAAA2gAAAFdTaHzGCzpldmVudC10eXBlBwARY29udGVudEJsb2NrRGVsdGENOmNvbnRlbnQtdHlwZQcAEGFwcGxpY2F0aW9uL2pzb24NOm1lc3NhZ2UtdHlwZQcABWV2ZW50eyJjb250ZW50QmxvY2tJbmRleCI6MCwiZGVsdGEiOnsidGV4dCI6IiBvZiA3LCJ9LCJwIjoiYWJjZGVmZ2hpamtsbW5vcHFyc3R1dnd4eXpBQkNERUZHSElKS0xNTk9QUVJTVFVWV1hZWjAxMjM0NTYifUsRwHsAAADXAAAAV6v4uHcLOmV2ZW50LXR5cGUHABFjb250ZW50QmxvY2tEZWx0YQ06Y29udGVudC10eXBlBwAQYXBwbGljYXRpb24vanNvbg06bWVzc2FnZS10eXBlBwAFZXZlbnR7ImNvbnRlbnRCbG9ja0luZGV4IjowLCJkZWx0YSI6eyJ0ZXh0IjoiIHdlIGNhbiB1c2UgdGhlIn0sInAiOiJhYmNkZWZnaGlqa2xtbm9wcXJzdHV2d3h5ekFCQ0RFRkdISUpLTE1OT1BRUlNUVSJ9jBuxjAAAALwAAABXRRr+Kws6ZXZlbnQtdHlwZQcAEWNvbnRlbnRCbG9ja0RlbHRhDTpjb250ZW50LXR5cGUHABBhcHBsaWNhdGlvbi9qc29uDTptZXNzYWdlLXR5cGUHAAVldmVudHsiY29udGVudEJsb2NrSW5kZXgiOjAsImRlbHRhIjp7InRleHQiOiIgXCIifSwicCI6ImFiY2RlZmdoaWprbG1ub3BxcnN0dXZ3eHl6QUJDREVGIn3SOp66AAAA2wAAAFduCFV2CzpldmVudC10eXBlBwARY29udGVudEJsb2NrRGVsdGENOmNvbnRlbnQtdHlwZQcAEGFwcGxpY2F0aW9uL2pzb24NOm1lc3NhZ2UtdHlwZQcABWV2ZW50eyJjb250ZW50QmxvY2tJbmRleCI6MCwiZGVsdGEiOnsidGV4dCI6ImNvc2luZVwiIGZ1bmN0aW9uIn0sInAiOiJhYmNkZWZnaGlqa2xtbm9wcXJzdHV2d3h5ekFCQ0RFRkdISUpLTE1OT1BRUlNUVVZXIn2f+1UQAAAA2QAAAFcUyAYWCzpldmVudC10eXBlBwARY29udGVudEJsb2NrRGVsdGENOmNvbnRlbnQtdHlwZQcAEGFwcGxpY2F0aW9uL2pzb24NOm1lc3NhZ2UtdHlwZQcABWV2ZW50eyJjb250ZW50QmxvY2tJbmRleCI6MCwiZGVsdGEiOnsidGV4dCI6IiB0aGF0In0sInAiOiJhYmNkZWZnaGlqa2xtbm9wcXJzdHV2d3h5ekFCQ0RFRkdISUpLTE1OT1BRUlNUVVZXWFlaMDEyMzQ1NiJ9uD7t8wAAAM8AAABX+2hkNAs6ZXZlbnQtdHlwZQcAEWNvbnRlbnRCbG9ja0RlbHRhDTpjb250ZW50LXR5cGUHABBhcHBsaWNhdGlvbi9qc29uDTptZXNzYWdlLXR5cGUHAAVldmVudHsiY29udGVudEJsb2NrSW5kZXgiOjAsImRlbHRhIjp7InRleHQiOiIgaXMifSwicCI6ImFiY2RlZmdoaWprbG1ub3BxcnN0dXZ3eHl6QUJDREVGR0hJSktMTU5PUFFSU1RVVldYWSJ9p52nrQAAAMQAAABXjLhVJQs6ZXZlbnQtdHlwZQcAEWNvbnRlbnRCbG9ja0RlbHRhDTpjb250ZW50LXR5cGUHABBhcHBsaWNhdGlvbi9qc29uDTptZXNzYWdlLXR5cGUHAAVldmVudHsiY29udGVudEJsb2NrSW5kZXgiOjAsImRlbHRhIjp7InRleHQiOiIgYXZhaWxhYmxlIHRvIn0sInAiOiJhYmNkZWZnaGlqa2xtbm9wcXJzdHV2d3h5ekFCQ0QifYC08b0AAADTAAAAV154HrcLOmV2ZW50LXR5cGUHABFjb250ZW50QmxvY2tEZWx0YQ06Y29udGVudC10eXBlBwAQYXBwbGljYXRpb24vanNvbg06bWVzc2FnZS10eXBlBwAFZXZlbnR7ImNvbnRlbnRCbG9ja0luZGV4IjowLCJkZWx0YSI6eyJ0ZXh0IjoiIHVzLiJ9LCJwIjoiYWJjZGVmZ2hpamtsbW5vcHFyc3R1dnd4eXpBQkNERUZHSElKS0xNTk9QUVJTVFVWV1hZWjAxIn0mTm4jAAAAtAAAAFd1arXqCzpldmVudC10eXBlBwARY29udGVudEJsb2NrRGVsdGENOmNvbnRlbnQtdHlwZQcAEGFwcGxpY2F0aW9uL2pzb24NOm1lc3NhZ2UtdHlwZQcABWV2ZW50eyJjb250ZW50QmxvY2tJbmRleCI6MCwiZGVsdGEiOnsidGV4dCI6IiBMZXQifSwicCI6ImFiY2RlZmdoaWprbG1ub3BxcnN0dXZ3In34BFwTAAAA0AAAAFcZ2GRnCzpldmVudC10eXBlBwARY29udGVudEJsb2NrRGVsdGENOmNvbnRlbnQtdHlwZQcAEGFwcGxpY2F0aW9uL2pzb24NOm1lc3NhZ2UtdHlwZQcABWV2ZW50eyJjb250ZW50QmxvY2tJbmRleCI6MCwiZGVsdGEiOnsidGV4dCI6IidzIHVzZSJ9LCJwIjoiYWJjZGVmZ2hpamtsbW5vcHFyc3R1dnd4eXpBQkNERUZHSElKS0xNTk9QUVJTVFVWVyJ9vfdBjQAAALwAAABXRRr+Kws6ZXZlbnQtdHlwZQcAEWNvbnRlbnRCbG9ja0RlbHRhDTpjb250ZW50LXR5cGUHABBhcHBsaWNhdGlvbi9qc29uDTptZXNzYWdlLXR5cGUHAAVldmVudHsiY29udGVudEJsb2NrSW5kZXgiOjAsImRlbHRhIjp7InRleHQiOiIgdGhpcyJ9LCJwIjoiYWJjZGVmZ2hpamtsbW5vcHFyc3R1dnd4eXpBQkNEIn1Xtb4jAAAAuAAAAFewmljrCzpldmVudC10eXBlBwARY29udGVudEJsb2NrRGVsdGENOmNvbnRlbnQtdHlwZQcAEGFwcGxpY2F0aW9uL2pzb24NOm1lc3NhZ2UtdHlwZQcABWV2ZW50eyJjb250ZW50QmxvY2tJbmRleCI6MCwiZGVsdGEiOnsidGV4dCI6IiBmdW5jdGlvbiB0byJ9LCJwIjoiYWJjZGVmZ2hpamtsbW5vcHFycyJ9GYv84AAAALQAAABXdWq16gs6ZXZlbnQtdHlwZQcAEWNvbnRlbnRCbG9ja0RlbHRhDTpjb250ZW50LXR5cGUHABBhcHBsaWNhdGlvbi9qc29uDTptZXNzYWdlLXR5cGUHAAVldmVudHsiY29udGVudEJsb2NrSW5kZXgiOjAsImRlbHRhIjp7InRleHQiOiIgZ2V0In0sInAiOiJhYmNkZWZnaGlqa2xtbm9wcXJzdHV2dyJ99bdUOgAAAN4AAABXpujaBgs6ZXZlbnQtdHlwZQcAEWNvbnRlbnRCbG9ja0RlbHRhDTpjb250ZW50LXR5cGUHABBhcHBsaWNhdGlvbi9qc29uDTptZXNzYWdlLXR5cGUHAAVldmVudHsiY29udGVudEJsb2NrSW5kZXgiOjAsImRlbHRhIjp7InRleHQiOiIgdGhlIHJlc3VsdCJ9LCJwIjoiYWJjZGVmZ2hpamtsbW5vcHFyc3R1dnd4eXpBQkNERUZHSElKS0xNTk9QUVJTVFVWV1hZWjAxMjM0NSJ9niPS/gAAAM0AAABXgag3VAs6ZXZlbnQtdHlwZQcAEWNvbnRlbnRCbG9ja0RlbHRhDTpjb250ZW50LXR5cGUHABBhcHBsaWNhdGlvbi9qc29uDTptZXNzYWdlLXR5cGUHAAVldmVudHsiY29udGVudEJsb2NrSW5kZXgiOjAsImRlbHRhIjp7InRleHQiOiIuIn0sInAiOiJhYmNkZWZnaGlqa2xtbm9wcXJzdHV2d3h5ekFCQ0RFRkdISUpLTE1OT1BRUlNUVVZXWFkifRc68JQAAACuAAAAVig9Cl8LOmV2ZW50LXR5cGUHABBjb250ZW50QmxvY2tTdG9wDTpjb250ZW50LXR5cGUHABBhcHBsaWNhdGlvbi9qc29uDTptZXNzYWdlLXR5cGUHAAVldmVudHsiY29udGVudEJsb2NrSW5kZXgiOjAsInAiOiJhYmNkZWZnaGlqa2xtbm9wcXJzdHV2d3h5ekFCQ0RFRkdISUpLTE1OT1AifY2eizoAAAEEAAAAV67xblsLOmV2ZW50LXR5cGUHABFjb250ZW50QmxvY2tTdGFydA06Y29udGVudC10eXBlBwAQYXBwbGljYXRpb24vanNvbg06bWVzc2FnZS10eXBlBwAFZXZlbnR7ImNvbnRlbnRCbG9ja0luZGV4IjoxLCJwIjoiYWJjZGVmZ2hpamtsbW5vcHFyc3R1dnd4eXpBQkNERUZHSElKS0xNTk9QUVIiLCJzdGFydCI6eyJ0b29sVXNlIjp7Im5hbWUiOiJjb3NpbmUiLCJ0b29sVXNlSWQiOiJ0b29sdXNlX1FrbHJFSEtqUnU2T2M0QlFVZnk3WlEifX19kpNGawAAAK0AAABXGJpAGQs6ZXZlbnQtdHlwZQcAEWNvbnRlbnRCbG9ja0RlbHRhDTpjb250ZW50LXR5cGUHABBhcHBsaWNhdGlvbi9qc29uDTptZXNzYWdlLXR5cGUHAAVldmVudHsiY29udGVudEJsb2NrSW5kZXgiOjEsImRlbHRhIjp7InRvb2xVc2UiOnsiaW5wdXQiOiIifX0sInAiOiJhYmNkZWZnIn3XeK+kAAAAswAAAFfHSmn6CzpldmVudC10eXBlBwARY29udGVudEJsb2NrRGVsdGENOmNvbnRlbnQtdHlwZQcAEGFwcGxpY2F0aW9uL2pzb24NOm1lc3NhZ2UtdHlwZQcABWV2ZW50eyJjb250ZW50QmxvY2tJbmRleCI6MSwiZGVsdGEiOnsidG9vbFVzZSI6eyJpbnB1dCI6IntcInhcIjogN30ifX0sInAiOiJhYmMifaN4jhsAAACxAAAAVsqNCgwLOmV2ZW50LXR5cGUHABBjb250ZW50QmxvY2tTdG9wDTpjb250ZW50LXR5cGUHABBhcHBsaWNhdGlvbi9qc29uDTptZXNzYWdlLXR5cGUHAAVldmVudHsiY29udGVudEJsb2NrSW5kZXgiOjEsInAiOiJhYmNkZWZnaGlqa2xtbm9wcXJzdHV2d3h5ekFCQ0RFRkdISUpLTE1OT1BRUlMifUJp3UkAAACFAAAAUQBIgekLOmV2ZW50LXR5cGUHAAttZXNzYWdlU3RvcA06Y29udGVudC10eXBlBwAQYXBwbGljYXRpb24vanNvbg06bWVzc2FnZS10eXBlBwAFZXZlbnR7InAiOiJhYmNkIiwic3RvcFJlYXNvbiI6InRvb2xfdXNlIn3ejv14AAAA6wAAAE6rQkUwCzpldmVudC10eXBlBwAIbWV0YWRhdGENOmNvbnRlbnQtdHlwZQcAEGFwcGxpY2F0aW9uL2pzb24NOm1lc3NhZ2UtdHlwZQcABWV2ZW50eyJtZXRyaWNzIjp7ImxhdGVuY3lNcyI6MTk1N30sInAiOiJhYmNkZWZnIiwic2VydmljZVRpZXIiOnsidHlwZSI6ImRlZmF1bHQifSwidXNhZ2UiOnsiaW5wdXRUb2tlbnMiOjM4Niwib3V0cHV0VG9rZW5zIjo3NSwidG90YWxUb2tlbnMiOjQ2MX19Oi638g=="

func TestOpenAIToAWSBedrockTranslatorExtractAmazonEventStreamEvents(t *testing.T) {
	buf := bytes.NewBuffer(nil)
	e := eventstream.NewEncoder()
	var offsets []int
	for _, data := range []awsbedrock.ConverseStreamEvent{
		{Delta: &awsbedrock.ConverseStreamEventContentBlockDelta{Text: ptr.To("1")}},
		{Delta: &awsbedrock.ConverseStreamEventContentBlockDelta{Text: ptr.To("2")}},
		{Delta: &awsbedrock.ConverseStreamEventContentBlockDelta{Text: ptr.To("3")}},
	} {
		offsets = append(offsets, buf.Len())
		eventPayload, err := json.Marshal(data)
		require.NoError(t, err)
		err = e.Encode(buf, eventstream.Message{
			Headers: eventstream.Headers{{Name: "event-type", Value: eventstream.StringValue("content")}},
			Payload: eventPayload,
		})
		require.NoError(t, err)
	}

	eventBytes := buf.Bytes()

	t.Run("all-at-once", func(t *testing.T) {
		o := &openAIToAWSBedrockTranslatorV1ChatCompletion{}
		o.bufferedBody = eventBytes
		o.extractAmazonEventStreamEvents()
		require.Len(t, o.events, 3)
		require.Empty(t, o.bufferedBody)
		for i, text := range []string{"1", "2", "3"} {
			require.Equal(t, text, *o.events[i].Delta.Text)
		}
	})

	t.Run("in-chunks", func(t *testing.T) {
		o := &openAIToAWSBedrockTranslatorV1ChatCompletion{}
		o.bufferedBody = eventBytes[0:1]
		o.extractAmazonEventStreamEvents()
		require.Empty(t, o.events)
		require.Len(t, o.bufferedBody, 1)

		o.bufferedBody = eventBytes[0 : offsets[1]+5]
		o.extractAmazonEventStreamEvents()
		require.Len(t, o.events, 1)
		require.Equal(t, eventBytes[offsets[1]:offsets[1]+5], o.bufferedBody)

		clear(o.events)
		o.events = o.events[:0]
		o.bufferedBody = eventBytes[0 : offsets[2]+5]
		o.extractAmazonEventStreamEvents()
		require.Len(t, o.events, 2)
		require.Equal(t, eventBytes[offsets[2]:offsets[2]+5], o.bufferedBody)
	})

	t.Run("real events", func(t *testing.T) {
		o := &openAIToAWSBedrockTranslatorV1ChatCompletion{}
		var err error
		o.bufferedBody, err = base64.StdEncoding.DecodeString(base64RealStreamingEvents)
		require.NoError(t, err)
		o.extractAmazonEventStreamEvents()

		var texts []string
		var usage *awsbedrock.TokenUsage
		for _, event := range o.events {
			if delta := event.Delta; delta != nil && delta.Text != nil && *delta.Text != "" {
				texts = append(texts, *event.Delta.Text)
			}
			if u := event.Usage; u != nil {
				usage = u
			}
		}
		require.Equal(t,
			"To calculate the cosine of 7, we can use the \"cosine\" function that is available to us. Let's use this function to get the result.",
			strings.Join(texts, ""),
		)
		require.NotNil(t, usage)
		require.Equal(t, int64(461), usage.TotalTokens)
	})
}

func TestOpenAIToAWSBedrockTranslator_convertEvent(t *testing.T) {
	ptrOf := func(s string) *string { return &s }
	for _, tc := range []struct {
		name string
		in   awsbedrock.ConverseStreamEvent
		out  *openai.ChatCompletionResponseChunk
	}{
		{
			name: "usage",
			in: awsbedrock.ConverseStreamEvent{
				EventType: awsbedrock.ConverseStreamEventTypeMetadata.String(),
				Usage: &awsbedrock.TokenUsage{
					InputTokens:          10,
					OutputTokens:         20,
					TotalTokens:          30,
					CacheReadInputTokens: ptr.To[int64](5),
				},
			},
			out: &openai.ChatCompletionResponseChunk{
				ID:      "123",
				Model:   "claude-sonnet-4",
				Created: openai.JSONUNIXTime(time.Unix(releaseDateUnix, 0)), // 0 nanoseconds
				Object:  "chat.completion.chunk",
				Choices: []openai.ChatCompletionResponseChunkChoice{},
				Usage: &openai.Usage{
					TotalTokens:      35,
					PromptTokens:     15,
					CompletionTokens: 20,
					PromptTokensDetails: &openai.PromptTokensDetails{
						CachedTokens: 5,
					},
				},
			},
		},
		{
			name: "role",
			in: awsbedrock.ConverseStreamEvent{
				EventType: awsbedrock.ConverseStreamEventTypeMessageStart.String(),
				Role:      ptrOf(awsbedrock.ConversationRoleAssistant),
			},
			out: &openai.ChatCompletionResponseChunk{
				ID:      "123",
				Model:   "claude-sonnet-4",
				Created: openai.JSONUNIXTime(time.Unix(releaseDateUnix, 0)), // 0 nanoseconds
				Object:  "chat.completion.chunk",
				Choices: []openai.ChatCompletionResponseChunkChoice{
					{
						Delta: &openai.ChatCompletionResponseChunkChoiceDelta{
							Role:    awsbedrock.ConversationRoleAssistant,
							Content: &emptyString,
						},
					},
				},
			},
		},
		{
			name: "delta",
			in: awsbedrock.ConverseStreamEvent{
				EventType: awsbedrock.ConverseStreamEventTypeContentBlockDelta.String(),
				Delta:     &awsbedrock.ConverseStreamEventContentBlockDelta{Text: ptr.To("response")},
			},
			out: &openai.ChatCompletionResponseChunk{
				ID:      "123",
				Model:   "claude-sonnet-4",
				Created: openai.JSONUNIXTime(time.Unix(releaseDateUnix, 0)), // 0 nanoseconds
				Object:  "chat.completion.chunk",
				Choices: []openai.ChatCompletionResponseChunkChoice{
					{
						Delta: &openai.ChatCompletionResponseChunkChoiceDelta{
							Content: ptrOf("response"),
						},
					},
				},
			},
		},
		{
			name: "reasoning delta",
			in: awsbedrock.ConverseStreamEvent{
				EventType: awsbedrock.ConverseStreamEventTypeContentBlockDelta.String(),
				Delta: &awsbedrock.ConverseStreamEventContentBlockDelta{
					ReasoningContent: &awsbedrock.ReasoningContentBlockDelta{
						Text: "thinking...",
					},
				},
			},
			out: &openai.ChatCompletionResponseChunk{
				ID:      "123",
				Model:   "claude-sonnet-4",
				Created: openai.JSONUNIXTime(time.Unix(releaseDateUnix, 0)), // 0 nanoseconds
				Object:  "chat.completion.chunk",
				Choices: []openai.ChatCompletionResponseChunkChoice{
					{
						Delta: &openai.ChatCompletionResponseChunkChoiceDelta{
							ReasoningContent: &openai.StreamReasoningContent{
								Text: "thinking...",
							},
						},
					},
				},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := &openAIToAWSBedrockTranslatorV1ChatCompletion{requestModel: "claude-sonnet-4"}
			chunk, ok := o.convertEvent(&tc.in)
			// Define the comparison options: Ignore the "ID" and "Created" field
			ignoreDynamicFields := cmpopts.IgnoreFields(openai.ChatCompletionResponseChunk{}, "ID", "Created")
			if tc.out == nil {
				require.False(t, ok)
			} else {
				// Use require.True and cmp.Equal with the options
				require.True(t, cmp.Equal(*tc.out, *chunk, ignoreDynamicFields),
					"The ChatCompletionResponseChunk structs should be equal ignoring the ID and Created field")
			}
		})
	}
}

func TestOpenAIToAWSBedrockTranslatorV1ChatCompletion_Streaming_WithReasoning(t *testing.T) {
	inputEvents := []awsbedrock.ConverseStreamEvent{
		{Role: ptr.To(awsbedrock.ConversationRoleAssistant)},
		{
			ContentBlockIndex: 0,
			EventType:         awsbedrock.ConverseStreamEventTypeContentBlockDelta.String(),
			Delta: &awsbedrock.ConverseStreamEventContentBlockDelta{
				ReasoningContent: &awsbedrock.ReasoningContentBlockDelta{
					Text: "Okay, 27 * 453. ",
				},
			},
		},
		{
			ContentBlockIndex: 0,
			EventType:         awsbedrock.ConverseStreamEventTypeContentBlockDelta.String(),
			Delta: &awsbedrock.ConverseStreamEventContentBlockDelta{
				ReasoningContent: &awsbedrock.ReasoningContentBlockDelta{
					Text: "Let's do the math...",
				},
			},
		},
		{
			ContentBlockIndex: 0,
			EventType:         awsbedrock.ConverseStreamEventTypeContentBlockDelta.String(),
			Delta: &awsbedrock.ConverseStreamEventContentBlockDelta{
				Text: ptr.To("The result of 27 multiplied by 453 is "),
			},
		},
		{
			ContentBlockIndex: 0,
			EventType:         awsbedrock.ConverseStreamEventTypeContentBlockDelta.String(),
			Delta: &awsbedrock.ConverseStreamEventContentBlockDelta{
				Text: ptr.To("12231."),
			},
		},
		{StopReason: ptr.To(awsbedrock.StopReasonEndTurn)},
	}

	buf := bytes.NewBuffer(nil)
	encoder := eventstream.NewEncoder()
	for _, event := range inputEvents {
		payload, err := json.Marshal(event)
		require.NoError(t, err)

		// This header is a simplification; the real stream has more, but this is sufficient for the decoder.
		err = encoder.Encode(buf, eventstream.Message{
			Headers: eventstream.Headers{
				{Name: ":event-type", Value: eventstream.StringValue("chunk")},
			},
			Payload: payload,
		})
		require.NoError(t, err)
	}
	// Process the entire encoded stream through the translator.
	o := &openAIToAWSBedrockTranslatorV1ChatCompletion{stream: true}
	_, outputBody, _, _, err := o.ResponseBody(nil, buf, true, nil)
	require.NoError(t, err)
	require.NotNil(t, outputBody)

	// Parse the translated SSE (Server-Sent Events) output.
	lines := strings.Split(string(outputBody), "\n")

	var generationChunks []string
	var reasoningChunks []string

	for _, line := range lines {
		if strings.HasPrefix(line, "data: ") {
			data := strings.TrimPrefix(line, "data: ")
			if strings.Contains(data, "[DONE]") {
				continue
			}

			var chunk openai.ChatCompletionResponseChunk
			err := json.Unmarshal([]byte(data), &chunk)
			require.NoError(t, err)

			if len(chunk.Choices) > 0 {
				delta := chunk.Choices[0].Delta
				if delta != nil {
					if delta.Content != nil && *delta.Content != "" {
						generationChunks = append(generationChunks, *delta.Content)
					}
					if delta.ReasoningContent != nil && delta.ReasoningContent.Text != "" {
						reasoningChunks = append(reasoningChunks, delta.ReasoningContent.Text)

						var untypedChunk map[string]interface{}
						err = json.Unmarshal([]byte(data), &untypedChunk)
						require.NoError(t, err)

						choices, _ := untypedChunk["choices"].([]interface{})
						choice, _ := choices[0].(map[string]interface{})
						deltaMap, _ := choice["delta"].(map[string]interface{})
						reasoningContent, ok := deltaMap["reasoning_content"].(string)
						require.True(t, ok, "Delta should have a 'reasoning_content' string")
						require.NotEmpty(t, reasoningContent)
					}
				}
			}
		}
	}

	generation := strings.Join(generationChunks, "")
	reasoning := strings.Join(reasoningChunks, "")

	require.Equal(t, "The result of 27 multiplied by 453 is 12231.", generation)
	require.Equal(t, "Okay, 27 * 453. Let's do the math...", reasoning)

	require.NotEmpty(t, generation, "Generation content should not be empty")
	require.NotEmpty(t, reasoning, "Reasoning content should not be empty")
}

func TestOpenAIToAWSBedrockTranslatorV1ChatCompletion_ResponseBody_WithReasoning(t *testing.T) {
	// Define a mock AWS Bedrock non-streaming response.
	// This response contains two content blocks: one for reasoning and one for the final text answer.
	mockBedrockResponse := awsbedrock.ConverseResponse{
		Output: &awsbedrock.ConverseOutput{
			Message: awsbedrock.Message{
				Role: awsbedrock.ConversationRoleAssistant,
				Content: []*awsbedrock.ContentBlock{
					{
						ReasoningContent: &awsbedrock.ReasoningContentBlock{
							ReasoningText: &awsbedrock.ReasoningTextBlock{
								Text: "The user wants to compare two numbers. 9.11 is larger than 9.8.",
							},
						},
					},
					{
						Text: ptr.To("9.11 is greater than 9.8."),
					},
				},
			},
		},
		StopReason: ptr.To(awsbedrock.StopReasonEndTurn),
	}

	body, err := json.Marshal(mockBedrockResponse)
	require.NoError(t, err)

	o := &openAIToAWSBedrockTranslatorV1ChatCompletion{}
	_, outputBody, _, _, err := o.ResponseBody(nil, bytes.NewBuffer(body), false, nil)
	require.NoError(t, err)
	require.NotNil(t, outputBody)

	var openAIResponse openai.ChatCompletionResponse
	err = json.Unmarshal(outputBody, &openAIResponse)
	require.NoError(t, err)

	// Assert that the translated response contains both content and reasoning.
	require.Len(t, openAIResponse.Choices, 1, "There should be exactly one choice in the response")
	message := openAIResponse.Choices[0].Message

	require.NotNil(t, message.Content, "Message content should not be nil")
	require.NotEmpty(t, *message.Content, "Message content should not be an empty string")
	require.Equal(t, "9.11 is greater than 9.8.", *message.Content)

	require.NotNil(t, message.ReasoningContent, "Reasoning content should not be nil")
	reasoningText, ok := message.ReasoningContent.Value.(string)
	require.True(t, ok, "reasoning_content should be a plain string")
	require.Equal(t, "The user wants to compare two numbers. 9.11 is larger than 9.8.", reasoningText)

	require.Len(t, message.ThinkingBlocks, 1, "Reasoning text should be echoed in thinking_blocks")
	require.Equal(t, "thinking", message.ThinkingBlocks[0].Type)
	require.Equal(t, reasoningText, message.ThinkingBlocks[0].Thinking)

	var untypedResponse map[string]interface{}
	err = json.Unmarshal(outputBody, &untypedResponse)
	require.NoError(t, err)

	// Verify reasoning_content is serialized as a plain string on the wire.
	choices, ok := untypedResponse["choices"].([]interface{})
	require.True(t, ok, "JSON should have a 'choices' array")
	require.Len(t, choices, 1)

	choice, ok := choices[0].(map[string]interface{})
	require.True(t, ok, "Choice item should be a map")

	messageMap, ok := choice["message"].(map[string]interface{})
	require.True(t, ok, "Choice should have a 'message' map")
	reasoningContent, ok := messageMap["reasoning_content"].(string)
	require.True(t, ok, "Message should have a 'reasoning_content' string")
	require.Equal(t, "The user wants to compare two numbers. 9.11 is larger than 9.8.", reasoningContent)
}

func TestOpenAIToAWSBedrockTranslatorV1ChatCompletion_ResponseBody_WithReasoningSignatureAndRedacted(t *testing.T) {
	// A signature and redacted reasoning content cannot live in the plain-string
	// reasoning_content, so they must surface via thinking_blocks.
	redactedBytes := []byte("a redacted thought")
	mockBedrockResponse := awsbedrock.ConverseResponse{
		Output: &awsbedrock.ConverseOutput{
			Message: awsbedrock.Message{
				Role: awsbedrock.ConversationRoleAssistant,
				Content: []*awsbedrock.ContentBlock{
					{
						ReasoningContent: &awsbedrock.ReasoningContentBlock{
							ReasoningText: &awsbedrock.ReasoningTextBlock{
								Text:      "Let me think about this.",
								Signature: "sig_xyz",
							},
						},
					},
					{
						ReasoningContent: &awsbedrock.ReasoningContentBlock{
							RedactedContent: redactedBytes,
						},
					},
					{
						Text: ptr.To("The answer is 42."),
					},
				},
			},
		},
		StopReason: ptr.To(awsbedrock.StopReasonEndTurn),
	}

	body, err := json.Marshal(mockBedrockResponse)
	require.NoError(t, err)

	o := &openAIToAWSBedrockTranslatorV1ChatCompletion{}
	_, outputBody, _, _, err := o.ResponseBody(nil, bytes.NewBuffer(body), false, nil)
	require.NoError(t, err)
	require.NotNil(t, outputBody)

	var openAIResponse openai.ChatCompletionResponse
	require.NoError(t, json.Unmarshal(outputBody, &openAIResponse))
	require.Len(t, openAIResponse.Choices, 1)
	message := openAIResponse.Choices[0].Message

	require.NotNil(t, message.Content)
	require.Equal(t, "The answer is 42.", *message.Content)

	reasoningText, ok := message.ReasoningContent.Value.(string)
	require.True(t, ok, "reasoning_content should be a plain string")
	require.Equal(t, "Let me think about this.", reasoningText)

	require.Len(t, message.ThinkingBlocks, 2)
	require.Equal(t, "thinking", message.ThinkingBlocks[0].Type)
	require.Equal(t, "Let me think about this.", message.ThinkingBlocks[0].Thinking)
	require.Equal(t, "sig_xyz", message.ThinkingBlocks[0].Signature)
	require.Equal(t, "redacted_thinking", message.ThinkingBlocks[1].Type)
	require.Equal(t, base64.StdEncoding.EncodeToString(redactedBytes), message.ThinkingBlocks[1].Data)
}

func TestOpenAIToAWSBedrockTranslatorV1ChatCompletion_ResponseBody_WithReasoningSignatureOnly(t *testing.T) {
	// A reasoning block carrying only a signature (no text) must not emit an empty
	// reasoning_content string; the signature still round-trips via thinking_blocks.
	mockBedrockResponse := awsbedrock.ConverseResponse{
		Output: &awsbedrock.ConverseOutput{
			Message: awsbedrock.Message{
				Role: awsbedrock.ConversationRoleAssistant,
				Content: []*awsbedrock.ContentBlock{
					{
						ReasoningContent: &awsbedrock.ReasoningContentBlock{
							ReasoningText: &awsbedrock.ReasoningTextBlock{
								Signature: "sig_only",
							},
						},
					},
					{
						Text: ptr.To("The answer is 42."),
					},
				},
			},
		},
		StopReason: ptr.To(awsbedrock.StopReasonEndTurn),
	}

	body, err := json.Marshal(mockBedrockResponse)
	require.NoError(t, err)

	o := &openAIToAWSBedrockTranslatorV1ChatCompletion{}
	_, outputBody, _, _, err := o.ResponseBody(nil, bytes.NewBuffer(body), false, nil)
	require.NoError(t, err)
	require.NotNil(t, outputBody)

	var openAIResponse openai.ChatCompletionResponse
	require.NoError(t, json.Unmarshal(outputBody, &openAIResponse))
	require.Len(t, openAIResponse.Choices, 1)
	message := openAIResponse.Choices[0].Message

	require.Nil(t, message.ReasoningContent, "reasoning_content must be absent when there is no reasoning text")
	require.Len(t, message.ThinkingBlocks, 1)
	require.Equal(t, "thinking", message.ThinkingBlocks[0].Type)
	require.Empty(t, message.ThinkingBlocks[0].Thinking)
	require.Equal(t, "sig_only", message.ThinkingBlocks[0].Signature)

	// reasoning_content must not appear on the wire at all.
	var untypedResponse map[string]interface{}
	require.NoError(t, json.Unmarshal(outputBody, &untypedResponse))
	choices := untypedResponse["choices"].([]interface{})
	messageMap := choices[0].(map[string]interface{})["message"].(map[string]interface{})
	_, hasReasoning := messageMap["reasoning_content"]
	require.False(t, hasReasoning, "reasoning_content key must be omitted for a signature-only block")
}

func TestOpenAIToAWSBedrockTranslatorV1ChatCompletion_Streaming_WithRedactedContent(t *testing.T) {
	redactedBytes := []byte("a redacted thought")
	inputEvents := []awsbedrock.ConverseStreamEvent{
		{
			EventType: awsbedrock.ConverseStreamEventTypeMessageStart.String(),
			Role:      ptr.To(awsbedrock.ConversationRoleAssistant),
		},
		{
			EventType: awsbedrock.ConverseStreamEventTypeContentBlockDelta.String(),
			Delta: &awsbedrock.ConverseStreamEventContentBlockDelta{
				ReasoningContent: &awsbedrock.ReasoningContentBlockDelta{
					RedactedContent: redactedBytes,
				},
			},
		},
		{
			EventType: awsbedrock.ConverseStreamEventTypeContentBlockDelta.String(),
			Delta: &awsbedrock.ConverseStreamEventContentBlockDelta{
				Text: ptr.To("This is the final answer."),
			},
		},
		{
			EventType:  awsbedrock.ConverseStreamEventTypeMessageStop.String(),
			StopReason: ptr.To(awsbedrock.StopReasonEndTurn),
		},
	}

	buf := bytes.NewBuffer(nil)
	encoder := eventstream.NewEncoder()
	for _, event := range inputEvents {
		payload, err := json.Marshal(event)
		require.NoError(t, err)
		err = encoder.Encode(buf, eventstream.Message{
			Headers: eventstream.Headers{{Name: ":event-type", Value: eventstream.StringValue("chunk")}},
			Payload: payload,
		})
		require.NoError(t, err)
	}

	o := &openAIToAWSBedrockTranslatorV1ChatCompletion{stream: true}
	_, outputBody, _, _, err := o.ResponseBody(nil, buf, true, nil)
	require.NoError(t, err)

	lines := strings.Split(string(outputBody), "\n")
	var foundReasoningChunk bool
	for _, line := range lines {
		if strings.HasPrefix(line, "data: ") {
			data := strings.TrimPrefix(line, "data: ")
			if strings.Contains(data, "[DONE]") {
				continue
			}
			var chunk openai.ChatCompletionResponseChunk
			err := json.Unmarshal([]byte(data), &chunk)
			require.NoError(t, err)

			if len(chunk.Choices) > 0 && chunk.Choices[0].Delta != nil && len(chunk.Choices[0].Delta.ThinkingBlocks) > 0 {
				require.Nil(t, chunk.Choices[0].Delta.ReasoningContent)
				block := chunk.Choices[0].Delta.ThinkingBlocks[0]
				require.Equal(t, "redacted_thinking", block.Type)
				require.Equal(t, base64.StdEncoding.EncodeToString(redactedBytes), block.Data)
				foundReasoningChunk = true
			}
		}
	}
	require.True(t, foundReasoningChunk, "A reasoning chunk with redacted content should have been found")
}

// TestResponseModel_AWSBedrock tests that AWS Bedrock returns the request model (no virtualization)
func TestResponseModel_AWSBedrock(t *testing.T) {
	modelName := "anthropic.claude-3-5-sonnet-20241022-v2:0"
	translator := NewChatCompletionOpenAIToAWSBedrockTranslator(modelName)

	// Initialize translator with the model
	req := &openai.ChatCompletionRequest{
		Model: "claude-3-5-sonnet",
	}
	reqBody, _ := json.Marshal(req)
	_, _, err := translator.RequestBody(reqBody, req, false)
	require.NoError(t, err)

	// AWS Bedrock response doesn't have model field
	bedrockResponse := `{
		"output": {
			"message": {
				"content": [{"text": "Hello"}],
				"role": "assistant"
			}
		},
		"usage": {
			"inputTokens": 10,
			"outputTokens": 5,
			"totalTokens": 15
		}
	}`

	_, bm, tokenUsage, responseModel, err := translator.ResponseBody(nil, bytes.NewReader([]byte(bedrockResponse)), true, nil)
	require.NoError(t, err)
	require.Equal(t, modelName, responseModel) // Returns the request model since no virtualization
	respBodyModel := gjson.GetBytes(bm, "model").Value()
	require.Equal(t, modelName, respBodyModel)
	inputTokens, ok := tokenUsage.InputTokens()
	require.True(t, ok)
	require.Equal(t, uint32(10), inputTokens)

	outputTokens, ok := tokenUsage.OutputTokens()
	require.True(t, ok)
	require.Equal(t, uint32(5), outputTokens)
}

func TestOpenAIToAWSBedrockTranslator_CacheControl(t *testing.T) {
	tests := []struct {
		name         string
		input        openai.ChatCompletionRequest
		expectedJSON string
	}{
		{
			name: "cache control on user text message",
			input: openai.ChatCompletionRequest{
				Model: "anthropic.claude-3-5-sonnet-20241022-v2:0",
				Messages: []openai.ChatCompletionMessageParamUnion{
					{
						OfUser: &openai.ChatCompletionUserMessageParam{
							Role: openai.ChatMessageRoleUser,
							Content: openai.StringOrUserRoleContentUnion{
								Value: []openai.ChatCompletionContentPartUserUnionParam{
									{
										OfText: &openai.ChatCompletionContentPartTextParam{
											Type: "text",
											Text: "Cache this content",
											AnthropicContentFields: &openai.AnthropicContentFields{
												CacheControl: anthropic.CacheControlEphemeralParam{
													Type: constant.ValueOf[constant.Ephemeral](),
												},
											},
										},
									},
								},
							},
						},
					},
				},
			},
			expectedJSON: `{"inferenceConfig":{},"messages":[{"content":[{"text":"Cache this content"},{"cachePoint":{"type":"default"}}],"role":"user"}]}`,
		},
		{
			name: "cache control on system message",
			input: openai.ChatCompletionRequest{
				Model: "anthropic.claude-3-5-sonnet-20241022-v2:0",
				Messages: []openai.ChatCompletionMessageParamUnion{
					{
						OfSystem: &openai.ChatCompletionSystemMessageParam{
							Role: openai.ChatMessageRoleSystem,
							Content: openai.ContentUnion{
								Value: []openai.ChatCompletionContentPartTextParam{
									{
										Type: "text",
										Text: "You are a helpful assistant",
										AnthropicContentFields: &openai.AnthropicContentFields{
											CacheControl: anthropic.CacheControlEphemeralParam{
												Type: constant.ValueOf[constant.Ephemeral](),
											},
										},
									},
								},
							},
						},
					},
				},
			},
			expectedJSON: `{"inferenceConfig":{},"messages":[],"system":[{"text":"You are a helpful assistant"},{"cachePoint":{"type":"default"}}]}`,
		},
		{
			name: "cache control on tool definition",
			input: openai.ChatCompletionRequest{
				Model: "anthropic.claude-3-5-sonnet-20241022-v2:0",
				Messages: []openai.ChatCompletionMessageParamUnion{
					{
						OfUser: &openai.ChatCompletionUserMessageParam{
							Role: openai.ChatMessageRoleUser,
							Content: openai.StringOrUserRoleContentUnion{
								Value: "Test message",
							},
						},
					},
				},
				Tools: []openai.Tool{
					{
						Type: openai.ToolTypeFunction,
						Function: &openai.FunctionDefinition{
							Name:        "get_weather",
							Description: "Get weather information",
							Parameters:  map[string]interface{}{"type": "object"},
							AnthropicContentFields: &openai.AnthropicContentFields{
								CacheControl: anthropic.CacheControlEphemeralParam{
									Type: constant.ValueOf[constant.Ephemeral](),
								},
							},
						},
					},
				},
			},
			expectedJSON: `{"inferenceConfig":{},"messages":[{"content":[{"text":"Test message"}],"role":"user"}],"toolConfig":{"tools":[{"toolSpec":{"description":"Get weather information","inputSchema":{"json":{"type":"object"}},"name":"get_weather"}},{"cachePoint":{"type":"default"}}]}}`,
		},
		{
			name: "no cache control",
			input: openai.ChatCompletionRequest{
				Model: "anthropic.claude-3-5-sonnet-20241022-v2:0",
				Messages: []openai.ChatCompletionMessageParamUnion{
					{
						OfUser: &openai.ChatCompletionUserMessageParam{
							Role: openai.ChatMessageRoleUser,
							Content: openai.StringOrUserRoleContentUnion{
								Value: "No cache",
							},
						},
					},
				},
			},
			expectedJSON: `{"inferenceConfig":{},"messages":[{"content":[{"text":"No cache"}],"role":"user"}]}`, // Should not contain cachePoint
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			translator := &openAIToAWSBedrockTranslatorV1ChatCompletion{}
			_, body, err := translator.RequestBody(nil, &test.input, false)
			require.NoError(t, err)
			require.Equal(t, test.expectedJSON, string(body))
		})
	}
}

func TestCacheControlHelpers(t *testing.T) {
	t.Run("isCacheEnabled", func(t *testing.T) {
		// Test with nil fields
		require.False(t, isCacheEnabled(nil))

		// Test with empty fields
		require.False(t, isCacheEnabled(&openai.AnthropicContentFields{}))

		// Test with ephemeral cache control
		require.True(t, isCacheEnabled(&openai.AnthropicContentFields{
			CacheControl: anthropic.CacheControlEphemeralParam{Type: constant.ValueOf[constant.Ephemeral]()},
		}))

		// Test with non-ephemeral cache control
		require.False(t, isCacheEnabled(&openai.AnthropicContentFields{
			CacheControl: anthropic.CacheControlEphemeralParam{Type: "other"},
		}))
	})

	t.Run("getCachePoint", func(t *testing.T) {
		// Test with cache control enabled
		cachePoint := getCachePoint(&openai.AnthropicContentFields{
			CacheControl: anthropic.CacheControlEphemeralParam{Type: constant.ValueOf[constant.Ephemeral]()},
		})
		require.NotNil(t, cachePoint)
		require.Equal(t, "default", cachePoint.Type)

		// Test with no cache control
		cachePoint2 := getCachePoint(nil)
		require.Nil(t, cachePoint2)

		// Test with non-ephemeral cache control
		cachePoint3 := getCachePoint(&openai.AnthropicContentFields{
			CacheControl: anthropic.CacheControlEphemeralParam{Type: "other"},
		})
		require.Nil(t, cachePoint3)
	})
}

func TestOpenAIToAWSBedrockTranslator_EmptyContentMessages(t *testing.T) {
	t.Run("empty assistant message without tool calls is dropped", func(t *testing.T) {
		for _, name := range []string{"empty string", "nil content"} {
			t.Run(name, func(t *testing.T) {
				assistantMsg := &openai.ChatCompletionAssistantMessageParam{
					Role: openai.ChatMessageRoleAssistant,
				}
				if name == "empty string" {
					assistantMsg.Content = openai.StringOrAssistantRoleContentUnion{Value: ""}
				}

				o := &openAIToAWSBedrockTranslatorV1ChatCompletion{}
				req := openai.ChatCompletionRequest{
					Model: "anthropic.claude-3-5-sonnet-20241022-v2:0",
					Messages: []openai.ChatCompletionMessageParamUnion{
						{OfUser: &openai.ChatCompletionUserMessageParam{Role: openai.ChatMessageRoleUser, Content: openai.StringOrUserRoleContentUnion{Value: "Hello"}}},
						{OfAssistant: assistantMsg},
						{OfUser: &openai.ChatCompletionUserMessageParam{Role: openai.ChatMessageRoleUser, Content: openai.StringOrUserRoleContentUnion{Value: "Can you help?"}}},
					},
				}

				_, newBody, err := o.RequestBody(nil, &req, false)
				require.NoError(t, err)

				var awsReq awsbedrock.ConverseInput
				require.NoError(t, json.Unmarshal(newBody, &awsReq))
				requireNoEmptyAssistantContent(t, awsReq.Messages)
			})
		}
	})

	t.Run("assistant message with only tool calls and no content should work", func(t *testing.T) {
		o := &openAIToAWSBedrockTranslatorV1ChatCompletion{}
		req := openai.ChatCompletionRequest{
			Model: "anthropic.claude-3-5-sonnet-20241022-v2:0",
			Messages: []openai.ChatCompletionMessageParamUnion{
				{OfUser: &openai.ChatCompletionUserMessageParam{Role: openai.ChatMessageRoleUser, Content: openai.StringOrUserRoleContentUnion{Value: "What is the weather?"}}},
				{OfAssistant: &openai.ChatCompletionAssistantMessageParam{
					Role: openai.ChatMessageRoleAssistant,
					ToolCalls: []openai.ChatCompletionMessageToolCallParam{
						{ID: ptr.To("call_abc123"), Type: openai.ChatCompletionMessageToolCallTypeFunction, Function: openai.ChatCompletionMessageToolCallFunctionParam{Name: "get_weather", Arguments: `{"location":"NYC"}`}},
					},
				}},
				{OfTool: &openai.ChatCompletionToolMessageParam{Role: openai.ChatMessageRoleTool, ToolCallID: "call_abc123", Content: openai.ContentUnion{Value: `{"temp":72}`}}},
			},
		}

		_, newBody, err := o.RequestBody(nil, &req, false)
		require.NoError(t, err)

		var awsReq awsbedrock.ConverseInput
		require.NoError(t, json.Unmarshal(newBody, &awsReq))

		var foundAssistant bool
		for _, msg := range awsReq.Messages {
			if msg.Role == openai.ChatMessageRoleAssistant {
				foundAssistant = true
				require.NotEmpty(t, msg.Content, "assistant message with tool calls should have non-empty content")
				require.NotNil(t, msg.Content[0].ToolUse, "expected a ToolUse block")
				require.Equal(t, "get_weather", msg.Content[0].ToolUse.Name)
			}
		}
		require.True(t, foundAssistant, "should have found an assistant message")
	})

	t.Run("mixed messages with some empty assistant messages should handle correctly", func(t *testing.T) {
		o := &openAIToAWSBedrockTranslatorV1ChatCompletion{}
		req := openai.ChatCompletionRequest{
			Model: "anthropic.claude-3-5-sonnet-20241022-v2:0",
			Messages: []openai.ChatCompletionMessageParamUnion{
				{OfUser: &openai.ChatCompletionUserMessageParam{Role: openai.ChatMessageRoleUser, Content: openai.StringOrUserRoleContentUnion{Value: "Hello"}}},
				{OfAssistant: &openai.ChatCompletionAssistantMessageParam{Role: openai.ChatMessageRoleAssistant, Content: openai.StringOrAssistantRoleContentUnion{Value: ""}}},
				{OfUser: &openai.ChatCompletionUserMessageParam{Role: openai.ChatMessageRoleUser, Content: openai.StringOrUserRoleContentUnion{Value: "Tell me more"}}},
				{OfAssistant: &openai.ChatCompletionAssistantMessageParam{Role: openai.ChatMessageRoleAssistant, Content: openai.StringOrAssistantRoleContentUnion{Value: "Here is the info"}}},
			},
		}

		_, newBody, err := o.RequestBody(nil, &req, false)
		require.NoError(t, err)

		var awsReq awsbedrock.ConverseInput
		require.NoError(t, json.Unmarshal(newBody, &awsReq))
		requireNoEmptyAssistantContent(t, awsReq.Messages)

		var assistantWithContent bool
		for _, msg := range awsReq.Messages {
			if msg.Role == openai.ChatMessageRoleAssistant {
				for _, block := range msg.Content {
					if block.Text != nil && *block.Text == "Here is the info" {
						assistantWithContent = true
					}
				}
			}
		}
		require.True(t, assistantWithContent, "should preserve assistant message with actual content")
	})
}

func requireNoEmptyAssistantContent(t *testing.T, messages []*awsbedrock.Message) {
	t.Helper()
	for i, msg := range messages {
		if msg.Role == openai.ChatMessageRoleAssistant && len(msg.Content) == 0 {
			t.Errorf("message at index %d is an assistant message with empty content array, which Bedrock will reject", i)
		}
	}
}

func TestOpenAIToAWSBedrockTranslatorV1ChatCompletion_Streaming_ResponseBody_MarshalError(t *testing.T) {
	// Build a valid Bedrock streaming event that will be successfully decoded,
	// but inject a json.Marshal failure to exercise the error return path
	// that was previously a panic.
	inputEvents := []awsbedrock.ConverseStreamEvent{
		{Role: ptr.To(awsbedrock.ConversationRoleAssistant)},
		{
			ContentBlockIndex: 0,
			EventType:         awsbedrock.ConverseStreamEventTypeContentBlockDelta.String(),
			Delta: &awsbedrock.ConverseStreamEventContentBlockDelta{
				Text: ptr.To("hello"),
			},
		},
		{StopReason: ptr.To(awsbedrock.StopReasonEndTurn)},
	}

	buf := bytes.NewBuffer(nil)
	encoder := eventstream.NewEncoder()
	for _, event := range inputEvents {
		payload, err := json.Marshal(event)
		require.NoError(t, err)
		err = encoder.Encode(buf, eventstream.Message{
			Headers: eventstream.Headers{
				{Name: ":event-type", Value: eventstream.StringValue("chunk")},
			},
			Payload: payload,
		})
		require.NoError(t, err)
	}

	// Override json.Marshal to force a failure during serializeOpenAIChatCompletionChunk.
	orig := json.Marshal
	t.Cleanup(func() { json.Marshal = orig })
	json.Marshal = func(v interface{}) ([]byte, error) {
		// Allow the eventstream decoding (internal json.Unmarshal) to succeed
		// by only failing on ChatCompletionResponseChunk marshaling.
		if _, ok := v.(*openai.ChatCompletionResponseChunk); ok {
			return nil, fmt.Errorf("injected marshal error")
		}
		return orig(v)
	}

	o := &openAIToAWSBedrockTranslatorV1ChatCompletion{stream: true, requestModel: "test-model"}
	_, _, _, _, err := o.ResponseBody(nil, buf, true, nil)
	require.Error(t, err)
	require.ErrorContains(t, err, "failed to marshal streaming event")
	require.ErrorContains(t, err, "injected marshal error")
}
