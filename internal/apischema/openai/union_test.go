// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package openai

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/envoyproxy/ai-gateway/internal/json"
)

// TestUnmarshalJSONNestedUnion tests the completion API prompt parsing.
// This function only supports: string, []string, []int64, [][]int64
func TestUnmarshalJSONNestedUnion(t *testing.T) {
	additionalSuccessCases := []struct {
		name     string
		data     []byte
		expected interface{}
	}{
		{
			name:     "string with escaped path", // Tests json.Unmarshal fallback when strconv.Unquote fails
			data:     []byte(`"/path\/to\/file"`),
			expected: "/path/to/file",
		},
		{
			name:     "truncated array defaults to string array",
			data:     []byte(`[]`),
			expected: []string{},
		},
		{
			name:     "array with whitespace before close bracket",
			data:     []byte(`[  ]`),
			expected: []string{},
		},
		{
			name:     "negative number in array",
			data:     []byte(`[-1, -2, -3]`),
			expected: []int64{-1, -2, -3},
		},
		{
			name:     "array with leading whitespace",
			data:     []byte(`[ "test"]`),
			expected: []string{"test"},
		},
		{
			name:     "data with leading whitespace",
			data:     []byte(`  "test"`),
			expected: "test",
		},
		{
			name:     "data with all whitespace types",
			data:     []byte(" \t\n\r\"test\""),
			expected: "test",
		},
		{
			name:     "array of token arrays",
			data:     []byte(`[[-1, -2, -3], [1, 2, 3]]`),
			expected: [][]int64{{-1, -2, -3}, {1, 2, 3}},
		},
		{
			name:     "array of strings",
			data:     []byte(`[ "aa", "bb", "cc" ]`),
			expected: []string{"aa", "bb", "cc"},
		},
	}

	allCases := append(promptUnionBenchmarkCases, additionalSuccessCases...) //nolint:gocritic // intentionally creating new slice
	for _, tc := range allCases {
		t.Run(tc.name, func(t *testing.T) {
			val, err := unmarshalJSONNestedUnion("prompt", tc.data)
			require.NoError(t, err)
			require.Equal(t, tc.expected, val)
		})
	}
}

func TestUnmarshalJSONNestedUnion_Errors(t *testing.T) {
	errorTestCases := []struct {
		name        string
		data        []byte
		expectedErr string
	}{
		{
			name:        "truncated data",
			data:        []byte{},
			expectedErr: "truncated prompt data",
		},
		{
			name:        "only whitespace",
			data:        []byte("   \t\n\r   "),
			expectedErr: "truncated prompt data",
		},
		{
			name:        "invalid JSON string",
			data:        []byte(`"unterminated`),
			expectedErr: "cannot unmarshal prompt as string",
		},
		{
			name:        "truncated data",
			data:        []byte(`[`),
			expectedErr: "truncated prompt data",
		},
		{
			name:        "invalid array element",
			data:        []byte(`[null]`),
			expectedErr: "invalid prompt array element",
		},
		{
			name:        "invalid array element - object",
			data:        []byte(`[{}]`),
			expectedErr: "invalid prompt array element",
		},
		{
			name:        "invalid string array",
			data:        []byte(`["test", 123]`),
			expectedErr: "cannot unmarshal prompt as []string",
		},
		{
			name:        "invalid int array",
			data:        []byte(`[1, "two", 3]`),
			expectedErr: "cannot unmarshal prompt as []int64",
		},
		{
			name:        "invalid nested int array",
			data:        []byte(`[[1, 2], ["three", 4]]`),
			expectedErr: "cannot unmarshal prompt as [][]int64",
		},
		{
			name:        "invalid type - object (objects not supported for completion prompts)",
			data:        []byte(`{"key": "value"}`),
			expectedErr: "invalid prompt type (must be string or array)",
		},
		{
			name:        "invalid type - null",
			data:        []byte(`null`),
			expectedErr: "invalid prompt type (must be string or array)",
		},
		{
			name:        "invalid type - boolean",
			data:        []byte(`true`),
			expectedErr: "invalid prompt type (must be string or array)",
		},
		{
			name:        "invalid type - bare number",
			data:        []byte(`42`),
			expectedErr: "invalid prompt type (must be string or array)",
		},
		{
			name:        "array with only whitespace after bracket",
			data:        []byte(`[   `),
			expectedErr: "truncated prompt data",
		},
	}

	for _, tc := range errorTestCases {
		t.Run(tc.name, func(t *testing.T) {
			val, err := unmarshalJSONNestedUnion("prompt", tc.data)
			require.Error(t, err)
			require.Contains(t, err.Error(), tc.expectedErr)
			require.Zero(t, val)
		})
	}
}

// TestUnmarshalJSONEmbeddingInput tests the embedding API input parsing.
// This function supports: string, []string, EmbeddingInputItem, []EmbeddingInputItem, []int64, [][]int64
func TestUnmarshalJSONEmbeddingInput(t *testing.T) {
	successCases := []struct {
		name     string
		data     []byte
		expected interface{}
	}{
		{
			name:     "simple string",
			data:     []byte(`"hello world"`),
			expected: "hello world",
		},
		{
			name:     "array of strings",
			data:     []byte(`["aa", "bb", "cc"]`),
			expected: []string{"aa", "bb", "cc"},
		},
		{
			name:     "empty array",
			data:     []byte(`[]`),
			expected: []string{},
		},
		{
			name:     "array of tokens",
			data:     []byte(`[1, 2, 3]`),
			expected: []int64{1, 2, 3},
		},
		{
			name:     "array of token arrays",
			data:     []byte(`[[1, 2], [3, 4]]`),
			expected: [][]int64{{1, 2}, {3, 4}},
		},
		{
			name: "array of EmbeddingInputItem objects",
			data: []byte(`[{"content":"hello"},{"content":"world","task_type":"RETRIEVAL_QUERY"}]`),
			expected: []EmbeddingInputItem{
				{Content: EmbeddingContent{Value: "hello"}},
				{Content: EmbeddingContent{Value: "world"}, TaskType: "RETRIEVAL_QUERY"},
			},
		},
		{
			name: "single EmbeddingInputItem object with string content",
			data: []byte(`{"content":"test content","task_type":"RETRIEVAL_DOCUMENT","title":"Test"}`),
			expected: EmbeddingInputItem{
				Content:  EmbeddingContent{Value: "test content"},
				TaskType: "RETRIEVAL_DOCUMENT",
				Title:    "Test",
			},
		},
		{
			name: "single EmbeddingInputItem object with array content",
			data: []byte(`{"content":["text1","text2"],"task_type":"RETRIEVAL_QUERY"}`),
			expected: EmbeddingInputItem{
				Content:  EmbeddingContent{Value: []string{"text1", "text2"}},
				TaskType: "RETRIEVAL_QUERY",
			},
		},
	}

	for _, tc := range successCases {
		t.Run(tc.name, func(t *testing.T) {
			val, err := unmarshalJSONEmbeddingInput("input", tc.data)
			require.NoError(t, err)
			require.Equal(t, tc.expected, val)
		})
	}
}

func TestUnmarshalJSONEmbeddingInput_Errors(t *testing.T) {
	errorTestCases := []struct {
		name        string
		data        []byte
		expectedErr string
	}{
		{
			name:        "truncated data",
			data:        []byte{},
			expectedErr: "truncated input data",
		},
		{
			name:        "object without content field",
			data:        []byte(`{"task_type":"RETRIEVAL_QUERY"}`),
			expectedErr: "invalid input type",
		},
		{
			name:        "object with empty content",
			data:        []byte(`{"content":""}`),
			expectedErr: "invalid input type",
		},
		{
			name:        "object with empty array content",
			data:        []byte(`{"content":[]}`),
			expectedErr: "invalid input type",
		},
		{
			name:        "array of objects with empty content",
			data:        []byte(`[{"content":"valid"},{"content":""}]`),
			expectedErr: "invalid input array element",
		},
		{
			name:        "invalid type - null",
			data:        []byte(`null`),
			expectedErr: "invalid input type (must be string, object, or array)",
		},
		{
			name:        "invalid type - boolean",
			data:        []byte(`true`),
			expectedErr: "invalid input type (must be string, object, or array)",
		},
	}

	for _, tc := range errorTestCases {
		t.Run(tc.name, func(t *testing.T) {
			val, err := unmarshalJSONEmbeddingInput("input", tc.data)
			require.Error(t, err)
			require.Contains(t, err.Error(), tc.expectedErr)
			require.Zero(t, val)
		})
	}
}

func TestThinkingUnion_UnmarshalJSON(t *testing.T) {
	tests := []struct {
		name   string
		data   string
		expect ThinkingUnion
	}{
		{
			name: "enabled",
			data: `{"type":"enabled","budget_tokens":1024}`,
			expect: ThinkingUnion{
				OfEnabled: &ThinkingEnabled{Type: "enabled", BudgetTokens: 1024},
			},
		},
		{
			name: "disabled",
			data: `{"type":"disabled"}`,
			expect: ThinkingUnion{
				OfDisabled: &ThinkingDisabled{Type: "disabled"},
			},
		},
		{
			name: "adaptive",
			data: `{"type":"adaptive"}`,
			expect: ThinkingUnion{
				OfAdaptive: &ThinkingAdaptive{Type: "adaptive"},
			},
		},
		{
			name: "enabled with display",
			data: `{"type":"enabled","budget_tokens":1024,"display":"omitted"}`,
			expect: ThinkingUnion{
				OfEnabled: &ThinkingEnabled{Type: "enabled", BudgetTokens: 1024, Display: "omitted"},
			},
		},
		{
			name: "adaptive with display",
			data: `{"type":"adaptive","display":"summarized"}`,
			expect: ThinkingUnion{
				OfAdaptive: &ThinkingAdaptive{Type: "adaptive", Display: "summarized"},
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var got ThinkingUnion
			err := json.Unmarshal([]byte(tc.data), &got)
			require.NoError(t, err)
			require.Equal(t, tc.expect, got)
		})
	}
}

func TestThinkingUnion_UnmarshalJSON_Errors(t *testing.T) {
	tests := []struct {
		name        string
		data        string
		expectedErr string
	}{
		{
			name:        "missing type field",
			data:        `{"budget_tokens":1024}`,
			expectedErr: "thinking config does not have a type",
		},
		{
			name:        "invalid type value",
			data:        `{"type":"unknown"}`,
			expectedErr: "invalid thinking union type: unknown",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var got ThinkingUnion
			err := json.Unmarshal([]byte(tc.data), &got)
			require.Error(t, err)
			require.Contains(t, err.Error(), tc.expectedErr)
		})
	}
}

func TestResponseToolUnion_Namespace_UnmarshalJSON(t *testing.T) {
	tests := []struct {
		name   string
		data   string
		expect ResponseToolUnion
	}{
		{
			name: "namespace with function tools",
			data: `{"type":"namespace","name":"crm","description":"CRM tools","tools":[{"type":"function","name":"get_contact","description":"Fetch a contact","parameters":{"type":"object"}}]}`,
			expect: ResponseToolUnion{
				OfNamespace: &NamespaceToolParam{
					Type:        "namespace",
					Name:        "crm",
					Description: "CRM tools",
					Tools: []NamespaceToolToolUnionParam{
						{OfFunction: &NamespaceToolToolFunctionParam{
							Type:        "function",
							Name:        "get_contact",
							Description: "Fetch a contact",
							Parameters:  map[string]any{"type": "object"},
						}},
					},
				},
			},
		},
		{
			name: "namespace with custom tool",
			data: `{"type":"namespace","name":"myns","description":"desc","tools":[{"type":"custom","name":"my_tool","description":"a custom tool"}]}`,
			expect: ResponseToolUnion{
				OfNamespace: &NamespaceToolParam{
					Type:        "namespace",
					Name:        "myns",
					Description: "desc",
					Tools: []NamespaceToolToolUnionParam{
						{OfCustom: &CustomToolParam{
							Type:        "custom",
							Name:        "my_tool",
							Description: "a custom tool",
						}},
					},
				},
			},
		},
		{
			name: "namespace tool without type field defaults to function",
			data: `{"type":"namespace","name":"ns","description":"d","tools":[{"name":"implicit_fn"}]}`,
			expect: ResponseToolUnion{
				OfNamespace: &NamespaceToolParam{
					Type:        "namespace",
					Name:        "ns",
					Description: "d",
					Tools: []NamespaceToolToolUnionParam{
						{OfFunction: &NamespaceToolToolFunctionParam{Name: "implicit_fn"}},
					},
				},
			},
		},
		{
			name: "namespace with defer_loading function",
			data: `{"type":"namespace","name":"ns","description":"d","tools":[{"type":"function","name":"deferred","defer_loading":true}]}`,
			expect: ResponseToolUnion{
				OfNamespace: &NamespaceToolParam{
					Type:        "namespace",
					Name:        "ns",
					Description: "d",
					Tools: []NamespaceToolToolUnionParam{
						{OfFunction: &NamespaceToolToolFunctionParam{
							Type:         "function",
							Name:         "deferred",
							DeferLoading: func() *bool { b := true; return &b }(),
						}},
					},
				},
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var got ResponseToolUnion
			err := json.Unmarshal([]byte(tc.data), &got)
			require.NoError(t, err)
			require.Equal(t, tc.expect, got)
		})
	}
}

func TestResponseToolUnion_Namespace_MarshalJSON(t *testing.T) {
	tests := []struct {
		name   string
		input  ResponseToolUnion
		expect string
	}{
		{
			name: "namespace with function tool",
			input: ResponseToolUnion{
				OfNamespace: &NamespaceToolParam{
					Type:        "namespace",
					Name:        "crm",
					Description: "CRM tools",
					Tools: []NamespaceToolToolUnionParam{
						{OfFunction: &NamespaceToolToolFunctionParam{
							Type:        "function",
							Name:        "get_contact",
							Description: "Fetch a contact",
						}},
					},
				},
			},
			expect: `{"type":"namespace","name":"crm","description":"CRM tools","tools":[{"name":"get_contact","type":"function","description":"Fetch a contact"}]}`,
		},
		{
			name: "namespace with custom tool",
			input: ResponseToolUnion{
				OfNamespace: &NamespaceToolParam{
					Type:        "namespace",
					Name:        "myns",
					Description: "desc",
					Tools: []NamespaceToolToolUnionParam{
						{OfCustom: &CustomToolParam{
							Type:        "custom",
							Name:        "my_tool",
							Description: "a custom tool",
						}},
					},
				},
			},
			expect: `{"type":"namespace","name":"myns","description":"desc","tools":[{"type":"custom","name":"my_tool","description":"a custom tool"}]}`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := json.Marshal(tc.input)
			require.NoError(t, err)
			require.JSONEq(t, tc.expect, string(got))
		})
	}
}

func TestResponseToolUnion_ToolSearch_UnmarshalJSON(t *testing.T) {
	tests := []struct {
		name   string
		data   string
		expect ResponseToolUnion
	}{
		{
			name: "tool_search server execution",
			data: `{"type":"tool_search","execution":"server"}`,
			expect: ResponseToolUnion{
				OfToolSearch: &ToolSearchToolParam{
					Type:      "tool_search",
					Execution: "server",
				},
			},
		},
		{
			name: "tool_search client execution with description and parameters",
			data: `{"type":"tool_search","execution":"client","description":"search tools","parameters":{"type":"object"}}`,
			expect: ResponseToolUnion{
				OfToolSearch: &ToolSearchToolParam{
					Type:        "tool_search",
					Execution:   "client",
					Description: "search tools",
					Parameters:  map[string]any{"type": "object"},
				},
			},
		},
		{
			name: "tool_search minimal",
			data: `{"type":"tool_search"}`,
			expect: ResponseToolUnion{
				OfToolSearch: &ToolSearchToolParam{
					Type: "tool_search",
				},
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var got ResponseToolUnion
			err := json.Unmarshal([]byte(tc.data), &got)
			require.NoError(t, err)
			require.Equal(t, tc.expect, got)
		})
	}
}

func TestResponseToolUnion_ToolSearch_MarshalJSON(t *testing.T) {
	tests := []struct {
		name   string
		input  ResponseToolUnion
		expect string
	}{
		{
			name: "tool_search server",
			input: ResponseToolUnion{
				OfToolSearch: &ToolSearchToolParam{
					Type:      "tool_search",
					Execution: "server",
				},
			},
			expect: `{"type":"tool_search","execution":"server","parameters":null}`,
		},
		{
			name: "tool_search client with description",
			input: ResponseToolUnion{
				OfToolSearch: &ToolSearchToolParam{
					Type:        "tool_search",
					Execution:   "client",
					Description: "search tools",
				},
			},
			expect: `{"type":"tool_search","execution":"client","description":"search tools","parameters":null}`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := json.Marshal(tc.input)
			require.NoError(t, err)
			require.JSONEq(t, tc.expect, string(got))
		})
	}
}

func TestResponseRequest_UnmarshalJSON_UnknownToolType(t *testing.T) {
	const body = `{"model":"gpt-5","input":"hi","tools":[` +
		`{"type":"function","name":"get_weather","parameters":{},"strict":true},` +
		`{"type":"some_future_tool","execution":"client"}` +
		`]}`

	var req ResponseRequest
	require.NoError(t, json.Unmarshal([]byte(body), &req))
	require.Len(t, req.Tools, 2)

	require.NotNil(t, req.Tools[0].OfFunction)
	require.Equal(t, "get_weather", req.Tools[0].OfFunction.Name)

	require.Nil(t, req.Tools[1].OfFunction)
	require.JSONEq(t, `{"type":"some_future_tool","execution":"client"}`, string(req.Tools[1].OfUnknown))
}

func TestResponseToolUnion_UnmarshalJSON_UnknownType(t *testing.T) {
	const raw = `{"type":"unknown_tool","execution":"client"}`
	var got ResponseToolUnion
	require.NoError(t, json.Unmarshal([]byte(raw), &got))
	require.Equal(t, json.RawMessage(raw), got.OfUnknown)

	out, err := json.Marshal(got)
	require.NoError(t, err)
	require.JSONEq(t, raw, string(out))
}

func TestThinkingUnion_MarshalJSON(t *testing.T) {
	tests := []struct {
		name   string
		input  ThinkingUnion
		expect string
	}{
		{
			name: "enabled",
			input: ThinkingUnion{
				OfEnabled: &ThinkingEnabled{Type: "enabled", BudgetTokens: 1024},
			},
			expect: `{"budget_tokens":1024,"type":"enabled"}`,
		},
		{
			name: "disabled",
			input: ThinkingUnion{
				OfDisabled: &ThinkingDisabled{Type: "disabled"},
			},
			expect: `{"type":"disabled"}`,
		},
		{
			name: "adaptive",
			input: ThinkingUnion{
				OfAdaptive: &ThinkingAdaptive{Type: "adaptive"},
			},
			expect: `{"type":"adaptive"}`,
		},
		{
			name: "enabled with display",
			input: ThinkingUnion{
				OfEnabled: &ThinkingEnabled{Type: "enabled", BudgetTokens: 1024, Display: "omitted"},
			},
			expect: `{"budget_tokens":1024,"type":"enabled","display":"omitted"}`,
		},
		{
			name: "adaptive with display",
			input: ThinkingUnion{
				OfAdaptive: &ThinkingAdaptive{Type: "adaptive", Display: "summarized"},
			},
			expect: `{"type":"adaptive","display":"summarized"}`,
		},
		{
			name:   "all nil returns empty object",
			input:  ThinkingUnion{},
			expect: `{}`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := json.Marshal(&tc.input)
			require.NoError(t, err)
			require.JSONEq(t, tc.expect, string(got))
		})
	}
}

func TestStreamReasoningContent_MarshalJSON(t *testing.T) {
	// reasoning_content on streaming chunks must serialize as a plain string;
	// Signature and RedactedContent are carried via delta.thinking_blocks instead.
	got, err := json.Marshal(&StreamReasoningContent{
		Text: "thinking...", Signature: "c2ln", RedactedContent: []byte("x"),
	})
	require.NoError(t, err)
	require.Equal(t, `"thinking..."`, string(got))
}

func TestStreamReasoningContent_UnmarshalJSON(t *testing.T) {
	tests := []struct {
		name   string
		data   string
		expect StreamReasoningContent
	}{
		{
			name:   "plain string (OpenAI-compatible backends)",
			data:   `"thinking..."`,
			expect: StreamReasoningContent{Text: "thinking..."},
		},
		{
			name:   "legacy object form",
			data:   `{"text":"thinking...","signature":"c2ln"}`,
			expect: StreamReasoningContent{Text: "thinking...", Signature: "c2ln"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var got StreamReasoningContent
			require.NoError(t, json.Unmarshal([]byte(tc.data), &got))
			require.Equal(t, tc.expect, got)
		})
	}
}
