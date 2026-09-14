// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package openai

import (
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/packages/param"
	"github.com/stretchr/testify/require"
	"google.golang.org/genai"
	"k8s.io/utils/ptr"

	"github.com/envoyproxy/ai-gateway/internal/json"
)

func TestOpenAIChatCompletionContentPartUserUnionParamUnmarshal(t *testing.T) {
	for _, tc := range []struct {
		name   string
		in     []byte
		out    *ChatCompletionContentPartUserUnionParam
		expErr string
	}{
		{
			name: "text",
			in: []byte(`{
"type": "text",
"text": "what do you see in this image"
}`),
			out: &ChatCompletionContentPartUserUnionParam{
				OfText: &ChatCompletionContentPartTextParam{
					Type: string(ChatCompletionContentPartTextTypeText),
					Text: "what do you see in this image",
				},
			},
		},
		{
			name: "image url",
			in: []byte(`{
"type": "image_url",
"image_url": {"url": "https://example.com/image.jpg"}
}`),
			out: &ChatCompletionContentPartUserUnionParam{
				OfImageURL: &ChatCompletionContentPartImageParam{
					Type: ChatCompletionContentPartImageTypeImageURL,
					ImageURL: ChatCompletionContentPartImageImageURLParam{
						URL: "https://example.com/image.jpg",
					},
				},
			},
		},
		{
			name: "audio url",
			in: []byte(`{
"type": "audio_url",
"audio_url": {"url": "https://example.com/audio.mp3"}
}`),
			out: &ChatCompletionContentPartUserUnionParam{
				OfAudioURL: &ChatCompletionContentPartAudioParam{
					Type: ChatCompletionContentPartAudioTypeAudioURL,
					AudioURL: ChatCompletionContentPartAudioAudioURLParam{
						URL: "https://example.com/audio.mp3",
					},
				},
			},
		},
		{
			name: "video url",
			in: []byte(`{
"type": "video_url",
"video_url": {"url": "https://example.com/video.mp4"}
}`),
			out: &ChatCompletionContentPartUserUnionParam{
				OfVideoURL: &ChatCompletionContentPartVideoParam{
					Type: ChatCompletionContentPartVideoTypeVideoURL,
					VideoURL: ChatCompletionContentPartVideoVideoURLParam{
						URL: "https://example.com/video.mp4",
					},
				},
			},
		},
		{
			name: "input audio",
			in: []byte(`{
"type": "input_audio",
"input_audio": {"data": "somebinarydata"}
}`),
			out: &ChatCompletionContentPartUserUnionParam{
				OfInputAudio: &ChatCompletionContentPartInputAudioParam{
					Type: ChatCompletionContentPartInputAudioTypeInputAudio,
					InputAudio: ChatCompletionContentPartInputAudioInputAudioParam{
						Data: "somebinarydata",
					},
				},
			},
		},
		{
			name:   "type not exist",
			in:     []byte(`{}`),
			expErr: "chat content does not have type",
		},
		{
			name: "unknown type",
			in: []byte(`{
"type": "unknown"
}`),
			expErr: "unknown ChatCompletionContentPartUnionParam type: unknown",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var contentPart ChatCompletionContentPartUserUnionParam
			err := json.Unmarshal(tc.in, &contentPart)
			if tc.expErr != "" {
				require.ErrorContains(t, err, tc.expErr)
				return
			}
			require.NoError(t, err)
			if !cmp.Equal(&contentPart, tc.out) {
				t.Errorf("UnmarshalOpenAIRequest(), diff(got, expected) = %s\n", cmp.Diff(&contentPart, tc.out))
			}
		})
	}
}

func TestOpenAIChatCompletionResponseFormatUnionUnmarshal(t *testing.T) {
	for _, tc := range []struct {
		name   string
		in     []byte
		out    *ChatCompletionResponseFormatUnion
		expErr string
	}{
		{
			name: "text",
			in:   []byte(`{"type": "text"}`),
			out: &ChatCompletionResponseFormatUnion{
				OfText: &ChatCompletionResponseFormatTextParam{
					Type: ChatCompletionResponseFormatTypeText,
				},
			},
		},
		{
			name: "json schema",
			in:   []byte(`{"json_schema": { "name": "math_response", "schema": { "type": "object", "properties": { "step": {"type": "string"} }, "required": [ "steps"], "additionalProperties": false }, "strict": true }, "type":"json_schema"}`),
			out: &ChatCompletionResponseFormatUnion{
				OfJSONSchema: &ChatCompletionResponseFormatJSONSchema{
					Type: "json_schema",
					JSONSchema: ChatCompletionResponseFormatJSONSchemaJSONSchema{
						Name:   "math_response",
						Strict: true,
						Schema: json.RawMessage(`{ "type": "object", "properties": { "step": {"type": "string"} }, "required": [ "steps"], "additionalProperties": false }`),
					},
				},
			},
		},
		{
			name: "json object",
			in:   []byte(`{"type": "json_object"}`),
			out: &ChatCompletionResponseFormatUnion{
				OfJSONObject: &ChatCompletionResponseFormatJSONObjectParam{
					Type: "json_object",
				},
			},
		},
		{
			name:   "type not exist",
			in:     []byte(`{}`),
			expErr: "response format does not have type",
		},
		{
			name: "unknown type",
			in: []byte(`{
"type": "unknown"
}`),
			expErr: "unsupported ChatCompletionResponseFormatType",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var contentPart ChatCompletionResponseFormatUnion
			err := json.Unmarshal(tc.in, &contentPart)
			if tc.expErr != "" {
				require.ErrorContains(t, err, tc.expErr)
				return
			}
			require.NoError(t, err)
			if !cmp.Equal(&contentPart, tc.out) {
				t.Errorf("UnmarshalOpenAIRequest(), diff(got, expected) = %s\n", cmp.Diff(&contentPart, tc.out))
			}
		})
	}
}

func TestOpenAIChatCompletionMessageUnmarshal(t *testing.T) {
	for _, tc := range []struct {
		name   string
		in     []byte
		out    *ChatCompletionRequest
		expErr string
	}{
		{
			name: "basic test",
			in: []byte(`{"model": "gpu-o4",
                        "messages": [
                         {"role": "system", "content": "you are a helpful assistant"},
                         {"role": "developer", "content": "you are a helpful dev assistant"},
                         {"role": "user", "content": "what do you see in this image"},
                         {"role": "tool", "content": "some tool", "tool_call_id": "123"},
			                   {"role": "assistant", "content": "you are a helpful assistant"}
                    ]}
`),
			out: &ChatCompletionRequest{
				Model: "gpu-o4",
				Messages: []ChatCompletionMessageParamUnion{
					{
						OfSystem: &ChatCompletionSystemMessageParam{
							Role: ChatMessageRoleSystem,
							Content: ContentUnion{
								Value: "you are a helpful assistant",
							},
						},
					},
					{
						OfDeveloper: &ChatCompletionDeveloperMessageParam{
							Role: ChatMessageRoleDeveloper,
							Content: ContentUnion{
								Value: "you are a helpful dev assistant",
							},
						},
					},
					{
						OfUser: &ChatCompletionUserMessageParam{
							Role: ChatMessageRoleUser,
							Content: StringOrUserRoleContentUnion{
								Value: "what do you see in this image",
							},
						},
					},
					{
						OfTool: &ChatCompletionToolMessageParam{
							Role:       ChatMessageRoleTool,
							ToolCallID: "123",
							Content:    ContentUnion{Value: "some tool"},
						},
					},
					{
						OfAssistant: &ChatCompletionAssistantMessageParam{
							Role:    ChatMessageRoleAssistant,
							Content: StringOrAssistantRoleContentUnion{Value: "you are a helpful assistant"},
						},
					},
				},
			},
		},
		{
			name: "assistant message string",
			in: []byte(`{"model": "gpu-o4",
                        "messages": [
                         {"role": "assistant", "content": "you are a helpful assistant"},
			                   {"role": "assistant", "content": [{"text": "you are a helpful assistant content", "type": "text"}]}
                    ]}
`),
			out: &ChatCompletionRequest{
				Model: "gpu-o4",
				Messages: []ChatCompletionMessageParamUnion{
					{
						OfAssistant: &ChatCompletionAssistantMessageParam{
							Role:    ChatMessageRoleAssistant,
							Content: StringOrAssistantRoleContentUnion{Value: "you are a helpful assistant"},
						},
					},
					{
						OfAssistant: &ChatCompletionAssistantMessageParam{
							Role: ChatMessageRoleAssistant,
							Content: StringOrAssistantRoleContentUnion{Value: []ChatCompletionAssistantMessageParamContent{
								{Text: ptr.To("you are a helpful assistant content"), Type: "text"},
							}},
						},
					},
				},
			},
		},
		{
			name: "content with array",
			in: []byte(`{"model": "gpu-o4",
                        "messages": [
                         {"role": "system", "content": [{"text": "you are a helpful assistant", "type": "text"}]},
                         {"role": "developer", "content": [{"text": "you are a helpful dev assistant", "type": "text"}]},
                         {"role": "user", "content": [{"text": "what do you see in this image", "type": "text"}]}]}`),
			out: &ChatCompletionRequest{
				Model: "gpu-o4",
				Messages: []ChatCompletionMessageParamUnion{
					{
						OfSystem: &ChatCompletionSystemMessageParam{
							Role: ChatMessageRoleSystem,
							Content: ContentUnion{
								Value: []ChatCompletionContentPartTextParam{
									{
										Text: "you are a helpful assistant",
										Type: "text",
									},
								},
							},
						},
					},
					{
						OfDeveloper: &ChatCompletionDeveloperMessageParam{
							Role: ChatMessageRoleDeveloper,
							Content: ContentUnion{
								Value: []ChatCompletionContentPartTextParam{
									{
										Text: "you are a helpful dev assistant",
										Type: "text",
									},
								},
							},
						},
					},
					{
						OfUser: &ChatCompletionUserMessageParam{
							Role: ChatMessageRoleUser,
							Content: StringOrUserRoleContentUnion{
								Value: []ChatCompletionContentPartUserUnionParam{
									{
										OfText: &ChatCompletionContentPartTextParam{Text: "what do you see in this image", Type: "text"},
									},
								},
							},
						},
					},
				},
			},
		},
		{
			name:   "no role",
			in:     []byte(`{"model": "gpu-o4","messages": [{}]}`),
			expErr: "chat message does not have role",
		},
		{
			name: "unknown role",
			in: []byte(`{"model": "gpu-o4",
                        "messages": [{"role": "some-funky", "content": [{"text": "what do you see in this image", "type": "text"}]}]}`),
			expErr: "unknown ChatCompletionMessageParam type: some-funky",
		},
		{
			name: "response_format",
			in:   []byte(`{ "model": "azure.gpt-4o", "messages": [ { "role": "user", "content": "Tell me a story" } ], "response_format": { "type": "json_schema", "json_schema": { "name": "math_response", "schema": { "type": "object", "properties": { "step": {"type": "string"} }, "required": [ "steps"], "additionalProperties": false }, "strict": true } } }`),
			out: &ChatCompletionRequest{
				Model: "azure.gpt-4o",
				Messages: []ChatCompletionMessageParamUnion{
					{
						OfUser: &ChatCompletionUserMessageParam{
							Role: ChatMessageRoleUser,
							Content: StringOrUserRoleContentUnion{
								Value: "Tell me a story",
							},
						},
					},
				},
				ResponseFormat: &ChatCompletionResponseFormatUnion{
					OfJSONSchema: &ChatCompletionResponseFormatJSONSchema{
						Type: "json_schema",
						JSONSchema: ChatCompletionResponseFormatJSONSchemaJSONSchema{
							Name:   "math_response",
							Strict: true,
							Schema: json.RawMessage(`{ "type": "object", "properties": { "step": {"type": "string"} }, "required": [ "steps"], "additionalProperties": false }`),
						},
					},
				},
			},
		},
		{
			name: "test fields",
			in: []byte(`{
				"model": "gpu-o4",
				"messages": [{"role": "user", "content": "hello"}],
				"max_completion_tokens": 1024,
				"parallel_tool_calls": true,
				"stop": ["\n", "stop"],
				"service_tier": "flex",
                "verbosity": "low",
                "reasoning_effort": "low"
			}`),
			out: &ChatCompletionRequest{
				Model: "gpu-o4",
				Messages: []ChatCompletionMessageParamUnion{
					{
						OfUser: &ChatCompletionUserMessageParam{
							Role:    ChatMessageRoleUser,
							Content: StringOrUserRoleContentUnion{Value: "hello"},
						},
					},
				},
				MaxCompletionTokens: ptr.To[int64](1024),
				ParallelToolCalls:   ptr.To(true),
				Stop: openai.ChatCompletionNewParamsStopUnion{
					OfStringArray: []string{"\n", "stop"},
				},
				ServiceTier:     "flex",
				Verbosity:       openai.ChatCompletionNewParamsVerbosityLow,
				ReasoningEffort: openai.ReasoningEffortLow,
			},
		},
		{
			name: "stop as string",
			in: []byte(`{
				"model": "gpu-o4",
				"messages": [{"role": "user", "content": "hello"}],
				"stop": "stop"
			}`),
			out: &ChatCompletionRequest{
				Model: "gpu-o4",
				Messages: []ChatCompletionMessageParamUnion{
					{
						OfUser: &ChatCompletionUserMessageParam{
							Role:    ChatMessageRoleUser,
							Content: StringOrUserRoleContentUnion{Value: "hello"},
						},
					},
				},
				Stop: openai.ChatCompletionNewParamsStopUnion{
					OfString: openai.Opt[string]("stop"),
				},
			},
		},
		{
			name: "stop as array",
			in: []byte(`{
				"model": "gpu-o4",
				"messages": [{"role": "user", "content": "hello"}],
				"stop": ["</s>", "__end_tag__", "<|eot_id|>", "[answer_end]"]
			}`),
			out: &ChatCompletionRequest{
				Model: "gpu-o4",
				Messages: []ChatCompletionMessageParamUnion{
					{
						OfUser: &ChatCompletionUserMessageParam{
							Role:    ChatMessageRoleUser,
							Content: StringOrUserRoleContentUnion{Value: "hello"},
						},
					},
				},
				Stop: openai.ChatCompletionNewParamsStopUnion{
					OfStringArray: []string{"</s>", "__end_tag__", "<|eot_id|>", "[answer_end]"},
				},
			},
		},
		{
			name: "web search options",
			in: []byte(`{
				"model": "gpt-4o-mini-search-preview",
				"messages": [{"role": "user", "content": "What's the latest news?"}],
				"web_search_options": {"search_context_size": "low"}
			}`),
			out: &ChatCompletionRequest{
				Model: "gpt-4o-mini-search-preview",
				Messages: []ChatCompletionMessageParamUnion{
					{
						OfUser: &ChatCompletionUserMessageParam{
							Role:    ChatMessageRoleUser,
							Content: StringOrUserRoleContentUnion{Value: "What's the latest news?"},
						},
					},
				},
				WebSearchOptions: &WebSearchOptions{
					SearchContextSize: WebSearchContextSizeLow,
				},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var chatCompletion ChatCompletionRequest
			err := json.Unmarshal(tc.in, &chatCompletion)
			if tc.expErr != "" {
				require.ErrorContains(t, err, tc.expErr)
				return
			}
			require.NoError(t, err)
			if !cmp.Equal(&chatCompletion, tc.out,
				cmpopts.IgnoreUnexported(openai.ChatCompletionNewParamsStopUnion{}, param.Opt[string]{})) {
				t.Errorf("UnmarshalOpenAIRequest(), diff(got, expected) = %s\n", cmp.Diff(&chatCompletion, tc.out,
					cmpopts.IgnoreUnexported(openai.ChatCompletionNewParamsStopUnion{}, param.Opt[string]{})))
			}
		})
	}
}

func TestModelListMarshal(t *testing.T) {
	var (
		model = Model{
			ID:      "gpt-3.5-turbo",
			Object:  "model",
			OwnedBy: "tetrate",
			Created: JSONUNIXTime(time.Date(2025, 0o1, 0o1, 0, 0, 0, 0, time.UTC)),
		}
		list = ModelList{Object: "list", Data: []Model{model}}
		raw  = `{"object":"list","data":[{"id":"gpt-3.5-turbo","object":"model","owned_by":"tetrate","created":1735689600}]}`
	)

	b, err := json.Marshal(list)
	require.NoError(t, err)
	require.JSONEq(t, raw, string(b))

	var out ModelList
	require.NoError(t, json.Unmarshal([]byte(raw), &out))
	require.Len(t, out.Data, 1)
	require.Equal(t, "list", out.Object)
	require.Equal(t, model.ID, out.Data[0].ID)
	require.Equal(t, model.Object, out.Data[0].Object)
	require.Equal(t, model.OwnedBy, out.Data[0].OwnedBy)
	// Unmarshalling initializes other fields in time.Time we're not interested with. Just compare the actual time.
	require.Equal(t, time.Time(model.Created).Unix(), time.Time(out.Data[0].Created).Unix())
}

func TestChatCompletionMessageParamUnionMarshal(t *testing.T) {
	testCases := []struct {
		name     string
		input    ChatCompletionMessageParamUnion
		expected string
	}{
		{
			name: "user message",
			input: ChatCompletionMessageParamUnion{
				OfUser: &ChatCompletionUserMessageParam{
					Role: ChatMessageRoleUser,
					Content: StringOrUserRoleContentUnion{
						Value: "Hello!",
					},
				},
			},
			expected: `{"content":"Hello!","role":"user"}`,
		},
		{
			name: "system message",
			input: ChatCompletionMessageParamUnion{
				OfSystem: &ChatCompletionSystemMessageParam{
					Role: ChatMessageRoleSystem,
					Content: ContentUnion{
						Value: "You are a helpful assistant",
					},
				},
			},
			expected: `{"content":"You are a helpful assistant","role":"system"}`,
		},
		{
			name: "assistant message",
			input: ChatCompletionMessageParamUnion{
				OfAssistant: &ChatCompletionAssistantMessageParam{
					Role: ChatMessageRoleAssistant,
					Content: StringOrAssistantRoleContentUnion{
						Value: "I can help you with that",
					},
				},
			},
			expected: `{"role":"assistant","content":"I can help you with that"}`,
		},
		{
			name: "tool message",
			input: ChatCompletionMessageParamUnion{
				OfTool: &ChatCompletionToolMessageParam{
					Role:       ChatMessageRoleTool,
					ToolCallID: "123",
					Content:    ContentUnion{Value: "tool result"},
				},
			},
			expected: `{"content":"tool result","role":"tool","tool_call_id":"123"}`,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result, err := json.Marshal(tc.input)
			require.NoError(t, err)
			require.JSONEq(t, tc.expected, string(result))
		})
	}
}

func TestContentUnionUnmarshal(t *testing.T) {
	for _, tc := range contentUnionBenchmarkCases {
		t.Run(tc.name, func(t *testing.T) {
			var p ContentUnion
			err := p.UnmarshalJSON(tc.data)
			require.NoError(t, err)
			require.Equal(t, tc.expected, p.Value)
		})
	}
	// Test cases that cover ContentUnion.UnmarshalJSON lines
	errorCases := []struct {
		name        string
		data        []byte
		expectedErr string
	}{
		{
			name:        "truncated data triggers skipLeadingWhitespace error",
			data:        []byte{},
			expectedErr: "truncated content data",
		},
		{
			name:        "invalid array unmarshal",
			data:        []byte(`["not a valid ChatCompletionContentPartTextParam"]`),
			expectedErr: "cannot unmarshal content as []ChatCompletionContentPartTextParam",
		},
		{
			name:        "invalid type (number)",
			data:        []byte(`123`),
			expectedErr: "invalid content type (must be string or array of ChatCompletionContentPartTextParam)",
		},
		{
			name:        "invalid type (object)",
			data:        []byte(`{"key": "value"}`),
			expectedErr: "invalid content type (must be string or array of ChatCompletionContentPartTextParam)",
		},
	}

	for _, tc := range errorCases {
		t.Run(tc.name, func(t *testing.T) {
			var p ContentUnion
			err := p.UnmarshalJSON(tc.data)
			require.Error(t, err)
			require.Contains(t, err.Error(), tc.expectedErr)
		})
	}
}

func TestContentUnionMarshal(t *testing.T) {
	for _, tc := range contentUnionBenchmarkCases {
		t.Run(tc.name, func(t *testing.T) {
			data, err := json.Marshal(tc.expected)
			require.NoError(t, err)
			require.JSONEq(t, string(tc.data), string(data))
		})
	}
}

func TestEmbeddingRequestInputUnmarshal(t *testing.T) {
	for _, tc := range embeddingRequestInputBenchmarkCases {
		t.Run(tc.name, func(t *testing.T) {
			var p EmbeddingRequestInput
			err := p.UnmarshalJSON(tc.data)
			require.NoError(t, err)
			require.Equal(t, tc.expected, p.Value)
		})
	}

	// Test error cases for EmbeddingRequestInput
	errorCases := []struct {
		name        string
		data        []byte
		expectedErr string
	}{
		{
			name:        "truncated data",
			data:        []byte{},
			expectedErr: "truncated input data",
		},
	}

	for _, tc := range errorCases {
		t.Run(tc.name, func(t *testing.T) {
			var p EmbeddingRequestInput
			err := p.UnmarshalJSON(tc.data)
			require.Error(t, err)
			require.Contains(t, err.Error(), tc.expectedErr)
		})
	}
}

func TestEmbeddingRequestInputMarshal(t *testing.T) {
	for _, tc := range embeddingRequestInputBenchmarkCases {
		t.Run(tc.name, func(t *testing.T) {
			// Wrap the expected value in EmbeddingRequestInput to test MarshalJSON
			input := EmbeddingRequestInput{Value: tc.expected}
			data, err := json.Marshal(input)
			require.NoError(t, err)
			require.JSONEq(t, string(tc.data), string(data))
		})
	}
}

func TestStringOrUserRoleContentUnionMarshal(t *testing.T) {
	testCases := []struct {
		name     string
		input    StringOrUserRoleContentUnion
		expected string
	}{
		{
			name:     "string value",
			input:    StringOrUserRoleContentUnion{Value: "What is the weather?"},
			expected: `"What is the weather?"`,
		},
		{
			name: "content array",
			input: StringOrUserRoleContentUnion{
				Value: []ChatCompletionContentPartUserUnionParam{
					{
						OfText: &ChatCompletionContentPartTextParam{
							Type: "text",
							Text: "What's in this image?",
						},
					},
				},
			},
			expected: `[{"text":"What's in this image?","type":"text"}]`,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result, err := json.Marshal(tc.input)
			require.NoError(t, err)
			require.JSONEq(t, tc.expected, string(result))
		})
	}
}

func TestStringOrAssistantRoleContentUnionMarshal(t *testing.T) {
	testCases := []struct {
		name     string
		input    StringOrAssistantRoleContentUnion
		expected string
	}{
		{
			name:     "string value",
			input:    StringOrAssistantRoleContentUnion{Value: "I can help with that"},
			expected: `"I can help with that"`,
		},
		{
			name: "content object",
			input: StringOrAssistantRoleContentUnion{
				Value: ChatCompletionAssistantMessageParamContent{
					Text: ptr.To("Here is the answer"),
					Type: ChatCompletionAssistantMessageParamContentTypeText,
				},
			},
			expected: `{"type":"text","text":"Here is the answer"}`,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result, err := json.Marshal(tc.input)
			require.NoError(t, err)
			require.JSONEq(t, tc.expected, string(result))
		})
	}
}

func TestChatCompletionResponseFormatUnionMarshal(t *testing.T) {
	testCases := []struct {
		name     string
		input    ChatCompletionResponseFormatUnion
		expected string
	}{
		{
			name: "text",
			input: ChatCompletionResponseFormatUnion{
				OfText: &ChatCompletionResponseFormatTextParam{
					Type: "text",
				},
			},
			expected: `{"type":"text"}`,
		},
		{
			name: "json schema",
			input: ChatCompletionResponseFormatUnion{
				OfJSONSchema: &ChatCompletionResponseFormatJSONSchema{
					Type: "json_schema",
					JSONSchema: ChatCompletionResponseFormatJSONSchemaJSONSchema{
						Name:   "math_response",
						Strict: true,
						Schema: json.RawMessage(`{ "type": "object", "properties": { "step": {"type": "string"} }, "required": [ "steps"], "additionalProperties": false }`),
					},
				},
			},

			expected: `{"json_schema": { "name": "math_response", "schema": { "type": "object", "properties": { "step": {"type": "string"} }, "required": [ "steps"], "additionalProperties": false }, "strict": true }, "type":"json_schema"}`,
		},
		{
			name: "json object",
			input: ChatCompletionResponseFormatUnion{
				OfJSONObject: &ChatCompletionResponseFormatJSONObjectParam{
					Type: "json_object",
				},
			},
			expected: `{"type":"json_object"}`,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result, err := json.Marshal(tc.input)
			require.NoError(t, err)
			require.JSONEq(t, tc.expected, string(result))
		})
	}
}

func TestChatCompletionContentPartUserUnionParamMarshal(t *testing.T) {
	testCases := []struct {
		name     string
		input    ChatCompletionContentPartUserUnionParam
		expected string
	}{
		{
			name: "text content",
			input: ChatCompletionContentPartUserUnionParam{
				OfText: &ChatCompletionContentPartTextParam{
					Type: "text",
					Text: "Hello world",
				},
			},
			expected: `{"text":"Hello world","type":"text"}`,
		},
		{
			name: "image content",
			input: ChatCompletionContentPartUserUnionParam{
				OfImageURL: &ChatCompletionContentPartImageParam{
					Type: ChatCompletionContentPartImageTypeImageURL,
					ImageURL: ChatCompletionContentPartImageImageURLParam{
						URL: "https://example.com/image.jpg",
					},
				},
			},
			expected: `{"image_url":{"url":"https://example.com/image.jpg"},"type":"image_url"}`,
		},
		{
			name: "audio content",
			input: ChatCompletionContentPartUserUnionParam{
				OfInputAudio: &ChatCompletionContentPartInputAudioParam{
					Type: ChatCompletionContentPartInputAudioTypeInputAudio,
					InputAudio: ChatCompletionContentPartInputAudioInputAudioParam{
						Data: "audio-data",
					},
				},
			},
			expected: `{"input_audio":{"data":"audio-data","format":""},"type":"input_audio"}`,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result, err := json.Marshal(tc.input)
			require.NoError(t, err)
			require.JSONEq(t, tc.expected, string(result))
		})
	}
}

func TestMarshalUnmarshalRoundTrip(t *testing.T) {
	// Test that we can marshal and unmarshal a complete chat completion request
	req := &ChatCompletionRequest{
		Model: "gpt-4",
		Messages: []ChatCompletionMessageParamUnion{
			{
				OfSystem: &ChatCompletionSystemMessageParam{
					Role:    ChatMessageRoleSystem,
					Content: ContentUnion{Value: "You are helpful"},
				},
			},
			{
				OfUser: &ChatCompletionUserMessageParam{
					Role: ChatMessageRoleUser,
					Content: StringOrUserRoleContentUnion{
						Value: []ChatCompletionContentPartUserUnionParam{
							{
								OfText: &ChatCompletionContentPartTextParam{
									Type: "text",
									Text: "What's in this image?",
								},
							},
							{
								OfImageURL: &ChatCompletionContentPartImageParam{
									Type: ChatCompletionContentPartImageTypeImageURL,
									ImageURL: ChatCompletionContentPartImageImageURLParam{
										URL: "https://example.com/image.jpg",
									},
								},
							},
						},
					},
				},
			},
		},
		Temperature: ptr.To(0.7),
		MaxTokens:   ptr.To[int64](100),
	}

	// Marshal to JSON
	jsonData, err := json.Marshal(req)
	require.NoError(t, err)

	// Unmarshal back
	var decoded ChatCompletionRequest
	err = json.Unmarshal(jsonData, &decoded)
	require.NoError(t, err)

	// Verify the structure
	require.Equal(t, req.Model, decoded.Model)
	require.Len(t, req.Messages, len(decoded.Messages))
	require.Equal(t, *req.Temperature, *decoded.Temperature)
	require.Equal(t, *req.MaxTokens, *decoded.MaxTokens)
}

func TestChatCompletionResponse(t *testing.T) {
	testCases := []struct {
		name     string
		response ChatCompletionResponse
		expected string
	}{
		{
			name: "basic response with new fields",
			response: ChatCompletionResponse{
				ID:                "chatcmpl-test123",
				Created:           JSONUNIXTime(time.Unix(1735689600, 0)),
				Model:             "gpt-4.1-nano",
				ServiceTier:       "default",
				SystemFingerprint: "",
				Object:            "chat.completion",
				Choices: []ChatCompletionResponseChoice{
					{
						Index:        0,
						FinishReason: ChatCompletionChoicesFinishReasonStop,
						Message: ChatCompletionResponseChoiceMessage{
							Role:    "assistant",
							Content: ptr.To("Hello!"),
						},
					},
				},
				Usage: Usage{
					CompletionTokens: 1,
					PromptTokens:     5,
					TotalTokens:      6,
				},
			},
			expected: `{
				"id": "chatcmpl-test123",
				"object": "chat.completion",
				"created": 1735689600,
				"model": "gpt-4.1-nano",
				"service_tier": "default",
				"choices": [{
					"index": 0,
					"message": {
						"role": "assistant",
						"content": "Hello!"
					},
					"finish_reason": "stop"
				}],
				"usage": {
					"prompt_tokens": 5,
					"completion_tokens": 1,
					"total_tokens": 6
				}
			}`,
		},
		{
			name: "response with web search annotations",
			response: ChatCompletionResponse{
				ID:      "chatcmpl-bf3e7207-9819-40a2-9225-87e8666fe23d",
				Created: JSONUNIXTime(time.Unix(1755135425, 0)),
				Model:   "gpt-4o-mini-search-preview-2025-03-11",
				Object:  "chat.completion",
				Choices: []ChatCompletionResponseChoice{
					{
						Index:        0,
						FinishReason: ChatCompletionChoicesFinishReasonStop,
						Message: ChatCompletionResponseChoiceMessage{
							Role:    "assistant",
							Content: ptr.To("Check out httpbin.org"),
							Annotations: ptr.To([]Annotation{
								{
									Type: "url_citation",
									URLCitation: &URLCitation{
										EndIndex:   21,
										StartIndex: 10,
										Title:      "httpbin.org",
										URL:        "https://httpbin.org/?utm_source=openai",
									},
								},
							}),
						},
					},
				},
				Usage: Usage{
					CompletionTokens: 192,
					PromptTokens:     14,
					TotalTokens:      206,
				},
			},
			expected: `{
				"id": "chatcmpl-bf3e7207-9819-40a2-9225-87e8666fe23d",
				"object": "chat.completion",
				"created": 1755135425,
				"model": "gpt-4o-mini-search-preview-2025-03-11",
				"choices": [{
					"index": 0,
					"message": {
						"role": "assistant",
						"content": "Check out httpbin.org",
						"annotations": [{
							"type": "url_citation",
							"url_citation": {
								"end_index": 21,
								"start_index": 10,
								"title": "httpbin.org",
								"url": "https://httpbin.org/?utm_source=openai"
							}
						}]
					},
					"finish_reason": "stop"
				}],
				"usage": {
					"prompt_tokens": 14,
					"completion_tokens": 192,
					"total_tokens": 206
				}
			}`,
		},
		{
			name: "response with safety settings",
			response: ChatCompletionResponse{
				ID:      "chatcmpl-safety-test",
				Created: JSONUNIXTime(time.Unix(1755135425, 0)),
				Model:   "gpt-4.1-nano",
				Object:  "chat.completion",
				Choices: []ChatCompletionResponseChoice{
					{
						Index:        0,
						FinishReason: ChatCompletionChoicesFinishReasonStop,
						Message: ChatCompletionResponseChoiceMessage{
							Role:    "assistant",
							Content: ptr.To("This is a safe response"),
							SafetyRatings: []*genai.SafetyRating{
								{
									Category:    genai.HarmCategoryHarassment,
									Probability: genai.HarmProbabilityLow,
								},
								{
									Category:    genai.HarmCategorySexuallyExplicit,
									Probability: genai.HarmProbabilityNegligible,
								},
							},
						},
					},
				},
				Usage: Usage{
					CompletionTokens: 5,
					PromptTokens:     3,
					TotalTokens:      8,
				},
			},
			expected: `{
				"id": "chatcmpl-safety-test",
				"object": "chat.completion",
				"created": 1755135425,
				"model": "gpt-4.1-nano",
				"choices": [{
					"index": 0,
					"message": {
						"role": "assistant",
						"content": "This is a safe response",
						"safety_ratings": [
							{
								"category": "HARM_CATEGORY_HARASSMENT",
								"probability": "LOW"
							},
							{
								"category": "HARM_CATEGORY_SEXUALLY_EXPLICIT",
								"probability": "NEGLIGIBLE"
							}
						]
					},
					"finish_reason": "stop"
				}],
				"usage": {
					"prompt_tokens": 3,
					"completion_tokens": 5,
					"total_tokens": 8
				}
			}`,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Marshal to JSON
			jsonData, err := json.Marshal(tc.response)
			require.NoError(t, err)
			require.JSONEq(t, tc.expected, string(jsonData))

			// Unmarshal back and verify round-trip
			var decoded ChatCompletionResponse
			err = json.Unmarshal(jsonData, &decoded)
			require.NoError(t, err)
			require.Equal(t, tc.response.ID, decoded.ID)
			require.Equal(t, tc.response.Model, decoded.Model)
			require.Equal(t, time.Time(tc.response.Created).Unix(), time.Time(decoded.Created).Unix())
		})
	}
}

func TestChatCompletionRequest(t *testing.T) {
	testCases := []struct {
		name     string
		jsonStr  string
		expected *ChatCompletionRequest
	}{
		{
			name: "text and audio modalities",
			jsonStr: `{
				"model": "gpt-4o-audio-preview",
				"messages": [{"role": "user", "content": "Hello!"}],
				"modalities": ["text", "audio"]
			}`,
			expected: &ChatCompletionRequest{
				Model: "gpt-4o-audio-preview",
				Messages: []ChatCompletionMessageParamUnion{
					{
						OfUser: &ChatCompletionUserMessageParam{
							Role:    ChatMessageRoleUser,
							Content: StringOrUserRoleContentUnion{Value: "Hello!"},
						},
					},
				},
				Modalities: []ChatCompletionModality{ChatCompletionModalityText, ChatCompletionModalityAudio},
			},
		},
		{
			name: "text only modality",
			jsonStr: `{
				"model": "gpt-4.1-nano",
				"messages": [{"role": "user", "content": "Hi"}],
				"modalities": ["text"]
			}`,
			expected: &ChatCompletionRequest{
				Model: "gpt-4.1-nano",
				Messages: []ChatCompletionMessageParamUnion{
					{
						OfUser: &ChatCompletionUserMessageParam{
							Role:    ChatMessageRoleUser,
							Content: StringOrUserRoleContentUnion{Value: "Hi"},
						},
					},
				},
				Modalities: []ChatCompletionModality{ChatCompletionModalityText},
			},
		},
		{
			name: "audio output parameters",
			jsonStr: `{
				"model": "gpt-4o-audio-preview",
				"messages": [{"role": "user", "content": "Hello!"}],
				"modalities": ["audio"],
				"audio": {
					"voice": "alloy",
					"format": "wav"
				}
			}`,
			expected: &ChatCompletionRequest{
				Model: "gpt-4o-audio-preview",
				Messages: []ChatCompletionMessageParamUnion{
					{
						OfUser: &ChatCompletionUserMessageParam{
							Role:    ChatMessageRoleUser,
							Content: StringOrUserRoleContentUnion{Value: "Hello!"},
						},
					},
				},
				Modalities: []ChatCompletionModality{ChatCompletionModalityAudio},
				Audio: &ChatCompletionAudioParam{
					Voice:  ChatCompletionAudioVoiceAlloy,
					Format: ChatCompletionAudioFormatWav,
				},
			},
		},
		{
			name: "prediction with string content",
			jsonStr: `{
				"model": "gpt-4.1-nano",
				"messages": [{"role": "user", "content": "Complete this: Hello"}],
				"prediction": {
					"type": "content",
					"content": "Hello world!"
				}
			}`,
			expected: &ChatCompletionRequest{
				Model: "gpt-4.1-nano",
				Messages: []ChatCompletionMessageParamUnion{
					{
						OfUser: &ChatCompletionUserMessageParam{
							Role:    ChatMessageRoleUser,
							Content: StringOrUserRoleContentUnion{Value: "Complete this: Hello"},
						},
					},
				},
				PredictionContent: &PredictionContent{
					Type:    PredictionContentTypeContent,
					Content: ContentUnion{Value: "Hello world!"},
				},
			},
		},
		{
			name: "web search options",
			jsonStr: `{
				"model": "gpt-4o-mini-search-preview",
				"messages": [{"role": "user", "content": "What's the latest news?"}],
				"web_search_options": {"search_context_size": "low"}
			}`,
			expected: &ChatCompletionRequest{
				Model: "gpt-4o-mini-search-preview",
				Messages: []ChatCompletionMessageParamUnion{
					{
						OfUser: &ChatCompletionUserMessageParam{
							Role:    ChatMessageRoleUser,
							Content: StringOrUserRoleContentUnion{Value: "What's the latest news?"},
						},
					},
				},
				WebSearchOptions: &WebSearchOptions{
					SearchContextSize: WebSearchContextSizeLow,
				},
			},
		},
		{
			name: "enterprise search tool",
			jsonStr: `{
				"model": "gemini-1.5-pro",
				"messages": [
					{
						"role": "user",
						"content": "Hello with enterprise search!"
					}
				],
				"tools": [
					{
						"type": "enterprise_search"
					}
				]
			}`,
			expected: &ChatCompletionRequest{
				Model: "gemini-1.5-pro",
				Messages: []ChatCompletionMessageParamUnion{
					{
						OfUser: &ChatCompletionUserMessageParam{
							Role:    ChatMessageRoleUser,
							Content: StringOrUserRoleContentUnion{Value: "Hello with enterprise search!"},
						},
					},
				},
				Tools: []Tool{
					{
						Type: ToolTypeEnterpriseWebSearch,
					},
				},
			},
		},
		{
			name: "mixed function and enterprise search tools",
			jsonStr: `{
				"model": "gemini-1.5-pro",
				"messages": [
					{
						"role": "user",
						"content": "Mixed tools test"
					}
				],
				"tools": [
					{
						"type": "function",
						"function": {
							"name": "get_weather",
							"description": "Get current weather"
						}
					},
					{
						"type": "enterprise_search"
					}
				]
			}`,
			expected: &ChatCompletionRequest{
				Model: "gemini-1.5-pro",
				Messages: []ChatCompletionMessageParamUnion{
					{
						OfUser: &ChatCompletionUserMessageParam{
							Role:    ChatMessageRoleUser,
							Content: StringOrUserRoleContentUnion{Value: "Mixed tools test"},
						},
					},
				},
				Tools: []Tool{
					{
						Type: ToolTypeFunction,
						Function: &FunctionDefinition{
							Name:        "get_weather",
							Description: "Get current weather",
						},
					},
					{
						Type: ToolTypeEnterpriseWebSearch,
					},
				},
			},
		},
		{
			name: "enterprise search with vendor fields",
			jsonStr: `{
				"model": "gemini-1.5-pro",
				"messages": [
					{
						"role": "user",
						"content": "Combined enterprise search and safety settings"
					}
				],
				"tools": [
					{
						"type": "enterprise_search"
					}
				],
				"safetySettings": [
					{
						"category": "HARM_CATEGORY_HARASSMENT",
						"threshold": "BLOCK_ONLY_HIGH"
					}
				]
			}`,
			expected: &ChatCompletionRequest{
				Model: "gemini-1.5-pro",
				Messages: []ChatCompletionMessageParamUnion{
					{
						OfUser: &ChatCompletionUserMessageParam{
							Role:    ChatMessageRoleUser,
							Content: StringOrUserRoleContentUnion{Value: "Combined enterprise search and safety settings"},
						},
					},
				},
				Tools: []Tool{
					{
						Type: ToolTypeEnterpriseWebSearch,
					},
				},
				GCPVertexAIVendorFields: &GCPVertexAIVendorFields{
					SafetySettings: []*genai.SafetySetting{
						{
							Category:  genai.HarmCategoryHarassment,
							Threshold: genai.HarmBlockThresholdBlockOnlyHigh,
						},
					},
				},
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			var req ChatCompletionRequest
			err := json.Unmarshal([]byte(tc.jsonStr), &req)
			require.NoError(t, err)
			require.Equal(t, *tc.expected, req)

			// Marshal back and verify it round-trips
			marshaled, err := json.Marshal(req)
			require.NoError(t, err)
			var req2 ChatCompletionRequest
			err = json.Unmarshal(marshaled, &req2)
			require.NoError(t, err)
			require.Equal(t, req, req2)
		})
	}
}

func TestChatCompletionAudioFormats(t *testing.T) {
	formats := []ChatCompletionAudioFormat{
		ChatCompletionAudioFormatWav,
		ChatCompletionAudioFormatAAC,
		ChatCompletionAudioFormatMP3,
		ChatCompletionAudioFormatFlac,
		ChatCompletionAudioFormatOpus,
		ChatCompletionAudioFormatPCM16,
	}

	for _, format := range formats {
		audio := ChatCompletionAudioParam{
			Voice:  ChatCompletionAudioVoiceNova,
			Format: format,
		}
		data, err := json.Marshal(audio)
		require.NoError(t, err)
		require.Contains(t, string(data), string(format))
	}
}

func TestPredictionContentType(t *testing.T) {
	// Verify the constant value matches OpenAPI spec.
	require.Equal(t, PredictionContentTypeContent, PredictionContentType("content"))
}

func TestUnmarshalJSON_Unmarshal(t *testing.T) {
	jsonStr := `{"value": 3.14}`
	var data struct {
		Time JSONUNIXTime `json:"value"`
	}
	err := json.Unmarshal([]byte(jsonStr), &data)
	require.NoError(t, err)
	require.Equal(t, int64(3), time.Time(data.Time).Unix())

	jsonStr = `{"value": 2}`
	err = json.Unmarshal([]byte(jsonStr), &data)
	require.NoError(t, err)
	require.Equal(t, int64(2), time.Time(data.Time).Unix())
}

func TestChatCompletionResponseChunkChoice(t *testing.T) {
	testCases := []struct {
		name     string
		choice   ChatCompletionResponseChunkChoice
		expected string
	}{
		{
			name: "streaming chunk with content",
			choice: ChatCompletionResponseChunkChoice{
				Index: 0,
				Delta: &ChatCompletionResponseChunkChoiceDelta{
					Content: ptr.To("Hello"),
					Role:    "assistant",
				},
			},
			expected: `{"index":0,"delta":{"content":"Hello","role":"assistant"}}`,
		},
		{
			name: "streaming chunk with truncated content",
			choice: ChatCompletionResponseChunkChoice{
				Index: 0,
				Delta: &ChatCompletionResponseChunkChoiceDelta{
					Content: ptr.To(""),
					Role:    "assistant",
				},
			},
			expected: `{"index":0,"delta":{"content":"","role":"assistant"}}`,
		},
		{
			name: "streaming chunk with tool calls",
			choice: ChatCompletionResponseChunkChoice{
				Index: 0,
				Delta: &ChatCompletionResponseChunkChoiceDelta{
					Role: "assistant",
					ToolCalls: []ChatCompletionChunkChoiceDeltaToolCall{
						{
							ID:   ptr.To("tooluse_QklrEHKjRu6Oc4BQUfy7ZQ"),
							Type: "function",
							Function: ChatCompletionMessageToolCallFunctionParam{
								Name:      "cosine",
								Arguments: "",
							},
							Index: 0,
						},
					},
				},
			},
			expected: `{"index":0,"delta":{"role":"assistant","tool_calls":[{"id":"tooluse_QklrEHKjRu6Oc4BQUfy7ZQ","function":{"arguments":"","name":"cosine"},"type":"function", "index": 0}]}}`,
		},
		{
			name: "streaming chunk with annotations",
			choice: ChatCompletionResponseChunkChoice{
				Index: 0,
				Delta: &ChatCompletionResponseChunkChoiceDelta{
					Annotations: ptr.To([]Annotation{
						{
							Type: "url_citation",
							URLCitation: &URLCitation{
								EndIndex:   215,
								StartIndex: 160,
								Title:      "httpbin.org",
								URL:        "https://httpbin.org/?utm_source=openai",
							},
						},
					}),
				},
				FinishReason: "stop",
			},
			expected: `{"index":0,"delta":{"annotations":[{"type":"url_citation","url_citation":{"end_index":215,"start_index":160,"title":"httpbin.org","url":"https://httpbin.org/?utm_source=openai"}}]},"finish_reason":"stop"}`,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			jsonData, err := json.Marshal(tc.choice)
			require.NoError(t, err)
			require.JSONEq(t, tc.expected, string(jsonData))
		})
	}
}

func TestChatCompletionResponseChunk(t *testing.T) {
	testCases := []struct {
		name     string
		chunk    ChatCompletionResponseChunk
		expected string
	}{
		{
			name: "chunk with obfuscation",
			chunk: ChatCompletionResponseChunk{
				ID:                "chatcmpl-123",
				Object:            "chat.completion.chunk",
				Created:           JSONUNIXTime(time.Unix(1755137933, 0)),
				Model:             "gpt-5-nano",
				ServiceTier:       "default",
				SystemFingerprint: "fp_123",
				Choices: []ChatCompletionResponseChunkChoice{
					{
						Index: 0,
						Delta: &ChatCompletionResponseChunkChoiceDelta{
							Content: ptr.To("Hello"),
						},
					},
				},
				Obfuscation: "yBUv8b1dlI5ORP",
			},
			expected: `{"id":"chatcmpl-123","object":"chat.completion.chunk","created":1755137933,"model":"gpt-5-nano","service_tier":"default","system_fingerprint":"fp_123","choices":[{"index":0,"delta":{"content":"Hello"}}],"obfuscation":"yBUv8b1dlI5ORP"}`,
		},
		{
			name: "chunk without obfuscation",
			chunk: ChatCompletionResponseChunk{
				ID:      "chatcmpl-456",
				Object:  "chat.completion.chunk",
				Created: JSONUNIXTime(time.Unix(1755137934, 0)),
				Model:   "gpt-5-nano",
				Choices: []ChatCompletionResponseChunkChoice{
					{
						Index: 0,
						Delta: &ChatCompletionResponseChunkChoiceDelta{
							Content: ptr.To("World"),
						},
					},
				},
			},
			expected: `{"id":"chatcmpl-456","object":"chat.completion.chunk","created":1755137934,"model":"gpt-5-nano","choices":[{"index":0,"delta":{"content":"World"}}]}`,
		},
		{
			name: "usage-only chunk with empty choices must serialize choices as empty array",
			chunk: ChatCompletionResponseChunk{
				ID:      "chatcmpl-789",
				Object:  "chat.completion.chunk",
				Created: JSONUNIXTime(time.Unix(1755137935, 0)),
				Model:   "gpt-5-nano",
				Choices: []ChatCompletionResponseChunkChoice{},
				Usage: &Usage{
					PromptTokens:     20,
					CompletionTokens: 10,
					TotalTokens:      30,
				},
			},
			expected: `{"id":"chatcmpl-789","object":"chat.completion.chunk","created":1755137935,"model":"gpt-5-nano","choices":[],"usage":{"prompt_tokens":20,"completion_tokens":10,"total_tokens":30}}`,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			jsonData, err := json.Marshal(tc.chunk)
			require.NoError(t, err)
			require.JSONEq(t, tc.expected, string(jsonData))
		})
	}
}

func TestURLCitation(t *testing.T) {
	testCases := []struct {
		name     string
		citation URLCitation
		expected string
	}{
		{
			name: "url citation with all fields",
			citation: URLCitation{
				EndIndex:   215,
				StartIndex: 160,
				Title:      "httpbin.org",
				URL:        "https://httpbin.org/?utm_source=openai",
			},
			expected: `{"end_index":215,"start_index":160,"title":"httpbin.org","url":"https://httpbin.org/?utm_source=openai"}`,
		},
		{
			name: "url citation minimal",
			citation: URLCitation{
				EndIndex:   10,
				StartIndex: 0,
				URL:        "https://example.com",
				Title:      "Example",
			},
			expected: `{"end_index":10,"start_index":0,"title":"Example","url":"https://example.com"}`,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			jsonData, err := json.Marshal(tc.citation)
			require.NoError(t, err)
			require.JSONEq(t, tc.expected, string(jsonData))

			var decoded URLCitation
			err = json.Unmarshal([]byte(tc.expected), &decoded)
			require.NoError(t, err)
			require.Equal(t, tc.citation, decoded)
		})
	}
}

func TestAnnotation(t *testing.T) {
	testCases := []struct {
		name       string
		annotation Annotation
		expected   string
	}{
		{
			name: "annotation with url citation",
			annotation: Annotation{
				Type: "url_citation",
				URLCitation: &URLCitation{
					EndIndex:   215,
					StartIndex: 160,
					Title:      "httpbin.org",
					URL:        "https://httpbin.org/?utm_source=openai",
				},
			},
			expected: `{"type":"url_citation","url_citation":{"end_index":215,"start_index":160,"title":"httpbin.org","url":"https://httpbin.org/?utm_source=openai"}}`,
		},
		{
			name: "annotation type only",
			annotation: Annotation{
				Type: "url_citation",
			},
			expected: `{"type":"url_citation"}`,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			jsonData, err := json.Marshal(tc.annotation)
			require.NoError(t, err)
			require.JSONEq(t, tc.expected, string(jsonData))

			var decoded Annotation
			err = json.Unmarshal([]byte(tc.expected), &decoded)
			require.NoError(t, err)
			require.Equal(t, tc.annotation, decoded)
		})
	}
}

func TestEmbeddingUnionUnmarshal(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    any
		wantErr bool
	}{
		{
			name:  "unmarshal array of floats",
			input: `[1.0, 2.0, 3.0]`,
			want:  []float64{1.0, 2.0, 3.0},
		},
		{
			name:  "unmarshal string",
			input: `"base64response"`,
			want:  "base64response",
		},
		{
			name:    "unmarshal int should error",
			input:   `123`,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var eu EmbeddingUnion
			err := json.Unmarshal([]byte(tt.input), &eu)
			if (err != nil) != tt.wantErr {
				t.Errorf("EmbeddingUnion Unmarshal Error. error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr {
				// Use reflect.DeepEqual to compare
				if !cmp.Equal(eu.Value, tt.want) {
					t.Errorf("EmbeddingUnion Unmarshal Error. got = %v, want %v", eu.Value, tt.want)
				}
			}
		})
	}
}

func TestEmbeddingUnionMarshal(t *testing.T) {
	tests := []struct {
		name     string
		value    any
		expected string
	}{
		{
			name:     "marshal array of floats",
			value:    []float64{1.0, 2.0, 3.0},
			expected: `[1,2,3]`,
		},
		{
			name:     "marshal string",
			value:    "base64response",
			expected: `"base64response"`,
		},
		{
			name:     "marshal empty array",
			value:    []float64{},
			expected: `[]`,
		},
		{
			name:     "marshal array with negative floats",
			value:    []float64{-0.5, 0.0, 0.5},
			expected: `[-0.5,0,0.5]`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			eu := EmbeddingUnion{Value: tt.value}
			data, err := json.Marshal(eu)
			require.NoError(t, err)
			require.JSONEq(t, tt.expected, string(data))
		})
	}
}

func TestEmbeddingRequestInputRoundTrip(t *testing.T) {
	tests := []struct {
		name  string
		input EmbeddingRequestInput
	}{
		{
			name:  "string input",
			input: EmbeddingRequestInput{Value: "test string"},
		},
		{
			name:  "array of strings",
			input: EmbeddingRequestInput{Value: []string{"hello", "world"}},
		},
		{
			name:  "array of ints",
			input: EmbeddingRequestInput{Value: []int64{1, 2, 3, 4, 5}},
		},
		{
			name: "EmbeddingInputItem",
			input: EmbeddingRequestInput{Value: EmbeddingInputItem{
				Content:  EmbeddingContent{Value: "test content"},
				TaskType: "RETRIEVAL_QUERY",
				Title:    "Test Title",
			}},
		},
		{
			name: "array of EmbeddingInputItem",
			input: EmbeddingRequestInput{Value: []EmbeddingInputItem{
				{Content: EmbeddingContent{Value: "first"}, TaskType: "RETRIEVAL_QUERY"},
				{Content: EmbeddingContent{Value: "second"}, TaskType: "RETRIEVAL_DOCUMENT", Title: "Doc"},
			}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Marshal
			data, err := json.Marshal(tt.input)
			require.NoError(t, err)

			// Unmarshal
			var result EmbeddingRequestInput
			err = json.Unmarshal(data, &result)
			require.NoError(t, err)

			// Compare
			require.Equal(t, tt.input.Value, result.Value)
		})
	}
}

func TestChatCompletionResponseChoiceMessageAudio(t *testing.T) {
	testCases := []struct {
		name     string
		audio    ChatCompletionResponseChoiceMessageAudio
		expected string
	}{
		{
			name: "audio with all fields",
			audio: ChatCompletionResponseChoiceMessageAudio{
				Data:       "base64audiodata",
				ExpiresAt:  1735689600,
				ID:         "audio-123",
				Transcript: "Hello, world!",
			},
			expected: `{
				"data": "base64audiodata",
				"expires_at": 1735689600,
				"id": "audio-123",
				"transcript": "Hello, world!"
			}`,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			jsonData, err := json.Marshal(tc.audio)
			require.NoError(t, err)
			require.JSONEq(t, tc.expected, string(jsonData))

			var decoded ChatCompletionResponseChoiceMessageAudio
			err = json.Unmarshal(jsonData, &decoded)
			require.NoError(t, err)
			require.Equal(t, tc.audio, decoded)
		})
	}
}

func TestCompletionTokensDetails(t *testing.T) {
	testCases := []struct {
		name     string
		details  CompletionTokensDetails
		expected string
	}{
		{
			name: "with text tokens",
			details: CompletionTokensDetails{
				TextTokens:               5,
				AcceptedPredictionTokens: 10,
				AudioTokens:              256,
				ReasoningTokens:          832,
				RejectedPredictionTokens: 2,
			},
			expected: `{
				"text_tokens": 5,
				"accepted_prediction_tokens": 10,
				"audio_tokens": 256,
				"reasoning_tokens": 832,
				"rejected_prediction_tokens": 2
			}`,
		},
		{
			name: "with zero text tokens omitted",
			details: CompletionTokensDetails{
				TextTokens:      0,
				AudioTokens:     256,
				ReasoningTokens: 832,
			},
			expected: `{
				"audio_tokens": 256,
				"reasoning_tokens": 832
			}`,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			jsonData, err := json.Marshal(tc.details)
			require.NoError(t, err)
			require.JSONEq(t, tc.expected, string(jsonData))

			var decoded CompletionTokensDetails
			err = json.Unmarshal(jsonData, &decoded)
			require.NoError(t, err)
			require.Equal(t, tc.details, decoded)
		})
	}
}

func TestPromptTokensDetails(t *testing.T) {
	testCases := []struct {
		name     string
		details  PromptTokensDetails
		expected string
	}{
		{
			name: "with text tokens",
			details: PromptTokensDetails{
				TextTokens:          15,
				AudioTokens:         8,
				CachedTokens:        384,
				CacheCreationTokens: 10,
			},
			expected: `{
				"text_tokens": 15,
				"audio_tokens": 8,
				"cached_tokens": 384,
				"cache_creation_input_tokens": 10
			}`,
		},
		{
			name: "with zero text tokens omitted",
			details: PromptTokensDetails{
				TextTokens:          0,
				AudioTokens:         8,
				CachedTokens:        384,
				CacheCreationTokens: 10,
			},
			expected: `{
				"audio_tokens": 8,
				"cached_tokens": 384,
				"cache_creation_input_tokens": 10
			}`,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			jsonData, err := json.Marshal(tc.details)
			require.NoError(t, err)
			require.JSONEq(t, tc.expected, string(jsonData))

			var decoded PromptTokensDetails
			err = json.Unmarshal(jsonData, &decoded)
			require.NoError(t, err)
			require.Equal(t, tc.details, decoded)
		})
	}
}

func TestChatCompletionResponseUsage(t *testing.T) {
	testCases := []struct {
		name     string
		usage    Usage
		expected string
	}{
		{
			name: "with zero values omitted",
			usage: Usage{
				CompletionTokens: 9,
				PromptTokens:     19,
				TotalTokens:      28,
				CompletionTokensDetails: &CompletionTokensDetails{
					AcceptedPredictionTokens: 0,
					AudioTokens:              0,
					ReasoningTokens:          0,
					RejectedPredictionTokens: 0,
				},
				PromptTokensDetails: &PromptTokensDetails{
					AudioTokens:  0,
					CachedTokens: 0,
				},
			},
			expected: `{"completion_tokens":9,"prompt_tokens":19,"total_tokens":28,"completion_tokens_details":{},"prompt_tokens_details":{}}`,
		},
		{
			name: "with non-zero values",
			usage: Usage{
				CompletionTokens: 11,
				PromptTokens:     37,
				TotalTokens:      48,
				CompletionTokensDetails: &CompletionTokensDetails{
					AcceptedPredictionTokens: 0,
					AudioTokens:              256,
					ReasoningTokens:          832,
					RejectedPredictionTokens: 0,
				},
				PromptTokensDetails: &PromptTokensDetails{
					AudioTokens:         8,
					CachedTokens:        384,
					CacheCreationTokens: 13,
				},
			},
			expected: `{
				"completion_tokens": 11,
				"prompt_tokens": 37,
				"total_tokens": 48,
				"completion_tokens_details": {
					"audio_tokens": 256,
					"reasoning_tokens": 832
				},
				"prompt_tokens_details": {
					"audio_tokens": 8,
					"cached_tokens": 384,
					"cache_creation_input_tokens": 13
				}
			}`,
		},
		{
			name: "with text tokens",
			usage: Usage{
				CompletionTokens: 11,
				PromptTokens:     37,
				TotalTokens:      48,
				CompletionTokensDetails: &CompletionTokensDetails{
					TextTokens:               5,
					AcceptedPredictionTokens: 0,
					AudioTokens:              256,
					ReasoningTokens:          832,
					RejectedPredictionTokens: 0,
				},
				PromptTokensDetails: &PromptTokensDetails{
					TextTokens:          15,
					AudioTokens:         8,
					CachedTokens:        384,
					CacheCreationTokens: 21,
				},
			},
			expected: `{
				"completion_tokens": 11,
				"prompt_tokens": 37,
				"total_tokens": 48,
				"completion_tokens_details": {
					"text_tokens": 5,
					"audio_tokens": 256,
					"reasoning_tokens": 832
				},
				"prompt_tokens_details": {
					"text_tokens": 15,
					"audio_tokens": 8,
					"cached_tokens": 384,
					"cache_creation_input_tokens": 21
				}
			}`,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Marshal to JSON
			jsonData, err := json.Marshal(tc.usage)
			require.NoError(t, err)
			require.JSONEq(t, tc.expected, string(jsonData))

			// Unmarshal and verify
			var decoded Usage
			err = json.Unmarshal(jsonData, &decoded)
			require.NoError(t, err)
			require.Equal(t, tc.usage, decoded)
		})
	}
}

func TestWebSearchOptions(t *testing.T) {
	testCases := []struct {
		name     string
		options  WebSearchOptions
		expected string
	}{
		{
			name: "search context size low",
			options: WebSearchOptions{
				SearchContextSize: WebSearchContextSizeLow,
			},
			expected: `{"search_context_size":"low"}`,
		},
		{
			name: "with user location",
			options: WebSearchOptions{
				SearchContextSize: WebSearchContextSizeMedium,
				UserLocation: &WebSearchUserLocation{
					Type: "approximate",
					Approximate: WebSearchLocation{
						City:    "San Francisco",
						Region:  "California",
						Country: "USA",
					},
				},
			},
			expected: `{"user_location":{"type":"approximate","approximate":{"city":"San Francisco","region":"California","country":"USA"}},"search_context_size":"medium"}`,
		},
		{
			name:     "truncated options",
			options:  WebSearchOptions{},
			expected: `{}`,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Marshal to JSON
			jsonData, err := json.Marshal(tc.options)
			require.NoError(t, err)
			require.JSONEq(t, tc.expected, string(jsonData))

			// Unmarshal and verify
			var decoded WebSearchOptions
			err = json.Unmarshal(jsonData, &decoded)
			require.NoError(t, err)
			require.Equal(t, tc.options, decoded)
		})
	}
}

// This tests ensures to use a pointer to the slice since otherwise for "annotations" field to maintain
// the same results after round trip.
func TestChatCompletionResponseChoiceMessage_annotations_round_trip(t *testing.T) {
	orig := []byte(`{"annotations": []}`)
	var msg ChatCompletionResponseChoiceMessage
	err := json.Unmarshal(orig, &msg)
	require.NoError(t, err)
	require.NotNil(t, msg.Annotations)
	marshaled, err := json.Marshal(msg)
	require.NoError(t, err)
	require.JSONEq(t, `{"annotations":[]}`, string(marshaled))

	var msg2 ChatCompletionResponseChoiceMessage
	err = json.Unmarshal([]byte(`{}`), &msg2)
	require.NoError(t, err)
	require.Nil(t, msg2.Annotations)
	marshaled, err = json.Marshal(msg2)
	require.NoError(t, err)
	require.JSONEq(t, `{}`, string(marshaled))
}

// This tests ensures to use a pointer to the slice since otherwise for "annotations" field to maintain
// the same results after round trip.
func TestChatCompletionResponseChunkChoiceDelta_annotations_round_trip(t *testing.T) {
	orig := []byte(`{"annotations": []}`)
	var msg ChatCompletionResponseChunkChoiceDelta
	err := json.Unmarshal(orig, &msg)
	require.NoError(t, err)
	require.NotNil(t, msg.Annotations)
	marshaled, err := json.Marshal(msg)
	require.NoError(t, err)
	require.JSONEq(t, `{"annotations":[]}`, string(marshaled))

	var msg2 ChatCompletionResponseChunkChoiceDelta
	err = json.Unmarshal([]byte(`{}`), &msg2)
	require.NoError(t, err)
	require.Nil(t, msg2.Annotations)
	marshaled, err = json.Marshal(msg2)
	require.NoError(t, err)
	require.JSONEq(t, `{}`, string(marshaled))
}

func TestStringOrAssistantRoleContentUnionUnmarshal(t *testing.T) {
	testCases := []struct {
		name     string
		input    string
		expected StringOrAssistantRoleContentUnion
		expErr   string
	}{
		{
			name:  "string value",
			input: `"hello"`,
			expected: StringOrAssistantRoleContentUnion{
				Value: "hello",
			},
		},
		{
			name:  "array of content objects",
			input: `[{"type": "text", "text": "hello from array"}]`,
			expected: StringOrAssistantRoleContentUnion{
				Value: []ChatCompletionAssistantMessageParamContent{
					{
						Type: ChatCompletionAssistantMessageParamContentTypeText,
						Text: ptr.To("hello from array"),
					},
				},
			},
		},
		{
			name:  "single content object",
			input: `{"type": "text", "text": "hello from single object"}`,
			expected: StringOrAssistantRoleContentUnion{
				Value: ChatCompletionAssistantMessageParamContent{
					Type: ChatCompletionAssistantMessageParamContentTypeText,
					Text: ptr.To("hello from single object"),
				},
			},
		},
		{
			name:   "invalid json",
			input:  `12345`,
			expErr: "cannot unmarshal JSON data as string or assistant content parts",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			var result StringOrAssistantRoleContentUnion
			err := json.Unmarshal([]byte(tc.input), &result)

			if tc.expErr != "" {
				require.ErrorContains(t, err, tc.expErr)
				return
			}

			require.NoError(t, err)
			if !cmp.Equal(tc.expected, result) {
				t.Errorf("Unmarshal diff(got, expected) = %s\n", cmp.Diff(result, tc.expected))
			}
		})
	}
}

func TestPromptUnionUnmarshal(t *testing.T) {
	for _, tc := range promptUnionBenchmarkCases {
		t.Run(tc.name, func(t *testing.T) {
			var p PromptUnion
			err := p.UnmarshalJSON(tc.data)
			require.NoError(t, err)
			require.Equal(t, tc.expected, p.Value)
		})
	}
	// just one error to avoid replicating tests in unmarshalJSONNestedUnion
	t.Run("error", func(t *testing.T) {
		var p PromptUnion
		err := p.UnmarshalJSON([]byte{})
		require.EqualError(t, err, "truncated prompt data")
	})
}

func TestPromptUnionMarshal(t *testing.T) {
	for _, tc := range promptUnionBenchmarkCases {
		t.Run(tc.name, func(t *testing.T) {
			data, err := json.Marshal(tc.expected)
			require.NoError(t, err)
			require.JSONEq(t, string(tc.data), string(data))
		})
	}
}

func TestCompletionRequest(t *testing.T) {
	testCases := []struct {
		name     string
		req      CompletionRequest
		expected string
	}{
		{
			name: "basic request",
			req: CompletionRequest{
				Model:       ModelGPT5Nano,
				Prompt:      PromptUnion{Value: "test"},
				MaxTokens:   ptr.To(10),
				Temperature: ptr.To(0.7),
			},
			expected: `{
				"model": "gpt-5-nano",
				"prompt": "test",
				"max_tokens": 10,
				"temperature": 0.7
			}`,
		},
		{
			name: "zero temperature is valid and serialized",
			req: CompletionRequest{
				Model:       ModelGPT5Nano,
				Prompt:      PromptUnion{Value: "test"},
				MaxTokens:   ptr.To(10),
				Temperature: ptr.To(0.0),
			},
			expected: `{
				"model": "gpt-5-nano",
				"prompt": "test",
				"max_tokens": 10,
				"temperature": 0
			}`,
		},
		{
			name: "nil temperature omitted",
			req: CompletionRequest{
				Model:     ModelGPT5Nano,
				Prompt:    PromptUnion{Value: "test"},
				MaxTokens: ptr.To(10),
			},
			expected: `{
				"model": "gpt-5-nano",
				"prompt": "test",
				"max_tokens": 10
			}`,
		},
		{
			name: "zero frequency penalty is valid",
			req: CompletionRequest{
				Model:            ModelGPT5Nano,
				Prompt:           PromptUnion{Value: "test"},
				FrequencyPenalty: ptr.To(0.0),
			},
			expected: `{
				"model": "gpt-5-nano",
				"prompt": "test",
				"frequency_penalty": 0
			}`,
		},
		{
			name: "with batch prompts",
			req: CompletionRequest{
				Model:       ModelGPT5Nano,
				Prompt:      PromptUnion{Value: []string{"prompt1", "prompt2"}},
				MaxTokens:   ptr.To(5),
				Temperature: ptr.To(1.0),
			},
			expected: `{
				"model": "gpt-5-nano",
				"prompt": ["prompt1", "prompt2"],
				"max_tokens": 5,
				"temperature": 1.0
			}`,
		},
		{
			name: "with token array",
			req: CompletionRequest{
				Model:     ModelGPT5Nano,
				Prompt:    PromptUnion{Value: []int{1212, 318}},
				MaxTokens: ptr.To(5),
			},
			expected: `{
				"model": "gpt-5-nano",
				"prompt": [1212, 318],
				"max_tokens": 5
			}`,
		},
		{
			name: "with stream",
			req: CompletionRequest{
				Model:  ModelGPT5Nano,
				Prompt: PromptUnion{Value: "test"},
				Stream: true,
			},
			expected: `{
				"model": "gpt-5-nano",
				"prompt": "test",
				"stream": true
			}`,
		},
		{
			name: "with suffix",
			req: CompletionRequest{
				Model:  ModelGPT5Nano,
				Prompt: PromptUnion{Value: "Once upon a time"},
				Suffix: " and they lived happily ever after.",
			},
			expected: `{
				"model": "gpt-5-nano",
				"prompt": "Once upon a time",
				"suffix": " and they lived happily ever after."
			}`,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			data, err := json.Marshal(tc.req)
			require.NoError(t, err)
			require.JSONEq(t, tc.expected, string(data))

			var decoded CompletionRequest
			err = json.Unmarshal(data, &decoded)
			require.NoError(t, err)
			require.Equal(t, tc.req.Model, decoded.Model)
			if tc.req.Temperature != nil {
				require.NotNil(t, decoded.Temperature)
				require.Equal(t, *tc.req.Temperature, *decoded.Temperature)
			}
		})
	}
}

func TestCompletionResponse(t *testing.T) {
	testCases := []struct {
		name     string
		resp     CompletionResponse
		expected string
	}{
		{
			name: "basic response",
			resp: CompletionResponse{
				ID:      "cmpl-123",
				Object:  "text_completion",
				Created: JSONUNIXTime(time.Unix(1589478378, 0)),
				Model:   ModelGPT5Nano,
				Choices: []CompletionChoice{
					{
						Text:         "\n\nThis is indeed a test",
						Index:        ptr.To(0),
						FinishReason: "length",
					},
				},
				Usage: &Usage{
					PromptTokens:     5,
					CompletionTokens: 7,
					TotalTokens:      12,
				},
			},
			expected: `{
				"id": "cmpl-123",
				"object": "text_completion",
				"created": 1589478378,
				"model": "gpt-5-nano",
				"choices": [{
					"text": "\n\nThis is indeed a test",
					"index": 0,
					"finish_reason": "length"
				}],
				"usage": {
					"prompt_tokens": 5,
					"completion_tokens": 7,
					"total_tokens": 12
				}
			}`,
		},
		{
			name: "with system fingerprint",
			resp: CompletionResponse{
				ID:                "cmpl-456",
				Object:            "text_completion",
				Created:           JSONUNIXTime(time.Unix(1589478378, 0)),
				Model:             ModelGPT5Nano,
				SystemFingerprint: "fp_44709d6fcb",
				Choices: []CompletionChoice{
					{
						Text:         "Response text",
						FinishReason: "stop",
					},
				},
			},
			expected: `{
				"id": "cmpl-456",
				"object": "text_completion",
				"created": 1589478378,
				"model": "gpt-5-nano",
				"system_fingerprint": "fp_44709d6fcb",
				"choices": [{
					"text": "Response text",
					"finish_reason": "stop"
				}]
			}`,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			data, err := json.Marshal(tc.resp)
			require.NoError(t, err)
			require.JSONEq(t, tc.expected, string(data))

			var decoded CompletionResponse
			err = json.Unmarshal(data, &decoded)
			require.NoError(t, err)
			require.Equal(t, tc.resp.ID, decoded.ID)
			require.Equal(t, tc.resp.Model, decoded.Model)
		})
	}
}

func TestCompletionLogprobs(t *testing.T) {
	testCases := []struct {
		name     string
		logprobs CompletionLogprobs
		expected string
	}{
		{
			name: "with logprobs",
			logprobs: CompletionLogprobs{
				Tokens:        []string{"\n", "\n", "This"},
				TokenLogprobs: []float64{-0.1, -0.2, -0.3},
				TopLogprobs: []map[string]float64{
					{"\n": -0.1, " ": -2.3},
					{"\n": -0.2, " ": -2.1},
				},
				TextOffset: []int{0, 1, 2},
			},
			expected: `{
				"tokens": ["\n", "\n", "This"],
				"token_logprobs": [-0.1, -0.2, -0.3],
				"top_logprobs": [
					{"\n": -0.1, " ": -2.3},
					{"\n": -0.2, " ": -2.1}
				],
				"text_offset": [0, 1, 2]
			}`,
		},
		{
			name:     "truncated logprobs omitted",
			logprobs: CompletionLogprobs{},
			expected: `{}`,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			data, err := json.Marshal(tc.logprobs)
			require.NoError(t, err)
			require.JSONEq(t, tc.expected, string(data))

			var decoded CompletionLogprobs
			err = json.Unmarshal(data, &decoded)
			require.NoError(t, err)
			require.Equal(t, tc.logprobs, decoded)
		})
	}
}

func TestUsage(t *testing.T) {
	testCases := []struct {
		name     string
		usage    Usage
		expected string
	}{
		{
			name: "with all fields",
			usage: Usage{
				PromptTokens:     10,
				CompletionTokens: 20,
				TotalTokens:      30,
			},
			expected: `{
				"prompt_tokens": 10,
				"completion_tokens": 20,
				"total_tokens": 30
			}`,
		},
		{
			name: "with zero values omitted",
			usage: Usage{
				PromptTokens: 5,
				TotalTokens:  5,
			},
			expected: `{
				"prompt_tokens": 5,
				"total_tokens": 5
			}`,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			data, err := json.Marshal(tc.usage)
			require.NoError(t, err)
			require.JSONEq(t, tc.expected, string(data))

			var decoded Usage
			err = json.Unmarshal(data, &decoded)
			require.NoError(t, err)
			require.Equal(t, tc.usage, decoded)
		})
	}
}

func TestChatCompletionNamedToolChoice_MarshalUnmarshal(t *testing.T) {
	original := ChatCompletionNamedToolChoice{
		Type: ToolTypeFunction,
		Function: ChatCompletionNamedToolChoiceFunction{
			Name: "my_func",
		},
	}
	data, err := json.Marshal(original)
	require.NoError(t, err)

	var unmarshaled ChatCompletionNamedToolChoice
	err = json.Unmarshal(data, &unmarshaled)
	require.NoError(t, err)

	require.Equal(t, original, unmarshaled)
	require.Equal(t, "my_func", unmarshaled.Function.Name)
}

func TestChatCompletionToolChoiceUnion_MarshalUnmarshal(t *testing.T) {
	// Test with string value
	unionStr := ChatCompletionToolChoiceUnion{Value: "auto"}
	dataStr, err := json.Marshal(unionStr)
	require.NoError(t, err)

	var unmarshaledStr ChatCompletionToolChoiceUnion
	err = json.Unmarshal(dataStr, &unmarshaledStr)
	require.NoError(t, err)
	require.Equal(t, "auto", unmarshaledStr.Value)

	// Test with ChatCompletionNamedToolChoice value
	unionObj := ChatCompletionToolChoiceUnion{Value: ChatCompletionNamedToolChoice{
		Type:     ToolTypeFunction,
		Function: ChatCompletionNamedToolChoiceFunction{Name: "my_func"},
	}}
	dataObj, err := json.Marshal(unionObj)
	require.NoError(t, err)

	var unmarshaledObj ChatCompletionToolChoiceUnion
	err = json.Unmarshal(dataObj, &unmarshaledObj)
	require.NoError(t, err)

	// Type assertion for struct value
	namedChoice, ok := unmarshaledObj.Value.(ChatCompletionNamedToolChoice)
	require.True(t, ok)
	require.Equal(t, unionObj.Value, namedChoice)
	require.Equal(t, "my_func", namedChoice.Function.Name)
}

func TestResponseFormatTextConfigUnionParamUnmarshal(t *testing.T) {
	strict := true
	for _, tc := range []struct {
		name   string
		in     []byte
		out    *ResponseFormatTextConfigUnionParam
		expErr string
	}{
		{
			name: "text",
			in:   []byte(`{"type": "text"}`),
			out: &ResponseFormatTextConfigUnionParam{
				OfText: &ResponseFormatTextParam{
					Type: "text",
				},
			},
		},
		{
			name: "json_schema",
			in:   []byte(`{"type": "json_schema", "name": "math_response", "strict": true, "schema": {"type": "object", "properties": {"step": {"type": "string"}}, "required": ["steps"]}}`),
			out: &ResponseFormatTextConfigUnionParam{
				OfJSONSchema: &ResponseFormatTextJSONSchemaConfigParam{
					Type:   "json_schema",
					Name:   "math_response",
					Strict: &strict,
					Schema: map[string]any{"type": "object", "properties": map[string]any{"step": map[string]any{"type": "string"}}, "required": []any{"steps"}},
				},
			},
		},
		{
			name: "json_object",
			in:   []byte(`{"type": "json_object"}`),
			out: &ResponseFormatTextConfigUnionParam{
				OfJSONObject: &ResponseFormatJSONObjectParam{
					Type: "json_object",
				},
			},
		},
		{
			name:   "type not exist",
			in:     []byte(`{}`),
			expErr: "invalid format type in response format text config",
		},
		{
			name:   "unknown type",
			in:     []byte(`{"type": "unknown"}`),
			expErr: "invalid format type in response format text config",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var result ResponseFormatTextConfigUnionParam
			err := json.Unmarshal(tc.in, &result)
			if tc.expErr != "" {
				require.ErrorContains(t, err, tc.expErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, &result, tc.out)
		})
	}
}

func TestResponseFormatTextConfigUnionParamMarshal(t *testing.T) {
	strict := true
	testCases := []struct {
		name        string
		input       ResponseFormatTextConfigUnionParam
		expected    *string
		expectedErr *string
	}{
		{
			name: "text",
			input: ResponseFormatTextConfigUnionParam{
				OfText: &ResponseFormatTextParam{
					Type: "text",
				},
			},
			expected:    ptr.To(`{"type":"text"}`),
			expectedErr: nil,
		},
		{
			name: "json_schema",
			input: ResponseFormatTextConfigUnionParam{
				OfJSONSchema: &ResponseFormatTextJSONSchemaConfigParam{
					Type:   "json_schema",
					Name:   "math_response",
					Strict: &strict,
					Schema: map[string]any{"type": "object", "properties": map[string]any{"step": map[string]any{"type": "string"}}, "required": []any{"steps"}},
				},
			},
			expected:    ptr.To(`{"name": "math_response", "schema": {"type": "object", "properties": {"step": {"type": "string"}}, "required": ["steps"]}, "strict": true, "type": "json_schema"}`),
			expectedErr: nil,
		},
		{
			name: "json_object",
			input: ResponseFormatTextConfigUnionParam{
				OfJSONObject: &ResponseFormatJSONObjectParam{
					Type: "json_object",
				},
			},
			expected:    ptr.To(`{"type":"json_object"}`),
			expectedErr: nil,
		},
		{
			name:        "marshal error no field set",
			input:       ResponseFormatTextConfigUnionParam{},
			expected:    nil,
			expectedErr: ptr.To("no format to marshal in ResponseFormatTextConfigUnionParam"),
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result, err := json.Marshal(tc.input)
			if tc.expectedErr != nil {
				require.ErrorContains(t, err, *tc.expectedErr)
				require.Nil(t, result)
			} else {
				require.NoError(t, err)
				require.JSONEq(t, *tc.expected, string(result))
			}
		})
	}
}

func TestResponsePromptVariableUnionParamMarshalJSON(t *testing.T) {
	for _, tc := range []struct {
		name   string
		input  ResponsePromptVariableUnionParam
		expect string
		expErr string
	}{
		{
			name: "marshal string",
			input: ResponsePromptVariableUnionParam{
				OfString: ptr.To("test string"),
			},
			expect: `"test string"`,
		},
		{
			name: "marshal input text",
			input: ResponsePromptVariableUnionParam{
				OfInputText: &ResponseInputTextParam{
					Type: "input_text",
					Text: "hello",
				},
			},
			expect: `{"type":"input_text","text":"hello"}`,
		},
		{
			name: "marshal input image",
			input: ResponsePromptVariableUnionParam{
				OfInputImage: &ResponseInputImageParam{
					Type:   "input_image",
					Detail: "auto",
				},
			},
			expect: `{"type":"input_image","detail":"auto"}`,
		},
		{
			name: "marshal input file",
			input: ResponsePromptVariableUnionParam{
				OfInputFile: &ResponseInputFileParam{
					Type:   "input_file",
					FileID: "file_123",
				},
			},
			expect: `{"type":"input_file","file_id":"file_123"}`,
		},
		{
			name:   "marshal no field set",
			input:  ResponsePromptVariableUnionParam{},
			expErr: "no prompt variable to marshal",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data, err := tc.input.MarshalJSON()
			if tc.expErr != "" {
				require.ErrorContains(t, err, tc.expErr)
				return
			}
			require.NoError(t, err)
			require.JSONEq(t, tc.expect, string(data))
		})
	}
}

func TestResponsePromptVariableUnionParamUnmarshalJSON(t *testing.T) {
	for _, tc := range []struct {
		name   string
		input  []byte
		expect *ResponsePromptVariableUnionParam
		expErr string
	}{
		{
			name:  "unmarshal string",
			input: []byte(`"test string"`),
			expect: &ResponsePromptVariableUnionParam{
				OfString: ptr.To("test string"),
			},
		},
		{
			name:  "unmarshal input text",
			input: []byte(`{"type":"input_text","text":"hello"}`),
			expect: &ResponsePromptVariableUnionParam{
				OfInputText: &ResponseInputTextParam{
					Type: "input_text",
					Text: "hello",
				},
			},
		},
		{
			name:  "unmarshal input image",
			input: []byte(`{"type":"input_image","detail":"auto"}`),
			expect: &ResponsePromptVariableUnionParam{
				OfInputImage: &ResponseInputImageParam{
					Type:   "input_image",
					Detail: "auto",
				},
			},
		},
		{
			name:  "unmarshal input file",
			input: []byte(`{"type":"input_file","file_id":"file_123"}`),
			expect: &ResponsePromptVariableUnionParam{
				OfInputFile: &ResponseInputFileParam{
					Type:   "input_file",
					FileID: "file_123",
				},
			},
		},
		{
			name:   "unmarshal unknown type",
			input:  []byte(`{"type":"unknown_type"}`),
			expErr: "unknown type for ResponsePromptVariableUnionParam",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var result ResponsePromptVariableUnionParam
			err := json.Unmarshal(tc.input, &result)
			if tc.expErr != "" {
				require.ErrorContains(t, err, tc.expErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, &result, tc.expect)
		})
	}
}

func TestResponseToolUnionMarshalJSON(t *testing.T) {
	for _, tc := range []struct {
		name   string
		input  ResponseToolUnion
		expRes string
		expErr string
	}{
		{
			name: "marshal function",
			input: ResponseToolUnion{
				OfFunction: &FunctionToolParam{
					Type:       "function",
					Name:       "test_func",
					Parameters: map[string]any{"arg1": "test"},
				},
			},
			expRes: `{"type": "function", "name": "test_func", "parameters": {"arg1": "test"}}`,
		},
		{
			name: "marshal file search",
			input: ResponseToolUnion{
				OfFileSearch: &FileSearchToolParam{
					Type: "file_search",
					Filters: FileSearchToolFiltersUnionParam{
						OfComparisonFilter: &ComparisonFilterParam{
							Key:  "test",
							Type: "eq",
							Value: ComparisonFilterValueUnionParam{
								OfString: ptr.To("value"),
							},
						},
					},
					RankingOptions: FileSearchToolRankingOptionsParam{
						Ranker:         "auto",
						ScoreThreshold: ptr.To(0.5),
					},
				},
			},
			expRes: `{"type": "file_search", "filters": {"type": "eq", "key": "test", "value": "value"}, "ranking_options": {"ranker": "auto", "score_threshold": 0.5}}`,
		},
		{
			name: "marshal computer tool",
			input: ResponseToolUnion{
				OfComputerTool: &ComputerToolParam{
					Type:          "computer_use_preview",
					DisplayHeight: 1080,
					DisplayWidth:  1920,
					Environment:   "linux",
				},
			},
			expRes: `{"type": "computer_use_preview", "display_height": 1080, "display_width": 1920, "environment": "linux"}`,
		},
		{
			name: "marshal web search",
			input: ResponseToolUnion{
				OfWebSearch: &WebSearchToolParam{
					Type: "web_search",
					Filters: WebSearchToolFiltersParam{
						AllowedDomains: []string{"example.com", "test.com"},
					},
					UserLocation: WebSearchToolUserLocationParam{
						Type:     "approximate",
						Timezone: "Asia/Calcutta",
						Region:   "Tamil Nadu",
						Country:  "India",
						City:     "Chennai",
					},
				},
			},
			expRes: `{"type": "web_search", "filters": {"allowed_domains": ["example.com", "test.com"]}, "user_location": {"type": "approximate", "timezone": "Asia/Calcutta", "region": "Tamil Nadu", "country": "India", "city": "Chennai"}}`,
		},
		{
			name: "marshal mcp",
			input: ResponseToolUnion{
				OfMcp: &ToolMcpParam{
					Type:              "mcp",
					ServerLabel:       "test",
					Authorization:     "xxxxx",
					ServerDescription: "test server",
					ServerURL:         "https://test.com",
					AllowedTools: ToolMcpAllowedToolsUnionParam{
						OfMcpToolFilter: &ToolMcpAllowedToolsMcpToolFilterParam{
							ReadOnly:  ptr.To(false),
							ToolNames: []string{"create_file", "update_file"},
						},
					},
					ConnectorID: "connector_googledrive",
				},
			},
			expRes: `{"type": "mcp", "server_label": "test", "authorization": "xxxxx", "server_description": "test server", "server_url": "https://test.com", "allowed_tools": {"read_only": false, "tool_names": ["create_file", "update_file"]}, "connector_id": "connector_googledrive"}`,
		},
		{
			name: "marshal code interpreter",
			input: ResponseToolUnion{
				OfCodeInterpreter: &ToolCodeInterpreterParam{
					Type: "code_interpreter",
					Container: ToolCodeInterpreterContainerUnionParam{
						OfString: ptr.To("test"),
					},
				},
			},
			expRes: `{"type": "code_interpreter", "container": "test"}`,
		},
		{
			name: "marshal image generation",
			input: ResponseToolUnion{
				OfImageGeneration: &ToolImageGenerationParam{
					Type:         "image_generation",
					Size:         "1024x1024",
					Quality:      "high",
					OutputFormat: "jpeg",
					InputImageMask: ToolImageGenerationInputImageMaskParam{
						FileID:   "ab23d",
						ImageURL: "https://test.com",
					},
				},
			},
			expRes: `{"type": "image_generation", "size": "1024x1024", "quality": "high", "output_format": "jpeg", "input_image_mask": {"file_id": "ab23d", "image_url": "https://test.com"}}`,
		},
		{
			name: "marshal custom",
			input: ResponseToolUnion{
				OfCustom: &CustomToolParam{
					Type: "custom",
					Name: "custom_tool",
					Format: CustomToolInputFormatUnionParam{
						OfText: &CustomToolInputFormatTextParam{
							Type: "text",
						},
					},
				},
			},
			expRes: `{"type": "custom", "name": "custom_tool", "format": {"type": "text"}}`,
		},
		{
			name: "marshal local shell",
			input: ResponseToolUnion{
				OfLocalShell: &ToolLocalShellParam{
					Type: "local_shell",
				},
			},
			expRes: `{"type": "local_shell"}`,
		},
		{
			name: "marshal shell",
			input: ResponseToolUnion{
				OfShell: &FunctionShellToolParam{
					Type: "shell",
				},
			},
			expRes: `{"type": "shell"}`,
		},
		{
			name: "marshal web search preview",
			input: ResponseToolUnion{
				OfWebSearchPreview: &WebSearchPreviewToolParam{
					Type: "web_search_preview",
					UserLocation: WebSearchPreviewToolUserLocationParam{
						Type:     "approximate",
						Timezone: "Asia/Calcutta",
						Region:   "Tamil Nadu",
						Country:  "India",
						City:     "Chennai",
					},
					SearchContextSize: "low",
				},
			},
			expRes: `{"type": "web_search_preview", "search_context_size": "low", "user_location": {"type": "approximate", "timezone": "Asia/Calcutta", "region": "Tamil Nadu", "country": "India", "city": "Chennai"}}`,
		},
		{
			name: "marshal apply patch",
			input: ResponseToolUnion{
				OfApplyPatch: &ApplyPatchToolParam{
					Type: "apply_patch",
				},
			},
			expRes: `{"type": "apply_patch"}`,
		},
		{
			name: "marshal unknown type is emitted verbatim",
			input: ResponseToolUnion{
				OfUnknown: json.RawMessage(`{"type":"unknown_tool","execution":"client"}`),
			},
			expRes: `{"type":"unknown_tool","execution":"client"}`,
		},
		{
			name:   "marshal no field set",
			input:  ResponseToolUnion{},
			expErr: "no tool to marshal",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data, err := json.Marshal(tc.input)
			if tc.expErr != "" {
				require.ErrorContains(t, err, tc.expErr)
				return
			}
			require.NoError(t, err)
			require.JSONEq(t, tc.expRes, string(data))
		})
	}
}

func TestResponseToolUnionUnmarshalJSON(t *testing.T) {
	for _, tc := range []struct {
		name   string
		input  []byte
		expRes ResponseToolUnion
		expErr string
	}{
		{
			name:  "unmarshal function",
			input: []byte(`{"type": "function", "name": "test_func", "parameters": {"arg1": "test"}}`),
			expRes: ResponseToolUnion{
				OfFunction: &FunctionToolParam{
					Type:       "function",
					Name:       "test_func",
					Parameters: map[string]any{"arg1": "test"},
				},
			},
		},
		{
			name:  "unmarshal file search",
			input: []byte(`{"type": "file_search", "filters": {"type": "eq", "key": "test", "value": "value"}, "ranking_options": {"ranker": "auto", "score_threshold": 0.5}}`),
			expRes: ResponseToolUnion{
				OfFileSearch: &FileSearchToolParam{
					Type: "file_search",
					Filters: FileSearchToolFiltersUnionParam{
						OfComparisonFilter: &ComparisonFilterParam{
							Key:  "test",
							Type: "eq",
							Value: ComparisonFilterValueUnionParam{
								OfString: ptr.To("value"),
							},
						},
					},
					RankingOptions: FileSearchToolRankingOptionsParam{
						Ranker:         "auto",
						ScoreThreshold: ptr.To(0.5),
					},
				},
			},
		},
		{
			name:  "unmarshal computer tool",
			input: []byte(`{"type": "computer_use_preview", "display_height": 1080, "display_width": 1920, "environment": "linux"}`),
			expRes: ResponseToolUnion{
				OfComputerTool: &ComputerToolParam{
					Type:          "computer_use_preview",
					DisplayHeight: 1080,
					DisplayWidth:  1920,
					Environment:   "linux",
				},
			},
		},
		{
			name:  "unmarshal web search",
			input: []byte(`{"type": "web_search", "filters": {"allowed_domains": ["example.com", "test.com"]}, "user_location": {"type": "approximate", "timezone": "Asia/Calcutta", "region": "Tamil Nadu", "country": "India", "city": "Chennai"}}`),
			expRes: ResponseToolUnion{
				OfWebSearch: &WebSearchToolParam{
					Type: "web_search",
					Filters: WebSearchToolFiltersParam{
						AllowedDomains: []string{"example.com", "test.com"},
					},
					UserLocation: WebSearchToolUserLocationParam{
						Type:     "approximate",
						Timezone: "Asia/Calcutta",
						Region:   "Tamil Nadu",
						Country:  "India",
						City:     "Chennai",
					},
				},
			},
		},
		{
			name:  "unmarshal web search 2025",
			input: []byte(`{"type": "web_search_2025_08_26", "filters": {"allowed_domains": ["example.com", "test.com"]}, "user_location": {"type": "approximate", "timezone": "Asia/Calcutta", "region": "Tamil Nadu", "country": "India", "city": "Chennai"}}`),
			expRes: ResponseToolUnion{
				OfWebSearch: &WebSearchToolParam{
					Type: "web_search_2025_08_26",
					Filters: WebSearchToolFiltersParam{
						AllowedDomains: []string{"example.com", "test.com"},
					},
					UserLocation: WebSearchToolUserLocationParam{
						Type:     "approximate",
						Timezone: "Asia/Calcutta",
						Region:   "Tamil Nadu",
						Country:  "India",
						City:     "Chennai",
					},
				},
			},
		},
		{
			name:  "unmarshal mcp",
			input: []byte(`{"type": "mcp", "server_label": "test", "authorization": "xxxxx", "server_description": "test server", "server_url": "https://test.com", "allowed_tools": {"read_only": false, "tool_names": ["create_file", "update_file"]}, "connector_id": "connector_googledrive"}`),
			expRes: ResponseToolUnion{
				OfMcp: &ToolMcpParam{
					Type:              "mcp",
					ServerLabel:       "test",
					Authorization:     "xxxxx",
					ServerDescription: "test server",
					ServerURL:         "https://test.com",
					AllowedTools: ToolMcpAllowedToolsUnionParam{
						OfMcpToolFilter: &ToolMcpAllowedToolsMcpToolFilterParam{
							ReadOnly:  ptr.To(false),
							ToolNames: []string{"create_file", "update_file"},
						},
					},
					ConnectorID: "connector_googledrive",
				},
			},
		},
		{
			name:  "unmarshal code interpreter",
			input: []byte(`{"type": "code_interpreter", "container": "test"}`),
			expRes: ResponseToolUnion{
				OfCodeInterpreter: &ToolCodeInterpreterParam{
					Type: "code_interpreter",
					Container: ToolCodeInterpreterContainerUnionParam{
						OfString: ptr.To("test"),
					},
				},
			},
		},
		{
			name:  "unmarshal image generation",
			input: []byte(`{"type": "image_generation", "size": "1024x1024", "quality": "high", "output_format": "jpeg", "input_image_mask": {"file_id": "ab23d", "image_url": "https://test.com"}}`),
			expRes: ResponseToolUnion{
				OfImageGeneration: &ToolImageGenerationParam{
					Type:         "image_generation",
					Size:         "1024x1024",
					Quality:      "high",
					OutputFormat: "jpeg",
					InputImageMask: ToolImageGenerationInputImageMaskParam{
						FileID:   "ab23d",
						ImageURL: "https://test.com",
					},
				},
			},
		},
		{
			name:  "unmarshal custom",
			input: []byte(`{"type": "custom", "name": "custom_tool", "format": {"type": "text"}}`),
			expRes: ResponseToolUnion{
				OfCustom: &CustomToolParam{
					Type: "custom",
					Name: "custom_tool",
					Format: CustomToolInputFormatUnionParam{
						OfText: &CustomToolInputFormatTextParam{
							Type: "text",
						},
					},
				},
			},
		},
		{
			name:  "unmarshal shell",
			input: []byte(`{"type":"shell"}`),
			expRes: ResponseToolUnion{
				OfShell: &FunctionShellToolParam{
					Type: "shell",
				},
			},
		},
		{
			name:  "unmarshal local shell",
			input: []byte(`{"type":"local_shell"}`),
			expRes: ResponseToolUnion{
				OfLocalShell: &ToolLocalShellParam{
					Type: "local_shell",
				},
			},
		},
		{
			name:  "unmarshal web search preview",
			input: []byte(`{"type": "web_search_preview", "search_context_size": "low", "user_location": {"type": "approximate", "timezone": "Asia/Calcutta", "region": "Tamil Nadu", "country": "India", "city": "Chennai"}}`),
			expRes: ResponseToolUnion{
				OfWebSearchPreview: &WebSearchPreviewToolParam{
					Type: "web_search_preview",
					UserLocation: WebSearchPreviewToolUserLocationParam{
						Type:     "approximate",
						Timezone: "Asia/Calcutta",
						Region:   "Tamil Nadu",
						Country:  "India",
						City:     "Chennai",
					},
					SearchContextSize: "low",
				},
			},
		},
		{
			name:  "unmarshal web search preview 2025",
			input: []byte(`{"type": "web_search_preview_2025_03_11", "search_context_size": "low", "user_location": {"type": "approximate", "timezone": "Asia/Calcutta", "region": "Tamil Nadu", "country": "India", "city": "Chennai"}}`),
			expRes: ResponseToolUnion{
				OfWebSearchPreview: &WebSearchPreviewToolParam{
					Type: "web_search_preview_2025_03_11",
					UserLocation: WebSearchPreviewToolUserLocationParam{
						Type:     "approximate",
						Timezone: "Asia/Calcutta",
						Region:   "Tamil Nadu",
						Country:  "India",
						City:     "Chennai",
					},
					SearchContextSize: "low",
				},
			},
		},
		{
			name:  "unmarshal apply patch",
			input: []byte(`{"type":"apply_patch"}`),
			expRes: ResponseToolUnion{
				OfApplyPatch: &ApplyPatchToolParam{
					Type: "apply_patch",
				},
			},
		},
		{
			name:  "unmarshal unknown type is preserved",
			input: []byte(`{"type":"unknown_tool","execution":"client"}`),
			expRes: ResponseToolUnion{
				OfUnknown: json.RawMessage(`{"type":"unknown_tool","execution":"client"}`),
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var result ResponseToolUnion
			err := json.Unmarshal(tc.input, &result)
			if tc.expErr != "" {
				require.ErrorContains(t, err, tc.expErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.expRes, result)
		})
	}
}

func TestFileSearchToolFiltersUnionParamMarshalJSON(t *testing.T) {
	testCases := []struct {
		name   string
		filter FileSearchToolFiltersUnionParam
		expRes string
		expErr string
	}{
		{
			name: "comparison filter",
			filter: FileSearchToolFiltersUnionParam{
				OfComparisonFilter: &ComparisonFilterParam{
					Key:  "filename",
					Type: "eq",
					Value: ComparisonFilterValueUnionParam{
						OfString: ptr.To("test.txt"),
					},
				},
			},
			expRes: `{"type": "eq", "key": "filename", "value": "test.txt"}`,
		},
		{
			name: "comparison filter with array value",
			filter: FileSearchToolFiltersUnionParam{
				OfComparisonFilter: &ComparisonFilterParam{
					Key:  "filename",
					Type: "eq",
					Value: ComparisonFilterValueUnionParam{
						OfComparisonFilterValueArray: []ComparisonFilterValueArrayItemUnionParam{
							{OfString: ptr.To("test.txt")},
						},
					},
				},
			},
			expRes: `{"type": "eq", "key": "filename", "value": ["test.txt"]}`,
		},
		{
			name: "compound filter with and",
			filter: FileSearchToolFiltersUnionParam{
				OfCompoundFilter: &CompoundFilterParam{
					Type: "and",
					Filters: []ComparisonFilterParam{
						{
							Key:  "size",
							Type: "gt",
							Value: ComparisonFilterValueUnionParam{
								OfComparisonFilterValueArray: []ComparisonFilterValueArrayItemUnionParam{
									{OfFloat: ptr.To(1.0)},
								},
							},
						},
					},
				},
			},
			expRes: `{"type": "and", "filters": [{"type": "gt", "key": "size", "value": [1.0]}]}`,
		},
		{
			name: "compound filter with or",
			filter: FileSearchToolFiltersUnionParam{
				OfCompoundFilter: &CompoundFilterParam{
					Type: "or",
					Filters: []ComparisonFilterParam{
						{
							Key:  "status",
							Type: "eq",
							Value: ComparisonFilterValueUnionParam{
								OfString: ptr.To("active"),
							},
						},
					},
				},
			},
			expRes: `{"type": "or", "filters": [{"key": "status", "type": "eq", "value": "active"}]}`,
		},
		{
			name:   "no filter set",
			filter: FileSearchToolFiltersUnionParam{},
			expErr: "no filesearch filter to marshal",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			data, err := tc.filter.MarshalJSON()
			if tc.expErr != "" {
				require.ErrorContains(t, err, tc.expErr)
				require.Nil(t, data)
			} else {
				require.NoError(t, err)
				require.JSONEq(t, tc.expRes, string(data))
			}
		})
	}
}

func TestFileSearchToolFiltersUnionParamUnmarshalJSON(t *testing.T) {
	testCases := []struct {
		name     string
		jsonData string
		expRes   FileSearchToolFiltersUnionParam
		expErr   string
	}{
		{
			name:     "unmarshal compound filter with or",
			jsonData: `{"type": "or", "filters": [{"key": "status", "type": "eq", "value": "active"}]}`,
			expRes: FileSearchToolFiltersUnionParam{
				OfCompoundFilter: &CompoundFilterParam{
					Type: "or",
					Filters: []ComparisonFilterParam{
						{
							Key:  "status",
							Type: "eq",
							Value: ComparisonFilterValueUnionParam{
								OfString: ptr.To("active"),
							},
						},
					},
				},
			},
		},
		{
			name: "unmarshal compound filter with and",
			expRes: FileSearchToolFiltersUnionParam{
				OfCompoundFilter: &CompoundFilterParam{
					Type: "and",
					Filters: []ComparisonFilterParam{
						{
							Key:  "size",
							Type: "gt",
							Value: ComparisonFilterValueUnionParam{
								OfComparisonFilterValueArray: []ComparisonFilterValueArrayItemUnionParam{
									{OfFloat: ptr.To(1.0)},
								},
							},
						},
					},
				},
			},
			jsonData: `{"type": "and", "filters": [{"type": "gt", "key": "size", "value": [1.0]}]}`,
		},
		{
			name: "unmarshal comparison filter",
			expRes: FileSearchToolFiltersUnionParam{
				OfComparisonFilter: &ComparisonFilterParam{
					Key:  "filename",
					Type: "eq",
					Value: ComparisonFilterValueUnionParam{
						OfString: ptr.To("test.txt"),
					},
				},
			},
			jsonData: `{"type": "eq", "key": "filename", "value": "test.txt"}`,
		},
		{
			name: "unmarshal comparison filter with array value",
			expRes: FileSearchToolFiltersUnionParam{
				OfComparisonFilter: &ComparisonFilterParam{
					Key:  "filename",
					Type: "eq",
					Value: ComparisonFilterValueUnionParam{
						OfComparisonFilterValueArray: []ComparisonFilterValueArrayItemUnionParam{
							{OfString: ptr.To("test.txt")},
						},
					},
				},
			},
			jsonData: `{"type": "eq", "key": "filename", "value": ["test.txt"]}`,
		},
		{
			name:     "unmarshal compound filter with error",
			jsonData: `{"type": "or", "filters": [{"key": 23, "type": "eq", "value": "active"}]}`,
			expErr:   "cannot unmarshal filesearch tool filters as compound filter",
		},
		{
			name:     "unmarshal comparison filter with error",
			jsonData: `{"type": "eq", "key": 45, "value": "test.txt"}`,
			expErr:   "cannot unmarshal filesearch tool filters as comparison filter",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			var result FileSearchToolFiltersUnionParam
			err := json.Unmarshal([]byte(tc.jsonData), &result)
			if tc.expErr != "" {
				require.ErrorContains(t, err, tc.expErr)
			} else {
				require.NoError(t, err)
				require.Equal(t, tc.expRes, result)
			}
		})
	}
}

func TestComparisonFilterValueUnionParamMarshalJSON(t *testing.T) {
	testCases := []struct {
		name    string
		value   ComparisonFilterValueUnionParam
		expJSON []byte
		expErr  string
	}{
		{
			name:    "string value",
			value:   ComparisonFilterValueUnionParam{OfString: ptr.To("test")},
			expJSON: []byte(`"test"`),
		},
		{
			name:    "float value",
			value:   ComparisonFilterValueUnionParam{OfFloat: ptr.To(42.5)},
			expJSON: []byte(`42.5`),
		},
		{
			name:    "bool true value",
			value:   ComparisonFilterValueUnionParam{OfBool: ptr.To(true)},
			expJSON: []byte(`true`),
		},
		{
			name:    "bool false value",
			value:   ComparisonFilterValueUnionParam{OfBool: ptr.To(false)},
			expJSON: []byte(`false`),
		},
		{
			name: "array value",
			value: ComparisonFilterValueUnionParam{
				OfComparisonFilterValueArray: []ComparisonFilterValueArrayItemUnionParam{
					{OfString: ptr.To("item1")},
					{OfFloat: ptr.To(3.14)},
				},
			},
			expJSON: []byte(`["item1",3.14]`),
		},
		{
			name:   "empty value - no field set",
			value:  ComparisonFilterValueUnionParam{},
			expErr: "no value to marshal in comparison filter",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			data, err := tc.value.MarshalJSON()
			if tc.expErr != "" {
				require.ErrorContains(t, err, tc.expErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.expJSON, data)
		})
	}
}

func TestComparisonFilterValueUnionParamUnmarshalJSON(t *testing.T) {
	testCases := []struct {
		name   string
		in     []byte
		expVal *ComparisonFilterValueUnionParam
		expErr string
	}{
		{
			name: "string value",
			in:   []byte(`"test"`),
			expVal: &ComparisonFilterValueUnionParam{
				OfString: ptr.To("test"),
			},
		},
		{
			name: "empty string value",
			in:   []byte(`""`),
			expVal: &ComparisonFilterValueUnionParam{
				OfString: ptr.To(""),
			},
		},
		{
			name: "float value",
			in:   []byte(`42.5`),
			expVal: &ComparisonFilterValueUnionParam{
				OfFloat: ptr.To(42.5),
			},
		},
		{
			name: "integer value (unmarshals as float)",
			in:   []byte(`100`),
			expVal: &ComparisonFilterValueUnionParam{
				OfFloat: ptr.To(100.0),
			},
		},
		{
			name: "zero float value",
			in:   []byte(`0.0`),
			expVal: &ComparisonFilterValueUnionParam{
				OfFloat: ptr.To(0.0),
			},
		},
		{
			name: "bool true value",
			in:   []byte(`true`),
			expVal: &ComparisonFilterValueUnionParam{
				OfBool: ptr.To(true),
			},
		},
		{
			name: "bool false value",
			in:   []byte(`false`),
			expVal: &ComparisonFilterValueUnionParam{
				OfBool: ptr.To(false),
			},
		},
		{
			name: "array with strings",
			in:   []byte(`["item1","item2"]`),
			expVal: &ComparisonFilterValueUnionParam{
				OfComparisonFilterValueArray: []ComparisonFilterValueArrayItemUnionParam{
					{OfString: ptr.To("item1")},
					{OfString: ptr.To("item2")},
				},
			},
		},
		{
			name: "array with floats",
			in:   []byte(`[1.5,2.5,3.5]`),
			expVal: &ComparisonFilterValueUnionParam{
				OfComparisonFilterValueArray: []ComparisonFilterValueArrayItemUnionParam{
					{OfFloat: ptr.To(1.5)},
					{OfFloat: ptr.To(2.5)},
					{OfFloat: ptr.To(3.5)},
				},
			},
		},
		{
			name: "array with mixed strings and floats",
			in:   []byte(`["item",42.5]`),
			expVal: &ComparisonFilterValueUnionParam{
				OfComparisonFilterValueArray: []ComparisonFilterValueArrayItemUnionParam{
					{OfString: ptr.To("item")},
					{OfFloat: ptr.To(42.5)},
				},
			},
		},
		{
			name: "empty array",
			in:   []byte(`[]`),
			expVal: &ComparisonFilterValueUnionParam{
				OfComparisonFilterValueArray: []ComparisonFilterValueArrayItemUnionParam{},
			},
		},
		{
			name:   "invalid JSON - object",
			in:     []byte(`{}`),
			expErr: "cannot unmarshal comparison filter value",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			var result ComparisonFilterValueUnionParam
			err := result.UnmarshalJSON(tc.in)
			if tc.expErr != "" {
				require.ErrorContains(t, err, tc.expErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.expVal, &result)
		})
	}
}

func TestComparisonFilterValueArrayItemUnionParamMarshalJSON(t *testing.T) {
	testCases := []struct {
		name    string
		value   ComparisonFilterValueArrayItemUnionParam
		expJSON []byte
		expErr  string
	}{
		{
			name:    "string value",
			value:   ComparisonFilterValueArrayItemUnionParam{OfString: ptr.To("test")},
			expJSON: []byte(`"test"`),
		},
		{
			name:    "empty string value",
			value:   ComparisonFilterValueArrayItemUnionParam{OfString: ptr.To("")},
			expJSON: []byte(`""`),
		},
		{
			name:    "float value",
			value:   ComparisonFilterValueArrayItemUnionParam{OfFloat: ptr.To(42.5)},
			expJSON: []byte(`42.5`),
		},
		{
			name:    "zero float value",
			value:   ComparisonFilterValueArrayItemUnionParam{OfFloat: ptr.To(0.0)},
			expJSON: []byte(`0`),
		},
		{
			name:    "negative float value",
			value:   ComparisonFilterValueArrayItemUnionParam{OfFloat: ptr.To(-123.456)},
			expJSON: []byte(`-123.456`),
		},
		{
			name:   "empty value - no field set",
			value:  ComparisonFilterValueArrayItemUnionParam{},
			expErr: "no value to marshal in comparison filter value array",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			data, err := tc.value.MarshalJSON()
			if tc.expErr != "" {
				require.ErrorContains(t, err, tc.expErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.expJSON, data)
		})
	}
}

func TestComparisonFilterValueArrayItemUnionParamUnmarshalJSON(t *testing.T) {
	testCases := []struct {
		name   string
		in     []byte
		expVal *ComparisonFilterValueArrayItemUnionParam
		expErr string
	}{
		{
			name: "string value",
			in:   []byte(`"test"`),
			expVal: &ComparisonFilterValueArrayItemUnionParam{
				OfString: ptr.To("test"),
			},
		},
		{
			name: "empty string value",
			in:   []byte(`""`),
			expVal: &ComparisonFilterValueArrayItemUnionParam{
				OfString: ptr.To(""),
			},
		},
		{
			name: "string with special characters",
			in:   []byte(`"test\nvalue\twith\"quotes"`),
			expVal: &ComparisonFilterValueArrayItemUnionParam{
				OfString: ptr.To("test\nvalue\twith\"quotes"),
			},
		},
		{
			name: "float value",
			in:   []byte(`42.5`),
			expVal: &ComparisonFilterValueArrayItemUnionParam{
				OfFloat: ptr.To(42.5),
			},
		},
		{
			name: "integer value (unmarshals as float)",
			in:   []byte(`100`),
			expVal: &ComparisonFilterValueArrayItemUnionParam{
				OfFloat: ptr.To(100.0),
			},
		},
		{
			name: "zero float value",
			in:   []byte(`0`),
			expVal: &ComparisonFilterValueArrayItemUnionParam{
				OfFloat: ptr.To(0.0),
			},
		},
		{
			name: "negative float value",
			in:   []byte(`-99.99`),
			expVal: &ComparisonFilterValueArrayItemUnionParam{
				OfFloat: ptr.To(-99.99),
			},
		},
		{
			name: "large float value",
			in:   []byte(`1.7976931348623157e+308`),
			expVal: &ComparisonFilterValueArrayItemUnionParam{
				OfFloat: ptr.To(1.7976931348623157e+308),
			},
		},
		{
			name:   "invalid JSON - object",
			in:     []byte(`{}`),
			expErr: "cannot unmarshal comparison filter value array item",
		},
		{
			name:   "invalid JSON - array",
			in:     []byte(`[]`),
			expErr: "cannot unmarshal comparison filter value array item",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			var result ComparisonFilterValueArrayItemUnionParam
			err := result.UnmarshalJSON(tc.in)
			if tc.expErr != "" {
				require.ErrorContains(t, err, tc.expErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.expVal, &result)
		})
	}
}

func TestToolMcpAllowedToolsUnionParamMarshalJSON(t *testing.T) {
	tests := []struct {
		name     string
		input    ToolMcpAllowedToolsUnionParam
		expected []byte
		expErr   string
	}{
		{
			name: "marshal string slice",
			input: ToolMcpAllowedToolsUnionParam{
				OfMcpAllowedTools: []string{"tool1", "tool2", "tool3"},
			},
			expected: []byte(`["tool1","tool2","tool3"]`),
		},
		{
			name: "marshal empty string slice",
			input: ToolMcpAllowedToolsUnionParam{
				OfMcpAllowedTools: []string{},
			},
			expected: []byte(`[]`),
		},
		{
			name: "marshal tool filter with read_only",
			input: ToolMcpAllowedToolsUnionParam{
				OfMcpToolFilter: &ToolMcpAllowedToolsMcpToolFilterParam{
					ReadOnly:  ptr.To(true),
					ToolNames: []string{"readonly_tool"},
				},
			},
			expected: []byte(`{"read_only":true,"tool_names":["readonly_tool"]}`),
		},
		{
			name: "marshal tool filter without read_only",
			input: ToolMcpAllowedToolsUnionParam{
				OfMcpToolFilter: &ToolMcpAllowedToolsMcpToolFilterParam{
					ToolNames: []string{"tool1", "tool2"},
				},
			},
			expected: []byte(`{"tool_names":["tool1","tool2"]}`),
		},
		{
			name: "marshal with neither field set",
			input: ToolMcpAllowedToolsUnionParam{
				OfMcpAllowedTools: nil,
				OfMcpToolFilter:   nil,
			},
			expErr: "no tools to marshal in mcp allowed tools",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			data, err := tc.input.MarshalJSON()
			if tc.expErr != "" {
				require.ErrorContains(t, err, tc.expErr)
				return
			}
			require.NoError(t, err)
			require.JSONEq(t, string(tc.expected), string(data))
		})
	}
}

func TestToolMcpAllowedToolsUnionParamUnmarshalJSON(t *testing.T) {
	tests := []struct {
		name   string
		input  []byte
		expect ToolMcpAllowedToolsUnionParam
		expErr string
	}{
		{
			name:  "unmarshal string slice",
			input: []byte(`["tool1","tool2"]`),
			expect: ToolMcpAllowedToolsUnionParam{
				OfMcpAllowedTools: []string{"tool1", "tool2"},
			},
		},
		{
			name:  "unmarshal empty string slice",
			input: []byte(`[]`),
			expect: ToolMcpAllowedToolsUnionParam{
				OfMcpAllowedTools: []string{},
			},
		},
		{
			name:  "unmarshal tool filter with read_only true",
			input: []byte(`{"read_only":true,"tool_names":["readonly_tool"]}`),
			expect: ToolMcpAllowedToolsUnionParam{
				OfMcpToolFilter: &ToolMcpAllowedToolsMcpToolFilterParam{
					ReadOnly:  ptr.To(true),
					ToolNames: []string{"readonly_tool"},
				},
			},
		},
		{
			name:  "unmarshal tool filter with read_only false",
			input: []byte(`{"read_only":false,"tool_names":["modify_tool"]}`),
			expect: ToolMcpAllowedToolsUnionParam{
				OfMcpToolFilter: &ToolMcpAllowedToolsMcpToolFilterParam{
					ReadOnly:  ptr.To(false),
					ToolNames: []string{"modify_tool"},
				},
			},
		},
		{
			name:  "unmarshal tool filter without read_only",
			input: []byte(`{"tool_names":["tool1","tool2"]}`),
			expect: ToolMcpAllowedToolsUnionParam{
				OfMcpToolFilter: &ToolMcpAllowedToolsMcpToolFilterParam{
					ReadOnly:  nil,
					ToolNames: []string{"tool1", "tool2"},
				},
			},
		},
		{
			name:  "unmarshal empty tool filter",
			input: []byte(`{}`),
			expect: ToolMcpAllowedToolsUnionParam{
				OfMcpToolFilter: &ToolMcpAllowedToolsMcpToolFilterParam{
					ReadOnly:  nil,
					ToolNames: nil,
				},
			},
		},
		{
			name:   "unmarshal invalid JSON",
			input:  []byte(`invalid`),
			expErr: "cannot unmarshal Mcp allowed tools",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var result ToolMcpAllowedToolsUnionParam
			err := result.UnmarshalJSON(tc.input)
			if tc.expErr != "" {
				require.ErrorContains(t, err, tc.expErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.expect, result)
		})
	}
}

func TestToolMcpRequireApprovalUnionParamMarshalJSON(t *testing.T) {
	tests := []struct {
		name     string
		input    ToolMcpRequireApprovalUnionParam
		expected []byte
		expErr   string
	}{
		{
			name: "marshal approval filter",
			input: ToolMcpRequireApprovalUnionParam{
				OfMcpToolApprovalFilter: &ToolMcpRequireApprovalMcpToolApprovalFilterParam{
					Always: ToolMcpRequireApprovalMcpToolApprovalFilterObjectParam{
						ReadOnly:  ptr.To(true),
						ToolNames: []string{"readonly"},
					},
				},
			},
			expected: []byte(`{"always":{"read_only":true,"tool_names":["readonly"]}}`),
		},
		{
			name: "marshal approval setting string",
			input: ToolMcpRequireApprovalUnionParam{
				OfMcpToolApprovalSetting: ptr.To("always"),
			},
			expected: []byte(`"always"`),
		},
		{
			name: "marshal approval filter with never",
			input: ToolMcpRequireApprovalUnionParam{
				OfMcpToolApprovalFilter: &ToolMcpRequireApprovalMcpToolApprovalFilterParam{
					Never: ToolMcpRequireApprovalMcpToolApprovalFilterObjectParam{
						ReadOnly:  ptr.To(true),
						ToolNames: []string{"readonly"},
					},
				},
			},
			expected: []byte(`{"never":{"read_only":true,"tool_names":["readonly"]}}`),
		},
		{
			name: "marshal approval filter with never & always",
			input: ToolMcpRequireApprovalUnionParam{
				OfMcpToolApprovalFilter: &ToolMcpRequireApprovalMcpToolApprovalFilterParam{
					Always: ToolMcpRequireApprovalMcpToolApprovalFilterObjectParam{
						ReadOnly:  ptr.To(false),
						ToolNames: []string{"create"},
					},
					Never: ToolMcpRequireApprovalMcpToolApprovalFilterObjectParam{
						ReadOnly:  ptr.To(true),
						ToolNames: []string{"readonly"},
					},
				},
			},
			expected: []byte(`{"never":{"read_only":true,"tool_names":["readonly"]}, "always":{"read_only":false,"tool_names":["create"]}}`),
		},
		{
			name: "marshal with neither field set",
			input: ToolMcpRequireApprovalUnionParam{
				OfMcpToolApprovalFilter:  nil,
				OfMcpToolApprovalSetting: nil,
			},
			expErr: "no tool to marshal in mcp require approval",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			data, err := tc.input.MarshalJSON()
			if tc.expErr != "" {
				require.ErrorContains(t, err, tc.expErr)
				return
			}
			require.NoError(t, err)
			require.JSONEq(t, string(tc.expected), string(data))
		})
	}
}

func TestToolMcpRequireApprovalUnionParamUnmarshalJSON(t *testing.T) {
	tests := []struct {
		name   string
		input  []byte
		expect ToolMcpRequireApprovalUnionParam
		expErr string
	}{
		{
			name:  "unmarshal approval setting always",
			input: []byte(`"always"`),
			expect: ToolMcpRequireApprovalUnionParam{
				OfMcpToolApprovalSetting: ptr.To("always"),
			},
		},
		{
			name:  "unmarshal approval filter with always",
			input: []byte(`{"always":{"read_only":true,"tool_names":["readonly"]}}`),
			expect: ToolMcpRequireApprovalUnionParam{
				OfMcpToolApprovalFilter: &ToolMcpRequireApprovalMcpToolApprovalFilterParam{
					Always: ToolMcpRequireApprovalMcpToolApprovalFilterObjectParam{
						ReadOnly:  ptr.To(true),
						ToolNames: []string{"readonly"},
					},
				},
			},
		},
		{
			name:  "unmarshal approval filter with never",
			input: []byte(`{"never":{"read_only":false,"tool_names":["modify_tool"]}}`),
			expect: ToolMcpRequireApprovalUnionParam{
				OfMcpToolApprovalFilter: &ToolMcpRequireApprovalMcpToolApprovalFilterParam{
					Never: ToolMcpRequireApprovalMcpToolApprovalFilterObjectParam{
						ReadOnly:  ptr.To(false),
						ToolNames: []string{"modify_tool"},
					},
				},
			},
		},
		{
			name: "unmarshal approval filter with never & always",
			expect: ToolMcpRequireApprovalUnionParam{
				OfMcpToolApprovalFilter: &ToolMcpRequireApprovalMcpToolApprovalFilterParam{
					Always: ToolMcpRequireApprovalMcpToolApprovalFilterObjectParam{
						ReadOnly:  ptr.To(false),
						ToolNames: []string{"create"},
					},
					Never: ToolMcpRequireApprovalMcpToolApprovalFilterObjectParam{
						ReadOnly:  ptr.To(true),
						ToolNames: []string{"readonly"},
					},
				},
			},
			input: []byte(`{"never":{"read_only":true,"tool_names":["readonly"]}, "always":{"read_only":false,"tool_names":["create"]}}`),
		},
		{
			name:  "unmarshal empty filter object",
			input: []byte(`{}`),
			expect: ToolMcpRequireApprovalUnionParam{
				OfMcpToolApprovalFilter: &ToolMcpRequireApprovalMcpToolApprovalFilterParam{},
			},
		},
		{
			name:   "unmarshal invalid JSON",
			input:  []byte(`invalid`),
			expErr: "cannot unmarshal Mcp require approval",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var result ToolMcpRequireApprovalUnionParam
			err := result.UnmarshalJSON(tc.input)
			if tc.expErr != "" {
				require.ErrorContains(t, err, tc.expErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, &tc.expect, &result)
		})
	}
}

func TestToolCodeInterpreterContainerUnionParamMarshalJSON(t *testing.T) {
	tests := []struct {
		name     string
		input    ToolCodeInterpreterContainerUnionParam
		expected []byte
		expErr   string
	}{
		{
			name: "marshal string container",
			input: ToolCodeInterpreterContainerUnionParam{
				OfString: ptr.To("auto"),
			},
			expected: []byte(`"auto"`),
		},
		{
			name: "marshal auto param with type",
			input: ToolCodeInterpreterContainerUnionParam{
				OfCodeInterpreterToolAuto: &ToolCodeInterpreterContainerCodeInterpreterContainerAutoParam{
					Type: "auto",
				},
			},
			expected: []byte(`{"type":"auto"}`),
		},
		{
			name: "marshal auto param with memory limit and files",
			input: ToolCodeInterpreterContainerUnionParam{
				OfCodeInterpreterToolAuto: &ToolCodeInterpreterContainerCodeInterpreterContainerAutoParam{
					Type:        "auto",
					MemoryLimit: "4g",
					FileIDs:     []string{"file1", "file2"},
				},
			},
			expected: []byte(`{"memory_limit":"4g","file_ids":["file1","file2"],"type":"auto"}`),
		},
		{
			name: "marshal with neither field set",
			input: ToolCodeInterpreterContainerUnionParam{
				OfString:                  nil,
				OfCodeInterpreterToolAuto: nil,
			},
			expErr: "no container to marshal in code interpreter",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			data, err := tc.input.MarshalJSON()
			if tc.expErr != "" {
				require.ErrorContains(t, err, tc.expErr)
				return
			}
			require.NoError(t, err)
			require.JSONEq(t, string(tc.expected), string(data))
		})
	}
}

func TestToolCodeInterpreterContainerUnionParamUnmarshalJSON(t *testing.T) {
	tests := []struct {
		name   string
		input  []byte
		expect ToolCodeInterpreterContainerUnionParam
		expErr string
	}{
		{
			name:  "unmarshal string",
			input: []byte(`"auto"`),
			expect: ToolCodeInterpreterContainerUnionParam{
				OfString: ptr.To("auto"),
			},
		},
		{
			name:  "unmarshal auto param with type",
			input: []byte(`{"type":"auto"}`),
			expect: ToolCodeInterpreterContainerUnionParam{
				OfCodeInterpreterToolAuto: &ToolCodeInterpreterContainerCodeInterpreterContainerAutoParam{
					Type: "auto",
				},
			},
		},
		{
			name:  "unmarshal auto param with all fields",
			input: []byte(`{"memory_limit":"4g","file_ids":["file1","file2"],"type":"auto"}`),
			expect: ToolCodeInterpreterContainerUnionParam{
				OfCodeInterpreterToolAuto: &ToolCodeInterpreterContainerCodeInterpreterContainerAutoParam{
					Type:        "auto",
					MemoryLimit: "4g",
					FileIDs:     []string{"file1", "file2"},
				},
			},
		},
		{
			name:  "unmarshal empty auto param",
			input: []byte(`{}`),
			expect: ToolCodeInterpreterContainerUnionParam{
				OfCodeInterpreterToolAuto: &ToolCodeInterpreterContainerCodeInterpreterContainerAutoParam{},
			},
		},
		{
			name:   "unmarshal invalid JSON",
			input:  []byte(`invalid`),
			expErr: "cannot unmarshal code interpreter container",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var result ToolCodeInterpreterContainerUnionParam
			err := result.UnmarshalJSON(tc.input)
			if tc.expErr != "" {
				require.ErrorContains(t, err, tc.expErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.expect, result)
		})
	}
}

func TestCustomToolInputFormatUnionParamMarshalJSON(t *testing.T) {
	tests := []struct {
		name     string
		input    CustomToolInputFormatUnionParam
		expected []byte
		expErr   string
	}{
		{
			name: "marshal text format with type",
			input: CustomToolInputFormatUnionParam{
				OfText: &CustomToolInputFormatTextParam{
					Type: "text",
				},
			},
			expected: []byte(`{"type":"text"}`),
		},
		{
			name: "marshal grammar format with type",
			input: CustomToolInputFormatUnionParam{
				OfGrammar: &CustomToolInputFormatGrammarParam{
					Type:       "grammar",
					Definition: "start: \"hello\" | \"hi\"",
					Syntax:     "lark",
				},
			},
			expected: []byte(`{"definition":"start: \"hello\" | \"hi\"","syntax":"lark","type":"grammar"}`),
		},
		{
			name: "marshal with neither field set",
			input: CustomToolInputFormatUnionParam{
				OfText:    nil,
				OfGrammar: nil,
			},
			expErr: "no format to marshal in CustomToolInputFormatUnionParam",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			data, err := tc.input.MarshalJSON()
			if tc.expErr != "" {
				require.ErrorContains(t, err, tc.expErr)
				return
			}
			require.NoError(t, err)
			require.JSONEq(t, string(tc.expected), string(data))
		})
	}
}

func TestCustomToolInputFormatUnionParamUnmarshalJSON(t *testing.T) {
	tests := []struct {
		name   string
		input  []byte
		expect CustomToolInputFormatUnionParam
		expErr string
	}{
		{
			name:  "unmarshal text format",
			input: []byte(`{"type":"text"}`),
			expect: CustomToolInputFormatUnionParam{
				OfText: &CustomToolInputFormatTextParam{
					Type: "text",
				},
			},
		},
		{
			name:  "unmarshal grammar format",
			input: []byte(`{"type":"grammar","definition":"start: value","syntax":"lark"}`),
			expect: CustomToolInputFormatUnionParam{
				OfGrammar: &CustomToolInputFormatGrammarParam{
					Type:       "grammar",
					Definition: "start: value",
					Syntax:     "lark",
				},
			},
		},
		{
			name:  "unmarshal grammar with regex syntax",
			input: []byte(`{"type":"grammar","definition":"[a-z]+","syntax":"regex"}`),
			expect: CustomToolInputFormatUnionParam{
				OfGrammar: &CustomToolInputFormatGrammarParam{
					Type:       "grammar",
					Definition: "[a-z]+",
					Syntax:     "regex",
				},
			},
		},
		{
			name:   "unmarshal invalid type",
			input:  []byte(`{"type":""}`),
			expErr: "invalid format type in custom tool input format",
		},
		{
			name:   "unmarshal missing type",
			input:  []byte(`{"definition":"test"}`),
			expErr: "invalid format type in custom tool input format",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var result CustomToolInputFormatUnionParam
			err := result.UnmarshalJSON(tc.input)
			if tc.expErr != "" {
				require.ErrorContains(t, err, tc.expErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.expect, result)
		})
	}
}

func TestResponseNewParamsConversationUnionMarshalJSON(t *testing.T) {
	tests := []struct {
		name     string
		input    ResponseNewParamsConversationUnion
		expected []byte
		expErr   string
	}{
		{
			name: "marshal string conversation ID",
			input: ResponseNewParamsConversationUnion{
				OfString: ptr.To("conv-123"),
			},
			expected: []byte(`"conv-123"`),
		},
		{
			name: "marshal conversation object",
			input: ResponseNewParamsConversationUnion{
				OfConversationObject: &ResponseConversationParam{
					ID: "conv-456",
				},
			},
			expected: []byte(`{"id":"conv-456"}`),
		},
		{
			name: "marshal with neither field set",
			input: ResponseNewParamsConversationUnion{
				OfString:             nil,
				OfConversationObject: nil,
			},
			expErr: "no conversation parameter to marshal",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			data, err := tc.input.MarshalJSON()
			if tc.expErr != "" {
				require.ErrorContains(t, err, tc.expErr)
				return
			}
			require.NoError(t, err)
			require.JSONEq(t, string(tc.expected), string(data))
		})
	}
}

func TestResponseNewParamsConversationUnionUnmarshalJSON(t *testing.T) {
	tests := []struct {
		name   string
		input  []byte
		expect ResponseNewParamsConversationUnion
		expErr string
	}{
		{
			name:  "unmarshal string conversation ID",
			input: []byte(`"conv-123"`),
			expect: ResponseNewParamsConversationUnion{
				OfString: ptr.To("conv-123"),
			},
		},
		{
			name:  "unmarshal conversation object",
			input: []byte(`{"id":"conv-456"}`),
			expect: ResponseNewParamsConversationUnion{
				OfConversationObject: &ResponseConversationParam{
					ID: "conv-456",
				},
			},
		},
		{
			name:   "unmarshal invalid",
			input:  []byte(`1234`),
			expErr: "cannot unmarshal conversation parameter",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var result ResponseNewParamsConversationUnion
			err := result.UnmarshalJSON(tc.input)
			if tc.expErr != "" {
				require.ErrorContains(t, err, tc.expErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.expect, result)
		})
	}
}

func TestResponseNewParamsInputUnionMarshalJSON(t *testing.T) {
	tests := []struct {
		name     string
		input    ResponseNewParamsInputUnion
		expected []byte
		expErr   string
	}{
		{
			name: "marshal string input",
			input: ResponseNewParamsInputUnion{
				OfString: ptr.To("input text"),
			},
			expected: []byte(`"input text"`),
		},
		{
			name: "marshal empty input list",
			input: ResponseNewParamsInputUnion{
				OfInputItemList: []ResponseInputItemUnionParam{},
			},
			expected: []byte(`[]`),
		},
		{
			name: "marshal input list",
			input: ResponseNewParamsInputUnion{
				OfInputItemList: []ResponseInputItemUnionParam{
					{
						OfMessage: &EasyInputMessageParam{
							Type: "message",
							Role: "user",
							Content: EasyInputMessageContentUnionParam{
								OfString: ptr.To("Hi"),
							},
						},
					},
				},
			},
			expected: []byte(`[{"type": "message", "role": "user", "content": "Hi"}]`),
		},
		{
			name: "marshal with neither field set",
			input: ResponseNewParamsInputUnion{
				OfString:        nil,
				OfInputItemList: nil,
			},
			expErr: "no response input to marshal",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			data, err := tc.input.MarshalJSON()
			if tc.expErr != "" {
				require.ErrorContains(t, err, tc.expErr)
				return
			}
			require.NoError(t, err)
			require.JSONEq(t, string(tc.expected), string(data))
		})
	}
}

func TestResponseNewParamsInputUnionUnmarshalJSON(t *testing.T) {
	tests := []struct {
		name   string
		input  []byte
		expect ResponseNewParamsInputUnion
		expErr string
	}{
		{
			name:  "unmarshal string input",
			input: []byte(`"input text"`),
			expect: ResponseNewParamsInputUnion{
				OfString:        ptr.To("input text"),
				OfInputItemList: nil,
			},
		},
		{
			name:  "unmarshal empty input list",
			input: []byte(`[]`),
			expect: ResponseNewParamsInputUnion{
				OfString:        nil,
				OfInputItemList: []ResponseInputItemUnionParam{},
			},
		},
		{
			name: "marshal input list",
			expect: ResponseNewParamsInputUnion{
				OfInputItemList: []ResponseInputItemUnionParam{
					{
						OfMessage: &EasyInputMessageParam{
							Type: "message",
							Role: "user",
							Content: EasyInputMessageContentUnionParam{
								OfString: ptr.To("Hi"),
							},
						},
					},
				},
			},
			input: []byte(`[{"type": "message", "role": "user", "content": "Hi"}]`),
		},
		{
			name:   "unmarshal invalid type",
			input:  []byte(`{"invalid": "data"}`),
			expErr: "mismatched type with value",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var result ResponseNewParamsInputUnion
			err := result.UnmarshalJSON(tc.input)
			if tc.expErr != "" {
				require.ErrorContains(t, err, tc.expErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.expect, result)
		})
	}
}

func TestResponseInputItemUnionParamMarshalJSON(t *testing.T) {
	tests := []struct {
		name   string
		input  ResponseInputItemUnionParam
		expRes []byte
		expErr string
	}{
		{
			name:   "marshal with neither field set",
			input:  ResponseInputItemUnionParam{},
			expErr: "no input item to marshal",
		},
		{
			name: "marshal message",
			input: ResponseInputItemUnionParam{
				OfMessage: &EasyInputMessageParam{
					Role: "user",
					Type: "message",
					Content: EasyInputMessageContentUnionParam{
						OfString: ptr.To("Hi"),
					},
				},
			},
			expRes: []byte(`{"type": "message", "role": "user", "content": "Hi"}`),
		},
		{
			name: "marshal input_message",
			input: ResponseInputItemUnionParam{
				OfInputMessage: &ResponseInputItemMessageParam{
					ID:     "resp-123",
					Type:   "message",
					Role:   "developer",
					Status: "completed",
					Content: []ResponseInputContentUnionParam{
						{OfInputText: &ResponseInputTextParam{Text: "Hello!", Type: "input_text"}},
					},
				},
			},
			expRes: []byte(`{"type": "message", "id": "resp-123", "role": "developer", "status": "completed", "content": [{"text": "Hello!", "type": "input_text"}]}`),
		},
		{
			name: "marshal output_message",
			input: ResponseInputItemUnionParam{
				OfOutputMessage: &ResponseOutputMessage{
					Type: "message",
					Role: "assistant",
					Content: ResponseOutputMessageContentUnion{
						OfContentArray: []ResponseOutputMessageContentArrayUnion{
							{OfOutputText: &ResponseOutputTextParam{Text: "Hello! How can I assist you ?", Type: "output_text"}},
						},
					},
					Status: "completed",
					ID:     "resp-123",
				},
			},
			expRes: []byte(`{"type": "message", "role": "assistant", "status": "completed", "id": "resp-123", "content": [{"text": "Hello! How can I assist you ?", "type": "output_text"}]}`),
		},
		{
			name: "marshal agent_message",
			input: ResponseInputItemUnionParam{
				OfAgentMessage: ptr.To(json.RawMessage(`{"type":"agent_message","role":"agent","content":[{"type":"text","text":"prior context"}]}`)),
			},
			expRes: []byte(`{"type":"agent_message","role":"agent","content":[{"type":"text","text":"prior context"}]}`),
		},
		{
			name: "marshal agent_reasoning",
			input: ResponseInputItemUnionParam{
				OfAgentReasoning: ptr.To(json.RawMessage(`{"type":"agent_reasoning","summary":"prior reasoning"}`)),
			},
			expRes: []byte(`{"type":"agent_reasoning","summary":"prior reasoning"}`),
		},
		{
			name: "marshal file_search_call",
			input: ResponseInputItemUnionParam{
				OfFileSearchCall: &ResponseFileSearchToolCall{
					Type: "file_search_call",
					ID:   "call-123",
					Results: []ResponseFileSearchToolCallResultParam{
						{FileID: "file-2d", Filename: "deep_research_blog.pdf"},
					},
					Queries: []string{"What is deep research?"},
				},
			},
			expRes: []byte(`{"type": "file_search_call", "id": "call-123", "queries": ["What is deep research?"], "results": [{"file_id": "file-2d", "filename": "deep_research_blog.pdf"}]}`),
		},
		{
			name: "marshal computer_call",
			input: ResponseInputItemUnionParam{
				OfComputerCall: &ResponseComputerToolCall{
					Type:   "computer_call",
					CallID: "call-456",
					ID:     "rs-123",
					Action: ResponseComputerToolCallActionUnionParam{
						OfClick: &ResponseComputerToolCallActionClickParam{
							Button: "left",
							X:      100,
							Y:      200,
							Type:   "click",
						},
					},
				},
			},
			expRes: []byte(`{"type": "computer_call", "call_id": "call-456", "id": "rs-123", "action": {"type": "click", "button": "left", "x": 100, "y": 200}}`),
		},
		{
			name: "marshal computer_call_output",
			input: ResponseInputItemUnionParam{
				OfComputerCallOutput: &ResponseInputItemComputerCallOutputParam{
					Type:   "computer_call_output",
					ID:     "rs-123",
					CallID: "call-456",
					Output: ResponseComputerToolCallOutputScreenshotParam{
						Type:     "computer_screenshot",
						ImageURL: "data:image/png;base64,screenshot_base64",
					},
				},
			},
			expRes: []byte(`{"type": "computer_call_output", "id": "rs-123", "call_id": "call-456", "output": {"type": "computer_screenshot", "image_url": "data:image/png;base64,screenshot_base64"}}`),
		},
		{
			name: "marshal web_search_call",
			input: ResponseInputItemUnionParam{
				OfWebSearchCall: &ResponseFunctionWebSearch{
					Type: "web_search_call",
					ID:   "call-123",
					Action: ResponseFunctionWebSearchActionUnionParam{
						OfSearch: &ResponseFunctionWebSearchActionSearchParam{
							Type:  "search",
							Query: "What is deep research?",
							Sources: []ResponseFunctionWebSearchActionSearchSourceParam{
								{Type: "url", URL: "https://example.com"},
							},
						},
					},
				},
			},
			expRes: []byte(`{"type": "web_search_call", "id": "call-123", "action": {"type": "search", "query": "What is deep research?", "sources": [{"type": "url", "url": "https://example.com"}]}}`),
		},
		{
			name: "marshal function_call",
			input: ResponseInputItemUnionParam{
				OfFunctionCall: &ResponseFunctionToolCall{
					Type:      "function_call",
					Name:      "test_function",
					Namespace: "billing",
					CallID:    "call-789",
					ID:        "rs-123",
					Arguments: `{"arg1": "value"}`,
				},
			},
			expRes: []byte(`{"type": "function_call", "name": "test_function", "namespace": "billing", "call_id": "call-789", "id": "rs-123", "arguments": "{\"arg1\": \"value\"}"}`),
		},
		{
			name: "marshal function_call_output",
			input: ResponseInputItemUnionParam{
				OfFunctionCallOutput: &ResponseInputItemFunctionCallOutputParam{
					Type:   "function_call_output",
					CallID: "call-789",
					ID:     "rs-123",
					Output: ResponseInputItemFunctionCallOutputOutputUnionParam{
						OfString: ptr.To("output"),
					},
				},
			},
			expRes: []byte(`{"type": "function_call_output", "call_id": "call-789", "id": "rs-123", "output": "output"}`),
		},
		{
			name: "marshal tool_search_call",
			input: ResponseInputItemUnionParam{
				OfToolSearchCall: &ResponseToolSearchCall{
					Type:      "tool_search_call",
					ID:        "tsc-123",
					Status:    "completed",
					Arguments: map[string]any{"paths": []string{"billing"}},
					CallID:    nil,
					Execution: "server",
				},
			},
			expRes: []byte(`{"type":"tool_search_call","id":"tsc-123","status":"completed","arguments":{"paths":["billing"]},"call_id":null,"execution":"server"}`),
		},
		{
			name: "marshal tool_search_output",
			input: ResponseInputItemUnionParam{
				OfToolSearchOutput: &ResponseToolSearchOutput{
					Type:      "tool_search_output",
					ID:        "tso-123",
					Status:    "completed",
					CallID:    nil,
					Execution: "server",
					Tools: []ResponseToolUnion{
						{
							OfNamespace: &NamespaceToolParam{
								Type:        "namespace",
								Name:        "billing",
								Description: "Billing tools.",
								Tools: []NamespaceToolToolUnionParam{
									{
										OfFunction: &NamespaceToolToolFunctionParam{
											Type:         "function",
											Name:         "get_invoice",
											Description:  "Get invoice details by invoice ID.",
											DeferLoading: ptr.To(true),
										},
									},
								},
							},
						},
					},
				},
			},
			expRes: []byte(`{"type":"tool_search_output","id":"tso-123","status":"completed","call_id":null,"execution":"server","tools":[{"type":"namespace","name":"billing","description":"Billing tools.","tools":[{"type":"function","name":"get_invoice","description":"Get invoice details by invoice ID.","defer_loading":true}]}]}`),
		},
		{
			name: "marshal reasoning",
			input: ResponseInputItemUnionParam{
				OfReasoning: &ResponseReasoningItem{
					Type: "reasoning",
					ID:   "reasoning-001",
					Content: []ResponseReasoningItemContentParam{
						{Text: "the capital of France is Paris.", Type: "reasoning_text"},
					},
					Summary: []ResponseReasoningItemSummaryParam{
						{Text: "looking at a straightforward question: the capital of France is Paris", Type: "summary_text"},
					},
				},
			},
			expRes: []byte(`{"type": "reasoning", "id": "reasoning-001", "content": [{"text": "the capital of France is Paris.", "type": "reasoning_text"}], "summary": [{"text": "looking at a straightforward question: the capital of France is Paris", "type": "summary_text"}]}`),
		},
		{
			name: "marshal compaction",
			input: ResponseInputItemUnionParam{
				OfCompaction: &ResponseCompactionItemParam{
					Type:             "compaction",
					ID:               "resp-123",
					EncryptedContent: "encrypted_content",
				},
			},
			expRes: []byte(`{"type": "compaction", "id": "resp-123", "encrypted_content": "encrypted_content"}`),
		},
		{
			name: "marshal image_generation_call",
			input: ResponseInputItemUnionParam{
				OfImageGenerationCall: &ResponseInputItemImageGenerationCallParam{
					Type:   "image_generation_call",
					ID:     "rs-123",
					Status: "completed",
					Result: "data:image/png;base64,encoded_image",
				},
			},
			expRes: []byte(`{"type": "image_generation_call", "id": "rs-123", "status": "completed", "result": "data:image/png;base64,encoded_image"}`),
		},
		{
			name: "marshal code_interpreter_call",
			input: ResponseInputItemUnionParam{
				OfCodeInterpreterCall: &ResponseCodeInterpreterToolCallParam{
					Type:        "code_interpreter_call",
					ID:          "resp-123",
					ContainerID: "cntr_320",
					Status:      "completed",
					Outputs: []ResponseCodeInterpreterToolCallOutputUnionParam{
						{OfLogs: &ResponseCodeInterpreterToolCallOutputLogsParam{
							Type: "logs",
							Logs: "log contents",
						}},
					},
				},
			},
			expRes: []byte(`{"type": "code_interpreter_call", "id": "resp-123", "container_id": "cntr_320", "status": "completed", "outputs": [{"type": "logs", "logs": "log contents"}]}`),
		},
		{
			name: "marshal local_shell_call",
			input: ResponseInputItemUnionParam{
				OfLocalShellCall: &ResponseInputItemLocalShellCallParam{
					Type:   "local_shell_call",
					CallID: "call-123",
					ID:     "rs-123",
					Status: "in_progress",
					Action: ResponseInputItemLocalShellCallActionParam{
						Type:    "exec",
						Command: []string{"ls", "-a"},
						Env:     map[string]string{"TEST": "test"},
					},
				},
			},
			expRes: []byte(`{"type": "local_shell_call", "call_id": "call-123", "id": "rs-123", "status": "in_progress", "action": {"type": "exec", "command": ["ls", "-a"], "env": {"TEST": "test"}}}`),
		},
		{
			name: "marshal local_shell_call_output",
			input: ResponseInputItemUnionParam{
				OfLocalShellCallOutput: &ResponseInputItemLocalShellCallOutputParam{
					Type:   "local_shell_call_output",
					ID:     "resp-123",
					Output: ".\n..\nDocuments\nDownloads",
				},
			},
			expRes: []byte(`{"type": "local_shell_call_output", "id": "resp-123", "output": ".\n..\nDocuments\nDownloads"}`),
		},
		{
			name: "marshal shell_call",
			input: ResponseInputItemUnionParam{
				OfShellCall: &ResponseInputItemShellCallParam{
					Type:   "shell_call",
					CallID: "call-123",
					ID:     "resp-123",
					Action: ResponseInputItemShellCallActionParam{
						Commands: []string{"ls", "-a"},
					},
				},
			},
			expRes: []byte(`{"type": "shell_call", "call_id": "call-123", "id": "resp-123", "action": {"commands": ["ls", "-a"]}}`),
		},
		{
			name: "marshal shell_call_output",
			input: ResponseInputItemUnionParam{
				OfShellCallOutput: &ResponseInputItemShellCallOutputParam{
					Type:   "shell_call_output",
					CallID: "call-123",
					ID:     "resp-123",
					Output: []ResponseFunctionShellCallOutputContentParam{
						{
							Stderr: "",
							Stdout: "Documents\nDownloads",
							Outcome: ResponseFunctionShellCallOutputContentOutcomeUnionParam{
								OfExit: &ResponseFunctionShellCallOutputOutputContentOutcomeExitParam{
									Type:     "exit",
									ExitCode: 0,
								},
							},
						},
					},
				},
			},
			expRes: []byte(`{"type": "shell_call_output", "call_id": "call-123", "id": "resp-123", "output": [{"stderr": "", "stdout": "Documents\nDownloads", "outcome": {"type": "exit", "exit_code": 0}}]}`),
		},
		{
			name: "marshal apply_patch_call",
			input: ResponseInputItemUnionParam{
				OfApplyPatchCall: &ResponseInputItemApplyPatchCallParam{
					Type:   "apply_patch_call",
					ID:     "resp-123",
					CallID: "call-123",
					Status: "completed",
					Operation: ResponseInputItemApplyPatchCallOperationUnionParam{
						OfCreateFile: &ResponseInputItemApplyPatchCallOperationCreateFileParam{
							Type: "create_file",
							Path: "/home/Documents",
							Diff: "",
						},
					},
				},
			},
			expRes: []byte(`{"type": "apply_patch_call", "id": "resp-123", "call_id": "call-123", "status": "completed", "operation": {"type": "create_file", "path": "/home/Documents", "diff": ""} }`),
		},
		{
			name: "marshal apply_patch_call_output",
			input: ResponseInputItemUnionParam{
				OfApplyPatchCallOutput: &ResponseInputItemApplyPatchCallOutputParam{
					Type:   "apply_patch_call_output",
					ID:     "resp-123",
					CallID: "call-123",
					Status: "completed",
					Output: "log contents",
				},
			},
			expRes: []byte(`{"type": "apply_patch_call_output", "id": "resp-123", "call_id": "call-123", "status": "completed", "output": "log contents"}`),
		},
		{
			name: "marshal mcp_list_tools",
			input: ResponseInputItemUnionParam{
				OfMcpListTools: &ResponseMcpListTools{
					Type:        "mcp_list_tools",
					ID:          "id-123",
					ServerLabel: "test-server",
					Tools: []ResponseInputItemMcpListToolsToolParam{
						{InputSchema: "{\"schemaVersion\": \"1.0\"}", Name: "test"},
					},
				},
			},
			expRes: []byte(`{"type": "mcp_list_tools", "id": "id-123", "server_label": "test-server", "tools": [{"input_schema": "{\"schemaVersion\": \"1.0\"}", "name": "test"}]}`),
		},
		{
			name: "marshal mcp_approval_request",
			input: ResponseInputItemUnionParam{
				OfMcpApprovalRequest: &ResponseMcpApprovalRequest{
					Type:        "mcp_approval_request",
					ID:          "id-123",
					ServerLabel: "test-server",
					Name:        "test",
					Arguments:   "{\"arg1\": \"val\"}",
				},
			},
			expRes: []byte(`{"type": "mcp_approval_request", "id": "id-123", "server_label": "test-server", "name": "test", "arguments": "{\"arg1\": \"val\"}"}`),
		},
		{
			name: "marshal mcp_approval_response",
			input: ResponseInputItemUnionParam{
				OfMcpApprovalResponse: &ResponseInputItemMcpApprovalResponseParam{
					Type:              "mcp_approval_response",
					ID:                "resp-123",
					Approve:           true,
					ApprovalRequestID: "req-123",
				},
			},
			expRes: []byte(`{"type": "mcp_approval_response", "id": "resp-123", "approve": true, "approval_request_id": "req-123"}`),
		},
		{
			name: "marshal mcp_call",
			input: ResponseInputItemUnionParam{
				OfMcpCall: &ResponseMcpCall{
					Type:              "mcp_call",
					ID:                "id-123",
					ServerLabel:       "test-server",
					Name:              "test",
					Arguments:         "{\"arg1\": \"val\"}",
					ApprovalRequestID: "req-123",
				},
			},
			expRes: []byte(`{"type": "mcp_call", "id": "id-123", "server_label": "test-server", "name": "test", "arguments": "{\"arg1\": \"val\"}", "approval_request_id": "req-123"}`),
		},
		{
			name: "marshal custom_tool_call_output",
			input: ResponseInputItemUnionParam{
				OfCustomToolCallOutput: &ResponseCustomToolCallOutputParam{
					Type:   "custom_tool_call_output",
					CallID: "call-123",
					ID:     "resp-123",
					Output: ResponseCustomToolCallOutputOutputUnionParam{
						OfString: ptr.To("some output"),
					},
				},
			},
			expRes: []byte(`{"type": "custom_tool_call_output", "call_id": "call-123", "id": "resp-123", "output": "some output"}`),
		},
		{
			name: "marshal custom_tool_call",
			input: ResponseInputItemUnionParam{
				OfCustomToolCall: &ResponseCustomToolCall{
					Type:   "custom_tool_call",
					ID:     "id-123",
					Name:   "test",
					CallID: "call-123",
					Input:  "some input",
				},
			},
			expRes: []byte(`{"type": "custom_tool_call", "id": "id-123", "name": "test", "call_id": "call-123", "input": "some input"}`),
		},
		{
			name: "marshal compaction_trigger",
			input: ResponseInputItemUnionParam{
				OfCompactionTrigger: &ResponseInputItemCompactionTriggerParam{
					Type: "compaction_trigger",
				},
			},
			expRes: []byte(`{"type":"compaction_trigger"}`),
		},
		{
			name: "marshal item_reference",
			input: ResponseInputItemUnionParam{
				OfItemReference: &ResponseInputItemItemReferenceParam{
					Type: "item_reference",
					ID:   "id-123",
				},
			},
			expRes: []byte(`{"type": "item_reference", "id": "id-123"}`),
		},
		{
			name: "marshal additional_tools",
			input: ResponseInputItemUnionParam{
				OfAdditionalTools: &ResponseInputItemAdditionalToolsParam{
					Type: "additional_tools",
					ID:   "tools-123",
					Role: "developer",
					Tools: []ResponseToolUnion{
						{
							OfFunction: &FunctionToolParam{
								Type:        "function",
								Name:        "get_weather",
								Description: "Get weather by city",
								Parameters: map[string]any{
									"type": "object",
									"properties": map[string]any{
										"city": map[string]any{"type": "string"},
									},
								},
								Strict: ptr.To(true),
							},
						},
					},
				},
			},
			expRes: []byte(`{"type": "additional_tools", "id": "tools-123", "role": "developer", "tools": [{"type": "function", "name": "get_weather", "description": "Get weather by city", "parameters": {"type": "object", "properties": {"city": {"type": "string"}}}, "strict": true}]}`),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			data, err := tc.input.MarshalJSON()
			if tc.expErr != "" {
				require.ErrorContains(t, err, tc.expErr)
				return
			}
			require.NoError(t, err)
			require.JSONEq(t, string(tc.expRes), string(data))
		})
	}
}

func TestResponseInputItemUnionParamUnmarshalJSON(t *testing.T) {
	tests := []struct {
		name   string
		expRes ResponseInputItemUnionParam
		input  []byte
		expErr string
	}{
		{
			name:   "unmarshal with neither field set",
			expRes: ResponseInputItemUnionParam{},
			input:  []byte(`{}`),
			expErr: "cannot unmarshal unknown input type: ",
		},
		{
			name: "unmarshal message without type field (role and content present)",
			expRes: ResponseInputItemUnionParam{
				OfMessage: &EasyInputMessageParam{
					Role: "user",
					Content: EasyInputMessageContentUnionParam{
						OfString: ptr.To("Hello"),
					},
				},
			},
			input: []byte(`{"role": "user", "content": "Hello"}`),
		},
		{
			name: "unmarshal assistant output_message without type field",
			expRes: ResponseInputItemUnionParam{
				OfOutputMessage: &ResponseOutputMessage{
					Role: "assistant",
					Content: ResponseOutputMessageContentUnion{
						OfContentArray: []ResponseOutputMessageContentArrayUnion{
							{OfOutputText: &ResponseOutputTextParam{Text: "Hi! I'm here and working.", Type: "output_text"}},
						},
					},
				},
			},
			input: []byte(`{"role": "assistant", "content": [{"text": "Hi! I'm here and working.", "type": "output_text"}]}`),
		},
		{
			name:   "unmarshal message without type field (role missing, should error)",
			expRes: ResponseInputItemUnionParam{},
			input:  []byte(`{"content": "Hello"}`),
			expErr: "cannot unmarshal unknown input type: ",
		},
		{
			name:   "unmarshal message without type field (content missing, should error)",
			expRes: ResponseInputItemUnionParam{},
			input:  []byte(`{"role": "user"}`),
			expErr: "cannot unmarshal unknown input type: ",
		},
		{
			name: "unmarshal message",
			expRes: ResponseInputItemUnionParam{
				OfMessage: &EasyInputMessageParam{
					Role: "user",
					Type: "message",
					Content: EasyInputMessageContentUnionParam{
						OfString: ptr.To("Hi"),
					},
				},
			},
			input: []byte(`{"type": "message", "role": "user", "content": "Hi"}`),
		},
		{
			name: "unmarshal input_message",
			expRes: ResponseInputItemUnionParam{
				OfInputMessage: &ResponseInputItemMessageParam{
					Type:   "message",
					Role:   "developer",
					Status: "completed",
					ID:     "resp-123",
					Content: []ResponseInputContentUnionParam{
						{OfInputText: &ResponseInputTextParam{Text: "Hello!", Type: "input_text"}},
					},
				},
			},
			input: []byte(`{"type": "message", "id": "resp-123", "role": "developer", "status": "completed", "content": [{"text": "Hello!", "type": "input_text"}]}`),
		},
		{
			name: "unmarshal output_message",
			expRes: ResponseInputItemUnionParam{
				OfOutputMessage: &ResponseOutputMessage{
					Type: "message",
					Role: "assistant",
					Content: ResponseOutputMessageContentUnion{
						OfContentArray: []ResponseOutputMessageContentArrayUnion{
							{OfOutputText: &ResponseOutputTextParam{Text: "Hello! How can I assist you ?", Type: "output_text"}},
						},
					},
					Status: "completed",
					ID:     "resp-123",
				},
			},
			input: []byte(`{"type": "message", "role": "assistant", "status": "completed", "id": "resp-123", "content": [{"text": "Hello! How can I assist you ?", "type": "output_text"}]}`),
		},
		{
			name: "unmarshal output_message without id (assistant with output_text content)",
			expRes: ResponseInputItemUnionParam{
				OfOutputMessage: &ResponseOutputMessage{
					Type: "message",
					Role: "assistant",
					Content: ResponseOutputMessageContentUnion{
						OfContentArray: []ResponseOutputMessageContentArrayUnion{
							{OfOutputText: &ResponseOutputTextParam{Text: "Hi! I'm here and working.", Type: "output_text"}},
						},
					},
				},
			},
			input: []byte(`{"type": "message", "role": "assistant", "content": [{"text": "Hi! I'm here and working.", "type": "output_text"}]}`),
		},
		{
			name: "unmarshal file_search_call",
			expRes: ResponseInputItemUnionParam{
				OfFileSearchCall: &ResponseFileSearchToolCall{
					Type: "file_search_call",
					ID:   "call-123",
					Results: []ResponseFileSearchToolCallResultParam{
						{FileID: "file-2d", Filename: "deep_research_blog.pdf"},
					},
					Queries: []string{"What is deep research?"},
				},
			},
			input: []byte(`{"type": "file_search_call", "id": "call-123", "queries": ["What is deep research?"], "results": [{"file_id": "file-2d", "filename": "deep_research_blog.pdf"}]}`),
		},
		{
			name: "unmarshal computer_call",
			expRes: ResponseInputItemUnionParam{
				OfComputerCall: &ResponseComputerToolCall{
					Type:   "computer_call",
					CallID: "call-456",
					ID:     "rs-123",
					Action: ResponseComputerToolCallActionUnionParam{
						OfClick: &ResponseComputerToolCallActionClickParam{
							Button: "left",
							X:      100,
							Y:      200,
							Type:   "click",
						},
					},
				},
			},
			input: []byte(`{"type": "computer_call", "call_id": "call-456", "id": "rs-123", "action": {"type": "click", "button": "left", "x": 100, "y": 200}}`),
		},
		{
			name: "unmarshal computer_call_output",
			expRes: ResponseInputItemUnionParam{
				OfComputerCallOutput: &ResponseInputItemComputerCallOutputParam{
					Type:   "computer_call_output",
					ID:     "rs-123",
					CallID: "call-456",
					Output: ResponseComputerToolCallOutputScreenshotParam{
						Type:     "computer_screenshot",
						ImageURL: "data:image/png;base64,screenshot_base64",
					},
				},
			},
			input: []byte(`{"type": "computer_call_output", "id": "rs-123", "call_id": "call-456", "output": {"type": "computer_screenshot", "image_url": "data:image/png;base64,screenshot_base64"}}`),
		},
		{
			name: "unmarshal web_search_call",
			expRes: ResponseInputItemUnionParam{
				OfWebSearchCall: &ResponseFunctionWebSearch{
					Type: "web_search_call",
					ID:   "call-123",
					Action: ResponseFunctionWebSearchActionUnionParam{
						OfSearch: &ResponseFunctionWebSearchActionSearchParam{
							Type:  "search",
							Query: "What is deep research?",
							Sources: []ResponseFunctionWebSearchActionSearchSourceParam{
								{Type: "url", URL: "https://example.com"},
							},
						},
					},
				},
			},
			input: []byte(`{"type": "web_search_call", "id": "call-123", "action": {"type": "search", "query": "What is deep research?", "sources": [{"type": "url", "url": "https://example.com"}]}}`),
		},
		{
			name: "unmarshal function_call",
			expRes: ResponseInputItemUnionParam{
				OfFunctionCall: &ResponseFunctionToolCall{
					Type:      "function_call",
					Name:      "test_function",
					Namespace: "billing",
					CallID:    "call-789",
					ID:        "rs-123",
					Arguments: `{"arg1": "value"}`,
				},
			},
			input: []byte(`{"type": "function_call", "name": "test_function", "namespace": "billing", "call_id": "call-789", "id": "rs-123", "arguments": "{\"arg1\": \"value\"}"}`),
		},
		{
			name: "unmarshal function_call_output",
			expRes: ResponseInputItemUnionParam{
				OfFunctionCallOutput: &ResponseInputItemFunctionCallOutputParam{
					Type:   "function_call_output",
					CallID: "call-789",
					ID:     "rs-123",
					Output: ResponseInputItemFunctionCallOutputOutputUnionParam{
						OfString: ptr.To("output"),
					},
				},
			},
			input: []byte(`{"type": "function_call_output", "call_id": "call-789", "id": "rs-123", "output": "output"}`),
		},
		{
			name: "unmarshal tool_search_call",
			expRes: ResponseInputItemUnionParam{
				OfToolSearchCall: &ResponseToolSearchCall{
					Type:      "tool_search_call",
					ID:        "tsc-123",
					Status:    "completed",
					Arguments: map[string]any{"paths": []any{"billing"}},
					CallID:    nil,
					Execution: "server",
				},
			},
			input: []byte(`{"type":"tool_search_call","id":"tsc-123","status":"completed","arguments":{"paths":["billing"]},"call_id":null,"execution":"server"}`),
		},
		{
			name: "unmarshal tool_search_output",
			expRes: ResponseInputItemUnionParam{
				OfToolSearchOutput: &ResponseToolSearchOutput{
					Type:      "tool_search_output",
					ID:        "tso-123",
					Status:    "completed",
					CallID:    nil,
					Execution: "server",
					Tools: []ResponseToolUnion{
						{
							OfNamespace: &NamespaceToolParam{
								Type:        "namespace",
								Name:        "billing",
								Description: "Billing tools.",
								Tools: []NamespaceToolToolUnionParam{
									{
										OfFunction: &NamespaceToolToolFunctionParam{
											Type:         "function",
											Name:         "get_invoice",
											Description:  "Get invoice details by invoice ID.",
											DeferLoading: ptr.To(true),
										},
									},
								},
							},
						},
					},
				},
			},
			input: []byte(`{"type":"tool_search_output","id":"tso-123","status":"completed","call_id":null,"execution":"server","tools":[{"type":"namespace","name":"billing","description":"Billing tools.","tools":[{"type":"function","name":"get_invoice","description":"Get invoice details by invoice ID.","defer_loading":true}]}]}`),
		},
		{
			name: "unmarshal reasoning",
			expRes: ResponseInputItemUnionParam{
				OfReasoning: &ResponseReasoningItem{
					Type: "reasoning",
					ID:   "reasoning-001",
					Content: []ResponseReasoningItemContentParam{
						{Text: "the capital of France is Paris.", Type: "reasoning_text"},
					},
					Summary: []ResponseReasoningItemSummaryParam{
						{Text: "looking at a straightforward question: the capital of France is Paris", Type: "summary_text"},
					},
				},
			},
			input: []byte(`{"type": "reasoning", "id": "reasoning-001", "content": [{"text": "the capital of France is Paris.", "type": "reasoning_text"}], "summary": [{"text": "looking at a straightforward question: the capital of France is Paris", "type": "summary_text"}]}`),
		},
		{
			name: "unmarshal compaction",
			expRes: ResponseInputItemUnionParam{
				OfCompaction: &ResponseCompactionItemParam{
					Type:             "compaction",
					ID:               "resp-123",
					EncryptedContent: "encrypted_content",
				},
			},
			input: []byte(`{"type": "compaction", "id": "resp-123", "encrypted_content": "encrypted_content"}`),
		},
		{
			name: "unmarshal image_generation_call",
			expRes: ResponseInputItemUnionParam{
				OfImageGenerationCall: &ResponseInputItemImageGenerationCallParam{
					Type:   "image_generation_call",
					ID:     "rs-123",
					Status: "completed",
					Result: "data:image/png;base64,encoded_image",
				},
			},
			input: []byte(`{"type": "image_generation_call", "id": "rs-123", "status": "completed", "result": "data:image/png;base64,encoded_image"}`),
		},
		{
			name: "unmarshal code_interpreter_call",
			expRes: ResponseInputItemUnionParam{
				OfCodeInterpreterCall: &ResponseCodeInterpreterToolCallParam{
					Type:        "code_interpreter_call",
					ID:          "resp-123",
					ContainerID: "cntr_320",
					Status:      "completed",
					Outputs: []ResponseCodeInterpreterToolCallOutputUnionParam{
						{OfLogs: &ResponseCodeInterpreterToolCallOutputLogsParam{
							Type: "logs",
							Logs: "log contents",
						}},
					},
				},
			},
			input: []byte(`{"type": "code_interpreter_call", "id": "resp-123", "container_id": "cntr_320", "status": "completed", "outputs": [{"type": "logs", "logs": "log contents"}]}`),
		},
		{
			name: "unmarshal local_shell_call",
			expRes: ResponseInputItemUnionParam{
				OfLocalShellCall: &ResponseInputItemLocalShellCallParam{
					Type:   "local_shell_call",
					CallID: "call-123",
					ID:     "rs-123",
					Status: "in_progress",
					Action: ResponseInputItemLocalShellCallActionParam{
						Type:    "exec",
						Command: []string{"ls", "-a"},
						Env:     map[string]string{"TEST": "test"},
					},
				},
			},
			input: []byte(`{"type": "local_shell_call", "call_id": "call-123", "id": "rs-123", "status": "in_progress", "action": {"type": "exec", "command": ["ls", "-a"], "env": {"TEST": "test"}}}`),
		},
		{
			name: "unmarshal local_shell_call_output",
			expRes: ResponseInputItemUnionParam{
				OfLocalShellCallOutput: &ResponseInputItemLocalShellCallOutputParam{
					Type:   "local_shell_call_output",
					ID:     "resp-123",
					Output: ".\n..\nDocuments\nDownloads",
				},
			},
			input: []byte(`{"type": "local_shell_call_output", "id": "resp-123", "output": ".\n..\nDocuments\nDownloads"}`),
		},
		{
			name: "unmarshal shell_call",
			expRes: ResponseInputItemUnionParam{
				OfShellCall: &ResponseInputItemShellCallParam{
					Type:   "shell_call",
					CallID: "call-123",
					ID:     "resp-123",
					Action: ResponseInputItemShellCallActionParam{
						Commands: []string{"ls", "-a"},
					},
				},
			},
			input: []byte(`{"type": "shell_call", "call_id": "call-123", "id": "resp-123", "action": {"commands": ["ls", "-a"]}}`),
		},
		{
			name: "unmarshal shell_call_output",
			expRes: ResponseInputItemUnionParam{
				OfShellCallOutput: &ResponseInputItemShellCallOutputParam{
					Type:   "shell_call_output",
					CallID: "call-123",
					ID:     "resp-123",
					Output: []ResponseFunctionShellCallOutputContentParam{
						{
							Stderr: "",
							Stdout: "Documents\nDownloads",
							Outcome: ResponseFunctionShellCallOutputContentOutcomeUnionParam{
								OfExit: &ResponseFunctionShellCallOutputOutputContentOutcomeExitParam{
									Type:     "exit",
									ExitCode: 0,
								},
							},
						},
					},
				},
			},
			input: []byte(`{"type": "shell_call_output", "call_id": "call-123", "id": "resp-123", "output": [{"stderr": "", "stdout": "Documents\nDownloads", "outcome": {"type": "exit", "exit_code": 0}}]}`),
		},
		{
			name: "unmarshal apply_patch_call",
			expRes: ResponseInputItemUnionParam{
				OfApplyPatchCall: &ResponseInputItemApplyPatchCallParam{
					Type:   "apply_patch_call",
					ID:     "resp-123",
					CallID: "call-123",
					Status: "completed",
					Operation: ResponseInputItemApplyPatchCallOperationUnionParam{
						OfCreateFile: &ResponseInputItemApplyPatchCallOperationCreateFileParam{
							Type: "create_file",
							Path: "/home/Documents",
							Diff: "",
						},
					},
				},
			},
			input: []byte(`{"type": "apply_patch_call", "id": "resp-123", "call_id": "call-123", "status": "completed", "operation": {"type": "create_file", "path": "/home/Documents", "diff": ""} }`),
		},
		{
			name: "unmarshal apply_patch_call_output",
			expRes: ResponseInputItemUnionParam{
				OfApplyPatchCallOutput: &ResponseInputItemApplyPatchCallOutputParam{
					Type:   "apply_patch_call_output",
					ID:     "resp-123",
					CallID: "call-123",
					Status: "completed",
					Output: "log contents",
				},
			},
			input: []byte(`{"type": "apply_patch_call_output", "id": "resp-123", "call_id": "call-123", "status": "completed", "output": "log contents"}`),
		},
		{
			name: "unmarshal mcp_list_tools",
			expRes: ResponseInputItemUnionParam{
				OfMcpListTools: &ResponseMcpListTools{
					Type:        "mcp_list_tools",
					ID:          "id-123",
					ServerLabel: "test-server",
					Tools: []ResponseInputItemMcpListToolsToolParam{
						{InputSchema: "{\"schemaVersion\": \"1.0\"}", Name: "test"},
					},
				},
			},
			input: []byte(`{"type": "mcp_list_tools", "id": "id-123", "server_label": "test-server", "tools": [{"input_schema": "{\"schemaVersion\": \"1.0\"}", "name": "test"}]}`),
		},
		{
			name: "unmarshal mcp_approval_request",
			expRes: ResponseInputItemUnionParam{
				OfMcpApprovalRequest: &ResponseMcpApprovalRequest{
					Type:        "mcp_approval_request",
					ID:          "id-123",
					ServerLabel: "test-server",
					Name:        "test",
					Arguments:   "{\"arg1\": \"val\"}",
				},
			},
			input: []byte(`{"type": "mcp_approval_request", "id": "id-123", "server_label": "test-server", "name": "test", "arguments": "{\"arg1\": \"val\"}"}`),
		},
		{
			name: "unmarshal mcp_approval_response",
			expRes: ResponseInputItemUnionParam{
				OfMcpApprovalResponse: &ResponseInputItemMcpApprovalResponseParam{
					Type:              "mcp_approval_response",
					ID:                "resp-123",
					Approve:           true,
					ApprovalRequestID: "req-123",
				},
			},
			input: []byte(`{"type": "mcp_approval_response", "id": "resp-123", "approve": true, "approval_request_id": "req-123"}`),
		},
		{
			name: "unmarshal mcp_call",
			expRes: ResponseInputItemUnionParam{
				OfMcpCall: &ResponseMcpCall{
					Type:              "mcp_call",
					ID:                "id-123",
					ServerLabel:       "test-server",
					Name:              "test",
					Arguments:         "{\"arg1\": \"val\"}",
					ApprovalRequestID: "req-123",
				},
			},
			input: []byte(`{"type": "mcp_call", "id": "id-123", "server_label": "test-server", "name": "test", "arguments": "{\"arg1\": \"val\"}", "approval_request_id": "req-123"}`),
		},
		{
			name: "unmarshal custom_tool_call_output",
			expRes: ResponseInputItemUnionParam{
				OfCustomToolCallOutput: &ResponseCustomToolCallOutputParam{
					Type:   "custom_tool_call_output",
					CallID: "call-123",
					ID:     "resp-123",
					Output: ResponseCustomToolCallOutputOutputUnionParam{
						OfString: ptr.To("some output"),
					},
				},
			},
			input: []byte(`{"type": "custom_tool_call_output", "call_id": "call-123", "id": "resp-123", "output": "some output"}`),
		},
		{
			name: "unmarshal custom_tool_call",
			expRes: ResponseInputItemUnionParam{
				OfCustomToolCall: &ResponseCustomToolCall{
					Type:   "custom_tool_call",
					ID:     "id-123",
					Name:   "test",
					CallID: "call-123",
					Input:  "some input",
				},
			},
			input: []byte(`{"type": "custom_tool_call", "id": "id-123", "name": "test", "call_id": "call-123", "input": "some input"}`),
		},
		{
			name: "unmarshal compaction_trigger",
			expRes: ResponseInputItemUnionParam{
				OfCompactionTrigger: &ResponseInputItemCompactionTriggerParam{
					Type: "compaction_trigger",
				},
			},
			input: []byte(`{"type":"compaction_trigger"}`),
		},
		{
			name: "unmarshal item_reference",
			expRes: ResponseInputItemUnionParam{
				OfItemReference: &ResponseInputItemItemReferenceParam{
					Type: "item_reference",
					ID:   "id-123",
				},
			},
			input: []byte(`{"type": "item_reference", "id": "id-123"}`),
		},
		{
			name: "unmarshal agent_message",
			expRes: ResponseInputItemUnionParam{
				OfAgentMessage: ptr.To(json.RawMessage(`{"type":"agent_message","role":"agent","content":[{"type":"text","text":"prior context"}]}`)),
			},
			input: []byte(`{"type":"agent_message","role":"agent","content":[{"type":"text","text":"prior context"}]}`),
		},
		{
			name: "unmarshal agent_reasoning",
			expRes: ResponseInputItemUnionParam{
				OfAgentReasoning: ptr.To(json.RawMessage(`{"type":"agent_reasoning","summary":"prior reasoning"}`)),
			},
			input: []byte(`{"type":"agent_reasoning","summary":"prior reasoning"}`),
		},
		{
			name: "unmarshal additional_tools",
			expRes: ResponseInputItemUnionParam{
				OfAdditionalTools: &ResponseInputItemAdditionalToolsParam{
					Type: "additional_tools",
					ID:   "tools-123",
					Role: "developer",
					Tools: []ResponseToolUnion{
						{
							OfFunction: &FunctionToolParam{
								Type:        "function",
								Name:        "get_weather",
								Description: "Get weather by city",
								Parameters: map[string]any{
									"type": "object",
									"properties": map[string]any{
										"city": map[string]any{"type": "string"},
									},
								},
								Strict: ptr.To(true),
							},
						},
					},
				},
			},
			input: []byte(`{"type": "additional_tools", "id": "tools-123", "role": "developer", "tools": [{"type": "function", "name": "get_weather", "description": "Get weather by city", "parameters": {"type": "object", "properties": {"city": {"type": "string"}}}, "strict": true}]}`),
		},
		{
			name:   "unmarshal empty type string",
			input:  []byte(`{"type":""}`),
			expErr: "cannot unmarshal unknown input type: ",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var result ResponseInputItemUnionParam
			err := json.Unmarshal(tc.input, &result)
			if tc.expErr != "" {
				require.ErrorContains(t, err, tc.expErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.expRes, result)
		})
	}
}

func TestEasyInputMessageContentUnionParamUnmarshalJSON(t *testing.T) {
	tests := []struct {
		name   string
		input  []byte
		expect EasyInputMessageContentUnionParam
		expErr string
	}{
		{
			name:  "unmarshal string content",
			input: []byte(`"Hello, world!"`),
			expect: EasyInputMessageContentUnionParam{
				OfString:               ptr.To("Hello, world!"),
				OfInputItemContentList: nil,
			},
		},
		{
			name:  "unmarshal empty string",
			input: []byte(`""`),
			expect: EasyInputMessageContentUnionParam{
				OfString:               ptr.To(""),
				OfInputItemContentList: nil,
			},
		},
		{
			name:  "unmarshal string with special characters",
			input: []byte(`"Hello\nWorld\t!"`),
			expect: EasyInputMessageContentUnionParam{
				OfString:               ptr.To("Hello\nWorld\t!"),
				OfInputItemContentList: nil,
			},
		},
		{
			name:  "unmarshal single item content list with input_text",
			input: []byte(`[{"type": "input_text", "text": "Hello"}]`),
			expect: EasyInputMessageContentUnionParam{
				OfString: nil,
				OfInputItemContentList: []ResponseInputContentUnionParam{
					{
						OfInputText: &ResponseInputTextParam{
							Type: "input_text",
							Text: "Hello",
						},
					},
				},
			},
		},
		{
			name:  "unmarshal multiple item content list",
			input: []byte(`[{"type": "input_text", "text": "Hello"}, {"type": "input_text", "text": "World"}]`),
			expect: EasyInputMessageContentUnionParam{
				OfString: nil,
				OfInputItemContentList: []ResponseInputContentUnionParam{
					{
						OfInputText: &ResponseInputTextParam{
							Type: "input_text",
							Text: "Hello",
						},
					},
					{
						OfInputText: &ResponseInputTextParam{
							Type: "input_text",
							Text: "World",
						},
					},
				},
			},
		},
		{
			name:  "unmarshal content list with input_image",
			input: []byte(`[{"type": "input_image", "image_url": "https://example.com/image.jpg"}]`),
			expect: EasyInputMessageContentUnionParam{
				OfString: nil,
				OfInputItemContentList: []ResponseInputContentUnionParam{
					{
						OfInputImage: &ResponseInputImageParam{
							Type:     "input_image",
							ImageURL: "https://example.com/image.jpg",
						},
					},
				},
			},
		},
		{
			name:  "unmarshal content list with input_image with file_id",
			input: []byte(`[{"type": "input_image", "file_id": "file_123"}]`),
			expect: EasyInputMessageContentUnionParam{
				OfString: nil,
				OfInputItemContentList: []ResponseInputContentUnionParam{
					{
						OfInputImage: &ResponseInputImageParam{
							Type:   "input_image",
							FileID: "file_123",
						},
					},
				},
			},
		},
		{
			name:  "unmarshal content list with input_image with detail",
			input: []byte(`[{"type": "input_image", "image_url": "https://example.com/image.jpg", "detail": "high"}]`),
			expect: EasyInputMessageContentUnionParam{
				OfString: nil,
				OfInputItemContentList: []ResponseInputContentUnionParam{
					{
						OfInputImage: &ResponseInputImageParam{
							Type:     "input_image",
							ImageURL: "https://example.com/image.jpg",
							Detail:   "high",
						},
					},
				},
			},
		},
		{
			name:  "unmarshal content list with input_file",
			input: []byte(`[{"type": "input_file", "file_id": "file_123"}]`),
			expect: EasyInputMessageContentUnionParam{
				OfString: nil,
				OfInputItemContentList: []ResponseInputContentUnionParam{
					{
						OfInputFile: &ResponseInputFileParam{
							Type:   "input_file",
							FileID: "file_123",
						},
					},
				},
			},
		},
		{
			name:  "unmarshal content list with input_file with file_url",
			input: []byte(`[{"type": "input_file", "file_url": "https://example.com/file.pdf"}]`),
			expect: EasyInputMessageContentUnionParam{
				OfString: nil,
				OfInputItemContentList: []ResponseInputContentUnionParam{
					{
						OfInputFile: &ResponseInputFileParam{
							Type:    "input_file",
							FileURL: "https://example.com/file.pdf",
						},
					},
				},
			},
		},
		{
			name:  "unmarshal content list with input_file with file_data",
			input: []byte(`[{"type": "input_file", "file_data": "base64encodeddata"}]`),
			expect: EasyInputMessageContentUnionParam{
				OfString: nil,
				OfInputItemContentList: []ResponseInputContentUnionParam{
					{
						OfInputFile: &ResponseInputFileParam{
							Type:     "input_file",
							FileData: "base64encodeddata",
						},
					},
				},
			},
		},
		{
			name:  "unmarshal content list with mixed content types",
			input: []byte(`[{"type": "input_text", "text": "Look at this image: "}, {"type": "input_image", "image_url": "https://example.com/image.jpg"}]`),
			expect: EasyInputMessageContentUnionParam{
				OfString: nil,
				OfInputItemContentList: []ResponseInputContentUnionParam{
					{
						OfInputText: &ResponseInputTextParam{
							Type: "input_text",
							Text: "Look at this image: ",
						},
					},
					{
						OfInputImage: &ResponseInputImageParam{
							Type:     "input_image",
							ImageURL: "https://example.com/image.jpg",
						},
					},
				},
			},
		},
		{
			name:  "unmarshal empty array",
			input: []byte(`[]`),
			expect: EasyInputMessageContentUnionParam{
				OfString:               nil,
				OfInputItemContentList: []ResponseInputContentUnionParam{},
			},
		},
		{
			name:   "unmarshal invalid type - number",
			input:  []byte(`123`),
			expErr: "content must be either a string or an array of input content items",
		},
		{
			name:   "unmarshal invalid type - object",
			input:  []byte(`{"type": "input_text", "text": "Hello"}`),
			expErr: "content must be either a string or an array of input content items",
		},
		{
			name:   "unmarshal invalid type - boolean",
			input:  []byte(`true`),
			expErr: "content must be either a string or an array of input content items",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var result EasyInputMessageContentUnionParam
			err := json.Unmarshal(tc.input, &result)
			if tc.expErr != "" {
				require.ErrorContains(t, err, tc.expErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.expect, result)
		})
	}
}

func TestEasyInputMessageContentUnionParamMarshalJSON(t *testing.T) {
	tests := []struct {
		name   string
		expect []byte
		input  EasyInputMessageContentUnionParam
		expErr string
	}{
		{
			name:   "marshal string content",
			expect: []byte(`"Hello, world!"`),
			input: EasyInputMessageContentUnionParam{
				OfString:               ptr.To("Hello, world!"),
				OfInputItemContentList: nil,
			},
		},
		{
			name:   "marshal empty string",
			expect: []byte(`""`),
			input: EasyInputMessageContentUnionParam{
				OfString:               ptr.To(""),
				OfInputItemContentList: nil,
			},
		},
		{
			name:   "marshal string with special characters",
			expect: []byte(`"Hello\nWorld\t!"`),
			input: EasyInputMessageContentUnionParam{
				OfString:               ptr.To("Hello\nWorld\t!"),
				OfInputItemContentList: nil,
			},
		},
		{
			name:   "marshal single item content list with input_text",
			expect: []byte(`[{"type": "input_text", "text": "Hello"}]`),
			input: EasyInputMessageContentUnionParam{
				OfString: nil,
				OfInputItemContentList: []ResponseInputContentUnionParam{
					{
						OfInputText: &ResponseInputTextParam{
							Type: "input_text",
							Text: "Hello",
						},
					},
				},
			},
		},
		{
			name:   "marshal multiple item content list",
			expect: []byte(`[{"type": "input_text", "text": "Hello"}, {"type": "input_text", "text": "World"}]`),
			input: EasyInputMessageContentUnionParam{
				OfString: nil,
				OfInputItemContentList: []ResponseInputContentUnionParam{
					{
						OfInputText: &ResponseInputTextParam{
							Type: "input_text",
							Text: "Hello",
						},
					},
					{
						OfInputText: &ResponseInputTextParam{
							Type: "input_text",
							Text: "World",
						},
					},
				},
			},
		},
		{
			name:   "marshal content list with input_image",
			expect: []byte(`[{"type": "input_image", "image_url": "https://example.com/image.jpg"}]`),
			input: EasyInputMessageContentUnionParam{
				OfString: nil,
				OfInputItemContentList: []ResponseInputContentUnionParam{
					{
						OfInputImage: &ResponseInputImageParam{
							Type:     "input_image",
							ImageURL: "https://example.com/image.jpg",
						},
					},
				},
			},
		},
		{
			name:   "marshal content list with input_image with file_id",
			expect: []byte(`[{"type": "input_image", "file_id": "file_123"}]`),
			input: EasyInputMessageContentUnionParam{
				OfString: nil,
				OfInputItemContentList: []ResponseInputContentUnionParam{
					{
						OfInputImage: &ResponseInputImageParam{
							Type:   "input_image",
							FileID: "file_123",
						},
					},
				},
			},
		},
		{
			name:   "marshal content list with input_image with detail",
			expect: []byte(`[{"type": "input_image", "image_url": "https://example.com/image.jpg", "detail": "high"}]`),
			input: EasyInputMessageContentUnionParam{
				OfString: nil,
				OfInputItemContentList: []ResponseInputContentUnionParam{
					{
						OfInputImage: &ResponseInputImageParam{
							Type:     "input_image",
							ImageURL: "https://example.com/image.jpg",
							Detail:   "high",
						},
					},
				},
			},
		},
		{
			name:   "marshal content list with input_file",
			expect: []byte(`[{"type": "input_file", "file_id": "file_123"}]`),
			input: EasyInputMessageContentUnionParam{
				OfString: nil,
				OfInputItemContentList: []ResponseInputContentUnionParam{
					{
						OfInputFile: &ResponseInputFileParam{
							Type:   "input_file",
							FileID: "file_123",
						},
					},
				},
			},
		},
		{
			name:   "marshal content list with input_file with file_url",
			expect: []byte(`[{"type": "input_file", "file_url": "https://example.com/file.pdf"}]`),
			input: EasyInputMessageContentUnionParam{
				OfString: nil,
				OfInputItemContentList: []ResponseInputContentUnionParam{
					{
						OfInputFile: &ResponseInputFileParam{
							Type:    "input_file",
							FileURL: "https://example.com/file.pdf",
						},
					},
				},
			},
		},
		{
			name:   "marshal content list with input_file with file_data",
			expect: []byte(`[{"type": "input_file", "file_data": "base64encodeddata"}]`),
			input: EasyInputMessageContentUnionParam{
				OfString: nil,
				OfInputItemContentList: []ResponseInputContentUnionParam{
					{
						OfInputFile: &ResponseInputFileParam{
							Type:     "input_file",
							FileData: "base64encodeddata",
						},
					},
				},
			},
		},
		{
			name:   "marshal content list with mixed content types",
			expect: []byte(`[{"type": "input_text", "text": "Look at this image: "}, {"type": "input_image", "image_url": "https://example.com/image.jpg"}]`),
			input: EasyInputMessageContentUnionParam{
				OfString: nil,
				OfInputItemContentList: []ResponseInputContentUnionParam{
					{
						OfInputText: &ResponseInputTextParam{
							Type: "input_text",
							Text: "Look at this image: ",
						},
					},
					{
						OfInputImage: &ResponseInputImageParam{
							Type:     "input_image",
							ImageURL: "https://example.com/image.jpg",
						},
					},
				},
			},
		},
		{
			name:   "marshal empty array",
			expect: []byte(`[]`),
			input: EasyInputMessageContentUnionParam{
				OfString:               nil,
				OfInputItemContentList: []ResponseInputContentUnionParam{},
			},
		},
		{
			name:   "marshal nil union",
			input:  EasyInputMessageContentUnionParam{},
			expErr: "no message content to marshal",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			data, err := json.Marshal(tc.input)
			if tc.expErr != "" {
				require.ErrorContains(t, err, tc.expErr)
				return
			}
			require.NoError(t, err)
			require.JSONEq(t, string(tc.expect), string(data))
		})
	}
}

func TestResponseInputContentUnionParamMarshalJSON(t *testing.T) {
	for _, tc := range []struct {
		name   string
		input  *ResponseInputContentUnionParam
		expect string
		expErr string
	}{
		{
			name: "text input",
			input: &ResponseInputContentUnionParam{
				OfInputText: &ResponseInputTextParam{
					Type: "input_text",
					Text: "What is this image?",
				},
			},
			expect: `{"text":"What is this image?","type":"input_text"}`,
		},
		{
			name: "image input with file id",
			input: &ResponseInputContentUnionParam{
				OfInputImage: &ResponseInputImageParam{
					Type:   "input_image",
					FileID: "file-12345",
					Detail: "high",
				},
			},
			expect: `{"detail":"high","file_id":"file-12345","type":"input_image"}`,
		},
		{
			name: "image input with url",
			input: &ResponseInputContentUnionParam{
				OfInputImage: &ResponseInputImageParam{
					Type:     "input_image",
					ImageURL: "https://example.com/image.jpg",
					Detail:   "low",
				},
			},
			expect: `{"detail":"low","image_url":"https://example.com/image.jpg","type":"input_image"}`,
		},
		{
			name: "file input with file id",
			input: &ResponseInputContentUnionParam{
				OfInputFile: &ResponseInputFileParam{
					Type:   "input_file",
					FileID: "file-12345",
				},
			},
			expect: `{"file_id":"file-12345","type":"input_file"}`,
		},
		{
			name: "file input with url",
			input: &ResponseInputContentUnionParam{
				OfInputFile: &ResponseInputFileParam{
					Type:    "input_file",
					FileURL: "https://example.com/file.pdf",
				},
			},
			expect: `{"file_url":"https://example.com/file.pdf","type":"input_file"}`,
		},
		{
			name: "file input with data",
			input: &ResponseInputContentUnionParam{
				OfInputFile: &ResponseInputFileParam{
					Type:     "input_file",
					FileData: "base64encodeddata",
					Filename: "document.pdf",
				},
			},
			expect: `{"file_data":"base64encodeddata","filename":"document.pdf","type":"input_file"}`,
		},
		{
			name:   "no content set - error case",
			input:  &ResponseInputContentUnionParam{},
			expErr: "no input content to marshal",
		},
		{
			name: "text input with empty text",
			input: &ResponseInputContentUnionParam{
				OfInputText: &ResponseInputTextParam{
					Type: "input_text",
					Text: "",
				},
			},
			expect: `{"text":"","type":"input_text"}`,
		},
		{
			name: "image input with all fields",
			input: &ResponseInputContentUnionParam{
				OfInputImage: &ResponseInputImageParam{
					Type:     "input_image",
					FileID:   "file-12345",
					ImageURL: "https://example.com/image.jpg",
					Detail:   "auto",
				},
			},
			expect: `{"detail":"auto","file_id":"file-12345","image_url":"https://example.com/image.jpg","type":"input_image"}`,
		},
		{
			name: "file input with all fields",
			input: &ResponseInputContentUnionParam{
				OfInputFile: &ResponseInputFileParam{
					Type:     "input_file",
					FileID:   "file-12345",
					FileData: "base64data",
					FileURL:  "https://example.com/file.pdf",
					Filename: "document.pdf",
				},
			},
			expect: `{"file_data":"base64data","file_id":"file-12345","file_url":"https://example.com/file.pdf","filename":"document.pdf","type":"input_file"}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data, err := json.Marshal(tc.input)
			if tc.expErr != "" {
				require.ErrorContains(t, err, tc.expErr)
				return
			}
			require.NoError(t, err)
			require.JSONEq(t, tc.expect, string(data))
		})
	}
}

func TestResponseInputContentUnionParamUnmarshalJSON(t *testing.T) {
	for _, tc := range []struct {
		name   string
		expect *ResponseInputContentUnionParam
		input  string
		expErr string
	}{
		{
			name: "text input",
			expect: &ResponseInputContentUnionParam{
				OfInputText: &ResponseInputTextParam{
					Type: "input_text",
					Text: "What is this image?",
				},
			},
			input: `{"text":"What is this image?","type":"input_text"}`,
		},
		{
			name: "image input with file id",
			expect: &ResponseInputContentUnionParam{
				OfInputImage: &ResponseInputImageParam{
					Type:   "input_image",
					FileID: "file-12345",
					Detail: "high",
				},
			},
			input: `{"detail":"high","file_id":"file-12345","type":"input_image"}`,
		},
		{
			name: "image input with url",
			expect: &ResponseInputContentUnionParam{
				OfInputImage: &ResponseInputImageParam{
					Type:     "input_image",
					ImageURL: "https://example.com/image.jpg",
					Detail:   "low",
				},
			},
			input: `{"detail":"low","image_url":"https://example.com/image.jpg","type":"input_image"}`,
		},
		{
			name: "file input with file id",
			expect: &ResponseInputContentUnionParam{
				OfInputFile: &ResponseInputFileParam{
					Type:   "input_file",
					FileID: "file-12345",
				},
			},
			input: `{"file_id":"file-12345","type":"input_file"}`,
		},
		{
			name: "file input with url",
			expect: &ResponseInputContentUnionParam{
				OfInputFile: &ResponseInputFileParam{
					Type:    "input_file",
					FileURL: "https://example.com/file.pdf",
				},
			},
			input: `{"file_url":"https://example.com/file.pdf","type":"input_file"}`,
		},
		{
			name: "file input with data",
			expect: &ResponseInputContentUnionParam{
				OfInputFile: &ResponseInputFileParam{
					Type:     "input_file",
					FileData: "base64encodeddata",
					Filename: "document.pdf",
				},
			},
			input: `{"file_data":"base64encodeddata","filename":"document.pdf","type":"input_file"}`,
		},
		{
			name:   "empty type - error case",
			input:  `{"type": ""}`,
			expErr: "unknown type for input content: ",
		},
		{
			name: "text input with empty text",
			expect: &ResponseInputContentUnionParam{
				OfInputText: &ResponseInputTextParam{
					Type: "input_text",
					Text: "",
				},
			},
			input: `{"text":"","type":"input_text"}`,
		},
		{
			name: "image input with all fields",
			expect: &ResponseInputContentUnionParam{
				OfInputImage: &ResponseInputImageParam{
					Type:     "input_image",
					FileID:   "file-12345",
					ImageURL: "https://example.com/image.jpg",
					Detail:   "auto",
				},
			},
			input: `{"detail":"auto","file_id":"file-12345","image_url":"https://example.com/image.jpg","type":"input_image"}`,
		},
		{
			name: "file input with all fields",
			expect: &ResponseInputContentUnionParam{
				OfInputFile: &ResponseInputFileParam{
					Type:     "input_file",
					FileID:   "file-12345",
					FileData: "base64data",
					FileURL:  "https://example.com/file.pdf",
					Filename: "document.pdf",
				},
			},
			input: `{"file_data":"base64data","file_id":"file-12345","file_url":"https://example.com/file.pdf","filename":"document.pdf","type":"input_file"}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var result ResponseInputContentUnionParam
			err := json.Unmarshal([]byte(tc.input), &result)
			if tc.expErr != "" {
				require.ErrorContains(t, err, tc.expErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, *tc.expect, result)
		})
	}
}

func TestResponseOutputMessageContentUnionMarshalJSON(t *testing.T) {
	for _, tc := range []struct {
		name     string
		input    ResponseOutputMessageContentUnion
		expected string
		expErr   string
	}{
		{
			name: "marshal output text",
			input: ResponseOutputMessageContentUnion{
				OfContentArray: []ResponseOutputMessageContentArrayUnion{
					{
						OfOutputText: &ResponseOutputTextParam{
							Text: "Hello, world!",
							Type: "output_text",
						},
					},
				},
			},
			expected: `[{"text":"Hello, world!","type":"output_text"}]`,
		},
		{
			name: "marshal output string",
			input: ResponseOutputMessageContentUnion{
				OfString: ptr.To("Hello, world!"),
			},
			expected: `"Hello, world!"`,
		},
		{
			name: "marshal output text with annotations",
			input: ResponseOutputMessageContentUnion{
				OfContentArray: []ResponseOutputMessageContentArrayUnion{
					{
						OfOutputText: &ResponseOutputTextParam{
							Text: "Check this [citation]",
							Type: "output_text",
							Annotations: []ResponseOutputTextAnnotationUnionParam{
								{
									OfFileCitation: &ResponseOutputTextAnnotationFileCitationParam{
										Type:     "file_citation",
										FileID:   "file-123",
										Filename: "document.pdf",
										Index:    0,
									},
								},
							},
						},
					},
				},
			},
			expected: `[{"annotations":[{"file_id":"file-123","filename":"document.pdf","index":0,"type":"file_citation"}],"text":"Check this [citation]","type":"output_text"}]`,
		},
		{
			name: "marshal refusal",
			input: ResponseOutputMessageContentUnion{
				OfContentArray: []ResponseOutputMessageContentArrayUnion{
					{
						OfRefusal: &ResponseOutputRefusalParam{
							Refusal: "I cannot help with that request",
							Type:    "refusal",
						},
					},
				},
			},
			expected: `[{"refusal":"I cannot help with that request","type":"refusal"}]`,
		},
		{
			name:   "marshal empty union",
			input:  ResponseOutputMessageContentUnion{},
			expErr: "no output message content to marshal",
		},
		{
			name: "marshal complex output text",
			input: ResponseOutputMessageContentUnion{
				OfContentArray: []ResponseOutputMessageContentArrayUnion{
					{
						OfOutputText: &ResponseOutputTextParam{
							Text: "Complex response",
							Type: "output_text",
							Logprobs: []ResponseOutputTextLogprobParam{
								{
									Token:   "Complex",
									Logprob: -0.25,
									TopLogprobs: []ResponseOutputTextLogprobTopLogprobParam{
										{
											Token:   "Complex",
											Logprob: -0.25,
										},
										{
											Token:   "Complicated",
											Logprob: -1.5,
										},
									},
								},
								{
									Token:   "response",
									Logprob: -0.1,
								},
							},
						},
					},
				},
			},
			expected: `[{"logprobs":[{"logprob":-0.25,"token":"Complex","top_logprobs":[{"logprob":-0.25,"token":"Complex"},{"logprob":-1.5,"token":"Complicated"}]},{"logprob":-0.1,"token":"response"}],"text":"Complex response","type":"output_text"}]`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data, err := json.Marshal(tc.input)
			if tc.expErr != "" {
				require.ErrorContains(t, err, tc.expErr)
				return
			}
			require.NoError(t, err)
			require.JSONEq(t, tc.expected, string(data))
		})
	}
}

func TestResponseOutputMessageContentArrayUnionMarshalJSON(t *testing.T) {
	for _, tc := range []struct {
		name     string
		input    ResponseOutputMessageContentArrayUnion
		expected string
		expErr   string
	}{
		{
			name: "marshal output text",
			input: ResponseOutputMessageContentArrayUnion{
				OfOutputText: &ResponseOutputTextParam{
					Text: "Hello, world!",
					Type: "output_text",
				},
			},
			expected: `{"text":"Hello, world!","type":"output_text"}`,
		},
		{
			name: "marshal output text with annotations",
			input: ResponseOutputMessageContentArrayUnion{
				OfOutputText: &ResponseOutputTextParam{
					Text: "Check this [citation]",
					Type: "output_text",
					Annotations: []ResponseOutputTextAnnotationUnionParam{
						{
							OfFileCitation: &ResponseOutputTextAnnotationFileCitationParam{
								Type:     "file_citation",
								FileID:   "file-123",
								Filename: "document.pdf",
								Index:    0,
							},
						},
					},
				},
			},
			expected: `{"annotations":[{"file_id":"file-123","filename":"document.pdf","index":0,"type":"file_citation"}],"text":"Check this [citation]","type":"output_text"}`,
		},
		{
			name: "marshal output text with logprobs",
			input: ResponseOutputMessageContentArrayUnion{
				OfOutputText: &ResponseOutputTextParam{
					Text: "test",
					Type: "output_text",
					Logprobs: []ResponseOutputTextLogprobParam{
						{
							Token:   "test",
							Logprob: -0.5,
						},
					},
				},
			},
			expected: `{"logprobs":[{"logprob":-0.5,"token":"test"}],"text":"test","type":"output_text"}`,
		},
		{
			name: "marshal refusal",
			input: ResponseOutputMessageContentArrayUnion{
				OfRefusal: &ResponseOutputRefusalParam{
					Refusal: "I cannot help with that request",
					Type:    "refusal",
				},
			},
			expected: `{"refusal":"I cannot help with that request","type":"refusal"}`,
		},
		{
			name: "marshal refusal with special characters",
			input: ResponseOutputMessageContentArrayUnion{
				OfRefusal: &ResponseOutputRefusalParam{
					Refusal: "Cannot process: \"illegal\" request with \\n newlines",
					Type:    "refusal",
				},
			},
			expected: `{"refusal":"Cannot process: \"illegal\" request with \\n newlines","type":"refusal"}`,
		},
		{
			name:   "marshal empty union",
			input:  ResponseOutputMessageContentArrayUnion{},
			expErr: "no output message content to marshal",
		},
		{
			name: "marshal complex output text",
			input: ResponseOutputMessageContentArrayUnion{
				OfOutputText: &ResponseOutputTextParam{
					Text: "Complex response",
					Type: "output_text",
					Logprobs: []ResponseOutputTextLogprobParam{
						{
							Token:   "Complex",
							Logprob: -0.25,
							TopLogprobs: []ResponseOutputTextLogprobTopLogprobParam{
								{
									Token:   "Complex",
									Logprob: -0.25,
								},
								{
									Token:   "Complicated",
									Logprob: -1.5,
								},
							},
						},
						{
							Token:   "response",
							Logprob: -0.1,
						},
					},
				},
			},
			expected: `{"logprobs":[{"logprob":-0.25,"token":"Complex","top_logprobs":[{"logprob":-0.25,"token":"Complex"},{"logprob":-1.5,"token":"Complicated"}]},{"logprob":-0.1,"token":"response"}],"text":"Complex response","type":"output_text"}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data, err := json.Marshal(tc.input)
			if tc.expErr != "" {
				require.ErrorContains(t, err, tc.expErr)
				return
			}
			require.NoError(t, err)
			require.JSONEq(t, tc.expected, string(data))
		})
	}
}

func TestResponseOutputMessageContentArrayUnionUnmarshalJSON(t *testing.T) {
	for _, tc := range []struct {
		name     string
		expected ResponseOutputMessageContentArrayUnion
		input    string
		expErr   string
	}{
		{
			name: "unmarshal output text",
			expected: ResponseOutputMessageContentArrayUnion{
				OfOutputText: &ResponseOutputTextParam{
					Text: "Hello, world!",
					Type: "output_text",
				},
			},
			input: `{"text":"Hello, world!","type":"output_text"}`,
		},
		{
			name: "unmarshal output text with annotations",
			expected: ResponseOutputMessageContentArrayUnion{
				OfOutputText: &ResponseOutputTextParam{
					Text: "Check this [citation]",
					Type: "output_text",
					Annotations: []ResponseOutputTextAnnotationUnionParam{
						{
							OfFileCitation: &ResponseOutputTextAnnotationFileCitationParam{
								Type:     "file_citation",
								FileID:   "file-123",
								Filename: "document.pdf",
								Index:    0,
							},
						},
					},
				},
			},
			input: `{"annotations":[{"file_id":"file-123","filename":"document.pdf","index":0,"type":"file_citation"}],"text":"Check this [citation]","type":"output_text"}`,
		},
		{
			name: "unmarshal output text with logprobs",
			expected: ResponseOutputMessageContentArrayUnion{
				OfOutputText: &ResponseOutputTextParam{
					Text: "test",
					Type: "output_text",
					Logprobs: []ResponseOutputTextLogprobParam{
						{
							Token:   "test",
							Logprob: -0.5,
						},
					},
				},
			},
			input: `{"logprobs":[{"logprob":-0.5,"token":"test"}],"text":"test","type":"output_text"}`,
		},
		{
			name: "unmarshal refusal",
			expected: ResponseOutputMessageContentArrayUnion{
				OfRefusal: &ResponseOutputRefusalParam{
					Refusal: "I cannot help with that request",
					Type:    "refusal",
				},
			},
			input: `{"refusal":"I cannot help with that request","type":"refusal"}`,
		},
		{
			name: "unmarshal refusal with special characters",
			expected: ResponseOutputMessageContentArrayUnion{
				OfRefusal: &ResponseOutputRefusalParam{
					Refusal: "Cannot process: \"illegal\" request with \\n newlines",
					Type:    "refusal",
				},
			},
			input: `{"refusal":"Cannot process: \"illegal\" request with \\n newlines","type":"refusal"}`,
		},
		{
			name:   "unmarshal empty type",
			input:  `{"type": ""}`,
			expErr: "unknown type for output message content: ",
		},
		{
			name: "unmarshal complex output text",
			expected: ResponseOutputMessageContentArrayUnion{
				OfOutputText: &ResponseOutputTextParam{
					Text: "Complex response",
					Type: "output_text",
					Logprobs: []ResponseOutputTextLogprobParam{
						{
							Token:   "Complex",
							Logprob: -0.25,
							TopLogprobs: []ResponseOutputTextLogprobTopLogprobParam{
								{
									Token:   "Complex",
									Logprob: -0.25,
								},
								{
									Token:   "Complicated",
									Logprob: -1.5,
								},
							},
						},
						{
							Token:   "response",
							Logprob: -0.1,
						},
					},
				},
			},
			input: `{"logprobs":[{"logprob":-0.25,"token":"Complex","top_logprobs":[{"logprob":-0.25,"token":"Complex"},{"logprob":-1.5,"token":"Complicated"}]},{"logprob":-0.1,"token":"response"}],"text":"Complex response","type":"output_text"}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var result ResponseOutputMessageContentArrayUnion
			err := json.Unmarshal([]byte(tc.input), &result)
			if tc.expErr != "" {
				require.ErrorContains(t, err, tc.expErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.expected, result)
		})
	}
}

func TestResponseOutputMessageContentUnionUnmarshalJSON(t *testing.T) {
	for _, tc := range []struct {
		name     string
		expected ResponseOutputMessageContentUnion
		input    string
		expErr   string
	}{
		{
			name: "unmarshal output text",
			expected: ResponseOutputMessageContentUnion{
				OfContentArray: []ResponseOutputMessageContentArrayUnion{
					{
						OfOutputText: &ResponseOutputTextParam{
							Text: "Hello, world!",
							Type: "output_text",
						},
					},
				},
			},
			input: `[{"text":"Hello, world!","type":"output_text"}]`,
		},
		{
			name: "unmarshal output string",
			expected: ResponseOutputMessageContentUnion{
				OfString: ptr.To("Hello, world!"),
			},
			input: `"Hello, world!"`,
		},
		{
			name: "unmarshal refusal",
			expected: ResponseOutputMessageContentUnion{
				OfContentArray: []ResponseOutputMessageContentArrayUnion{
					{
						OfRefusal: &ResponseOutputRefusalParam{
							Refusal: "I cannot help with that request",
							Type:    "refusal",
						},
					},
				},
			},
			input: `[{"refusal":"I cannot help with that request","type":"refusal"}]`,
		},
		{
			name:   "unmarshal empty type",
			input:  `{"type": ""}`,
			expErr: "unable to unmarshal output message content",
		},
		{
			name: "unmarshal complex output text",
			expected: ResponseOutputMessageContentUnion{
				OfContentArray: []ResponseOutputMessageContentArrayUnion{
					{
						OfOutputText: &ResponseOutputTextParam{
							Text: "Complex response",
							Type: "output_text",
							Logprobs: []ResponseOutputTextLogprobParam{
								{
									Token:   "Complex",
									Logprob: -0.25,
									TopLogprobs: []ResponseOutputTextLogprobTopLogprobParam{
										{
											Token:   "Complex",
											Logprob: -0.25,
										},
										{
											Token:   "Complicated",
											Logprob: -1.5,
										},
									},
								},
								{
									Token:   "response",
									Logprob: -0.1,
								},
							},
						},
					},
				},
			},
			input: `[{"logprobs":[{"logprob":-0.25,"token":"Complex","top_logprobs":[{"logprob":-0.25,"token":"Complex"},{"logprob":-1.5,"token":"Complicated"}]},{"logprob":-0.1,"token":"response"}],"text":"Complex response","type":"output_text"}]`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var result ResponseOutputMessageContentUnion
			err := json.Unmarshal([]byte(tc.input), &result)
			if tc.expErr != "" {
				require.ErrorContains(t, err, tc.expErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.expected, result)
		})
	}
}

func TestResponseOutputTextAnnotationUnionParamMarshalJSON(t *testing.T) {
	for _, tc := range []struct {
		name     string
		input    ResponseOutputTextAnnotationUnionParam
		expected string
		expErr   string
	}{
		{
			name: "file citation",
			input: ResponseOutputTextAnnotationUnionParam{
				OfFileCitation: &ResponseOutputTextAnnotationFileCitationParam{
					FileID:   "file123",
					Filename: "document.pdf",
					Index:    0,
					Type:     "file_citation",
				},
			},
			expected: `{"file_id":"file123","filename":"document.pdf","index":0,"type":"file_citation"}`,
		},
		{
			name: "url citation",
			input: ResponseOutputTextAnnotationUnionParam{
				OfURLCitation: &ResponseOutputTextAnnotationURLCitationParam{
					EndIndex:   100,
					StartIndex: 50,
					Title:      "Example Website",
					URL:        "https://example.com",
					Type:       "url_citation",
				},
			},
			expected: `{"end_index":100,"start_index":50,"title":"Example Website","url":"https://example.com","type":"url_citation"}`,
		},
		{
			name: "container file citation",
			input: ResponseOutputTextAnnotationUnionParam{
				OfContainerFileCitation: &ResponseOutputTextAnnotationContainerFileCitationParam{
					ContainerID: "container456",
					EndIndex:    150,
					FileID:      "file789",
					Filename:    "nested.txt",
					StartIndex:  100,
					Type:        "container_file_citation",
				},
			},
			expected: `{"container_id":"container456","end_index":150,"file_id":"file789","filename":"nested.txt","start_index":100,"type":"container_file_citation"}`,
		},
		{
			name: "file path",
			input: ResponseOutputTextAnnotationUnionParam{
				OfFilePath: &ResponseOutputTextAnnotationFilePathParam{
					FileID: "filePath123",
					Index:  2,
					Type:   "file_path",
				},
			},
			expected: `{"file_id":"filePath123","index":2,"type":"file_path"}`,
		},
		{
			name:   "no annotation - error case",
			input:  ResponseOutputTextAnnotationUnionParam{},
			expErr: "no text annotation to marshal in output text annotation",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data, err := json.Marshal(tc.input)
			if tc.expErr != "" {
				require.ErrorContains(t, err, tc.expErr)
				return
			}
			require.NoError(t, err)
			require.JSONEq(t, tc.expected, string(data))
		})
	}
}

func TestResponseOutputTextAnnotationUnionParamUnmarshalJSON(t *testing.T) {
	for _, tc := range []struct {
		name     string
		expected ResponseOutputTextAnnotationUnionParam
		input    string
		expErr   string
	}{
		{
			name: "file citation",
			expected: ResponseOutputTextAnnotationUnionParam{
				OfFileCitation: &ResponseOutputTextAnnotationFileCitationParam{
					FileID:   "file123",
					Filename: "document.pdf",
					Index:    0,
					Type:     "file_citation",
				},
			},
			input: `{"file_id":"file123","filename":"document.pdf","index":0,"type":"file_citation"}`,
		},
		{
			name: "url citation",
			expected: ResponseOutputTextAnnotationUnionParam{
				OfURLCitation: &ResponseOutputTextAnnotationURLCitationParam{
					EndIndex:   100,
					StartIndex: 50,
					Title:      "Example Website",
					URL:        "https://example.com",
					Type:       "url_citation",
				},
			},
			input: `{"end_index":100,"start_index":50,"title":"Example Website","url":"https://example.com","type":"url_citation"}`,
		},
		{
			name: "container file citation",
			expected: ResponseOutputTextAnnotationUnionParam{
				OfContainerFileCitation: &ResponseOutputTextAnnotationContainerFileCitationParam{
					ContainerID: "container456",
					EndIndex:    150,
					FileID:      "file789",
					Filename:    "nested.txt",
					StartIndex:  100,
					Type:        "container_file_citation",
				},
			},
			input: `{"container_id":"container456","end_index":150,"file_id":"file789","filename":"nested.txt","start_index":100,"type":"container_file_citation"}`,
		},
		{
			name: "file path",
			expected: ResponseOutputTextAnnotationUnionParam{
				OfFilePath: &ResponseOutputTextAnnotationFilePathParam{
					FileID: "filePath123",
					Index:  2,
					Type:   "file_path",
				},
			},
			input: `{"file_id":"filePath123","index":2,"type":"file_path"}`,
		},
		{
			name:   "empty type",
			input:  `{"type": ""}`,
			expErr: "unknown type for output text annotation: ",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var result ResponseOutputTextAnnotationUnionParam
			err := json.Unmarshal([]byte(tc.input), &result)
			if tc.expErr != "" {
				require.ErrorContains(t, err, tc.expErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.expected, result)
		})
	}
}

func TestResponseFileSearchToolCallResultAttributeUnionParamMarshalJSON(t *testing.T) {
	str := "test_value"
	flt := 42.5
	bl := true
	for _, tc := range []struct {
		name   string
		input  *ResponseFileSearchToolCallResultAttributeUnionParam
		expect string
		expErr string
	}{
		{
			name: "string attribute",
			input: &ResponseFileSearchToolCallResultAttributeUnionParam{
				OfString: &str,
			},
			expect: `"test_value"`,
		},
		{
			name: "float attribute",
			input: &ResponseFileSearchToolCallResultAttributeUnionParam{
				OfFloat: &flt,
			},
			expect: `42.5`,
		},
		{
			name: "bool attribute",
			input: &ResponseFileSearchToolCallResultAttributeUnionParam{
				OfBool: &bl,
			},
			expect: `true`,
		},
		{
			name: "string attribute with empty string",
			input: &ResponseFileSearchToolCallResultAttributeUnionParam{
				OfString: func() *string { s := ""; return &s }(),
			},
			expect: `""`,
		},
		{
			name: "float attribute with zero",
			input: &ResponseFileSearchToolCallResultAttributeUnionParam{
				OfFloat: func() *float64 { f := 0.0; return &f }(),
			},
			expect: `0`,
		},
		{
			name: "bool attribute false",
			input: &ResponseFileSearchToolCallResultAttributeUnionParam{
				OfBool: func() *bool { b := false; return &b }(),
			},
			expect: `false`,
		},
		{
			name:   "no attribute set - error case",
			input:  &ResponseFileSearchToolCallResultAttributeUnionParam{},
			expErr: "no attribute to marshal in response file search tool call result",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data, err := json.Marshal(tc.input)
			if tc.expErr != "" {
				require.ErrorContains(t, err, tc.expErr)
				return
			}
			require.NoError(t, err)
			require.JSONEq(t, tc.expect, string(data))
		})
	}
}

func TestResponseFileSearchToolCallResultAttributeUnionParamUnmarshalJSON(t *testing.T) {
	str := "test_value"
	flt := 42.5
	bl := true
	for _, tc := range []struct {
		name   string
		expect *ResponseFileSearchToolCallResultAttributeUnionParam
		input  string
		expErr string
	}{
		{
			name: "string attribute",
			expect: &ResponseFileSearchToolCallResultAttributeUnionParam{
				OfString: &str,
			},
			input: `"test_value"`,
		},
		{
			name: "float attribute",
			expect: &ResponseFileSearchToolCallResultAttributeUnionParam{
				OfFloat: &flt,
			},
			input: `42.5`,
		},
		{
			name: "bool attribute",
			expect: &ResponseFileSearchToolCallResultAttributeUnionParam{
				OfBool: &bl,
			},
			input: `true`,
		},
		{
			name: "string attribute with empty string",
			expect: &ResponseFileSearchToolCallResultAttributeUnionParam{
				OfString: func() *string { s := ""; return &s }(),
			},
			input: `""`,
		},
		{
			name: "float attribute with zero",
			expect: &ResponseFileSearchToolCallResultAttributeUnionParam{
				OfFloat: func() *float64 { f := 0.0; return &f }(),
			},
			input: `0`,
		},
		{
			name: "bool attribute false",
			expect: &ResponseFileSearchToolCallResultAttributeUnionParam{
				OfBool: func() *bool { b := false; return &b }(),
			},
			input: `false`,
		},
		{
			name:   "no attribute set - error case",
			input:  `[]`,
			expErr: "unknown attribute type in response file search tool call result",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var result ResponseFileSearchToolCallResultAttributeUnionParam
			err := json.Unmarshal([]byte(tc.input), &result)
			if tc.expErr != "" {
				require.ErrorContains(t, err, tc.expErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.expect, &result)
		})
	}
}

func TestResponseComputerToolCallActionUnionParamMarshalJSON(t *testing.T) {
	tests := []struct {
		name   string
		expect []byte
		input  ResponseComputerToolCallActionUnionParam
		expErr string
	}{
		{
			name:   "marshal click action",
			expect: []byte(`{"type":"click","button":"left","x":100,"y":200}`),
			input: ResponseComputerToolCallActionUnionParam{
				OfClick: &ResponseComputerToolCallActionClickParam{
					Type:   "click",
					Button: "left",
					X:      100,
					Y:      200,
				},
			},
		},
		{
			name:   "marshal click action with right button",
			expect: []byte(`{"type":"click","button":"right","x":150,"y":250}`),
			input: ResponseComputerToolCallActionUnionParam{
				OfClick: &ResponseComputerToolCallActionClickParam{
					Type:   "click",
					Button: "right",
					X:      150,
					Y:      250,
				},
			},
		},
		{
			name:   "marshal double click action",
			expect: []byte(`{"type":"double_click","x":300,"y":400}`),
			input: ResponseComputerToolCallActionUnionParam{
				OfDoubleClick: &ResponseComputerToolCallActionDoubleClickParam{
					Type: "double_click",
					X:    300,
					Y:    400,
				},
			},
		},
		{
			name:   "marshal drag action",
			expect: []byte(`{"type":"drag","path":[{"x":100,"y":200},{"x":200,"y":300}]}`),
			input: ResponseComputerToolCallActionUnionParam{
				OfDrag: &ResponseComputerToolCallActionDragParam{
					Type: "drag",
					Path: []ResponseComputerToolCallActionDragPathParam{
						{X: 100, Y: 200},
						{X: 200, Y: 300},
					},
				},
			},
		},
		{
			name:   "marshal keypress action",
			expect: []byte(`{"type":"keypress","keys":["ctrl","c"]}`),
			input: ResponseComputerToolCallActionUnionParam{
				OfKeypress: &ResponseComputerToolCallActionKeypressParam{
					Type: "keypress",
					Keys: []string{"ctrl", "c"},
				},
			},
		},
		{
			name:   "marshal move action",
			expect: []byte(`{"type":"move","x":500,"y":600}`),
			input: ResponseComputerToolCallActionUnionParam{
				OfMove: &ResponseComputerToolCallActionMoveParam{
					Type: "move",
					X:    500,
					Y:    600,
				},
			},
		},
		{
			name:   "marshal screenshot action",
			expect: []byte(`{"type":"screenshot"}`),
			input: ResponseComputerToolCallActionUnionParam{
				OfScreenshot: &ResponseComputerToolCallActionScreenshotParam{
					Type: "screenshot",
				},
			},
		},
		{
			name:   "marshal scroll action",
			expect: []byte(`{"type":"scroll","scroll_x":50,"scroll_y":100,"x":400,"y":500}`),
			input: ResponseComputerToolCallActionUnionParam{
				OfScroll: &ResponseComputerToolCallActionScrollParam{
					Type:    "scroll",
					ScrollX: 50,
					ScrollY: 100,
					X:       400,
					Y:       500,
				},
			},
		},
		{
			name:   "marshal type action",
			expect: []byte(`{"type":"type","text":"Hello World"}`),
			input: ResponseComputerToolCallActionUnionParam{
				OfType: &ResponseComputerToolCallActionTypeParam{
					Type: "type",
					Text: "Hello World",
				},
			},
		},
		{
			name:   "marshal wait action",
			expect: []byte(`{"type":"wait"}`),
			input: ResponseComputerToolCallActionUnionParam{
				OfWait: &ResponseComputerToolCallActionWaitParam{
					Type: "wait",
				},
			},
		},
		{
			name:   "marshal nil union",
			input:  ResponseComputerToolCallActionUnionParam{},
			expErr: "no computer tool call action to marshal in computer tool call action",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			data, err := json.Marshal(tc.input)
			if tc.expErr != "" {
				require.ErrorContains(t, err, tc.expErr)
				return
			}
			require.NoError(t, err)
			require.JSONEq(t, string(tc.expect), string(data))
		})
	}
}

func TestResponseComputerToolCallActionUnionParamUnmarshalJSON(t *testing.T) {
	tests := []struct {
		name   string
		input  []byte
		expect ResponseComputerToolCallActionUnionParam
		expErr string
	}{
		{
			name:  "click action",
			input: []byte(`{"type":"click","button":"left","x":100,"y":200}`),
			expect: ResponseComputerToolCallActionUnionParam{
				OfClick: &ResponseComputerToolCallActionClickParam{
					Type:   "click",
					Button: "left",
					X:      100,
					Y:      200,
				},
			},
		},
		{
			name:  "click action with right button",
			input: []byte(`{"type":"click","button":"right","x":150,"y":250}`),
			expect: ResponseComputerToolCallActionUnionParam{
				OfClick: &ResponseComputerToolCallActionClickParam{
					Type:   "click",
					Button: "right",
					X:      150,
					Y:      250,
				},
			},
		},
		{
			name:  "double click action",
			input: []byte(`{"type":"double_click","x":300,"y":400}`),
			expect: ResponseComputerToolCallActionUnionParam{
				OfDoubleClick: &ResponseComputerToolCallActionDoubleClickParam{
					Type: "double_click",
					X:    300,
					Y:    400,
				},
			},
		},
		{
			name:  "drag action",
			input: []byte(`{"type":"drag","path":[{"x":100,"y":200},{"x":200,"y":300}]}`),
			expect: ResponseComputerToolCallActionUnionParam{
				OfDrag: &ResponseComputerToolCallActionDragParam{
					Type: "drag",
					Path: []ResponseComputerToolCallActionDragPathParam{
						{X: 100, Y: 200},
						{X: 200, Y: 300},
					},
				},
			},
		},
		{
			name:  "keypress action",
			input: []byte(`{"type":"keypress","keys":["ctrl","c"]}`),
			expect: ResponseComputerToolCallActionUnionParam{
				OfKeypress: &ResponseComputerToolCallActionKeypressParam{
					Type: "keypress",
					Keys: []string{"ctrl", "c"},
				},
			},
		},
		{
			name:  "move action",
			input: []byte(`{"type":"move","x":500,"y":600}`),
			expect: ResponseComputerToolCallActionUnionParam{
				OfMove: &ResponseComputerToolCallActionMoveParam{
					Type: "move",
					X:    500,
					Y:    600,
				},
			},
		},
		{
			name:  "screenshot action",
			input: []byte(`{"type":"screenshot"}`),
			expect: ResponseComputerToolCallActionUnionParam{
				OfScreenshot: &ResponseComputerToolCallActionScreenshotParam{
					Type: "screenshot",
				},
			},
		},
		{
			name:  "scroll action",
			input: []byte(`{"type":"scroll","scroll_x":50,"scroll_y":100,"x":400,"y":500}`),
			expect: ResponseComputerToolCallActionUnionParam{
				OfScroll: &ResponseComputerToolCallActionScrollParam{
					Type:    "scroll",
					ScrollX: 50,
					ScrollY: 100,
					X:       400,
					Y:       500,
				},
			},
		},
		{
			name:  "type action",
			input: []byte(`{"type":"type","text":"Hello World"}`),
			expect: ResponseComputerToolCallActionUnionParam{
				OfType: &ResponseComputerToolCallActionTypeParam{
					Type: "type",
					Text: "Hello World",
				},
			},
		},
		{
			name:  "wait action",
			input: []byte(`{"type":"wait"}`),
			expect: ResponseComputerToolCallActionUnionParam{
				OfWait: &ResponseComputerToolCallActionWaitParam{
					Type: "wait",
				},
			},
		},
		{
			name:   "empty type",
			input:  []byte(`{"type": ""}`),
			expErr: "unknown type for computer tool call action: ",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var result ResponseComputerToolCallActionUnionParam
			err := json.Unmarshal(tc.input, &result)
			if tc.expErr != "" {
				require.ErrorContains(t, err, tc.expErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.expect, result)
		})
	}
}

func TestResponseFunctionWebSearchActionUnionParamMarshalJSON(t *testing.T) {
	tests := []struct {
		name   string
		expect []byte
		input  ResponseFunctionWebSearchActionUnionParam
		expErr string
	}{
		{
			name:   "search action",
			expect: []byte(`{"type":"search","query":"golang","sources":[{"type":"url","url":"https://golang.org"}]}`),
			input: ResponseFunctionWebSearchActionUnionParam{
				OfSearch: &ResponseFunctionWebSearchActionSearchParam{
					Type:  "search",
					Query: "golang",
					Sources: []ResponseFunctionWebSearchActionSearchSourceParam{
						{
							Type: "url",
							URL:  "https://golang.org",
						},
					},
				},
			},
		},
		{
			name:   "search action without sources",
			expect: []byte(`{"type":"search","query":"python"}`),
			input: ResponseFunctionWebSearchActionUnionParam{
				OfSearch: &ResponseFunctionWebSearchActionSearchParam{
					Type:  "search",
					Query: "python",
				},
			},
		},
		{
			name:   "search action with multiple sources",
			expect: []byte(`{"type":"search","query":"machine learning","sources":[{"type":"url","url":"https://github.com"},{"type":"url","url":"https://wikipedia.org"}]}`),
			input: ResponseFunctionWebSearchActionUnionParam{
				OfSearch: &ResponseFunctionWebSearchActionSearchParam{
					Type:  "search",
					Query: "machine learning",
					Sources: []ResponseFunctionWebSearchActionSearchSourceParam{
						{
							Type: "url",
							URL:  "https://github.com",
						},
						{
							Type: "url",
							URL:  "https://wikipedia.org",
						},
					},
				},
			},
		},
		{
			name:   "open page action",
			expect: []byte(`{"type":"open_page","url":"https://example.com"}`),
			input: ResponseFunctionWebSearchActionUnionParam{
				OfOpenPage: &ResponseFunctionWebSearchActionOpenPageParam{
					Type: "open_page",
					URL:  "https://example.com",
				},
			},
		},
		{
			name:   "open page action with complex url",
			expect: []byte(`{"type":"open_page","url":"https://example.com/path?query=value&foo=bar"}`),
			input: ResponseFunctionWebSearchActionUnionParam{
				OfOpenPage: &ResponseFunctionWebSearchActionOpenPageParam{
					Type: "open_page",
					URL:  "https://example.com/path?query=value&foo=bar",
				},
			},
		},
		{
			name:   "find action",
			expect: []byte(`{"type":"find","pattern":"error","url":"https://example.com/logs"}`),
			input: ResponseFunctionWebSearchActionUnionParam{
				OfFind: &ResponseFunctionWebSearchActionFindParam{
					Type:    "find",
					Pattern: "error",
					URL:     "https://example.com/logs",
				},
			},
		},
		{
			name:   "find action with regex pattern",
			expect: []byte(`{"type":"find","pattern":"^[0-9]{3}-[0-9]{2}-[0-9]{4}$","url":"https://example.com/data"}`),
			input: ResponseFunctionWebSearchActionUnionParam{
				OfFind: &ResponseFunctionWebSearchActionFindParam{
					Type:    "find",
					Pattern: "^[0-9]{3}-[0-9]{2}-[0-9]{4}$",
					URL:     "https://example.com/data",
				},
			},
		},
		{
			name:   "nil union",
			input:  ResponseFunctionWebSearchActionUnionParam{},
			expErr: "no web search action to marshal in websearch action",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			data, err := json.Marshal(tc.input)
			if tc.expErr != "" {
				require.ErrorContains(t, err, tc.expErr)
				return
			}
			require.NoError(t, err)
			require.JSONEq(t, string(tc.expect), string(data))
		})
	}
}

func TestResponseFunctionWebSearchActionUnionParamUnmarshalJSON(t *testing.T) {
	tests := []struct {
		name   string
		input  []byte
		expect ResponseFunctionWebSearchActionUnionParam
		expErr string
	}{
		{
			name:  "search action",
			input: []byte(`{"type":"search","query":"golang","sources":[{"type":"url","url":"https://golang.org"}]}`),
			expect: ResponseFunctionWebSearchActionUnionParam{
				OfSearch: &ResponseFunctionWebSearchActionSearchParam{
					Type:  "search",
					Query: "golang",
					Sources: []ResponseFunctionWebSearchActionSearchSourceParam{
						{
							Type: "url",
							URL:  "https://golang.org",
						},
					},
				},
			},
		},
		{
			name:  "search action without sources",
			input: []byte(`{"type":"search","query":"python"}`),
			expect: ResponseFunctionWebSearchActionUnionParam{
				OfSearch: &ResponseFunctionWebSearchActionSearchParam{
					Type:  "search",
					Query: "python",
				},
			},
		},
		{
			name:  "search action with multiple sources",
			input: []byte(`{"type":"search","query":"machine learning","sources":[{"type":"url","url":"https://github.com"},{"type":"url","url":"https://wikipedia.org"}]}`),
			expect: ResponseFunctionWebSearchActionUnionParam{
				OfSearch: &ResponseFunctionWebSearchActionSearchParam{
					Type:  "search",
					Query: "machine learning",
					Sources: []ResponseFunctionWebSearchActionSearchSourceParam{
						{
							Type: "url",
							URL:  "https://github.com",
						},
						{
							Type: "url",
							URL:  "https://wikipedia.org",
						},
					},
				},
			},
		},
		{
			name:  "open page action",
			input: []byte(`{"type":"open_page","url":"https://example.com"}`),
			expect: ResponseFunctionWebSearchActionUnionParam{
				OfOpenPage: &ResponseFunctionWebSearchActionOpenPageParam{
					Type: "open_page",
					URL:  "https://example.com",
				},
			},
		},
		{
			name:  "open page action with complex url",
			input: []byte(`{"type":"open_page","url":"https://example.com/path?query=value&foo=bar"}`),
			expect: ResponseFunctionWebSearchActionUnionParam{
				OfOpenPage: &ResponseFunctionWebSearchActionOpenPageParam{
					Type: "open_page",
					URL:  "https://example.com/path?query=value&foo=bar",
				},
			},
		},
		{
			name:  "find action",
			input: []byte(`{"type":"find","pattern":"error","url":"https://example.com/logs"}`),
			expect: ResponseFunctionWebSearchActionUnionParam{
				OfFind: &ResponseFunctionWebSearchActionFindParam{
					Type:    "find",
					Pattern: "error",
					URL:     "https://example.com/logs",
				},
			},
		},
		{
			name:  "find action with regex pattern",
			input: []byte(`{"type":"find","pattern":"^[0-9]{3}-[0-9]{2}-[0-9]{4}$","url":"https://example.com/data"}`),
			expect: ResponseFunctionWebSearchActionUnionParam{
				OfFind: &ResponseFunctionWebSearchActionFindParam{
					Type:    "find",
					Pattern: "^[0-9]{3}-[0-9]{2}-[0-9]{4}$",
					URL:     "https://example.com/data",
				},
			},
		},
		{
			name:   "empty type",
			input:  []byte(`{"type": ""}`),
			expErr: "unknown type for websearch action: ",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var result ResponseFunctionWebSearchActionUnionParam
			err := json.Unmarshal(tc.input, &result)
			if tc.expErr != "" {
				require.ErrorContains(t, err, tc.expErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.expect, result)
		})
	}
}

func TestResponseInputItemFunctionCallOutputOutputUnionParamMarshalJSON(t *testing.T) {
	tests := []struct {
		name   string
		expect []byte
		input  ResponseInputItemFunctionCallOutputOutputUnionParam
		expErr string
	}{
		{
			name:   "string output",
			expect: []byte(`"output from function"`),
			input: ResponseInputItemFunctionCallOutputOutputUnionParam{
				OfString: ptr.To("output from function"),
			},
		},
		{
			name:   "string output with empty value",
			expect: []byte(`""`),
			input: ResponseInputItemFunctionCallOutputOutputUnionParam{
				OfString: ptr.To(""),
			},
		},
		{
			name:   "string output with special characters",
			expect: []byte(`"output with \"quotes\" and newline\n"`),
			input: ResponseInputItemFunctionCallOutputOutputUnionParam{
				OfString: ptr.To("output with \"quotes\" and newline\n"),
			},
		},
		{
			name:   "array with single text item",
			expect: []byte(`[{"text":"hello","type":"input_text"}]`),
			input: ResponseInputItemFunctionCallOutputOutputUnionParam{
				OfResponseFunctionCallOutputItemArray: []ResponseInputItemFunctionCallOutputItemUnionParam{
					{
						OfInputText: &ResponseInputTextContentParam{
							Text: "hello",
							Type: "input_text",
						},
					},
				},
			},
		},
		{
			name:   "array with text and image items",
			expect: []byte(`[{"text":"hello","type":"input_text"},{"type":"input_image","file_id":"file-123"}]`),
			input: ResponseInputItemFunctionCallOutputOutputUnionParam{
				OfResponseFunctionCallOutputItemArray: []ResponseInputItemFunctionCallOutputItemUnionParam{
					{
						OfInputText: &ResponseInputTextContentParam{
							Text: "hello",
							Type: "input_text",
						},
					},
					{
						OfInputImage: &ResponseInputImageContentParam{
							Type:   "input_image",
							FileID: "file-123",
						},
					},
				},
			},
		},
		{
			name:   "array with image item with url",
			expect: []byte(`[{"type":"input_image","image_url":"https://example.com/image.jpg","detail":"high"}]`),
			input: ResponseInputItemFunctionCallOutputOutputUnionParam{
				OfResponseFunctionCallOutputItemArray: []ResponseInputItemFunctionCallOutputItemUnionParam{
					{
						OfInputImage: &ResponseInputImageContentParam{
							Type:     "input_image",
							ImageURL: "https://example.com/image.jpg",
							Detail:   "high",
						},
					},
				},
			},
		},
		{
			name:   "array with file item",
			expect: []byte(`[{"type":"input_file","file_id":"file-456","filename":"data.json"}]`),
			input: ResponseInputItemFunctionCallOutputOutputUnionParam{
				OfResponseFunctionCallOutputItemArray: []ResponseInputItemFunctionCallOutputItemUnionParam{
					{
						OfInputFile: &ResponseInputFileContentParam{
							Type:     "input_file",
							FileID:   "file-456",
							Filename: "data.json",
						},
					},
				},
			},
		},
		{
			name:   "array with multiple items mixed types",
			expect: []byte(`[{"text":"hello","type":"input_text"},{"type":"input_image","file_id":"file-123"},{"type":"input_file","file_id":"file-456"}]`),
			input: ResponseInputItemFunctionCallOutputOutputUnionParam{
				OfResponseFunctionCallOutputItemArray: []ResponseInputItemFunctionCallOutputItemUnionParam{
					{
						OfInputText: &ResponseInputTextContentParam{
							Text: "hello",
							Type: "input_text",
						},
					},
					{
						OfInputImage: &ResponseInputImageContentParam{
							Type:   "input_image",
							FileID: "file-123",
						},
					},
					{
						OfInputFile: &ResponseInputFileContentParam{
							Type:   "input_file",
							FileID: "file-456",
						},
					},
				},
			},
		},
		{
			name:   "array with empty text item",
			expect: []byte(`[{"text":"","type":"input_text"}]`),
			input: ResponseInputItemFunctionCallOutputOutputUnionParam{
				OfResponseFunctionCallOutputItemArray: []ResponseInputItemFunctionCallOutputItemUnionParam{
					{
						OfInputText: &ResponseInputTextContentParam{
							Text: "",
							Type: "input_text",
						},
					},
				},
			},
		},
		{
			name:   "nil union",
			input:  ResponseInputItemFunctionCallOutputOutputUnionParam{},
			expErr: "no function call output to marshal in input item function call output",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			data, err := json.Marshal(tc.input)
			if tc.expErr != "" {
				require.ErrorContains(t, err, tc.expErr)
				return
			}
			require.NoError(t, err)
			require.JSONEq(t, string(tc.expect), string(data))
		})
	}
}

func TestResponseInputItemFunctionCallOutputOutputUnionParamUnmarshalJSON(t *testing.T) {
	tests := []struct {
		name   string
		input  []byte
		expect ResponseInputItemFunctionCallOutputOutputUnionParam
		expErr string
	}{
		{
			name:  "string output",
			input: []byte(`"output from function"`),
			expect: ResponseInputItemFunctionCallOutputOutputUnionParam{
				OfString: ptr.To("output from function"),
			},
		},
		{
			name:  "string output with empty value",
			input: []byte(`""`),
			expect: ResponseInputItemFunctionCallOutputOutputUnionParam{
				OfString: ptr.To(""),
			},
		},
		{
			name:  "string output with special characters",
			input: []byte(`"output with \"quotes\" and newline\n"`),
			expect: ResponseInputItemFunctionCallOutputOutputUnionParam{
				OfString: ptr.To("output with \"quotes\" and newline\n"),
			},
		},
		{
			name:  "array with single text item",
			input: []byte(`[{"text":"hello","type":"input_text"}]`),
			expect: ResponseInputItemFunctionCallOutputOutputUnionParam{
				OfResponseFunctionCallOutputItemArray: []ResponseInputItemFunctionCallOutputItemUnionParam{
					{
						OfInputText: &ResponseInputTextContentParam{
							Text: "hello",
							Type: "input_text",
						},
					},
				},
			},
		},
		{
			name:  "array with text and image items",
			input: []byte(`[{"text":"hello","type":"input_text"},{"type":"input_image","file_id":"file-123"}]`),
			expect: ResponseInputItemFunctionCallOutputOutputUnionParam{
				OfResponseFunctionCallOutputItemArray: []ResponseInputItemFunctionCallOutputItemUnionParam{
					{
						OfInputText: &ResponseInputTextContentParam{
							Text: "hello",
							Type: "input_text",
						},
					},
					{
						OfInputImage: &ResponseInputImageContentParam{
							Type:   "input_image",
							FileID: "file-123",
						},
					},
				},
			},
		},
		{
			name:  "array with image item with url",
			input: []byte(`[{"type":"input_image","image_url":"https://example.com/image.jpg","detail":"high"}]`),
			expect: ResponseInputItemFunctionCallOutputOutputUnionParam{
				OfResponseFunctionCallOutputItemArray: []ResponseInputItemFunctionCallOutputItemUnionParam{
					{
						OfInputImage: &ResponseInputImageContentParam{
							Type:     "input_image",
							ImageURL: "https://example.com/image.jpg",
							Detail:   "high",
						},
					},
				},
			},
		},
		{
			name:  "array with file item",
			input: []byte(`[{"type":"input_file","file_id":"file-456","filename":"data.json"}]`),
			expect: ResponseInputItemFunctionCallOutputOutputUnionParam{
				OfResponseFunctionCallOutputItemArray: []ResponseInputItemFunctionCallOutputItemUnionParam{
					{
						OfInputFile: &ResponseInputFileContentParam{
							Type:     "input_file",
							FileID:   "file-456",
							Filename: "data.json",
						},
					},
				},
			},
		},
		{
			name:  "array with multiple items mixed types",
			input: []byte(`[{"text":"hello","type":"input_text"},{"type":"input_image","file_id":"file-123"},{"type":"input_file","file_id":"file-456"}]`),
			expect: ResponseInputItemFunctionCallOutputOutputUnionParam{
				OfResponseFunctionCallOutputItemArray: []ResponseInputItemFunctionCallOutputItemUnionParam{
					{
						OfInputText: &ResponseInputTextContentParam{
							Text: "hello",
							Type: "input_text",
						},
					},
					{
						OfInputImage: &ResponseInputImageContentParam{
							Type:   "input_image",
							FileID: "file-123",
						},
					},
					{
						OfInputFile: &ResponseInputFileContentParam{
							Type:   "input_file",
							FileID: "file-456",
						},
					},
				},
			},
		},
		{
			name:  "array with empty text item",
			input: []byte(`[{"text":"","type":"input_text"}]`),
			expect: ResponseInputItemFunctionCallOutputOutputUnionParam{
				OfResponseFunctionCallOutputItemArray: []ResponseInputItemFunctionCallOutputItemUnionParam{
					{
						OfInputText: &ResponseInputTextContentParam{
							Text: "",
							Type: "input_text",
						},
					},
				},
			},
		},
		{
			name:   "invalid input",
			input:  []byte(`1231`),
			expErr: "unknown type for function call output in input item function call output",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var result ResponseInputItemFunctionCallOutputOutputUnionParam
			err := json.Unmarshal(tc.input, &result)
			if tc.expErr != "" {
				require.ErrorContains(t, err, tc.expErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.expect, result)
		})
	}
}

func TestResponseInputItemFunctionCallOutputItemUnionParamMarshalJSON(t *testing.T) {
	tests := []struct {
		name   string
		expect []byte
		input  ResponseInputItemFunctionCallOutputItemUnionParam
		expErr string
	}{
		{
			name:   "text input item",
			expect: []byte(`{"text":"hello","type":"input_text"}`),
			input: ResponseInputItemFunctionCallOutputItemUnionParam{
				OfInputText: &ResponseInputTextContentParam{
					Text: "hello",
					Type: "input_text",
				},
			},
		},
		{
			name:   "text input item with empty value",
			expect: []byte(`{"text":"","type":"input_text"}`),
			input: ResponseInputItemFunctionCallOutputItemUnionParam{
				OfInputText: &ResponseInputTextContentParam{
					Text: "",
					Type: "input_text",
				},
			},
		},
		{
			name:   "text input item with special characters",
			expect: []byte(`{"text":"hello with \"quotes\" and newline\n","type":"input_text"}`),
			input: ResponseInputItemFunctionCallOutputItemUnionParam{
				OfInputText: &ResponseInputTextContentParam{
					Text: "hello with \"quotes\" and newline\n",
					Type: "input_text",
				},
			},
		},
		{
			name:   "image input item with file_id",
			expect: []byte(`{"type":"input_image","file_id":"file-123"}`),
			input: ResponseInputItemFunctionCallOutputItemUnionParam{
				OfInputImage: &ResponseInputImageContentParam{
					Type:   "input_image",
					FileID: "file-123",
				},
			},
		},
		{
			name:   "image input item with image_url",
			expect: []byte(`{"type":"input_image","image_url":"https://example.com/image.jpg"}`),
			input: ResponseInputItemFunctionCallOutputItemUnionParam{
				OfInputImage: &ResponseInputImageContentParam{
					Type:     "input_image",
					ImageURL: "https://example.com/image.jpg",
				},
			},
		},
		{
			name:   "image input item with detail",
			expect: []byte(`{"type":"input_image","image_url":"https://example.com/image.jpg","detail":"high"}`),
			input: ResponseInputItemFunctionCallOutputItemUnionParam{
				OfInputImage: &ResponseInputImageContentParam{
					Type:     "input_image",
					ImageURL: "https://example.com/image.jpg",
					Detail:   "high",
				},
			},
		},
		{
			name:   "file input item with file_id",
			expect: []byte(`{"type":"input_file","file_id":"file-456"}`),
			input: ResponseInputItemFunctionCallOutputItemUnionParam{
				OfInputFile: &ResponseInputFileContentParam{
					Type:   "input_file",
					FileID: "file-456",
				},
			},
		},
		{
			name:   "file input item with filename",
			expect: []byte(`{"type":"input_file","file_id":"file-456","filename":"data.json"}`),
			input: ResponseInputItemFunctionCallOutputItemUnionParam{
				OfInputFile: &ResponseInputFileContentParam{
					Type:     "input_file",
					FileID:   "file-456",
					Filename: "data.json",
				},
			},
		},
		{
			name:   "file input item with all fields",
			expect: []byte(`{"type":"input_file","file_id":"file-456","filename":"document.pdf","file_data":"base64data"}`),
			input: ResponseInputItemFunctionCallOutputItemUnionParam{
				OfInputFile: &ResponseInputFileContentParam{
					Type:     "input_file",
					FileID:   "file-456",
					Filename: "document.pdf",
					FileData: "base64data",
				},
			},
		},
		{
			name:   "file input item with file_url",
			expect: []byte(`{"type":"input_file","file_id":"file-789","file_url":"https://example.com/file.pdf"}`),
			input: ResponseInputItemFunctionCallOutputItemUnionParam{
				OfInputFile: &ResponseInputFileContentParam{
					Type:    "input_file",
					FileID:  "file-789",
					FileURL: "https://example.com/file.pdf",
				},
			},
		},
		{
			name:   "video input item with url",
			expect: []byte(`{"type":"input_video","video_url":"https://example.com/video.mp4"}`),
			input: ResponseInputItemFunctionCallOutputItemUnionParam{
				OfInputVideo: &ResponseInputVideoContentParam{
					Type:     "input_video",
					VideoURL: "https://example.com/video.mp4",
				},
			},
		},
		{
			name:   "nil union",
			input:  ResponseInputItemFunctionCallOutputItemUnionParam{},
			expErr: "no function call output item to marshal in function call output item",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			data, err := json.Marshal(tc.input)
			if tc.expErr != "" {
				require.ErrorContains(t, err, tc.expErr)
				return
			}
			require.NoError(t, err)
			require.JSONEq(t, string(tc.expect), string(data))
		})
	}
}

func TestResponseInputItemFunctionCallOutputItemUnionParamUnmarshalJSON(t *testing.T) {
	tests := []struct {
		name   string
		input  []byte
		expect ResponseInputItemFunctionCallOutputItemUnionParam
		expErr string
	}{
		{
			name:  "text input item",
			input: []byte(`{"text":"hello","type":"input_text"}`),
			expect: ResponseInputItemFunctionCallOutputItemUnionParam{
				OfInputText: &ResponseInputTextContentParam{
					Text: "hello",
					Type: "input_text",
				},
			},
		},
		{
			name:  "text input item with empty value",
			input: []byte(`{"text":"","type":"input_text"}`),
			expect: ResponseInputItemFunctionCallOutputItemUnionParam{
				OfInputText: &ResponseInputTextContentParam{
					Text: "",
					Type: "input_text",
				},
			},
		},
		{
			name:  "text input item with special characters",
			input: []byte(`{"text":"hello with \"quotes\" and newline\n","type":"input_text"}`),
			expect: ResponseInputItemFunctionCallOutputItemUnionParam{
				OfInputText: &ResponseInputTextContentParam{
					Text: "hello with \"quotes\" and newline\n",
					Type: "input_text",
				},
			},
		},
		{
			name:  "image input item with file_id",
			input: []byte(`{"type":"input_image","file_id":"file-123"}`),
			expect: ResponseInputItemFunctionCallOutputItemUnionParam{
				OfInputImage: &ResponseInputImageContentParam{
					Type:   "input_image",
					FileID: "file-123",
				},
			},
		},
		{
			name:  "image input item with image_url",
			input: []byte(`{"type":"input_image","image_url":"https://example.com/image.jpg"}`),
			expect: ResponseInputItemFunctionCallOutputItemUnionParam{
				OfInputImage: &ResponseInputImageContentParam{
					Type:     "input_image",
					ImageURL: "https://example.com/image.jpg",
				},
			},
		},
		{
			name:  "image input item with detail",
			input: []byte(`{"type":"input_image","image_url":"https://example.com/image.jpg","detail":"high"}`),
			expect: ResponseInputItemFunctionCallOutputItemUnionParam{
				OfInputImage: &ResponseInputImageContentParam{
					Type:     "input_image",
					ImageURL: "https://example.com/image.jpg",
					Detail:   "high",
				},
			},
		},
		{
			name:  "file input item with file_id",
			input: []byte(`{"type":"input_file","file_id":"file-456"}`),
			expect: ResponseInputItemFunctionCallOutputItemUnionParam{
				OfInputFile: &ResponseInputFileContentParam{
					Type:   "input_file",
					FileID: "file-456",
				},
			},
		},
		{
			name:  "file input item with filename",
			input: []byte(`{"type":"input_file","file_id":"file-456","filename":"data.json"}`),
			expect: ResponseInputItemFunctionCallOutputItemUnionParam{
				OfInputFile: &ResponseInputFileContentParam{
					Type:     "input_file",
					FileID:   "file-456",
					Filename: "data.json",
				},
			},
		},
		{
			name:  "file input item with all fields",
			input: []byte(`{"type":"input_file","file_id":"file-456","filename":"document.pdf","file_data":"base64data"}`),
			expect: ResponseInputItemFunctionCallOutputItemUnionParam{
				OfInputFile: &ResponseInputFileContentParam{
					Type:     "input_file",
					FileID:   "file-456",
					Filename: "document.pdf",
					FileData: "base64data",
				},
			},
		},
		{
			name:  "file input item with file_url",
			input: []byte(`{"type":"input_file","file_id":"file-789","file_url":"https://example.com/file.pdf"}`),
			expect: ResponseInputItemFunctionCallOutputItemUnionParam{
				OfInputFile: &ResponseInputFileContentParam{
					Type:    "input_file",
					FileID:  "file-789",
					FileURL: "https://example.com/file.pdf",
				},
			},
		},
		{
			name:  "video input item with url",
			input: []byte(`{"type":"input_video","video_url":"https://example.com/video.mp4"}`),
			expect: ResponseInputItemFunctionCallOutputItemUnionParam{
				OfInputVideo: &ResponseInputVideoContentParam{
					Type:     "input_video",
					VideoURL: "https://example.com/video.mp4",
				},
			},
		},
		{
			name:   "empty type",
			input:  []byte(`{"type": ""}`),
			expErr: "unknown type for function call output item: ",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var result ResponseInputItemFunctionCallOutputItemUnionParam
			err := json.Unmarshal(tc.input, &result)
			if tc.expErr != "" {
				require.ErrorContains(t, err, tc.expErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.expect, result)
		})
	}
}

func TestResponseCodeInterpreterToolCallOutputUnionParamMarshalJSON(t *testing.T) {
	tests := []struct {
		name   string
		expect []byte
		input  ResponseCodeInterpreterToolCallOutputUnionParam
		expErr string
	}{
		{
			name:   "logs output",
			expect: []byte(`{"logs":"output from code execution","type":"logs"}`),
			input: ResponseCodeInterpreterToolCallOutputUnionParam{
				OfLogs: &ResponseCodeInterpreterToolCallOutputLogsParam{
					Logs: "output from code execution",
					Type: "logs",
				},
			},
		},
		{
			name:   "logs output with empty value",
			expect: []byte(`{"logs":"","type":"logs"}`),
			input: ResponseCodeInterpreterToolCallOutputUnionParam{
				OfLogs: &ResponseCodeInterpreterToolCallOutputLogsParam{
					Logs: "",
					Type: "logs",
				},
			},
		},
		{
			name:   "logs output with special characters",
			expect: []byte(`{"logs":"error: \"file not found\"\nstack trace:\nline 1","type":"logs"}`),
			input: ResponseCodeInterpreterToolCallOutputUnionParam{
				OfLogs: &ResponseCodeInterpreterToolCallOutputLogsParam{
					Logs: "error: \"file not found\"\nstack trace:\nline 1",
					Type: "logs",
				},
			},
		},
		{
			name:   "logs output with multiline content",
			expect: []byte(`{"logs":"line1\nline2\nline3","type":"logs"}`),
			input: ResponseCodeInterpreterToolCallOutputUnionParam{
				OfLogs: &ResponseCodeInterpreterToolCallOutputLogsParam{
					Logs: "line1\nline2\nline3",
					Type: "logs",
				},
			},
		},
		{
			name:   "image output",
			expect: []byte(`{"type":"image","url":"https://example.com/image.png"}`),
			input: ResponseCodeInterpreterToolCallOutputUnionParam{
				OfImage: &ResponseCodeInterpreterToolCallOutputImageParam{
					Type: "image",
					URL:  "https://example.com/image.png",
				},
			},
		},
		{
			name:   "image output with data url",
			expect: []byte(`{"type":"image","url":"data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg=="}`),
			input: ResponseCodeInterpreterToolCallOutputUnionParam{
				OfImage: &ResponseCodeInterpreterToolCallOutputImageParam{
					Type: "image",
					URL:  "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg==",
				},
			},
		},
		{
			name:   "nil union",
			input:  ResponseCodeInterpreterToolCallOutputUnionParam{},
			expErr: "no code interpreter tool call output to marshal",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			data, err := json.Marshal(tc.input)
			if tc.expErr != "" {
				require.ErrorContains(t, err, tc.expErr)
				return
			}
			require.NoError(t, err)
			require.JSONEq(t, string(tc.expect), string(data))
		})
	}
}

func TestResponseCodeInterpreterToolCallOutputUnionParamUnmarshalJSON(t *testing.T) {
	tests := []struct {
		name   string
		input  []byte
		expect ResponseCodeInterpreterToolCallOutputUnionParam
		expErr string
	}{
		{
			name:  "logs output",
			input: []byte(`{"logs":"output from code execution","type":"logs"}`),
			expect: ResponseCodeInterpreterToolCallOutputUnionParam{
				OfLogs: &ResponseCodeInterpreterToolCallOutputLogsParam{
					Logs: "output from code execution",
					Type: "logs",
				},
			},
		},
		{
			name:  "logs output with empty value",
			input: []byte(`{"logs":"","type":"logs"}`),
			expect: ResponseCodeInterpreterToolCallOutputUnionParam{
				OfLogs: &ResponseCodeInterpreterToolCallOutputLogsParam{
					Logs: "",
					Type: "logs",
				},
			},
		},
		{
			name:  "logs output with special characters",
			input: []byte(`{"logs":"error: \"file not found\"\nstack trace:\nline 1","type":"logs"}`),
			expect: ResponseCodeInterpreterToolCallOutputUnionParam{
				OfLogs: &ResponseCodeInterpreterToolCallOutputLogsParam{
					Logs: "error: \"file not found\"\nstack trace:\nline 1",
					Type: "logs",
				},
			},
		},
		{
			name:  "logs output with multiline content",
			input: []byte(`{"logs":"line1\nline2\nline3","type":"logs"}`),
			expect: ResponseCodeInterpreterToolCallOutputUnionParam{
				OfLogs: &ResponseCodeInterpreterToolCallOutputLogsParam{
					Logs: "line1\nline2\nline3",
					Type: "logs",
				},
			},
		},
		{
			name:  "image output",
			input: []byte(`{"type":"image","url":"https://example.com/image.png"}`),
			expect: ResponseCodeInterpreterToolCallOutputUnionParam{
				OfImage: &ResponseCodeInterpreterToolCallOutputImageParam{
					Type: "image",
					URL:  "https://example.com/image.png",
				},
			},
		},
		{
			name:  "image output with data url",
			input: []byte(`{"type":"image","url":"data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg=="}`),
			expect: ResponseCodeInterpreterToolCallOutputUnionParam{
				OfImage: &ResponseCodeInterpreterToolCallOutputImageParam{
					Type: "image",
					URL:  "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg==",
				},
			},
		},
		{
			name:   "empty type",
			input:  []byte(`{"type": ""}`),
			expErr: "unknown type for code interpreter tool call output: ",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var result ResponseCodeInterpreterToolCallOutputUnionParam
			err := json.Unmarshal(tc.input, &result)
			if tc.expErr != "" {
				require.ErrorContains(t, err, tc.expErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.expect, result)
		})
	}
}

func TestResponseFunctionShellCallOutputContentOutcomeUnionParamMarshalJSON(t *testing.T) {
	tests := []struct {
		name   string
		expect []byte
		input  ResponseFunctionShellCallOutputContentOutcomeUnionParam
		expErr string
	}{
		{
			name:   "timeout outcome",
			expect: []byte(`{"type":"timeout"}`),
			input: ResponseFunctionShellCallOutputContentOutcomeUnionParam{
				OfTimeout: &ResponseFunctionShellCallOutputContentOutcomeTimeoutParam{
					Type: "timeout",
				},
			},
		},
		{
			name:   "exit outcome with exit code 0",
			expect: []byte(`{"exit_code":0,"type":"exit"}`),
			input: ResponseFunctionShellCallOutputContentOutcomeUnionParam{
				OfExit: &ResponseFunctionShellCallOutputOutputContentOutcomeExitParam{
					ExitCode: 0,
					Type:     "exit",
				},
			},
		},
		{
			name:   "exit outcome with non-zero exit code",
			expect: []byte(`{"exit_code":127,"type":"exit"}`),
			input: ResponseFunctionShellCallOutputContentOutcomeUnionParam{
				OfExit: &ResponseFunctionShellCallOutputOutputContentOutcomeExitParam{
					ExitCode: 127,
					Type:     "exit",
				},
			},
		},
		{
			name:   "exit outcome with negative exit code",
			expect: []byte(`{"exit_code":-1,"type":"exit"}`),
			input: ResponseFunctionShellCallOutputContentOutcomeUnionParam{
				OfExit: &ResponseFunctionShellCallOutputOutputContentOutcomeExitParam{
					ExitCode: -1,
					Type:     "exit",
				},
			},
		},
		{
			name:   "exit outcome with large exit code",
			expect: []byte(`{"exit_code":255,"type":"exit"}`),
			input: ResponseFunctionShellCallOutputContentOutcomeUnionParam{
				OfExit: &ResponseFunctionShellCallOutputOutputContentOutcomeExitParam{
					ExitCode: 255,
					Type:     "exit",
				},
			},
		},
		{
			name:   "nil union",
			input:  ResponseFunctionShellCallOutputContentOutcomeUnionParam{},
			expErr: "no shell call output content outcome to marshal",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			data, err := json.Marshal(tc.input)
			if tc.expErr != "" {
				require.ErrorContains(t, err, tc.expErr)
				return
			}
			require.NoError(t, err)
			require.JSONEq(t, string(tc.expect), string(data))
		})
	}
}

func TestResponseFunctionShellCallOutputContentOutcomeUnionParamUnmarshalJSON(t *testing.T) {
	tests := []struct {
		name   string
		input  []byte
		expect ResponseFunctionShellCallOutputContentOutcomeUnionParam
		expErr string
	}{
		{
			name:  "timeout outcome",
			input: []byte(`{"type":"timeout"}`),
			expect: ResponseFunctionShellCallOutputContentOutcomeUnionParam{
				OfTimeout: &ResponseFunctionShellCallOutputContentOutcomeTimeoutParam{
					Type: "timeout",
				},
			},
		},
		{
			name:  "exit outcome with exit code 0",
			input: []byte(`{"exit_code":0,"type":"exit"}`),
			expect: ResponseFunctionShellCallOutputContentOutcomeUnionParam{
				OfExit: &ResponseFunctionShellCallOutputOutputContentOutcomeExitParam{
					ExitCode: 0,
					Type:     "exit",
				},
			},
		},
		{
			name:  "exit outcome with non-zero exit code",
			input: []byte(`{"exit_code":127,"type":"exit"}`),
			expect: ResponseFunctionShellCallOutputContentOutcomeUnionParam{
				OfExit: &ResponseFunctionShellCallOutputOutputContentOutcomeExitParam{
					ExitCode: 127,
					Type:     "exit",
				},
			},
		},
		{
			name:  "exit outcome with negative exit code",
			input: []byte(`{"exit_code":-1,"type":"exit"}`),
			expect: ResponseFunctionShellCallOutputContentOutcomeUnionParam{
				OfExit: &ResponseFunctionShellCallOutputOutputContentOutcomeExitParam{
					ExitCode: -1,
					Type:     "exit",
				},
			},
		},
		{
			name:  "exit outcome with large exit code",
			input: []byte(`{"exit_code":255,"type":"exit"}`),
			expect: ResponseFunctionShellCallOutputContentOutcomeUnionParam{
				OfExit: &ResponseFunctionShellCallOutputOutputContentOutcomeExitParam{
					ExitCode: 255,
					Type:     "exit",
				},
			},
		},
		{
			name:   "empty type",
			input:  []byte(`{"type": ""}`),
			expErr: "unknown type for function shell call output content outcome: ",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var result ResponseFunctionShellCallOutputContentOutcomeUnionParam
			err := json.Unmarshal(tc.input, &result)
			if tc.expErr != "" {
				require.ErrorContains(t, err, tc.expErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.expect, result)
		})
	}
}

func TestResponseInputItemApplyPatchCallOperationUnionParamMarshalJSON(t *testing.T) {
	tests := []struct {
		name   string
		expect []byte
		input  ResponseInputItemApplyPatchCallOperationUnionParam
		expErr string
	}{
		{
			name:   "create file operation",
			expect: []byte(`{"diff":"--- a/file.txt\n+++ b/file.txt\n@@ -1 +1 @@\n-old\n+new","path":"src/file.txt","type":"create_file"}`),
			input: ResponseInputItemApplyPatchCallOperationUnionParam{
				OfCreateFile: &ResponseInputItemApplyPatchCallOperationCreateFileParam{
					Diff: "--- a/file.txt\n+++ b/file.txt\n@@ -1 +1 @@\n-old\n+new",
					Path: "src/file.txt",
					Type: "create_file",
				},
			},
		},
		{
			name:   "create file operation with simple diff",
			expect: []byte(`{"diff":"@@ -1 +1 @@\n-hello\n+goodbye","path":"test.txt","type":"create_file"}`),
			input: ResponseInputItemApplyPatchCallOperationUnionParam{
				OfCreateFile: &ResponseInputItemApplyPatchCallOperationCreateFileParam{
					Diff: "@@ -1 +1 @@\n-hello\n+goodbye",
					Path: "test.txt",
					Type: "create_file",
				},
			},
		},
		{
			name:   "delete file operation",
			expect: []byte(`{"path":"src/old_file.txt","type":"delete_file"}`),
			input: ResponseInputItemApplyPatchCallOperationUnionParam{
				OfDeleteFile: &ResponseInputItemApplyPatchCallOperationDeleteFileParam{
					Path: "src/old_file.txt",
					Type: "delete_file",
				},
			},
		},
		{
			name:   "delete file operation with nested path",
			expect: []byte(`{"path":"src/components/button.tsx","type":"delete_file"}`),
			input: ResponseInputItemApplyPatchCallOperationUnionParam{
				OfDeleteFile: &ResponseInputItemApplyPatchCallOperationDeleteFileParam{
					Path: "src/components/button.tsx",
					Type: "delete_file",
				},
			},
		},
		{
			name:   "update file operation",
			expect: []byte(`{"diff":"--- a/config.yaml\n+++ b/config.yaml\n@@ -1,3 +1,4 @@\n version: 1\n+enabled: true","path":"config.yaml","type":"update_file"}`),
			input: ResponseInputItemApplyPatchCallOperationUnionParam{
				OfUpdateFile: &ResponseInputItemApplyPatchCallOperationUpdateFileParam{
					Diff: "--- a/config.yaml\n+++ b/config.yaml\n@@ -1,3 +1,4 @@\n version: 1\n+enabled: true",
					Path: "config.yaml",
					Type: "update_file",
				},
			},
		},
		{
			name:   "update file operation with empty diff",
			expect: []byte(`{"diff":"","path":"empty.txt","type":"update_file"}`),
			input: ResponseInputItemApplyPatchCallOperationUnionParam{
				OfUpdateFile: &ResponseInputItemApplyPatchCallOperationUpdateFileParam{
					Diff: "",
					Path: "empty.txt",
					Type: "update_file",
				},
			},
		},
		{
			name:   "update file operation with multi-line diff",
			expect: []byte(`{"diff":"--- a/main.go\n+++ b/main.go\n@@ -10,5 +10,6 @@\n package main\n import (\n \"fmt\"\n +\"os\"\n )\n func main() {","path":"main.go","type":"update_file"}`),
			input: ResponseInputItemApplyPatchCallOperationUnionParam{
				OfUpdateFile: &ResponseInputItemApplyPatchCallOperationUpdateFileParam{
					Diff: "--- a/main.go\n+++ b/main.go\n@@ -10,5 +10,6 @@\n package main\n import (\n \"fmt\"\n +\"os\"\n )\n func main() {",
					Path: "main.go",
					Type: "update_file",
				},
			},
		},
		{
			name:   "nil union",
			input:  ResponseInputItemApplyPatchCallOperationUnionParam{},
			expErr: "no apply patch call operation to marshal",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			data, err := json.Marshal(tc.input)
			if tc.expErr != "" {
				require.ErrorContains(t, err, tc.expErr)
				return
			}
			require.NoError(t, err)
			require.JSONEq(t, string(tc.expect), string(data))
		})
	}
}

func TestResponseInputItemApplyPatchCallOperationUnionParamUnmarshalJSON(t *testing.T) {
	tests := []struct {
		name   string
		input  []byte
		expect ResponseInputItemApplyPatchCallOperationUnionParam
		expErr string
	}{
		{
			name:  "create file operation",
			input: []byte(`{"diff":"--- a/file.txt\n+++ b/file.txt\n@@ -1 +1 @@\n-old\n+new","path":"src/file.txt","type":"create_file"}`),
			expect: ResponseInputItemApplyPatchCallOperationUnionParam{
				OfCreateFile: &ResponseInputItemApplyPatchCallOperationCreateFileParam{
					Diff: "--- a/file.txt\n+++ b/file.txt\n@@ -1 +1 @@\n-old\n+new",
					Path: "src/file.txt",
					Type: "create_file",
				},
			},
		},
		{
			name:  "create file operation with simple diff",
			input: []byte(`{"diff":"@@ -1 +1 @@\n-hello\n+goodbye","path":"test.txt","type":"create_file"}`),
			expect: ResponseInputItemApplyPatchCallOperationUnionParam{
				OfCreateFile: &ResponseInputItemApplyPatchCallOperationCreateFileParam{
					Diff: "@@ -1 +1 @@\n-hello\n+goodbye",
					Path: "test.txt",
					Type: "create_file",
				},
			},
		},
		{
			name:  "delete file operation",
			input: []byte(`{"path":"src/old_file.txt","type":"delete_file"}`),
			expect: ResponseInputItemApplyPatchCallOperationUnionParam{
				OfDeleteFile: &ResponseInputItemApplyPatchCallOperationDeleteFileParam{
					Path: "src/old_file.txt",
					Type: "delete_file",
				},
			},
		},
		{
			name:  "delete file operation with nested path",
			input: []byte(`{"path":"src/components/button.tsx","type":"delete_file"}`),
			expect: ResponseInputItemApplyPatchCallOperationUnionParam{
				OfDeleteFile: &ResponseInputItemApplyPatchCallOperationDeleteFileParam{
					Path: "src/components/button.tsx",
					Type: "delete_file",
				},
			},
		},
		{
			name:  "update file operation",
			input: []byte(`{"diff":"--- a/config.yaml\n+++ b/config.yaml\n@@ -1,3 +1,4 @@\n version: 1\n+enabled: true","path":"config.yaml","type":"update_file"}`),
			expect: ResponseInputItemApplyPatchCallOperationUnionParam{
				OfUpdateFile: &ResponseInputItemApplyPatchCallOperationUpdateFileParam{
					Diff: "--- a/config.yaml\n+++ b/config.yaml\n@@ -1,3 +1,4 @@\n version: 1\n+enabled: true",
					Path: "config.yaml",
					Type: "update_file",
				},
			},
		},
		{
			name:  "update file operation with empty diff",
			input: []byte(`{"diff":"","path":"empty.txt","type":"update_file"}`),
			expect: ResponseInputItemApplyPatchCallOperationUnionParam{
				OfUpdateFile: &ResponseInputItemApplyPatchCallOperationUpdateFileParam{
					Diff: "",
					Path: "empty.txt",
					Type: "update_file",
				},
			},
		},
		{
			name:  "update file operation with multi-line diff",
			input: []byte(`{"diff":"--- a/main.go\n+++ b/main.go\n@@ -10,5 +10,6 @@\n package main\n import (\n \"fmt\"\n +\"os\"\n )\n func main() {","path":"main.go","type":"update_file"}`),
			expect: ResponseInputItemApplyPatchCallOperationUnionParam{
				OfUpdateFile: &ResponseInputItemApplyPatchCallOperationUpdateFileParam{
					Diff: "--- a/main.go\n+++ b/main.go\n@@ -10,5 +10,6 @@\n package main\n import (\n \"fmt\"\n +\"os\"\n )\n func main() {",
					Path: "main.go",
					Type: "update_file",
				},
			},
		},
		{
			name:   "empty type",
			input:  []byte(`{"type": ""}`),
			expErr: "unknown type for input item apply patch call operation: ",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var result ResponseInputItemApplyPatchCallOperationUnionParam
			err := json.Unmarshal(tc.input, &result)
			if tc.expErr != "" {
				require.ErrorContains(t, err, tc.expErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.expect, result)
		})
	}
}

func TestResponseCustomToolCallOutputOutputUnionParamMarshalJSON(t *testing.T) {
	outputResult := "output result"
	resultWithSpecialChars := "result with\nnewline and\ttab"
	emptyString := ""

	tests := []struct {
		name   string
		expect []byte
		input  ResponseCustomToolCallOutputOutputUnionParam
		expErr string
	}{
		{
			name:   "string output",
			expect: []byte(`"output result"`),
			input: ResponseCustomToolCallOutputOutputUnionParam{
				OfString: &outputResult,
			},
		},
		{
			name:   "string output with special characters",
			expect: []byte(`"result with\nnewline and\ttab"`),
			input: ResponseCustomToolCallOutputOutputUnionParam{
				OfString: &resultWithSpecialChars,
			},
		},
		{
			name:   "string output with empty value",
			expect: []byte(`""`),
			input: ResponseCustomToolCallOutputOutputUnionParam{
				OfString: &emptyString,
			},
		},
		{
			name:   "output content list with single text item",
			expect: []byte(`[{"text":"Hello World","type":"input_text"}]`),
			input: ResponseCustomToolCallOutputOutputUnionParam{
				OfOutputContentList: []ResponseCustomToolCallOutputContentListItemUnionParam{
					{
						OfInputText: &ResponseInputTextParam{
							Text: "Hello World",
							Type: "input_text",
						},
					},
				},
			},
		},
		{
			name:   "output content list with multiple text items",
			expect: []byte(`[{"text":"First","type":"input_text"},{"text":"Second","type":"input_text"}]`),
			input: ResponseCustomToolCallOutputOutputUnionParam{
				OfOutputContentList: []ResponseCustomToolCallOutputContentListItemUnionParam{
					{
						OfInputText: &ResponseInputTextParam{
							Text: "First",
							Type: "input_text",
						},
					},
					{
						OfInputText: &ResponseInputTextParam{
							Text: "Second",
							Type: "input_text",
						},
					},
				},
			},
		},
		{
			name:   "output content list with image item",
			expect: []byte(`[{"detail":"high","file_id":"file-123","type":"input_image"}]`),
			input: ResponseCustomToolCallOutputOutputUnionParam{
				OfOutputContentList: []ResponseCustomToolCallOutputContentListItemUnionParam{
					{
						OfInputImage: &ResponseInputImageParam{
							Detail: "high",
							FileID: "file-123",
							Type:   "input_image",
						},
					},
				},
			},
		},
		{
			name:   "output content list with image item with image_url",
			expect: []byte(`[{"detail":"low","image_url":"https://example.com/image.png","type":"input_image"}]`),
			input: ResponseCustomToolCallOutputOutputUnionParam{
				OfOutputContentList: []ResponseCustomToolCallOutputContentListItemUnionParam{
					{
						OfInputImage: &ResponseInputImageParam{
							Detail:   "low",
							ImageURL: "https://example.com/image.png",
							Type:     "input_image",
						},
					},
				},
			},
		},
		{
			name:   "nil union",
			input:  ResponseCustomToolCallOutputOutputUnionParam{},
			expErr: "no custom tool call output to marshal",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			data, err := json.Marshal(tc.input)
			if tc.expErr != "" {
				require.ErrorContains(t, err, tc.expErr)
				return
			}
			require.NoError(t, err)
			require.JSONEq(t, string(tc.expect), string(data))
		})
	}
}

func TestResponseCustomToolCallOutputOutputUnionParamUnmarshalJSON(t *testing.T) {
	outputResult := "output result"
	resultWithSpecialChars := "result with\nnewline and\ttab"
	emptyString := ""

	tests := []struct {
		name   string
		input  []byte
		expect ResponseCustomToolCallOutputOutputUnionParam
		expErr string
	}{
		{
			name:  "string output",
			input: []byte(`"output result"`),
			expect: ResponseCustomToolCallOutputOutputUnionParam{
				OfString: &outputResult,
			},
		},
		{
			name:  "string output with special characters",
			input: []byte(`"result with\nnewline and\ttab"`),
			expect: ResponseCustomToolCallOutputOutputUnionParam{
				OfString: &resultWithSpecialChars,
			},
		},
		{
			name:  "string output with empty value",
			input: []byte(`""`),
			expect: ResponseCustomToolCallOutputOutputUnionParam{
				OfString: &emptyString,
			},
		},
		{
			name:  "output content list with single text item",
			input: []byte(`[{"text":"Hello World","type":"input_text"}]`),
			expect: ResponseCustomToolCallOutputOutputUnionParam{
				OfOutputContentList: []ResponseCustomToolCallOutputContentListItemUnionParam{
					{
						OfInputText: &ResponseInputTextParam{
							Text: "Hello World",
							Type: "input_text",
						},
					},
				},
			},
		},
		{
			name:  "output content list with multiple text items",
			input: []byte(`[{"text":"First","type":"input_text"},{"text":"Second","type":"input_text"}]`),
			expect: ResponseCustomToolCallOutputOutputUnionParam{
				OfOutputContentList: []ResponseCustomToolCallOutputContentListItemUnionParam{
					{
						OfInputText: &ResponseInputTextParam{
							Text: "First",
							Type: "input_text",
						},
					},
					{
						OfInputText: &ResponseInputTextParam{
							Text: "Second",
							Type: "input_text",
						},
					},
				},
			},
		},
		{
			name:  "output content list with image item",
			input: []byte(`[{"detail":"high","file_id":"file-123","type":"input_image"}]`),
			expect: ResponseCustomToolCallOutputOutputUnionParam{
				OfOutputContentList: []ResponseCustomToolCallOutputContentListItemUnionParam{
					{
						OfInputImage: &ResponseInputImageParam{
							Detail: "high",
							FileID: "file-123",
							Type:   "input_image",
						},
					},
				},
			},
		},
		{
			name:  "output content list with image item with image_url",
			input: []byte(`[{"detail":"low","image_url":"https://example.com/image.png","type":"input_image"}]`),
			expect: ResponseCustomToolCallOutputOutputUnionParam{
				OfOutputContentList: []ResponseCustomToolCallOutputContentListItemUnionParam{
					{
						OfInputImage: &ResponseInputImageParam{
							Detail:   "low",
							ImageURL: "https://example.com/image.png",
							Type:     "input_image",
						},
					},
				},
			},
		},
		{
			name:   "nil union",
			input:  []byte(`123`),
			expErr: "Mismatch type",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var result ResponseCustomToolCallOutputOutputUnionParam
			err := json.Unmarshal(tc.input, &result)
			if tc.expErr != "" {
				require.ErrorContains(t, err, tc.expErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.expect, result)
		})
	}
}

func TestResponseCustomToolCallOutputContentListItemUnionParamMarshalJSON(t *testing.T) {
	tests := []struct {
		name   string
		expect []byte
		input  ResponseCustomToolCallOutputContentListItemUnionParam
		expErr string
	}{
		{
			name:   "input text item",
			expect: []byte(`{"text":"Hello World","type":"input_text"}`),
			input: ResponseCustomToolCallOutputContentListItemUnionParam{
				OfInputText: &ResponseInputTextParam{
					Text: "Hello World",
					Type: "input_text",
				},
			},
		},
		{
			name:   "input text item with empty text",
			expect: []byte(`{"text":"","type":"input_text"}`),
			input: ResponseCustomToolCallOutputContentListItemUnionParam{
				OfInputText: &ResponseInputTextParam{
					Text: "",
					Type: "input_text",
				},
			},
		},
		{
			name:   "input text item with special characters",
			expect: []byte(`{"text":"Line 1\nLine 2\tTab","type":"input_text"}`),
			input: ResponseCustomToolCallOutputContentListItemUnionParam{
				OfInputText: &ResponseInputTextParam{
					Text: "Line 1\nLine 2\tTab",
					Type: "input_text",
				},
			},
		},
		{
			name:   "input image item with file ID",
			expect: []byte(`{"detail":"high","file_id":"file-123","type":"input_image"}`),
			input: ResponseCustomToolCallOutputContentListItemUnionParam{
				OfInputImage: &ResponseInputImageParam{
					Detail: "high",
					FileID: "file-123",
					Type:   "input_image",
				},
			},
		},
		{
			name:   "input image item with image URL",
			expect: []byte(`{"detail":"low","image_url":"https://example.com/image.png","type":"input_image"}`),
			input: ResponseCustomToolCallOutputContentListItemUnionParam{
				OfInputImage: &ResponseInputImageParam{
					Detail:   "low",
					ImageURL: "https://example.com/image.png",
					Type:     "input_image",
				},
			},
		},
		{
			name:   "input image item with auto detail",
			expect: []byte(`{"detail":"auto","file_id":"file-abc","type":"input_image"}`),
			input: ResponseCustomToolCallOutputContentListItemUnionParam{
				OfInputImage: &ResponseInputImageParam{
					Detail: "auto",
					FileID: "file-abc",
					Type:   "input_image",
				},
			},
		},
		{
			name:   "input file item with file ID",
			expect: []byte(`{"file_id":"file-456","type":"input_file"}`),
			input: ResponseCustomToolCallOutputContentListItemUnionParam{
				OfInputFile: &ResponseInputFileParam{
					FileID: "file-456",
					Type:   "input_file",
				},
			},
		},
		{
			name:   "input file item with file data",
			expect: []byte(`{"file_data":"base64encodeddata","type":"input_file"}`),
			input: ResponseCustomToolCallOutputContentListItemUnionParam{
				OfInputFile: &ResponseInputFileParam{
					FileData: "base64encodeddata",
					Type:     "input_file",
				},
			},
		},
		{
			name:   "input file item with both file ID and file data",
			expect: []byte(`{"file_data":"encoded","file_id":"file-789","type":"input_file"}`),
			input: ResponseCustomToolCallOutputContentListItemUnionParam{
				OfInputFile: &ResponseInputFileParam{
					FileID:   "file-789",
					FileData: "encoded",
					Type:     "input_file",
				},
			},
		},
		{
			name:   "nil union",
			input:  ResponseCustomToolCallOutputContentListItemUnionParam{},
			expErr: "no custom tool call output content list item to marshal",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			data, err := json.Marshal(tc.input)
			if tc.expErr != "" {
				require.ErrorContains(t, err, tc.expErr)
				return
			}
			require.NoError(t, err)
			require.JSONEq(t, string(tc.expect), string(data))
		})
	}
}

func TestResponseCustomToolCallOutputContentListItemUnionParamUnmarshalJSON(t *testing.T) {
	tests := []struct {
		name   string
		input  []byte
		expect ResponseCustomToolCallOutputContentListItemUnionParam
		expErr string
	}{
		{
			name:  "input text item",
			input: []byte(`{"text":"Hello World","type":"input_text"}`),
			expect: ResponseCustomToolCallOutputContentListItemUnionParam{
				OfInputText: &ResponseInputTextParam{
					Text: "Hello World",
					Type: "input_text",
				},
			},
		},
		{
			name:  "input text item with empty text",
			input: []byte(`{"text":"","type":"input_text"}`),
			expect: ResponseCustomToolCallOutputContentListItemUnionParam{
				OfInputText: &ResponseInputTextParam{
					Text: "",
					Type: "input_text",
				},
			},
		},
		{
			name:  "input text item with special characters",
			input: []byte(`{"text":"Line 1\nLine 2\tTab","type":"input_text"}`),
			expect: ResponseCustomToolCallOutputContentListItemUnionParam{
				OfInputText: &ResponseInputTextParam{
					Text: "Line 1\nLine 2\tTab",
					Type: "input_text",
				},
			},
		},
		{
			name:  "input image item with file ID",
			input: []byte(`{"detail":"high","file_id":"file-123","type":"input_image"}`),
			expect: ResponseCustomToolCallOutputContentListItemUnionParam{
				OfInputImage: &ResponseInputImageParam{
					Detail: "high",
					FileID: "file-123",
					Type:   "input_image",
				},
			},
		},
		{
			name:  "input image item with image URL",
			input: []byte(`{"detail":"low","image_url":"https://example.com/image.png","type":"input_image"}`),
			expect: ResponseCustomToolCallOutputContentListItemUnionParam{
				OfInputImage: &ResponseInputImageParam{
					Detail:   "low",
					ImageURL: "https://example.com/image.png",
					Type:     "input_image",
				},
			},
		},
		{
			name:  "input image item with auto detail",
			input: []byte(`{"detail":"auto","file_id":"file-abc","type":"input_image"}`),
			expect: ResponseCustomToolCallOutputContentListItemUnionParam{
				OfInputImage: &ResponseInputImageParam{
					Detail: "auto",
					FileID: "file-abc",
					Type:   "input_image",
				},
			},
		},
		{
			name:  "input file item with file ID",
			input: []byte(`{"file_id":"file-456","type":"input_file"}`),
			expect: ResponseCustomToolCallOutputContentListItemUnionParam{
				OfInputFile: &ResponseInputFileParam{
					FileID: "file-456",
					Type:   "input_file",
				},
			},
		},
		{
			name:  "input file item with file data",
			input: []byte(`{"file_data":"base64encodeddata","type":"input_file"}`),
			expect: ResponseCustomToolCallOutputContentListItemUnionParam{
				OfInputFile: &ResponseInputFileParam{
					FileData: "base64encodeddata",
					Type:     "input_file",
				},
			},
		},
		{
			name:  "input file item with both file ID and file data",
			input: []byte(`{"file_data":"encoded","file_id":"file-789","type":"input_file"}`),
			expect: ResponseCustomToolCallOutputContentListItemUnionParam{
				OfInputFile: &ResponseInputFileParam{
					FileID:   "file-789",
					FileData: "encoded",
					Type:     "input_file",
				},
			},
		},
		{
			name:   "empty type",
			input:  []byte(`{"type": ""}`),
			expErr: "unknown type for custom tool call output content list item: ",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var result ResponseCustomToolCallOutputContentListItemUnionParam
			err := json.Unmarshal(tc.input, &result)
			if tc.expErr != "" {
				require.ErrorContains(t, err, tc.expErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.expect, result)
		})
	}
}

func TestResponseToolChoiceUnionMarshalJSON(t *testing.T) {
	tests := []struct {
		name   string
		expect []byte
		input  ResponseToolChoiceUnion
		expErr string
	}{
		{
			name:   "tool choice mode - auto",
			expect: []byte(`"auto"`),
			input: ResponseToolChoiceUnion{
				OfToolChoiceMode: ptr.To("auto"),
			},
		},
		{
			name:   "tool choice mode - required",
			expect: []byte(`"required"`),
			input: ResponseToolChoiceUnion{
				OfToolChoiceMode: ptr.To("required"),
			},
		},
		{
			name:   "allowed tools",
			expect: []byte(`{"mode":"auto","tools":[{"type":"function","name":"get_weather"}],"type":"allowed_tools"}`),
			input: ResponseToolChoiceUnion{
				OfAllowedTools: &ToolChoiceAllowed{
					Mode: "auto",
					Tools: []map[string]any{
						{"type": "function", "name": "get_weather"},
					},
					Type: "allowed_tools",
				},
			},
		},
		{
			name:   "hosted tool - file search",
			expect: []byte(`{"type":"file_search"}`),
			input: ResponseToolChoiceUnion{
				OfHostedTool: &ToolChoiceTypes{
					Type: "file_search",
				},
			},
		},
		{
			name:   "hosted tool - code interpreter",
			expect: []byte(`{"type":"code_interpreter"}`),
			input: ResponseToolChoiceUnion{
				OfHostedTool: &ToolChoiceTypes{
					Type: "code_interpreter",
				},
			},
		},
		{
			name:   "hosted tool - image generation",
			expect: []byte(`{"type":"image_generation"}`),
			input: ResponseToolChoiceUnion{
				OfHostedTool: &ToolChoiceTypes{
					Type: "image_generation",
				},
			},
		},
		{
			name:   "function tool",
			expect: []byte(`{"name":"get_weather","type":"function"}`),
			input: ResponseToolChoiceUnion{
				OfFunctionTool: &ToolChoiceFunction{
					Name: "get_weather",
					Type: "function",
				},
			},
		},
		{
			name:   "mcp tool with server label only",
			expect: []byte(`{"server_label":"deepwiki","type":"mcp"}`),
			input: ResponseToolChoiceUnion{
				OfMcpTool: &ToolChoiceMcp{
					ServerLabel: "deepwiki",
					Type:        "mcp",
				},
			},
		},
		{
			name:   "mcp tool with server label and name",
			expect: []byte(`{"server_label":"deepwiki","name":"search","type":"mcp"}`),
			input: ResponseToolChoiceUnion{
				OfMcpTool: &ToolChoiceMcp{
					ServerLabel: "deepwiki",
					Name:        "search",
					Type:        "mcp",
				},
			},
		},
		{
			name:   "custom tool",
			expect: []byte(`{"name":"my_custom_tool","type":"custom"}`),
			input: ResponseToolChoiceUnion{
				OfCustomTool: &ToolChoiceCustom{
					Name: "my_custom_tool",
					Type: "custom",
				},
			},
		},
		{
			name:   "apply patch tool",
			expect: []byte(`{"type":"apply_patch"}`),
			input: ResponseToolChoiceUnion{
				OfApplyPatchTool: &ToolChoiceApplyPatch{
					Type: "apply_patch",
				},
			},
		},
		{
			name:   "shell tool",
			expect: []byte(`{"type":"shell"}`),
			input: ResponseToolChoiceUnion{
				OfShellTool: &ToolChoiceShell{
					Type: "shell",
				},
			},
		},
		{
			name:   "nil union",
			input:  ResponseToolChoiceUnion{},
			expErr: "no tool choice to marshal",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			data, err := json.Marshal(tc.input)
			if tc.expErr != "" {
				require.ErrorContains(t, err, tc.expErr)
				return
			}
			require.NoError(t, err)
			require.JSONEq(t, string(tc.expect), string(data))
		})
	}
}

func TestResponseToolChoiceUnionUnmarshalJSON(t *testing.T) {
	tests := []struct {
		name   string
		input  []byte
		expect ResponseToolChoiceUnion
		expErr string
	}{
		{
			name:  "tool choice mode - auto",
			input: []byte(`"auto"`),
			expect: ResponseToolChoiceUnion{
				OfToolChoiceMode: ptr.To("auto"),
			},
		},
		{
			name:  "tool choice mode - required",
			input: []byte(`"required"`),
			expect: ResponseToolChoiceUnion{
				OfToolChoiceMode: ptr.To("required"),
			},
		},
		{
			name:  "allowed tools",
			input: []byte(`{"mode":"auto","tools":[{"type":"function","name":"get_weather"}],"type":"allowed_tools"}`),
			expect: ResponseToolChoiceUnion{
				OfAllowedTools: &ToolChoiceAllowed{
					Mode: "auto",
					Tools: []map[string]any{
						{"type": "function", "name": "get_weather"},
					},
					Type: "allowed_tools",
				},
			},
		},
		{
			name:  "hosted tool - file search",
			input: []byte(`{"type":"file_search"}`),
			expect: ResponseToolChoiceUnion{
				OfHostedTool: &ToolChoiceTypes{
					Type: "file_search",
				},
			},
		},
		{
			name:  "hosted tool - code interpreter",
			input: []byte(`{"type":"code_interpreter"}`),
			expect: ResponseToolChoiceUnion{
				OfHostedTool: &ToolChoiceTypes{
					Type: "code_interpreter",
				},
			},
		},
		{
			name:  "hosted tool - image generation",
			input: []byte(`{"type":"image_generation"}`),
			expect: ResponseToolChoiceUnion{
				OfHostedTool: &ToolChoiceTypes{
					Type: "image_generation",
				},
			},
		},
		{
			name:  "hosted tool - web search preview",
			input: []byte(`{"type": "web_search_preview"}`),
			expect: ResponseToolChoiceUnion{
				OfHostedTool: &ToolChoiceTypes{
					Type: "web_search_preview",
				},
			},
		},
		{
			name:  "hosted tool - web search preview 2025",
			input: []byte(`{"type": "web_search_preview_2025_03_11"}`),
			expect: ResponseToolChoiceUnion{
				OfHostedTool: &ToolChoiceTypes{
					Type: "web_search_preview_2025_03_11",
				},
			},
		},
		{
			name:  "hosted tool - computer use preview",
			input: []byte(`{"type": "computer_use_preview"}`),
			expect: ResponseToolChoiceUnion{
				OfHostedTool: &ToolChoiceTypes{
					Type: "computer_use_preview",
				},
			},
		},
		{
			name:  "function tool",
			input: []byte(`{"name":"get_weather","type":"function"}`),
			expect: ResponseToolChoiceUnion{
				OfFunctionTool: &ToolChoiceFunction{
					Name: "get_weather",
					Type: "function",
				},
			},
		},
		{
			name:  "mcp tool with server label only",
			input: []byte(`{"server_label":"deepwiki","type":"mcp"}`),
			expect: ResponseToolChoiceUnion{
				OfMcpTool: &ToolChoiceMcp{
					ServerLabel: "deepwiki",
					Type:        "mcp",
				},
			},
		},
		{
			name:  "mcp tool with server label and name",
			input: []byte(`{"server_label":"deepwiki","name":"search","type":"mcp"}`),
			expect: ResponseToolChoiceUnion{
				OfMcpTool: &ToolChoiceMcp{
					ServerLabel: "deepwiki",
					Name:        "search",
					Type:        "mcp",
				},
			},
		},
		{
			name:  "custom tool",
			input: []byte(`{"name":"my_custom_tool","type":"custom"}`),
			expect: ResponseToolChoiceUnion{
				OfCustomTool: &ToolChoiceCustom{
					Name: "my_custom_tool",
					Type: "custom",
				},
			},
		},
		{
			name:  "apply patch tool",
			input: []byte(`{"type":"apply_patch"}`),
			expect: ResponseToolChoiceUnion{
				OfApplyPatchTool: &ToolChoiceApplyPatch{
					Type: "apply_patch",
				},
			},
		},
		{
			name:  "shell tool",
			input: []byte(`{"type":"shell"}`),
			expect: ResponseToolChoiceUnion{
				OfShellTool: &ToolChoiceShell{
					Type: "shell",
				},
			},
		},
		{
			name:   "nil union",
			input:  []byte(`{"type": ""}`),
			expErr: "unknown tool choice type: ",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var result ResponseToolChoiceUnion
			err := json.Unmarshal(tc.input, &result)
			if tc.expErr != "" {
				require.ErrorContains(t, err, tc.expErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.expect, result)
		})
	}
}

func TestResponseInstructionsUnionMarshalJSON(t *testing.T) {
	tests := []struct {
		name   string
		expect []byte
		input  ResponseInstructionsUnion
		expErr string
	}{
		{
			name:   "instructions as string",
			expect: []byte(`"You are a helpful assistant."`),
			input: ResponseInstructionsUnion{
				OfString: ptr.To("You are a helpful assistant."),
			},
		},
		{
			name:   "instructions as empty string",
			expect: []byte(`""`),
			input: ResponseInstructionsUnion{
				OfString: ptr.To(""),
			},
		},
		{
			name:   "instructions as input item list",
			expect: []byte(`[]`),
			input: ResponseInstructionsUnion{
				OfInputItemList: []ResponseInputItemUnionParam{},
			},
		},
		{
			name:   "nil union",
			input:  ResponseInstructionsUnion{},
			expErr: "no instructions to marshal",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			data, err := json.Marshal(tc.input)
			if tc.expErr != "" {
				require.ErrorContains(t, err, tc.expErr)
				return
			}
			require.NoError(t, err)
			require.JSONEq(t, string(tc.expect), string(data))
		})
	}
}

func TestResponseInstructionsUnionUnmarshalJSON(t *testing.T) {
	tests := []struct {
		name   string
		input  []byte
		expect ResponseInstructionsUnion
		expErr string
	}{
		{
			name:  "instructions as string",
			input: []byte(`"You are a helpful assistant."`),
			expect: ResponseInstructionsUnion{
				OfString: ptr.To("You are a helpful assistant."),
			},
		},
		{
			name:  "instructions as empty string",
			input: []byte(`""`),
			expect: ResponseInstructionsUnion{
				OfString: ptr.To(""),
			},
		},
		{
			name:  "instructions as input item list",
			input: []byte(`[]`),
			expect: ResponseInstructionsUnion{
				OfInputItemList: []ResponseInputItemUnionParam{},
			},
		},
		{
			name:   "invalid",
			input:  []byte(`123`),
			expErr: "Mismatch type",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var result ResponseInstructionsUnion
			err := json.Unmarshal(tc.input, &result)
			if tc.expErr != "" {
				require.ErrorContains(t, err, tc.expErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.expect, result)
		})
	}
}

func TestResponseOutputItemUnionMarshalJSON(t *testing.T) {
	tests := []struct {
		name   string
		expect []byte
		input  ResponseOutputItemUnion
		expErr string
	}{
		{
			name:   "output message",
			expect: []byte(`{"id":"msg_123","type":"message","role":"assistant","content": [{"text": "Hello! How can I assist you ?", "type": "output_text"}], "status":"completed"}`),
			input: ResponseOutputItemUnion{
				OfOutputMessage: &ResponseOutputMessage{
					ID:   "msg_123",
					Type: "message",
					Role: "assistant",
					Content: ResponseOutputMessageContentUnion{
						OfContentArray: []ResponseOutputMessageContentArrayUnion{
							{OfOutputText: &ResponseOutputTextParam{Text: "Hello! How can I assist you ?", Type: "output_text"}},
						},
					},
					Status: "completed",
				},
			},
		},
		{
			name:   "reasoning item",
			expect: []byte(`{"id":"reasoning_123","type":"reasoning","status":"completed","content": [{"text": "the capital of France is Paris.", "type": "reasoning_text"}], "summary": [{"text": "looking at a straightforward question: the capital of France is Paris", "type": "summary_text"}]}`),
			input: ResponseOutputItemUnion{
				OfReasoning: &ResponseReasoningItem{
					ID:     "reasoning_123",
					Type:   "reasoning",
					Status: "completed",
					Content: []ResponseReasoningItemContentParam{
						{Text: "the capital of France is Paris.", Type: "reasoning_text"},
					},
					Summary: []ResponseReasoningItemSummaryParam{
						{Text: "looking at a straightforward question: the capital of France is Paris", Type: "summary_text"},
					},
				},
			},
		},
		{
			name:   "function call",
			expect: []byte(`{"type":"function_call","id":"func_123","call_id": "call-789","name":"get_weather","namespace":"weather","arguments": "{\"arg1\": \"value\"}"}`),
			input: ResponseOutputItemUnion{
				OfFunctionCall: &ResponseFunctionToolCall{
					Type:      "function_call",
					ID:        "func_123",
					Name:      "get_weather",
					Namespace: "weather",
					CallID:    "call-789",
					Arguments: `{"arg1": "value"}`,
				},
			},
		},
		{
			name: "function call output",
			input: ResponseOutputItemUnion{
				OfFunctionCallOutput: &ResponseFunctionCallOutput{
					CallID: "call-123",
					Output: ResponseFunctionCallOutputOutputUnionParam{
						OfString: ptr.To("function output string"),
					},
					ID:     "resp-123",
					Status: "completed",
					Type:   "function_call_output",
				},
			},
			expect: []byte(`{"type":"function_call_output","call_id":"call-123","output":"function output string","id":"resp-123","status":"completed"}`),
		},
		{
			name:   "code interpreter call",
			expect: []byte(`{"type": "code_interpreter_call", "id": "resp-123", "container_id": "cntr_320", "status": "completed", "code": "print(\"Hello, World!\")", "outputs": [{"type": "logs", "logs": "log contents"}]}`),
			input: ResponseOutputItemUnion{
				OfCodeInterpreterCall: &ResponseCodeInterpreterToolCall{
					Type:        "code_interpreter_call",
					ID:          "resp-123",
					ContainerID: "cntr_320",
					Status:      "completed",
					Code:        "print(\"Hello, World!\")",
					Outputs: []ResponseCodeInterpreterToolCallOutputUnion{
						{OfResponseCodeInterpreterToolCallOutputLogs: &ResponseCodeInterpreterToolCallOutputLogs{
							Type: "logs",
							Logs: "log contents",
						}},
					},
				},
			},
		},
		{
			name: "computer_call",
			input: ResponseOutputItemUnion{
				OfComputerCall: &ResponseComputerToolCall{
					Type:   "computer_call",
					CallID: "call-456",
					ID:     "rs-123",
					Action: ResponseComputerToolCallActionUnionParam{
						OfClick: &ResponseComputerToolCallActionClickParam{
							Button: "left",
							X:      100,
							Y:      200,
							Type:   "click",
						},
					},
				},
			},
			expect: []byte(`{"type": "computer_call", "call_id": "call-456", "id": "rs-123", "action": {"type": "click", "button": "left", "x": 100, "y": 200}}`),
		},
		{
			name: "computer_call_output",
			input: ResponseOutputItemUnion{
				OfComputerCallOutput: &ResponseComputerToolCallOutputItem{
					Type:   "computer_call_output",
					ID:     "rs-123",
					CallID: "call-456",
					Status: "completed",
					Output: ResponseComputerToolCallOutputScreenshotParam{
						Type:     "computer_screenshot",
						ImageURL: "data:image/png;base64,screenshot_base64",
					},
				},
			},
			expect: []byte(`{"type": "computer_call_output", "id": "rs-123", "call_id": "call-456", "status": "completed", "output": {"type": "computer_screenshot", "image_url": "data:image/png;base64,screenshot_base64"}}`),
		},
		{
			name:   "file search call",
			expect: []byte(`{"type":"file_search_call","id":"search_123","queries": ["What is deep research?"], "results": [{"file_id": "file-2d", "filename": "deep_research_blog.pdf"}]}`),
			input: ResponseOutputItemUnion{
				OfFileSearchCall: &ResponseFileSearchToolCall{
					Type: "file_search_call",
					ID:   "search_123",
					Results: []ResponseFileSearchToolCallResultParam{
						{FileID: "file-2d", Filename: "deep_research_blog.pdf"},
					},
					Queries: []string{"What is deep research?"},
				},
			},
		},
		{
			name:   "web search call",
			expect: []byte(`{"type":"web_search_call","id":"web_123","action": {"type": "search", "query": "What is deep research?", "sources": [{"type": "url", "url": "https://example.com"}]}}`),
			input: ResponseOutputItemUnion{
				OfWebSearchCall: &ResponseFunctionWebSearch{
					Type: "web_search_call",
					ID:   "web_123",
					Action: ResponseFunctionWebSearchActionUnionParam{
						OfSearch: &ResponseFunctionWebSearchActionSearchParam{
							Type:  "search",
							Query: "What is deep research?",
							Sources: []ResponseFunctionWebSearchActionSearchSourceParam{
								{Type: "url", URL: "https://example.com"},
							},
						},
					},
				},
			},
		},
		{
			name:   "tool search call",
			expect: []byte(`{"type":"tool_search_call","id":"tsc-123","status":"completed","arguments":{"paths":["billing"]},"call_id":null,"execution":"server"}`),
			input: ResponseOutputItemUnion{
				OfToolSearchCall: &ResponseToolSearchCall{
					Type:      "tool_search_call",
					ID:        "tsc-123",
					Status:    "completed",
					Arguments: map[string]any{"paths": []string{"billing"}},
					CallID:    nil,
					Execution: "server",
				},
			},
		},
		{
			name:   "tool search output",
			expect: []byte(`{"type":"tool_search_output","id":"tso-123","status":"completed","call_id":null,"execution":"server","tools":[{"type":"namespace","name":"billing","description":"Billing tools.","tools":[{"type":"function","name":"get_invoice","description":"Get invoice details by invoice ID.","defer_loading":true}]}]}`),
			input: ResponseOutputItemUnion{
				OfToolSearchOutput: &ResponseToolSearchOutput{
					Type:      "tool_search_output",
					ID:        "tso-123",
					Status:    "completed",
					CallID:    nil,
					Execution: "server",
					Tools: []ResponseToolUnion{
						{
							OfNamespace: &NamespaceToolParam{
								Type:        "namespace",
								Name:        "billing",
								Description: "Billing tools.",
								Tools: []NamespaceToolToolUnionParam{
									{
										OfFunction: &NamespaceToolToolFunctionParam{
											Type:         "function",
											Name:         "get_invoice",
											Description:  "Get invoice details by invoice ID.",
											DeferLoading: ptr.To(true),
										},
									},
								},
							},
						},
					},
				},
			},
		},
		{
			name:   "additional tools",
			expect: []byte(`{"type":"additional_tools","id":"at-123","role":"developer","tools":[{"type":"tool_search","parameters":null}]}`),
			input: ResponseOutputItemUnion{
				OfAdditionalTools: &ResponseAdditionalTools{
					Type: "additional_tools",
					ID:   "at-123",
					Role: "developer",
					Tools: []ResponseToolUnion{
						{
							OfToolSearch: &ToolSearchToolParam{
								Type: "tool_search",
							},
						},
					},
				},
			},
		},
		{
			name: "compaction",
			input: ResponseOutputItemUnion{
				OfCompaction: &ResponseCompactionItem{
					Type:             "compaction",
					ID:               "resp-123",
					EncryptedContent: "encrypted_content",
				},
			},
			expect: []byte(`{"type": "compaction", "id": "resp-123", "encrypted_content": "encrypted_content"}`),
		},
		{
			name: "image_generation_call",
			input: ResponseOutputItemUnion{
				OfImageGenerationCall: &ResponseOutputItemImageGenerationCall{
					Type:   "image_generation_call",
					ID:     "rs-123",
					Status: "completed",
					Result: "data:image/png;base64,encoded_image",
				},
			},
			expect: []byte(`{"type": "image_generation_call", "id": "rs-123", "status": "completed", "result": "data:image/png;base64,encoded_image"}`),
		},
		{
			name: "local_shell_call",
			input: ResponseOutputItemUnion{
				OfLocalShellCall: &ResponseOutputItemLocalShellCall{
					Type:   "local_shell_call",
					CallID: "call-123",
					ID:     "rs-123",
					Status: "in_progress",
					Action: ResponseOutputItemLocalShellCallAction{
						Type:    "exec",
						Command: []string{"ls", "-a"},
						Env:     map[string]string{"TEST": "test"},
					},
				},
			},
			expect: []byte(`{"type": "local_shell_call", "call_id": "call-123", "id": "rs-123", "status": "in_progress", "action": {"type": "exec", "command": ["ls", "-a"], "env": {"TEST": "test"}}}`),
		},
		{
			name: "shell_call",
			input: ResponseOutputItemUnion{
				OfShellCall: &ResponseFunctionShellToolCall{
					Type:   "shell_call",
					CallID: "call-123",
					ID:     "resp-123",
					Status: "completed",
					Action: ResponseFunctionShellToolCallAction{
						Commands:        []string{"ls", "-a"},
						MaxOutputLength: 200,
					},
				},
			},
			expect: []byte(`{"type": "shell_call", "call_id": "call-123", "id": "resp-123","status": "completed", "action": {"commands": ["ls", "-a"],"max_output_length":200}}`),
		},
		{
			name: "shell_call_output",
			input: ResponseOutputItemUnion{
				OfShellCallOutput: &ResponseFunctionShellToolCallOutput{
					Type:            "shell_call_output",
					CallID:          "call-123",
					ID:              "resp-123",
					MaxOutputLength: 200,
					Output: []ResponseFunctionShellToolCallOutputOutput{
						{
							Stderr: "",
							Stdout: "Documents\nDownloads",
							Outcome: ResponseFunctionShellToolCallOutputOutputOutcomeUnion{
								OfResponseFunctionShellToolCallOutputOutputOutcomeExit: &ResponseFunctionShellToolCallOutputOutputOutcomeExit{
									Type:     "exit",
									ExitCode: 0,
								},
							},
						},
					},
				},
			},
			expect: []byte(`{"type": "shell_call_output", "call_id": "call-123", "id": "resp-123", "max_output_length": 200, "output": [{"stderr": "", "stdout": "Documents\nDownloads", "outcome": {"type": "exit", "exit_code": 0}}]}`),
		},
		{
			name: "apply_patch_call",
			input: ResponseOutputItemUnion{
				OfApplyPatchCall: &ResponseApplyPatchToolCall{
					Type:   "apply_patch_call",
					ID:     "resp-123",
					CallID: "call-123",
					Status: "completed",
					Operation: ResponseApplyPatchToolCallOperationUnion{
						OfResponseApplyPatchToolCallOperationCreateFile: &ResponseApplyPatchToolCallOperationCreateFile{
							Type: "create_file",
							Path: "/home/Documents",
							Diff: "",
						},
					},
				},
			},
			expect: []byte(`{"type": "apply_patch_call", "id": "resp-123", "call_id": "call-123", "status": "completed", "operation": {"type": "create_file", "path": "/home/Documents", "diff": ""} }`),
		},
		{
			name: "apply_patch_call_output",
			input: ResponseOutputItemUnion{
				OfApplyPatchCallOutput: &ResponseApplyPatchToolCallOutput{
					Type:   "apply_patch_call_output",
					ID:     "resp-123",
					CallID: "call-123",
					Status: "completed",
					Output: "log contents",
				},
			},
			expect: []byte(`{"type": "apply_patch_call_output", "id": "resp-123", "call_id": "call-123", "status": "completed", "output": "log contents"}`),
		},
		{
			name: "mcp_list_tools",
			input: ResponseOutputItemUnion{
				OfMcpListTools: &ResponseMcpListTools{
					Type:        "mcp_list_tools",
					ID:          "id-123",
					ServerLabel: "test-server",
					Tools: []ResponseOutputItemMcpListTools{
						{InputSchema: "{\"schemaVersion\": \"1.0\"}", Name: "test"},
					},
				},
			},
			expect: []byte(`{"type": "mcp_list_tools", "id": "id-123", "server_label": "test-server", "tools": [{"input_schema": "{\"schemaVersion\": \"1.0\"}", "name": "test"}]}`),
		},
		{
			name: "mcp_approval_request",
			input: ResponseOutputItemUnion{
				OfMcpApprovalRequest: &ResponseMcpApprovalRequest{
					Type:        "mcp_approval_request",
					ID:          "id-123",
					ServerLabel: "test-server",
					Name:        "test",
					Arguments:   "{\"arg1\": \"val\"}",
				},
			},
			expect: []byte(`{"type": "mcp_approval_request", "id": "id-123", "server_label": "test-server", "name": "test", "arguments": "{\"arg1\": \"val\"}"}`),
		},
		{
			name: "mcp_approval_response",
			input: ResponseOutputItemUnion{
				OfMcpApprovalResponse: &ResponseInputItemMcpApprovalResponseParam{
					Type:              "mcp_approval_response",
					ID:                "resp-123",
					Approve:           true,
					ApprovalRequestID: "req-123",
					Reason:            "approved",
				},
			},
			expect: []byte(`{"type": "mcp_approval_response", "id": "resp-123", "approve": true, "approval_request_id": "req-123", "reason": "approved"}`),
		},
		{
			name:   "mcp call",
			expect: []byte(`{"type":"mcp_call","id":"mcp_123","server_label": "test-server", "name": "test", "arguments": "{\"arg1\": \"val\"}", "approval_request_id": "req-123"}`),
			input: ResponseOutputItemUnion{
				OfMcpCall: &ResponseMcpCall{
					Type:              "mcp_call",
					ID:                "mcp_123",
					ServerLabel:       "test-server",
					Name:              "test",
					Arguments:         "{\"arg1\": \"val\"}",
					ApprovalRequestID: "req-123",
				},
			},
		},
		{
			name:   "custom tool call",
			expect: []byte(`{"type":"custom_tool_call","name":"test","id":"custom_123","call_id":"call-123","input": "some input"}`),
			input: ResponseOutputItemUnion{
				OfCustomToolCall: &ResponseCustomToolCall{
					Type:   "custom_tool_call",
					ID:     "custom_123",
					CallID: "call-123",
					Input:  "some input",
					Name:   "test",
				},
			},
		},
		{
			name:   "custom tool call output",
			expect: []byte(`{"type":"custom_tool_call_output","id":"custom-output-123","call_id":"call-123","status":"completed","output":"some output"}`),
			input: ResponseOutputItemUnion{
				OfCustomToolCallOutput: &ResponseCustomToolCallOutputItem{
					Type:   "custom_tool_call_output",
					ID:     "custom-output-123",
					CallID: "call-123",
					Status: "completed",
					Output: ResponseCustomToolCallOutputOutputUnionParam{
						OfString: ptr.To("some output"),
					},
				},
			},
		},
		{
			name:   "nil union",
			input:  ResponseOutputItemUnion{},
			expErr: "no output item to marshal",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			data, err := json.Marshal(tc.input)
			if tc.expErr != "" {
				require.ErrorContains(t, err, tc.expErr)
				return
			}
			require.NoError(t, err)
			require.JSONEq(t, string(tc.expect), string(data))
		})
	}
}

func TestResponseOutputItemUnionUnmarshalJSON(t *testing.T) {
	tests := []struct {
		name   string
		input  []byte
		expect ResponseOutputItemUnion
		expErr string
	}{
		{
			name:  "output message",
			input: []byte(`{"id":"msg_123","type":"message","role":"assistant","content": [{"text": "Hello! How can I assist you ?", "type": "output_text"}], "status":"completed"}`),
			expect: ResponseOutputItemUnion{
				OfOutputMessage: &ResponseOutputMessage{
					ID:   "msg_123",
					Type: "message",
					Role: "assistant",
					Content: ResponseOutputMessageContentUnion{
						OfContentArray: []ResponseOutputMessageContentArrayUnion{
							{OfOutputText: &ResponseOutputTextParam{Text: "Hello! How can I assist you ?", Type: "output_text"}},
						},
					},
					Status: "completed",
				},
			},
		},
		{
			name:  "reasoning item",
			input: []byte(`{"id":"reasoning_123","type":"reasoning","status":"completed","content": [{"text": "the capital of France is Paris.", "type": "reasoning_text"}], "summary": [{"text": "looking at a straightforward question: the capital of France is Paris", "type": "summary_text"}]}`),
			expect: ResponseOutputItemUnion{
				OfReasoning: &ResponseReasoningItem{
					ID:     "reasoning_123",
					Type:   "reasoning",
					Status: "completed",
					Content: []ResponseReasoningItemContentParam{
						{Text: "the capital of France is Paris.", Type: "reasoning_text"},
					},
					Summary: []ResponseReasoningItemSummaryParam{
						{Text: "looking at a straightforward question: the capital of France is Paris", Type: "summary_text"},
					},
				},
			},
		},
		{
			name:  "function call",
			input: []byte(`{"type":"function_call","id":"func_123","call_id": "call-789","name":"get_weather","namespace":"weather","arguments": "{\"arg1\": \"value\"}"}`),
			expect: ResponseOutputItemUnion{
				OfFunctionCall: &ResponseFunctionToolCall{
					Type:      "function_call",
					ID:        "func_123",
					Name:      "get_weather",
					Namespace: "weather",
					CallID:    "call-789",
					Arguments: `{"arg1": "value"}`,
				},
			},
		},
		{
			name: "function call output",
			expect: ResponseOutputItemUnion{
				OfFunctionCallOutput: &ResponseFunctionCallOutput{
					CallID: "call-123",
					Output: ResponseFunctionCallOutputOutputUnionParam{
						OfString: ptr.To("function output string"),
					},
					ID:     "resp-123",
					Status: "completed",
					Type:   "function_call_output",
				},
			},
			input: []byte(`{"type":"function_call_output","call_id":"call-123","output":"function output string","id":"resp-123","status":"completed"}`),
		},
		{
			name:  "code interpreter call",
			input: []byte(`{"type": "code_interpreter_call", "id": "resp-123", "container_id": "cntr_320", "status": "completed", "code": "print(\"Hello, World!\")", "outputs": [{"type": "logs", "logs": "log contents"}]}`),
			expect: ResponseOutputItemUnion{
				OfCodeInterpreterCall: &ResponseCodeInterpreterToolCall{
					Type:        "code_interpreter_call",
					ID:          "resp-123",
					ContainerID: "cntr_320",
					Status:      "completed",
					Code:        "print(\"Hello, World!\")",
					Outputs: []ResponseCodeInterpreterToolCallOutputUnion{
						{OfResponseCodeInterpreterToolCallOutputLogs: &ResponseCodeInterpreterToolCallOutputLogs{
							Type: "logs",
							Logs: "log contents",
						}},
					},
				},
			},
		},
		{
			name: "computer_call",
			expect: ResponseOutputItemUnion{
				OfComputerCall: &ResponseComputerToolCall{
					Type:   "computer_call",
					CallID: "call-456",
					ID:     "rs-123",
					Action: ResponseComputerToolCallActionUnionParam{
						OfClick: &ResponseComputerToolCallActionClickParam{
							Button: "left",
							X:      100,
							Y:      200,
							Type:   "click",
						},
					},
				},
			},
			input: []byte(`{"type": "computer_call", "call_id": "call-456", "id": "rs-123", "action": {"type": "click", "button": "left", "x": 100, "y": 200}}`),
		},
		{
			name: "computer_call_output",
			expect: ResponseOutputItemUnion{
				OfComputerCallOutput: &ResponseComputerToolCallOutputItem{
					Type:   "computer_call_output",
					ID:     "rs-123",
					CallID: "call-456",
					Status: "completed",
					Output: ResponseComputerToolCallOutputScreenshotParam{
						Type:     "computer_screenshot",
						ImageURL: "data:image/png;base64,screenshot_base64",
					},
				},
			},
			input: []byte(`{"type": "computer_call_output", "id": "rs-123", "call_id": "call-456", "status": "completed", "output": {"type": "computer_screenshot", "image_url": "data:image/png;base64,screenshot_base64"}}`),
		},
		{
			name:  "file search call",
			input: []byte(`{"type":"file_search_call","id":"search_123","queries": ["What is deep research?"], "results": [{"file_id": "file-2d", "filename": "deep_research_blog.pdf"}]}`),
			expect: ResponseOutputItemUnion{
				OfFileSearchCall: &ResponseFileSearchToolCall{
					Type: "file_search_call",
					ID:   "search_123",
					Results: []ResponseFileSearchToolCallResultParam{
						{FileID: "file-2d", Filename: "deep_research_blog.pdf"},
					},
					Queries: []string{"What is deep research?"},
				},
			},
		},
		{
			name:  "web search call",
			input: []byte(`{"type":"web_search_call","id":"web_123","action": {"type": "search", "query": "What is deep research?", "sources": [{"type": "url", "url": "https://example.com"}]}}`),
			expect: ResponseOutputItemUnion{
				OfWebSearchCall: &ResponseFunctionWebSearch{
					Type: "web_search_call",
					ID:   "web_123",
					Action: ResponseFunctionWebSearchActionUnionParam{
						OfSearch: &ResponseFunctionWebSearchActionSearchParam{
							Type:  "search",
							Query: "What is deep research?",
							Sources: []ResponseFunctionWebSearchActionSearchSourceParam{
								{Type: "url", URL: "https://example.com"},
							},
						},
					},
				},
			},
		},
		{
			name:  "tool search call",
			input: []byte(`{"type":"tool_search_call","id":"tsc-123","status":"completed","arguments":{"paths":["billing"]},"call_id":null,"execution":"server"}`),
			expect: ResponseOutputItemUnion{
				OfToolSearchCall: &ResponseToolSearchCall{
					Type:      "tool_search_call",
					ID:        "tsc-123",
					Status:    "completed",
					Arguments: map[string]any{"paths": []any{"billing"}},
					CallID:    nil,
					Execution: "server",
				},
			},
		},
		{
			name:  "tool search output",
			input: []byte(`{"type":"tool_search_output","id":"tso-123","status":"completed","call_id":null,"execution":"server","tools":[{"type":"namespace","name":"billing","description":"Billing tools.","tools":[{"type":"function","name":"get_invoice","description":"Get invoice details by invoice ID.","defer_loading":true}]}]}`),
			expect: ResponseOutputItemUnion{
				OfToolSearchOutput: &ResponseToolSearchOutput{
					Type:      "tool_search_output",
					ID:        "tso-123",
					Status:    "completed",
					CallID:    nil,
					Execution: "server",
					Tools: []ResponseToolUnion{
						{
							OfNamespace: &NamespaceToolParam{
								Type:        "namespace",
								Name:        "billing",
								Description: "Billing tools.",
								Tools: []NamespaceToolToolUnionParam{
									{
										OfFunction: &NamespaceToolToolFunctionParam{
											Type:         "function",
											Name:         "get_invoice",
											Description:  "Get invoice details by invoice ID.",
											DeferLoading: ptr.To(true),
										},
									},
								},
							},
						},
					},
				},
			},
		},
		{
			name:  "additional tools",
			input: []byte(`{"type":"additional_tools","id":"at-123","role":"developer","tools":[{"type":"tool_search","parameters":null}]}`),
			expect: ResponseOutputItemUnion{
				OfAdditionalTools: &ResponseAdditionalTools{
					Type: "additional_tools",
					ID:   "at-123",
					Role: "developer",
					Tools: []ResponseToolUnion{
						{
							OfToolSearch: &ToolSearchToolParam{
								Type: "tool_search",
							},
						},
					},
				},
			},
		},
		{
			name: "compaction",
			expect: ResponseOutputItemUnion{
				OfCompaction: &ResponseCompactionItem{
					Type:             "compaction",
					ID:               "resp-123",
					EncryptedContent: "encrypted_content",
				},
			},
			input: []byte(`{"type": "compaction", "id": "resp-123", "encrypted_content": "encrypted_content"}`),
		},
		{
			name: "image_generation_call",
			expect: ResponseOutputItemUnion{
				OfImageGenerationCall: &ResponseOutputItemImageGenerationCall{
					Type:   "image_generation_call",
					ID:     "rs-123",
					Status: "completed",
					Result: "data:image/png;base64,encoded_image",
				},
			},
			input: []byte(`{"type": "image_generation_call", "id": "rs-123", "status": "completed", "result": "data:image/png;base64,encoded_image"}`),
		},
		{
			name: "local_shell_call",
			expect: ResponseOutputItemUnion{
				OfLocalShellCall: &ResponseOutputItemLocalShellCall{
					Type:   "local_shell_call",
					CallID: "call-123",
					ID:     "rs-123",
					Status: "in_progress",
					Action: ResponseOutputItemLocalShellCallAction{
						Type:    "exec",
						Command: []string{"ls", "-a"},
						Env:     map[string]string{"TEST": "test"},
					},
				},
			},
			input: []byte(`{"type": "local_shell_call", "call_id": "call-123", "id": "rs-123", "status": "in_progress", "action": {"type": "exec", "command": ["ls", "-a"], "env": {"TEST": "test"}}}`),
		},
		{
			name: "shell_call",
			expect: ResponseOutputItemUnion{
				OfShellCall: &ResponseFunctionShellToolCall{
					Type:   "shell_call",
					CallID: "call-123",
					ID:     "resp-123",
					Status: "completed",
					Action: ResponseFunctionShellToolCallAction{
						Commands:        []string{"ls", "-a"},
						MaxOutputLength: 200,
					},
				},
			},
			input: []byte(`{"type": "shell_call", "call_id": "call-123", "id": "resp-123","status": "completed", "action": {"commands": ["ls", "-a"],"max_output_length":200}}`),
		},
		{
			name: "shell_call_output",
			expect: ResponseOutputItemUnion{
				OfShellCallOutput: &ResponseFunctionShellToolCallOutput{
					Type:            "shell_call_output",
					CallID:          "call-123",
					ID:              "resp-123",
					MaxOutputLength: 200,
					Output: []ResponseFunctionShellToolCallOutputOutput{
						{
							Stderr: "",
							Stdout: "Documents\nDownloads",
							Outcome: ResponseFunctionShellToolCallOutputOutputOutcomeUnion{
								OfResponseFunctionShellToolCallOutputOutputOutcomeExit: &ResponseFunctionShellToolCallOutputOutputOutcomeExit{
									Type:     "exit",
									ExitCode: 0,
								},
							},
						},
					},
				},
			},
			input: []byte(`{"type": "shell_call_output", "call_id": "call-123", "id": "resp-123", "max_output_length": 200, "output": [{"stderr": "", "stdout": "Documents\nDownloads", "outcome": {"type": "exit", "exit_code": 0}}]}`),
		},
		{
			name: "apply_patch_call",
			expect: ResponseOutputItemUnion{
				OfApplyPatchCall: &ResponseApplyPatchToolCall{
					Type:   "apply_patch_call",
					ID:     "resp-123",
					CallID: "call-123",
					Status: "completed",
					Operation: ResponseApplyPatchToolCallOperationUnion{
						OfResponseApplyPatchToolCallOperationCreateFile: &ResponseApplyPatchToolCallOperationCreateFile{
							Type: "create_file",
							Path: "/home/Documents",
							Diff: "",
						},
					},
				},
			},
			input: []byte(`{"type": "apply_patch_call", "id": "resp-123", "call_id": "call-123", "status": "completed", "operation": {"type": "create_file", "path": "/home/Documents", "diff": ""} }`),
		},
		{
			name: "apply_patch_call_output",
			expect: ResponseOutputItemUnion{
				OfApplyPatchCallOutput: &ResponseApplyPatchToolCallOutput{
					Type:   "apply_patch_call_output",
					ID:     "resp-123",
					CallID: "call-123",
					Status: "completed",
					Output: "log contents",
				},
			},
			input: []byte(`{"type": "apply_patch_call_output", "id": "resp-123", "call_id": "call-123", "status": "completed", "output": "log contents"}`),
		},
		{
			name: "mcp_list_tools",
			expect: ResponseOutputItemUnion{
				OfMcpListTools: &ResponseMcpListTools{
					Type:        "mcp_list_tools",
					ID:          "id-123",
					ServerLabel: "test-server",
					Tools: []ResponseOutputItemMcpListTools{
						{InputSchema: "{\"schemaVersion\": \"1.0\"}", Name: "test"},
					},
				},
			},
			input: []byte(`{"type": "mcp_list_tools", "id": "id-123", "server_label": "test-server", "tools": [{"input_schema": "{\"schemaVersion\": \"1.0\"}", "name": "test"}]}`),
		},
		{
			name: "mcp_approval_request",
			expect: ResponseOutputItemUnion{
				OfMcpApprovalRequest: &ResponseMcpApprovalRequest{
					Type:        "mcp_approval_request",
					ID:          "id-123",
					ServerLabel: "test-server",
					Name:        "test",
					Arguments:   "{\"arg1\": \"val\"}",
				},
			},
			input: []byte(`{"type": "mcp_approval_request", "id": "id-123", "server_label": "test-server", "name": "test", "arguments": "{\"arg1\": \"val\"}"}`),
		},
		{
			name: "mcp_approval_response",
			expect: ResponseOutputItemUnion{
				OfMcpApprovalResponse: &ResponseInputItemMcpApprovalResponseParam{
					Type:              "mcp_approval_response",
					ID:                "resp-123",
					Approve:           true,
					ApprovalRequestID: "req-123",
					Reason:            "approved",
				},
			},
			input: []byte(`{"type": "mcp_approval_response", "id": "resp-123", "approve": true, "approval_request_id": "req-123", "reason": "approved"}`),
		},
		{
			name:  "mcp call",
			input: []byte(`{"type":"mcp_call","id":"mcp_123","server_label": "test-server", "name": "test", "arguments": "{\"arg1\": \"val\"}", "approval_request_id": "req-123"}`),
			expect: ResponseOutputItemUnion{
				OfMcpCall: &ResponseMcpCall{
					Type:              "mcp_call",
					ID:                "mcp_123",
					ServerLabel:       "test-server",
					Name:              "test",
					Arguments:         "{\"arg1\": \"val\"}",
					ApprovalRequestID: "req-123",
				},
			},
		},
		{
			name:  "custom tool call",
			input: []byte(`{"type":"custom_tool_call","name":"test","id":"custom_123","call_id":"call-123","input": "some input"}`),
			expect: ResponseOutputItemUnion{
				OfCustomToolCall: &ResponseCustomToolCall{
					Type:   "custom_tool_call",
					ID:     "custom_123",
					CallID: "call-123",
					Input:  "some input",
					Name:   "test",
				},
			},
		},
		{
			name:  "custom tool call output",
			input: []byte(`{"type":"custom_tool_call_output","id":"custom-output-123","call_id":"call-123","status":"completed","output":"some output"}`),
			expect: ResponseOutputItemUnion{
				OfCustomToolCallOutput: &ResponseCustomToolCallOutputItem{
					Type:   "custom_tool_call_output",
					ID:     "custom-output-123",
					CallID: "call-123",
					Status: "completed",
					Output: ResponseCustomToolCallOutputOutputUnionParam{
						OfString: ptr.To("some output"),
					},
				},
			},
		},
		{
			name:   "empty type",
			input:  []byte(`{"type": ""}`),
			expErr: "unknown type field value '' for response output item union",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var result ResponseOutputItemUnion
			err := json.Unmarshal(tc.input, &result)
			if tc.expErr != "" {
				require.ErrorContains(t, err, tc.expErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.expect, result)
		})
	}
}

func TestResponseunctionCallOutputOutputUnionParamMarshalJSON(t *testing.T) {
	tests := []struct {
		name   string
		expect []byte
		input  ResponseFunctionCallOutputOutputUnionParam
		expErr string
	}{
		{
			name:   "string output",
			expect: []byte(`"output from function"`),
			input: ResponseFunctionCallOutputOutputUnionParam{
				OfString: ptr.To("output from function"),
			},
		},
		{
			name:   "string output with empty value",
			expect: []byte(`""`),
			input: ResponseFunctionCallOutputOutputUnionParam{
				OfString: ptr.To(""),
			},
		},
		{
			name:   "string output with special characters",
			expect: []byte(`"output with \"quotes\" and newline\n"`),
			input: ResponseFunctionCallOutputOutputUnionParam{
				OfString: ptr.To("output with \"quotes\" and newline\n"),
			},
		},
		{
			name:   "array with single text item",
			expect: []byte(`[{"text":"hello","type":"input_text"}]`),
			input: ResponseFunctionCallOutputOutputUnionParam{
				OfResponseFunctionCallOutputItemArray: []ResponseFunctionCallOutputItemUnionParam{
					{
						OfInputText: &ResponseInputTextContentParam{
							Text: "hello",
							Type: "input_text",
						},
					},
				},
			},
		},
		{
			name:   "array with text and image items",
			expect: []byte(`[{"text":"hello","type":"input_text"},{"type":"input_image","file_id":"file-123"}]`),
			input: ResponseFunctionCallOutputOutputUnionParam{
				OfResponseFunctionCallOutputItemArray: []ResponseFunctionCallOutputItemUnionParam{
					{
						OfInputText: &ResponseInputTextContentParam{
							Text: "hello",
							Type: "input_text",
						},
					},
					{
						OfInputImage: &ResponseInputImageContentParam{
							Type:   "input_image",
							FileID: "file-123",
						},
					},
				},
			},
		},
		{
			name:   "array with image item with url",
			expect: []byte(`[{"type":"input_image","image_url":"https://example.com/image.jpg","detail":"high"}]`),
			input: ResponseFunctionCallOutputOutputUnionParam{
				OfResponseFunctionCallOutputItemArray: []ResponseFunctionCallOutputItemUnionParam{
					{
						OfInputImage: &ResponseInputImageContentParam{
							Type:     "input_image",
							ImageURL: "https://example.com/image.jpg",
							Detail:   "high",
						},
					},
				},
			},
		},
		{
			name:   "array with file item",
			expect: []byte(`[{"type":"input_file","file_id":"file-456","filename":"data.json"}]`),
			input: ResponseFunctionCallOutputOutputUnionParam{
				OfResponseFunctionCallOutputItemArray: []ResponseFunctionCallOutputItemUnionParam{
					{
						OfInputFile: &ResponseInputFileContentParam{
							Type:     "input_file",
							FileID:   "file-456",
							Filename: "data.json",
						},
					},
				},
			},
		},
		{
			name:   "array with multiple items mixed types",
			expect: []byte(`[{"text":"hello","type":"input_text"},{"type":"input_image","file_id":"file-123"},{"type":"input_file","file_id":"file-456"}]`),
			input: ResponseFunctionCallOutputOutputUnionParam{
				OfResponseFunctionCallOutputItemArray: []ResponseFunctionCallOutputItemUnionParam{
					{
						OfInputText: &ResponseInputTextContentParam{
							Text: "hello",
							Type: "input_text",
						},
					},
					{
						OfInputImage: &ResponseInputImageContentParam{
							Type:   "input_image",
							FileID: "file-123",
						},
					},
					{
						OfInputFile: &ResponseInputFileContentParam{
							Type:   "input_file",
							FileID: "file-456",
						},
					},
				},
			},
		},
		{
			name:   "array with empty text item",
			expect: []byte(`[{"text":"","type":"input_text"}]`),
			input: ResponseFunctionCallOutputOutputUnionParam{
				OfResponseFunctionCallOutputItemArray: []ResponseFunctionCallOutputItemUnionParam{
					{
						OfInputText: &ResponseInputTextContentParam{
							Text: "",
							Type: "input_text",
						},
					},
				},
			},
		},
		{
			name:   "nil union",
			input:  ResponseFunctionCallOutputOutputUnionParam{},
			expErr: "no function call output to marshal in function call output",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			data, err := json.Marshal(tc.input)
			if tc.expErr != "" {
				require.ErrorContains(t, err, tc.expErr)
				return
			}
			require.NoError(t, err)
			require.JSONEq(t, string(tc.expect), string(data))
		})
	}
}

func TestResponseFunctionCallOutputOutputUnionParamUnmarshalJSON(t *testing.T) {
	tests := []struct {
		name   string
		input  []byte
		expect ResponseFunctionCallOutputOutputUnionParam
		expErr string
	}{
		{
			name:  "string output",
			input: []byte(`"output from function"`),
			expect: ResponseFunctionCallOutputOutputUnionParam{
				OfString: ptr.To("output from function"),
			},
		},
		{
			name:  "string output with empty value",
			input: []byte(`""`),
			expect: ResponseFunctionCallOutputOutputUnionParam{
				OfString: ptr.To(""),
			},
		},
		{
			name:  "string output with special characters",
			input: []byte(`"output with \"quotes\" and newline\n"`),
			expect: ResponseFunctionCallOutputOutputUnionParam{
				OfString: ptr.To("output with \"quotes\" and newline\n"),
			},
		},
		{
			name:  "array with single text item",
			input: []byte(`[{"text":"hello","type":"input_text"}]`),
			expect: ResponseFunctionCallOutputOutputUnionParam{
				OfResponseFunctionCallOutputItemArray: []ResponseFunctionCallOutputItemUnionParam{
					{
						OfInputText: &ResponseInputTextContentParam{
							Text: "hello",
							Type: "input_text",
						},
					},
				},
			},
		},
		{
			name:  "array with text and image items",
			input: []byte(`[{"text":"hello","type":"input_text"},{"type":"input_image","file_id":"file-123"}]`),
			expect: ResponseFunctionCallOutputOutputUnionParam{
				OfResponseFunctionCallOutputItemArray: []ResponseFunctionCallOutputItemUnionParam{
					{
						OfInputText: &ResponseInputTextContentParam{
							Text: "hello",
							Type: "input_text",
						},
					},
					{
						OfInputImage: &ResponseInputImageContentParam{
							Type:   "input_image",
							FileID: "file-123",
						},
					},
				},
			},
		},
		{
			name:  "array with image item with url",
			input: []byte(`[{"type":"input_image","image_url":"https://example.com/image.jpg","detail":"high"}]`),
			expect: ResponseFunctionCallOutputOutputUnionParam{
				OfResponseFunctionCallOutputItemArray: []ResponseFunctionCallOutputItemUnionParam{
					{
						OfInputImage: &ResponseInputImageContentParam{
							Type:     "input_image",
							ImageURL: "https://example.com/image.jpg",
							Detail:   "high",
						},
					},
				},
			},
		},
		{
			name:  "array with file item",
			input: []byte(`[{"type":"input_file","file_id":"file-456","filename":"data.json"}]`),
			expect: ResponseFunctionCallOutputOutputUnionParam{
				OfResponseFunctionCallOutputItemArray: []ResponseFunctionCallOutputItemUnionParam{
					{
						OfInputFile: &ResponseInputFileContentParam{
							Type:     "input_file",
							FileID:   "file-456",
							Filename: "data.json",
						},
					},
				},
			},
		},
		{
			name:  "array with multiple items mixed types",
			input: []byte(`[{"text":"hello","type":"input_text"},{"type":"input_image","file_id":"file-123"},{"type":"input_file","file_id":"file-456"}]`),
			expect: ResponseFunctionCallOutputOutputUnionParam{
				OfResponseFunctionCallOutputItemArray: []ResponseFunctionCallOutputItemUnionParam{
					{
						OfInputText: &ResponseInputTextContentParam{
							Text: "hello",
							Type: "input_text",
						},
					},
					{
						OfInputImage: &ResponseInputImageContentParam{
							Type:   "input_image",
							FileID: "file-123",
						},
					},
					{
						OfInputFile: &ResponseInputFileContentParam{
							Type:   "input_file",
							FileID: "file-456",
						},
					},
				},
			},
		},
		{
			name:  "array with empty text item",
			input: []byte(`[{"text":"","type":"input_text"}]`),
			expect: ResponseFunctionCallOutputOutputUnionParam{
				OfResponseFunctionCallOutputItemArray: []ResponseFunctionCallOutputItemUnionParam{
					{
						OfInputText: &ResponseInputTextContentParam{
							Text: "",
							Type: "input_text",
						},
					},
				},
			},
		},
		{
			name:   "invalid input",
			input:  []byte(`1231`),
			expErr: "unknown type for function call output in function call output",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var result ResponseFunctionCallOutputOutputUnionParam
			err := json.Unmarshal(tc.input, &result)
			if tc.expErr != "" {
				require.ErrorContains(t, err, tc.expErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.expect, result)
		})
	}
}

func TestResponseFunctionCallOutputItemUnionParamMarshalJSON(t *testing.T) {
	tests := []struct {
		name   string
		expect []byte
		input  ResponseFunctionCallOutputItemUnionParam
		expErr string
	}{
		{
			name:   "text input item",
			expect: []byte(`{"text":"hello","type":"input_text"}`),
			input: ResponseFunctionCallOutputItemUnionParam{
				OfInputText: &ResponseInputTextContentParam{
					Text: "hello",
					Type: "input_text",
				},
			},
		},
		{
			name:   "text input item with empty value",
			expect: []byte(`{"text":"","type":"input_text"}`),
			input: ResponseFunctionCallOutputItemUnionParam{
				OfInputText: &ResponseInputTextContentParam{
					Text: "",
					Type: "input_text",
				},
			},
		},
		{
			name:   "text input item with special characters",
			expect: []byte(`{"text":"hello with \"quotes\" and newline\n","type":"input_text"}`),
			input: ResponseFunctionCallOutputItemUnionParam{
				OfInputText: &ResponseInputTextContentParam{
					Text: "hello with \"quotes\" and newline\n",
					Type: "input_text",
				},
			},
		},
		{
			name:   "image input item with file_id",
			expect: []byte(`{"type":"input_image","file_id":"file-123"}`),
			input: ResponseFunctionCallOutputItemUnionParam{
				OfInputImage: &ResponseInputImageContentParam{
					Type:   "input_image",
					FileID: "file-123",
				},
			},
		},
		{
			name:   "image input item with image_url",
			expect: []byte(`{"type":"input_image","image_url":"https://example.com/image.jpg"}`),
			input: ResponseFunctionCallOutputItemUnionParam{
				OfInputImage: &ResponseInputImageContentParam{
					Type:     "input_image",
					ImageURL: "https://example.com/image.jpg",
				},
			},
		},
		{
			name:   "image input item with detail",
			expect: []byte(`{"type":"input_image","image_url":"https://example.com/image.jpg","detail":"high"}`),
			input: ResponseFunctionCallOutputItemUnionParam{
				OfInputImage: &ResponseInputImageContentParam{
					Type:     "input_image",
					ImageURL: "https://example.com/image.jpg",
					Detail:   "high",
				},
			},
		},
		{
			name:   "file input item with file_id",
			expect: []byte(`{"type":"input_file","file_id":"file-456"}`),
			input: ResponseFunctionCallOutputItemUnionParam{
				OfInputFile: &ResponseInputFileContentParam{
					Type:   "input_file",
					FileID: "file-456",
				},
			},
		},
		{
			name:   "file input item with filename",
			expect: []byte(`{"type":"input_file","file_id":"file-456","filename":"data.json"}`),
			input: ResponseFunctionCallOutputItemUnionParam{
				OfInputFile: &ResponseInputFileContentParam{
					Type:     "input_file",
					FileID:   "file-456",
					Filename: "data.json",
				},
			},
		},
		{
			name:   "file input item with all fields",
			expect: []byte(`{"type":"input_file","file_id":"file-456","filename":"document.pdf","file_data":"base64data"}`),
			input: ResponseFunctionCallOutputItemUnionParam{
				OfInputFile: &ResponseInputFileContentParam{
					Type:     "input_file",
					FileID:   "file-456",
					Filename: "document.pdf",
					FileData: "base64data",
				},
			},
		},
		{
			name:   "file input item with file_url",
			expect: []byte(`{"type":"input_file","file_id":"file-789","file_url":"https://example.com/file.pdf"}`),
			input: ResponseFunctionCallOutputItemUnionParam{
				OfInputFile: &ResponseInputFileContentParam{
					Type:    "input_file",
					FileID:  "file-789",
					FileURL: "https://example.com/file.pdf",
				},
			},
		},
		{
			name:   "nil union",
			input:  ResponseFunctionCallOutputItemUnionParam{},
			expErr: "no function call output item to marshal in function call output item",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			data, err := json.Marshal(tc.input)
			if tc.expErr != "" {
				require.ErrorContains(t, err, tc.expErr)
				return
			}
			require.NoError(t, err)
			require.JSONEq(t, string(tc.expect), string(data))
		})
	}
}

func TestResponseFunctionCallOutputItemUnionParamUnmarshalJSON(t *testing.T) {
	tests := []struct {
		name   string
		input  []byte
		expect ResponseFunctionCallOutputItemUnionParam
		expErr string
	}{
		{
			name:  "text input item",
			input: []byte(`{"text":"hello","type":"input_text"}`),
			expect: ResponseFunctionCallOutputItemUnionParam{
				OfInputText: &ResponseInputTextContentParam{
					Text: "hello",
					Type: "input_text",
				},
			},
		},
		{
			name:  "text input item with empty value",
			input: []byte(`{"text":"","type":"input_text"}`),
			expect: ResponseFunctionCallOutputItemUnionParam{
				OfInputText: &ResponseInputTextContentParam{
					Text: "",
					Type: "input_text",
				},
			},
		},
		{
			name:  "text input item with special characters",
			input: []byte(`{"text":"hello with \"quotes\" and newline\n","type":"input_text"}`),
			expect: ResponseFunctionCallOutputItemUnionParam{
				OfInputText: &ResponseInputTextContentParam{
					Text: "hello with \"quotes\" and newline\n",
					Type: "input_text",
				},
			},
		},
		{
			name:  "image input item with file_id",
			input: []byte(`{"type":"input_image","file_id":"file-123"}`),
			expect: ResponseFunctionCallOutputItemUnionParam{
				OfInputImage: &ResponseInputImageContentParam{
					Type:   "input_image",
					FileID: "file-123",
				},
			},
		},
		{
			name:  "image input item with image_url",
			input: []byte(`{"type":"input_image","image_url":"https://example.com/image.jpg"}`),
			expect: ResponseFunctionCallOutputItemUnionParam{
				OfInputImage: &ResponseInputImageContentParam{
					Type:     "input_image",
					ImageURL: "https://example.com/image.jpg",
				},
			},
		},
		{
			name:  "image input item with detail",
			input: []byte(`{"type":"input_image","image_url":"https://example.com/image.jpg","detail":"high"}`),
			expect: ResponseFunctionCallOutputItemUnionParam{
				OfInputImage: &ResponseInputImageContentParam{
					Type:     "input_image",
					ImageURL: "https://example.com/image.jpg",
					Detail:   "high",
				},
			},
		},
		{
			name:  "file input item with file_id",
			input: []byte(`{"type":"input_file","file_id":"file-456"}`),
			expect: ResponseFunctionCallOutputItemUnionParam{
				OfInputFile: &ResponseInputFileContentParam{
					Type:   "input_file",
					FileID: "file-456",
				},
			},
		},
		{
			name:  "file input item with filename",
			input: []byte(`{"type":"input_file","file_id":"file-456","filename":"data.json"}`),
			expect: ResponseFunctionCallOutputItemUnionParam{
				OfInputFile: &ResponseInputFileContentParam{
					Type:     "input_file",
					FileID:   "file-456",
					Filename: "data.json",
				},
			},
		},
		{
			name:  "file input item with all fields",
			input: []byte(`{"type":"input_file","file_id":"file-456","filename":"document.pdf","file_data":"base64data"}`),
			expect: ResponseFunctionCallOutputItemUnionParam{
				OfInputFile: &ResponseInputFileContentParam{
					Type:     "input_file",
					FileID:   "file-456",
					Filename: "document.pdf",
					FileData: "base64data",
				},
			},
		},
		{
			name:  "file input item with file_url",
			input: []byte(`{"type":"input_file","file_id":"file-789","file_url":"https://example.com/file.pdf"}`),
			expect: ResponseFunctionCallOutputItemUnionParam{
				OfInputFile: &ResponseInputFileContentParam{
					Type:    "input_file",
					FileID:  "file-789",
					FileURL: "https://example.com/file.pdf",
				},
			},
		},
		{
			name:   "empty type",
			input:  []byte(`{"type": ""}`),
			expErr: "unknown type for function call output item: ",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var result ResponseFunctionCallOutputItemUnionParam
			err := json.Unmarshal(tc.input, &result)
			if tc.expErr != "" {
				require.ErrorContains(t, err, tc.expErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.expect, result)
		})
	}
}

func TestResponseFunctionShellToolCallOutputOutputOutcomeUnionMarshalJSON(t *testing.T) {
	tests := []struct {
		name   string
		expect []byte
		input  ResponseFunctionShellToolCallOutputOutputOutcomeUnion
		expErr string
	}{
		{
			name:   "exit outcome",
			expect: []byte(`{"type":"exit","exit_code":0}`),
			input: ResponseFunctionShellToolCallOutputOutputOutcomeUnion{
				OfResponseFunctionShellToolCallOutputOutputOutcomeExit: &ResponseFunctionShellToolCallOutputOutputOutcomeExit{
					Type:     "exit",
					ExitCode: 0,
				},
			},
		},
		{
			name:   "exit outcome with non-zero exit code",
			expect: []byte(`{"type":"exit","exit_code":127}`),
			input: ResponseFunctionShellToolCallOutputOutputOutcomeUnion{
				OfResponseFunctionShellToolCallOutputOutputOutcomeExit: &ResponseFunctionShellToolCallOutputOutputOutcomeExit{
					Type:     "exit",
					ExitCode: 127,
				},
			},
		},
		{
			name:   "timeout outcome",
			expect: []byte(`{"type":"timeout"}`),
			input: ResponseFunctionShellToolCallOutputOutputOutcomeUnion{
				OfResponseFunctionShellToolCallOutputOutputOutcomeTimeout: &ResponseFunctionShellToolCallOutputOutputOutcomeTimeout{
					Type: "timeout",
				},
			},
		},
		{
			name:   "nil union",
			input:  ResponseFunctionShellToolCallOutputOutputOutcomeUnion{},
			expErr: "no function tool call output outcome to marshal",
		},
		{
			name:   "exit outcome with large exit code",
			expect: []byte(`{"type":"exit","exit_code":255}`),
			input: ResponseFunctionShellToolCallOutputOutputOutcomeUnion{
				OfResponseFunctionShellToolCallOutputOutputOutcomeExit: &ResponseFunctionShellToolCallOutputOutputOutcomeExit{
					Type:     "exit",
					ExitCode: 255,
				},
			},
		},
		{
			name:   "exit outcome with negative exit code",
			expect: []byte(`{"type":"exit","exit_code":-1}`),
			input: ResponseFunctionShellToolCallOutputOutputOutcomeUnion{
				OfResponseFunctionShellToolCallOutputOutputOutcomeExit: &ResponseFunctionShellToolCallOutputOutputOutcomeExit{
					Type:     "exit",
					ExitCode: -1,
				},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			data, err := json.Marshal(tc.input)
			if tc.expErr != "" {
				require.ErrorContains(t, err, tc.expErr)
				return
			}
			require.NoError(t, err)
			require.JSONEq(t, string(tc.expect), string(data))
		})
	}
}

func TestResponseFunctionShellToolCallOutputOutputOutcomeUnionUnmarshalJSON(t *testing.T) {
	tests := []struct {
		name   string
		input  []byte
		expect ResponseFunctionShellToolCallOutputOutputOutcomeUnion
		expErr string
	}{
		{
			name:  "exit outcome",
			input: []byte(`{"type":"exit","exit_code":0}`),
			expect: ResponseFunctionShellToolCallOutputOutputOutcomeUnion{
				OfResponseFunctionShellToolCallOutputOutputOutcomeExit: &ResponseFunctionShellToolCallOutputOutputOutcomeExit{
					Type:     "exit",
					ExitCode: 0,
				},
			},
		},
		{
			name:  "exit outcome with non-zero exit code",
			input: []byte(`{"type":"exit","exit_code":127}`),
			expect: ResponseFunctionShellToolCallOutputOutputOutcomeUnion{
				OfResponseFunctionShellToolCallOutputOutputOutcomeExit: &ResponseFunctionShellToolCallOutputOutputOutcomeExit{
					Type:     "exit",
					ExitCode: 127,
				},
			},
		},
		{
			name:  "timeout outcome",
			input: []byte(`{"type":"timeout"}`),
			expect: ResponseFunctionShellToolCallOutputOutputOutcomeUnion{
				OfResponseFunctionShellToolCallOutputOutputOutcomeTimeout: &ResponseFunctionShellToolCallOutputOutputOutcomeTimeout{
					Type: "timeout",
				},
			},
		},
		{
			name:   "empty type",
			input:  []byte(`{"type": ""}`),
			expErr: "unknown type field value '' for function shell tool call output outcome",
		},
		{
			name:  "exit outcome with large exit code",
			input: []byte(`{"type":"exit","exit_code":255}`),
			expect: ResponseFunctionShellToolCallOutputOutputOutcomeUnion{
				OfResponseFunctionShellToolCallOutputOutputOutcomeExit: &ResponseFunctionShellToolCallOutputOutputOutcomeExit{
					Type:     "exit",
					ExitCode: 255,
				},
			},
		},
		{
			name:  "exit outcome with negative exit code",
			input: []byte(`{"type":"exit","exit_code":-1}`),
			expect: ResponseFunctionShellToolCallOutputOutputOutcomeUnion{
				OfResponseFunctionShellToolCallOutputOutputOutcomeExit: &ResponseFunctionShellToolCallOutputOutputOutcomeExit{
					Type:     "exit",
					ExitCode: -1,
				},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var result ResponseFunctionShellToolCallOutputOutputOutcomeUnion
			err := json.Unmarshal(tc.input, &result)
			if tc.expErr != "" {
				require.ErrorContains(t, err, tc.expErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.expect, result)
		})
	}
}

func TestResponseApplyPatchToolCallOperationUnionMarshalJSON(t *testing.T) {
	tests := []struct {
		name   string
		expect []byte
		input  ResponseApplyPatchToolCallOperationUnion
		expErr string
	}{
		{
			name:   "create file operation",
			expect: []byte(`{"diff":"--- a/file.txt\n+++ b/file.txt\n@@ -1 +1 @@\n-old\n+new","path":"src/file.txt","type":"create_file"}`),
			input: ResponseApplyPatchToolCallOperationUnion{
				OfResponseApplyPatchToolCallOperationCreateFile: &ResponseApplyPatchToolCallOperationCreateFile{
					Diff: "--- a/file.txt\n+++ b/file.txt\n@@ -1 +1 @@\n-old\n+new",
					Path: "src/file.txt",
					Type: "create_file",
				},
			},
		},
		{
			name:   "create file operation with simple diff",
			expect: []byte(`{"diff":"@@ -1 +1 @@\n-hello\n+goodbye","path":"test.txt","type":"create_file"}`),
			input: ResponseApplyPatchToolCallOperationUnion{
				OfResponseApplyPatchToolCallOperationCreateFile: &ResponseApplyPatchToolCallOperationCreateFile{
					Diff: "@@ -1 +1 @@\n-hello\n+goodbye",
					Path: "test.txt",
					Type: "create_file",
				},
			},
		},
		{
			name:   "delete file operation",
			expect: []byte(`{"path":"src/old_file.txt","type":"delete_file"}`),
			input: ResponseApplyPatchToolCallOperationUnion{
				OfResponseApplyPatchToolCallOperationDeleteFile: &ResponseApplyPatchToolCallOperationDeleteFile{
					Path: "src/old_file.txt",
					Type: "delete_file",
				},
			},
		},
		{
			name:   "delete file operation with nested path",
			expect: []byte(`{"path":"src/components/button.tsx","type":"delete_file"}`),
			input: ResponseApplyPatchToolCallOperationUnion{
				OfResponseApplyPatchToolCallOperationDeleteFile: &ResponseApplyPatchToolCallOperationDeleteFile{
					Path: "src/components/button.tsx",
					Type: "delete_file",
				},
			},
		},
		{
			name:   "update file operation",
			expect: []byte(`{"diff":"--- a/config.yaml\n+++ b/config.yaml\n@@ -1,3 +1,4 @@\n version: 1\n+enabled: true","path":"config.yaml","type":"update_file"}`),
			input: ResponseApplyPatchToolCallOperationUnion{
				OfResponseApplyPatchToolCallOperationUpdateFile: &ResponseApplyPatchToolCallOperationUpdateFile{
					Diff: "--- a/config.yaml\n+++ b/config.yaml\n@@ -1,3 +1,4 @@\n version: 1\n+enabled: true",
					Path: "config.yaml",
					Type: "update_file",
				},
			},
		},
		{
			name:   "update file operation with empty diff",
			expect: []byte(`{"diff":"","path":"empty.txt","type":"update_file"}`),
			input: ResponseApplyPatchToolCallOperationUnion{
				OfResponseApplyPatchToolCallOperationUpdateFile: &ResponseApplyPatchToolCallOperationUpdateFile{
					Diff: "",
					Path: "empty.txt",
					Type: "update_file",
				},
			},
		},
		{
			name:   "update file operation with multi-line diff",
			expect: []byte(`{"diff":"--- a/main.go\n+++ b/main.go\n@@ -10,5 +10,6 @@\n package main\n import (\n \"fmt\"\n +\"os\"\n )\n func main() {","path":"main.go","type":"update_file"}`),
			input: ResponseApplyPatchToolCallOperationUnion{
				OfResponseApplyPatchToolCallOperationUpdateFile: &ResponseApplyPatchToolCallOperationUpdateFile{
					Diff: "--- a/main.go\n+++ b/main.go\n@@ -10,5 +10,6 @@\n package main\n import (\n \"fmt\"\n +\"os\"\n )\n func main() {",
					Path: "main.go",
					Type: "update_file",
				},
			},
		},
		{
			name:   "nil union",
			input:  ResponseApplyPatchToolCallOperationUnion{},
			expErr: "no apply patch tool call operation to marshal",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			data, err := json.Marshal(tc.input)
			if tc.expErr != "" {
				require.ErrorContains(t, err, tc.expErr)
				return
			}
			require.NoError(t, err)
			require.JSONEq(t, string(tc.expect), string(data))
		})
	}
}

func TestResponseApplyPatchToolCallOperationUnionUnmarshalJSON(t *testing.T) {
	tests := []struct {
		name   string
		input  []byte
		expect ResponseApplyPatchToolCallOperationUnion
		expErr string
	}{
		{
			name:  "create file operation",
			input: []byte(`{"diff":"--- a/file.txt\n+++ b/file.txt\n@@ -1 +1 @@\n-old\n+new","path":"src/file.txt","type":"create_file"}`),
			expect: ResponseApplyPatchToolCallOperationUnion{
				OfResponseApplyPatchToolCallOperationCreateFile: &ResponseApplyPatchToolCallOperationCreateFile{
					Diff: "--- a/file.txt\n+++ b/file.txt\n@@ -1 +1 @@\n-old\n+new",
					Path: "src/file.txt",
					Type: "create_file",
				},
			},
		},
		{
			name:  "create file operation with simple diff",
			input: []byte(`{"diff":"@@ -1 +1 @@\n-hello\n+goodbye","path":"test.txt","type":"create_file"}`),
			expect: ResponseApplyPatchToolCallOperationUnion{
				OfResponseApplyPatchToolCallOperationCreateFile: &ResponseApplyPatchToolCallOperationCreateFile{
					Diff: "@@ -1 +1 @@\n-hello\n+goodbye",
					Path: "test.txt",
					Type: "create_file",
				},
			},
		},
		{
			name:  "delete file operation",
			input: []byte(`{"path":"src/old_file.txt","type":"delete_file"}`),
			expect: ResponseApplyPatchToolCallOperationUnion{
				OfResponseApplyPatchToolCallOperationDeleteFile: &ResponseApplyPatchToolCallOperationDeleteFile{
					Path: "src/old_file.txt",
					Type: "delete_file",
				},
			},
		},
		{
			name:  "delete file operation with nested path",
			input: []byte(`{"path":"src/components/button.tsx","type":"delete_file"}`),
			expect: ResponseApplyPatchToolCallOperationUnion{
				OfResponseApplyPatchToolCallOperationDeleteFile: &ResponseApplyPatchToolCallOperationDeleteFile{
					Path: "src/components/button.tsx",
					Type: "delete_file",
				},
			},
		},
		{
			name:  "update file operation",
			input: []byte(`{"diff":"--- a/config.yaml\n+++ b/config.yaml\n@@ -1,3 +1,4 @@\n version: 1\n+enabled: true","path":"config.yaml","type":"update_file"}`),
			expect: ResponseApplyPatchToolCallOperationUnion{
				OfResponseApplyPatchToolCallOperationUpdateFile: &ResponseApplyPatchToolCallOperationUpdateFile{
					Diff: "--- a/config.yaml\n+++ b/config.yaml\n@@ -1,3 +1,4 @@\n version: 1\n+enabled: true",
					Path: "config.yaml",
					Type: "update_file",
				},
			},
		},
		{
			name:  "update file operation with empty diff",
			input: []byte(`{"diff":"","path":"empty.txt","type":"update_file"}`),
			expect: ResponseApplyPatchToolCallOperationUnion{
				OfResponseApplyPatchToolCallOperationUpdateFile: &ResponseApplyPatchToolCallOperationUpdateFile{
					Diff: "",
					Path: "empty.txt",
					Type: "update_file",
				},
			},
		},
		{
			name:  "update file operation with multi-line diff",
			input: []byte(`{"diff":"--- a/main.go\n+++ b/main.go\n@@ -10,5 +10,6 @@\n package main\n import (\n \"fmt\"\n +\"os\"\n )\n func main() {","path":"main.go","type":"update_file"}`),
			expect: ResponseApplyPatchToolCallOperationUnion{
				OfResponseApplyPatchToolCallOperationUpdateFile: &ResponseApplyPatchToolCallOperationUpdateFile{
					Diff: "--- a/main.go\n+++ b/main.go\n@@ -10,5 +10,6 @@\n package main\n import (\n \"fmt\"\n +\"os\"\n )\n func main() {",
					Path: "main.go",
					Type: "update_file",
				},
			},
		},
		{
			name:   "empty type",
			input:  []byte(`{"type": ""}`),
			expErr: "unknown type field value '' for apply patch tool call operation",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var result ResponseApplyPatchToolCallOperationUnion
			err := json.Unmarshal(tc.input, &result)
			if tc.expErr != "" {
				require.ErrorContains(t, err, tc.expErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.expect, result)
		})
	}
}

func TestResponseStreamEventUnionUnmarshalJSON(t *testing.T) {
	tests := []struct {
		name   string
		input  []byte
		expect ResponseStreamEventUnion
		expErr string
	}{
		{
			name:  "audio delta event",
			input: []byte(`{"delta":"base64data","sequence_number":1,"type":"response.audio.delta"}`),
			expect: ResponseStreamEventUnion{
				OfAudioDelta: &ResponseAudioDeltaEvent{
					Delta:          "base64data",
					SequenceNumber: 1,
					Type:           "response.audio.delta",
				},
			},
		},
		{
			name:  "audio done event",
			input: []byte(`{"sequence_number":2,"type":"response.audio.done"}`),
			expect: ResponseStreamEventUnion{
				OfAudioDone: &ResponseAudioDoneEvent{
					SequenceNumber: 2,
					Type:           "response.audio.done",
				},
			},
		},
		{
			name:  "audio transcript delta event",
			input: []byte(`{"delta":"hello world","sequence_number":3,"type":"response.audio.transcript.delta"}`),
			expect: ResponseStreamEventUnion{
				OfAudioTranscriptDelta: &ResponseAudioTranscriptDeltaEvent{
					Delta:          "hello world",
					SequenceNumber: 3,
					Type:           "response.audio.transcript.delta",
				},
			},
		},
		{
			name:  "audio transcript done event",
			input: []byte(`{"sequence_number":4,"type":"response.audio.transcript.done"}`),
			expect: ResponseStreamEventUnion{
				OfAudioTranscriptDone: &ResponseAudioTranscriptDoneEvent{
					SequenceNumber: 4,
					Type:           "response.audio.transcript.done",
				},
			},
		},
		{
			name:  "response created event",
			input: []byte(`{"response":{"id":"resp_123", "object": "response","created_at": 1741487325,"model": "gpt-4o-2024-08-06","status": "in_progress","temperature":1,"parallel_tool_calls":true,"top_p":1,"text":{"format": {"type": "text"}}},"sequence_number":1,"type":"response.created"}`),
			expect: ResponseStreamEventUnion{
				OfResponseCreated: &ResponseCreatedEvent{
					Response: Response{
						ID:                "resp_123",
						Object:            "response",
						Model:             "gpt-4o-2024-08-06",
						CreatedAt:         JSONUNIXTime(time.Unix(int64(1741487325), 0).UTC()),
						ParallelToolCalls: ptr.To(true),
						Temperature:       1,
						Status:            "in_progress",
						TopP:              1,
						Text: ResponseTextConfig{
							Format: ResponseFormatTextConfigUnionParam{
								OfText: &ResponseFormatTextParam{
									Type: "text",
								},
							},
						},
					},
					SequenceNumber: 1,
					Type:           "response.created",
				},
			},
		},
		{
			name:  "response completed event",
			input: []byte(`{"response":{"id":"resp_123","object": "response","created_at": 1741487325,"completed_at": 1741487326,"model":"gpt-4o-2024-08-06","status":"completed","temperature":1,"parallel_tool_calls":true,"top_p":1,"output":[{"type":"message","id":"msg-123","role":"assistant","content":[{"type":"output_text","text":"Hello World!"}]}],"text":{"format": {"type": "text"}}},"sequence_number":6,"type":"response.completed"}`),
			expect: ResponseStreamEventUnion{
				OfResponseCompleted: &ResponseCompletedEvent{
					Response: Response{
						ID:                "resp_123",
						Object:            "response",
						Model:             "gpt-4o-2024-08-06",
						CreatedAt:         JSONUNIXTime(time.Unix(int64(1741487325), 0).UTC()),
						CompletedAt:       ptr.To(JSONUNIXTime(time.Unix(int64(1741487326), 0).UTC())),
						ParallelToolCalls: ptr.To(true),
						Temperature:       1,
						Status:            "completed",
						TopP:              1,
						Output: []ResponseOutputItemUnion{
							{
								OfOutputMessage: &ResponseOutputMessage{
									Type: "message",
									ID:   "msg-123",
									Role: "assistant",
									Content: ResponseOutputMessageContentUnion{
										OfContentArray: []ResponseOutputMessageContentArrayUnion{
											{
												OfOutputText: &ResponseOutputTextParam{
													Type: "output_text",
													Text: "Hello World!",
												},
											},
										},
									},
								},
							},
						},
						Text: ResponseTextConfig{
							Format: ResponseFormatTextConfigUnionParam{
								OfText: &ResponseFormatTextParam{
									Type: "text",
								},
							},
						},
					},
					SequenceNumber: 6,
					Type:           "response.completed",
				},
			},
		},
		{
			name:  "response failed event",
			input: []byte(`{"sequence_number":7,"type":"response.failed","response":{"id":"resp_123","object": "response","created_at": 1741487325,"model":"gpt-4o-2024-08-06","status":"failed","temperature":1,"parallel_tool_calls":true,"top_p":1,"error":{"code":"server_error","message":"The model failed to generate a response."},"text":{"format": {"type": "text"}}}}`),
			expect: ResponseStreamEventUnion{
				OfResponseFailed: &ResponseFailedEvent{
					SequenceNumber: 7,
					Type:           "response.failed",
					Response: Response{
						ID:        "resp_123",
						Object:    "response",
						Model:     "gpt-4o-2024-08-06",
						CreatedAt: JSONUNIXTime(time.Unix(int64(1741487325), 0).UTC()),
						Error: ResponseError{
							Code:    "server_error",
							Message: "The model failed to generate a response.",
						},
						ParallelToolCalls: ptr.To(true),
						Temperature:       1,
						Status:            "failed",
						TopP:              1,
						Text: ResponseTextConfig{
							Format: ResponseFormatTextConfigUnionParam{
								OfText: &ResponseFormatTextParam{
									Type: "text",
								},
							},
						},
					},
				},
			},
		},
		{
			name:  "response incomplete event",
			input: []byte(`{"response":{"id":"resp_123","object": "response","created_at": 1741487325,"model":"gpt-4o-2024-08-06","status":"incomplete","temperature":1,"parallel_tool_calls":true,"top_p":1,"incomplete_details":{"reason":"max_output_tokens"},"text":{"format": {"type": "text"}}},"sequence_number":8,"type":"response.incomplete"}`),
			expect: ResponseStreamEventUnion{
				OfResponseIncomplete: &ResponseIncompleteEvent{
					Response: Response{
						ID:        "resp_123",
						Object:    "response",
						Model:     "gpt-4o-2024-08-06",
						CreatedAt: JSONUNIXTime(time.Unix(int64(1741487325), 0).UTC()),
						IncompleteDetails: ResponseIncompleteDetails{
							Reason: "max_output_tokens",
						},
						ParallelToolCalls: ptr.To(true),
						Temperature:       1,
						Status:            "incomplete",
						TopP:              1,
						Text: ResponseTextConfig{
							Format: ResponseFormatTextConfigUnionParam{
								OfText: &ResponseFormatTextParam{
									Type: "text",
								},
							},
						},
					},
					SequenceNumber: 8,
					Type:           "response.incomplete",
				},
			},
		},
		{
			name:  "response in progress event",
			input: []byte(`{"sequence_number":9,"type":"response.in_progress","response":{"id":"resp_123", "object": "response","created_at": 1741487325,"model": "gpt-4o-2024-08-06","status": "in_progress","temperature":1,"parallel_tool_calls":true,"top_p":1,"text":{"format": {"type": "text"}}}}`),
			expect: ResponseStreamEventUnion{
				OfResponseInProgress: &ResponseInProgressEvent{
					SequenceNumber: 9,
					Type:           "response.in_progress",
					Response: Response{
						ID:                "resp_123",
						Object:            "response",
						Model:             "gpt-4o-2024-08-06",
						CreatedAt:         JSONUNIXTime(time.Unix(int64(1741487325), 0).UTC()),
						ParallelToolCalls: ptr.To(true),
						Temperature:       1,
						Status:            "in_progress",
						TopP:              1,
						Text: ResponseTextConfig{
							Format: ResponseFormatTextConfigUnionParam{
								OfText: &ResponseFormatTextParam{
									Type: "text",
								},
							},
						},
					},
				},
			},
		},
		{
			name:  "response queued event",
			input: []byte(`{"response":{"id":"resp_123", "object": "response","created_at": 1741487325,"model": "gpt-4o-2024-08-06","status": "queued","temperature":1,"parallel_tool_calls":true,"top_p":1,"text":{"format": {"type": "text"}}},"sequence_number":10,"type":"response.queued"}`),
			expect: ResponseStreamEventUnion{
				OfResponseQueued: &ResponseQueuedEvent{
					Response: Response{
						ID:                "resp_123",
						Object:            "response",
						Model:             "gpt-4o-2024-08-06",
						CreatedAt:         JSONUNIXTime(time.Unix(int64(1741487325), 0).UTC()),
						ParallelToolCalls: ptr.To(true),
						Temperature:       1,
						Status:            "queued",
						TopP:              1,
						Text: ResponseTextConfig{
							Format: ResponseFormatTextConfigUnionParam{
								OfText: &ResponseFormatTextParam{
									Type: "text",
								},
							},
						},
					},
					SequenceNumber: 10,
					Type:           "response.queued",
				},
			},
		},
		{
			name:  "error event",
			input: []byte(`{"code":"invalid_request_error","message":"Invalid request","param":"model","sequence_number":11,"type":"error"}`),
			expect: ResponseStreamEventUnion{
				OfError: &ResponseErrorEvent{
					Code:           "invalid_request_error",
					Message:        "Invalid request",
					Param:          "model",
					SequenceNumber: 11,
					Type:           "error",
				},
			},
		},
		{
			name:  "response text delta event",
			input: []byte(`{"delta":"test","sequence_number":1,"content_index":0,"item_id":"msg-1","output_index":0,"type":"response.output_text.delta"}`),
			expect: ResponseStreamEventUnion{
				OfResponseTextDelta: &ResponseTextDeltaEvent{
					Delta:          "test",
					SequenceNumber: 1,
					Type:           "response.output_text.delta",
					ContentIndex:   0,
					ItemID:         "msg-1",
					OutputIndex:    0,
				},
			},
		},
		{
			name:  "response text done event",
			input: []byte(`{"sequence_number":13,"text":"Hello World!","content_index":0,"output_index":0,"item_id":"msg_123","type":"response.output_text.done"}`),
			expect: ResponseStreamEventUnion{
				OfResponseTextDone: &ResponseTextDoneEvent{
					SequenceNumber: 13,
					Type:           "response.output_text.done",
					Text:           "Hello World!",
					ItemID:         "msg_123",
					ContentIndex:   0,
					OutputIndex:    0,
				},
			},
		},
		{
			name:  "response refusal delta event",
			input: []byte(`{"delta":"I can't","sequence_number":14,"content_index":0,"output_index":0,"item_id":"msg_123","type":"response.refusal.delta"}`),
			expect: ResponseStreamEventUnion{
				OfResponseRefusalDelta: &ResponseRefusalDeltaEvent{
					Delta:          "I can't",
					SequenceNumber: 14,
					Type:           "response.refusal.delta",
					ContentIndex:   0,
					OutputIndex:    0,
					ItemID:         "msg_123",
				},
			},
		},
		{
			name:  "response refusal done event",
			input: []byte(`{"sequence_number":15,"content_index":0,"output_index":0,"item_id":"msg_123","refusal":"final refusal text","type":"response.refusal.done"}`),
			expect: ResponseStreamEventUnion{
				OfResponseRefusalDone: &ResponseRefusalDoneEvent{
					SequenceNumber: 15,
					Type:           "response.refusal.done",
					ContentIndex:   0,
					OutputIndex:    0,
					ItemID:         "msg_123",
					Refusal:        "final refusal text",
				},
			},
		},
		{
			name:  "response content part added event",
			input: []byte(`{"content_index":0,"item_id":"item_1","output_index":0,"part":{"type":"output_text","text":""},"sequence_number":16,"type":"response.content_part.added"}`),
			expect: ResponseStreamEventUnion{
				OfResponseContentPartAdded: &ResponseContentPartAddedEvent{
					ContentIndex: 0,
					ItemID:       "item_1",
					OutputIndex:  0,
					Part: ResponseContentPartAddedEventPartUnion{
						OfResponseOutputText: &ResponseOutputTextParam{
							Type: "output_text",
							Text: "",
						},
					},
					SequenceNumber: 16,
					Type:           "response.content_part.added",
				},
			},
		},
		{
			name:  "response content part done event",
			input: []byte(`{"content_index":0,"item_id":"item_1","output_index":0,"part":{"type":"output_text","text":"Hello World!"},"sequence_number":17,"type":"response.content_part.done"}`),
			expect: ResponseStreamEventUnion{
				OfResponseContentPartDone: &ResponseContentPartDoneEvent{
					ContentIndex: 0,
					ItemID:       "item_1",
					OutputIndex:  0,
					Part: ResponseContentPartDoneEventPartUnion{
						OfResponseOutputText: &ResponseOutputTextParam{
							Text: "Hello World!",
							Type: "output_text",
						},
					},
					SequenceNumber: 17,
					Type:           "response.content_part.done",
				},
			},
		},
		{
			name:  "response output item added event",
			input: []byte(`{"item":{"type":"message","id":"msg_123","status":"in_progress","role":"assistant"},"output_index":0,"sequence_number":18,"type":"response.output_item.added"}`),
			expect: ResponseStreamEventUnion{
				OfResponseOutputItemAdded: &ResponseOutputItemAddedEvent{
					Item: ResponseOutputItemUnion{
						OfOutputMessage: &ResponseOutputMessage{
							Type:   "message",
							ID:     "msg_123",
							Status: "in_progress",
							Role:   "assistant",
						},
					},
					OutputIndex:    0,
					SequenceNumber: 18,
					Type:           "response.output_item.added",
				},
			},
		},
		{
			name:  "response output item done event",
			input: []byte(`{"item":{"type":"message","id":"msg_123","status":"in_progress","role":"assistant","content":[{"type":"output_text", "text":"Hello World!"}]},"output_index":1,"sequence_number":19,"type":"response.output_item.done"}`),
			expect: ResponseStreamEventUnion{
				OfResponseOutputItemDone: &ResponseOutputItemDoneEvent{
					Item: ResponseOutputItemUnion{
						OfOutputMessage: &ResponseOutputMessage{
							Type:   "message",
							ID:     "msg_123",
							Status: "in_progress",
							Role:   "assistant",
							Content: ResponseOutputMessageContentUnion{
								OfContentArray: []ResponseOutputMessageContentArrayUnion{
									{
										OfOutputText: &ResponseOutputTextParam{
											Type: "output_text",
											Text: "Hello World!",
										},
									},
								},
							},
						},
					},
					OutputIndex:    1,
					SequenceNumber: 19,
					Type:           "response.output_item.done",
				},
			},
		},
		{
			name:  "response function call arguments delta event",
			input: []byte(`{"delta":"{","item_id":"item_1","output_index":0,"sequence_number":20,"type":"response.function_call_arguments.delta"}`),
			expect: ResponseStreamEventUnion{
				OfResponseFunctionCallArgumentsDelta: &ResponseFunctionCallArgumentsDeltaEvent{
					Delta:          "{",
					ItemID:         "item_1",
					OutputIndex:    0,
					SequenceNumber: 20,
					Type:           "response.function_call_arguments.delta",
				},
			},
		},
		{
			name:  "response function call arguments done event",
			input: []byte(`{"arguments":"{}","item_id":"item_1","name":"test_function","output_index":0,"sequence_number":21,"type":"response.function_call_arguments.done"}`),
			expect: ResponseStreamEventUnion{
				OfResponseFunctionCallArgumentsDone: &ResponseFunctionCallArgumentsDoneEvent{
					Arguments:      "{}",
					ItemID:         "item_1",
					Name:           "test_function",
					OutputIndex:    0,
					SequenceNumber: 21,
					Type:           "response.function_call_arguments.done",
				},
			},
		},
		{
			name:  "response reasoning text delta event",
			input: []byte(`{"content_index":0,"delta":"thinking","item_id":"item_1","output_index":0,"sequence_number":22,"type":"response.reasoning_text.delta"}`),
			expect: ResponseStreamEventUnion{
				OfResponseReasoningTextDelta: &ResponseReasoningTextDeltaEvent{
					ContentIndex:   0,
					Delta:          "thinking",
					ItemID:         "item_1",
					OutputIndex:    0,
					SequenceNumber: 22,
					Type:           "response.reasoning_text.delta",
				},
			},
		},
		{
			name:  "response reasoning text done event",
			input: []byte(`{"content_index":0,"item_id":"item_1","output_index":0,"sequence_number":23,"text":"reasoning text","type":"response.reasoning_text.done"}`),
			expect: ResponseStreamEventUnion{
				OfResponseReasoningTextDone: &ResponseReasoningTextDoneEvent{
					ContentIndex:   0,
					ItemID:         "item_1",
					OutputIndex:    0,
					SequenceNumber: 23,
					Text:           "reasoning text",
					Type:           "response.reasoning_text.done",
				},
			},
		},
		{
			name:  "response reasoning summary text delta event",
			input: []byte(`{"delta":"summary","item_id":"item_1","output_index":0,"summary_index":0,"sequence_number":24,"type":"response.reasoning_summary_text.delta"}`),
			expect: ResponseStreamEventUnion{
				OfResponseReasoningSummaryTextDelta: &ResponseReasoningSummaryTextDeltaEvent{
					Delta:          "summary",
					ItemID:         "item_1",
					OutputIndex:    0,
					SequenceNumber: 24,
					SummaryIndex:   0,
					Type:           "response.reasoning_summary_text.delta",
				},
			},
		},
		{
			name:  "response reasoning summary text done event",
			input: []byte(`{"item_id":"item_1","output_index":0,"summary_index":0,"sequence_number":25,"text":"summary done","type":"response.reasoning_summary_text.done"}`),
			expect: ResponseStreamEventUnion{
				OfResponseReasoningSummaryTextDone: &ResponseReasoningSummaryTextDoneEvent{
					ItemID:         "item_1",
					OutputIndex:    0,
					SequenceNumber: 25,
					Text:           "summary done",
					SummaryIndex:   0,
					Type:           "response.reasoning_summary_text.done",
				},
			},
		},
		{
			name:  "response reasoning summary part added event",
			input: []byte(`{"item_id":"item_1","output_index":0,"part":{"text":"text","type":"summary_text"},"sequence_number":26,"summary_index":0,"type":"response.reasoning_summary_part.added"}`),
			expect: ResponseStreamEventUnion{
				OfResponseReasoningSummaryPartAdded: &ResponseReasoningSummaryPartAddedEvent{
					ItemID:         "item_1",
					OutputIndex:    0,
					Part:           ResponseReasoningSummaryPartAddedEventPart{Text: "text", Type: "summary_text"},
					SequenceNumber: 26,
					SummaryIndex:   0,
					Type:           "response.reasoning_summary_part.added",
				},
			},
		},
		{
			name:  "response reasoning summary part done event",
			input: []byte(`{"item_id":"item_1","output_index":0,"part":{"text":"text","type":"summary_text"},"sequence_number":27,"summary_index":0,"type":"response.reasoning_summary_part.done"}`),
			expect: ResponseStreamEventUnion{
				OfResponseReasoningSummaryPartDone: &ResponseReasoningSummaryPartDoneEvent{
					ItemID:         "item_1",
					OutputIndex:    0,
					Part:           ResponseReasoningSummaryPartDoneEventPart{Text: "text", Type: "summary_text"},
					SequenceNumber: 27,
					SummaryIndex:   0,
					Type:           "response.reasoning_summary_part.done",
				},
			},
		},
		{
			name:  "code interpreter call code delta event",
			input: []byte(`{"delta":"code","item_id":"item_1","output_index":0,"sequence_number":28,"type":"response.code_interpreter_call_code.delta"}`),
			expect: ResponseStreamEventUnion{
				OfCodeInterpreterCallCodeDelta: &ResponseCodeInterpreterCallCodeDeltaEvent{
					Delta:          "code",
					ItemID:         "item_1",
					OutputIndex:    0,
					SequenceNumber: 28,
					Type:           "response.code_interpreter_call_code.delta",
				},
			},
		},
		{
			name:  "code interpreter call code done event",
			input: []byte(`{"code":"print('hello')","item_id":"item_1","output_index":0,"sequence_number":29,"type":"response.code_interpreter_call_code.done"}`),
			expect: ResponseStreamEventUnion{
				OfCodeInterpreterCallCodeDone: &ResponseCodeInterpreterCallCodeDoneEvent{
					Code:           "print('hello')",
					ItemID:         "item_1",
					OutputIndex:    0,
					SequenceNumber: 29,
					Type:           "response.code_interpreter_call_code.done",
				},
			},
		},
		{
			name:  "code interpreter call in progress event",
			input: []byte(`{"item_id":"item_1","output_index":0,"sequence_number":30,"type":"response.code_interpreter_call.in_progress"}`),
			expect: ResponseStreamEventUnion{
				OfCodeInterpreterCallInprogress: &ResponseCodeInterpreterCallInProgressEvent{
					ItemID:         "item_1",
					OutputIndex:    0,
					SequenceNumber: 30,
					Type:           "response.code_interpreter_call.in_progress",
				},
			},
		},
		{
			name:  "code interpreter call interpreting event",
			input: []byte(`{"item_id":"item_1","output_index":0,"sequence_number":31,"type":"response.code_interpreter_call.interpreting"}`),
			expect: ResponseStreamEventUnion{
				OfCodeInterpreterCallInterpreting: &ResponseCodeInterpreterCallInterpretingEvent{
					ItemID:         "item_1",
					OutputIndex:    0,
					SequenceNumber: 31,
					Type:           "response.code_interpreter_call.interpreting",
				},
			},
		},
		{
			name:  "code interpreter call completed event",
			input: []byte(`{"item_id":"item_1","output_index":0,"sequence_number":32,"type":"response.code_interpreter_call.completed"}`),
			expect: ResponseStreamEventUnion{
				OfCodeInterpreterCallCompleted: &ResponseCodeInterpreterCallCompletedEvent{
					ItemID:         "item_1",
					OutputIndex:    0,
					SequenceNumber: 32,
					Type:           "response.code_interpreter_call.completed",
				},
			},
		},
		{
			name:  "response file search call searching event",
			input: []byte(`{"item_id":"item_1","output_index":0,"sequence_number":33,"type":"response.file_search_call.searching"}`),
			expect: ResponseStreamEventUnion{
				OfResponseFileSearchCallSearching: &ResponseFileSearchCallSearchingEvent{
					ItemID:         "item_1",
					OutputIndex:    0,
					SequenceNumber: 33,
					Type:           "response.file_search_call.searching",
				},
			},
		},
		{
			name:  "response file search call in progress event",
			input: []byte(`{"item_id":"item_1","output_index":0,"sequence_number":34,"type":"response.file_search_call.in_progress"}`),
			expect: ResponseStreamEventUnion{
				OfResponseFileSearchCallInProgress: &ResponseFileSearchCallInProgressEvent{
					ItemID:         "item_1",
					OutputIndex:    0,
					SequenceNumber: 34,
					Type:           "response.file_search_call.in_progress",
				},
			},
		},
		{
			name:  "response file search call completed event",
			input: []byte(`{"item_id":"item_1","output_index":0,"sequence_number":35,"type":"response.file_search_call.completed"}`),
			expect: ResponseStreamEventUnion{
				OfResponseFileSearchCallCompleted: &ResponseFileSearchCallCompletedEvent{
					ItemID:         "item_1",
					OutputIndex:    0,
					SequenceNumber: 35,
					Type:           "response.file_search_call.completed",
				},
			},
		},
		{
			name:  "response web search call searching event",
			input: []byte(`{"item_id":"item_1","output_index":0,"sequence_number":36,"type":"response.web_search_call.searching"}`),
			expect: ResponseStreamEventUnion{
				OfResponseWebSearchCallSearching: &ResponseWebSearchCallSearchingEvent{
					ItemID:         "item_1",
					OutputIndex:    0,
					SequenceNumber: 36,
					Type:           "response.web_search_call.searching",
				},
			},
		},
		{
			name:  "response web search call in progress event",
			input: []byte(`{"item_id":"item_1","output_index":0,"sequence_number":37,"type":"response.web_search_call.in_progress"}`),
			expect: ResponseStreamEventUnion{
				OfResponseWebSearchCallInProgress: &ResponseWebSearchCallInProgressEvent{
					ItemID:         "item_1",
					OutputIndex:    0,
					SequenceNumber: 37,
					Type:           "response.web_search_call.in_progress",
				},
			},
		},
		{
			name:  "response web search call completed event",
			input: []byte(`{"item_id":"item_1","output_index":0,"sequence_number":38,"type":"response.web_search_call.completed"}`),
			expect: ResponseStreamEventUnion{
				OfResponseWebSearchCallCompleted: &ResponseWebSearchCallCompletedEvent{
					ItemID:         "item_1",
					OutputIndex:    0,
					SequenceNumber: 38,
					Type:           "response.web_search_call.completed",
				},
			},
		},
		{
			name:  "response image gen call in progress event",
			input: []byte(`{"item_id":"item_1","output_index":0,"sequence_number":39,"type":"response.image_generation_call.in_progress"}`),
			expect: ResponseStreamEventUnion{
				OfResponseImageGenCallInProgress: &ResponseImageGenCallInProgressEvent{
					ItemID:         "item_1",
					OutputIndex:    0,
					SequenceNumber: 39,
					Type:           "response.image_generation_call.in_progress",
				},
			},
		},
		{
			name:  "response image gen call generating event",
			input: []byte(`{"item_id":"item_1","output_index":0,"sequence_number":40,"type":"response.image_generation_call.generating"}`),
			expect: ResponseStreamEventUnion{
				OfResponseImageGenCallGenerating: &ResponseImageGenCallGeneratingEvent{
					ItemID:         "item_1",
					OutputIndex:    0,
					SequenceNumber: 40,
					Type:           "response.image_generation_call.generating",
				},
			},
		},
		{
			name:  "response image gen call partial image event",
			input: []byte(`{"item_id":"item_1","output_index":0,"partial_image_index":0,"partial_image_b64":"bas64encodedImage","sequence_number":41,"type":"response.image_generation_call.partial_image"}`),
			expect: ResponseStreamEventUnion{
				OfResponseImageGenCallPartialImage: &ResponseImageGenCallPartialImageEvent{
					ItemID:            "item_1",
					OutputIndex:       0,
					SequenceNumber:    41,
					PartialImageIndex: 0,
					PartialImageB64:   "bas64encodedImage",
					Type:              "response.image_generation_call.partial_image",
				},
			},
		},
		{
			name:  "response image gen call completed event",
			input: []byte(`{"item_id":"item_1","output_index":0,"sequence_number":42,"type":"response.image_generation_call.completed"}`),
			expect: ResponseStreamEventUnion{
				OfResponseImageGenCallCompleted: &ResponseImageGenCallCompletedEvent{
					ItemID:         "item_1",
					OutputIndex:    0,
					SequenceNumber: 42,
					Type:           "response.image_generation_call.completed",
				},
			},
		},
		{
			name:  "response mcp call arguments delta event",
			input: []byte(`{"delta":"{","item_id":"item_1","output_index":0,"sequence_number":43,"type":"response.mcp_call_arguments.delta"}`),
			expect: ResponseStreamEventUnion{
				OfResponseMcpCallArgumentsDelta: &ResponseMcpCallArgumentsDeltaEvent{
					Delta:          "{",
					ItemID:         "item_1",
					OutputIndex:    0,
					SequenceNumber: 43,
					Type:           "response.mcp_call_arguments.delta",
				},
			},
		},
		{
			name:  "response mcp call arguments done event",
			input: []byte(`{"arguments":"{}","item_id":"item_1","output_index":0,"sequence_number":44,"type":"response.mcp_call_arguments.done"}`),
			expect: ResponseStreamEventUnion{
				OfResponseMcpCallArgumentsDone: &ResponseMcpCallArgumentsDoneEvent{
					Arguments:      "{}",
					ItemID:         "item_1",
					OutputIndex:    0,
					SequenceNumber: 44,
					Type:           "response.mcp_call_arguments.done",
				},
			},
		},
		{
			name:  "response mcp call in progress event",
			input: []byte(`{"item_id":"item_1","output_index":0,"sequence_number":45,"type":"response.mcp_call.in_progress"}`),
			expect: ResponseStreamEventUnion{
				OfResponseMcpCallInProgress: &ResponseMcpCallInProgressEvent{
					ItemID:         "item_1",
					OutputIndex:    0,
					SequenceNumber: 45,
					Type:           "response.mcp_call.in_progress",
				},
			},
		},
		{
			name:  "response mcp call completed event",
			input: []byte(`{"item_id":"item_1","output_index":0,"sequence_number":46,"type":"response.mcp_call.completed"}`),
			expect: ResponseStreamEventUnion{
				OfResponseMcpCallCompleted: &ResponseMcpCallCompletedEvent{
					ItemID:         "item_1",
					OutputIndex:    0,
					SequenceNumber: 46,
					Type:           "response.mcp_call.completed",
				},
			},
		},
		{
			name:  "response mcp call failed event",
			input: []byte(`{"item_id":"item_1","output_index":0,"sequence_number":47,"type":"response.mcp_call.failed"}`),
			expect: ResponseStreamEventUnion{
				OfResponseMcpCallFailed: &ResponseMcpCallFailedEvent{
					ItemID:         "item_1",
					OutputIndex:    0,
					SequenceNumber: 47,
					Type:           "response.mcp_call.failed",
				},
			},
		},
		{
			name:  "response mcp list tools in progress event",
			input: []byte(`{"item_id":"item_1","output_index":0,"sequence_number":48,"type":"response.mcp_list_tools.in_progress"}`),
			expect: ResponseStreamEventUnion{
				OfResponseMcpListToolsInProgress: &ResponseMcpListToolsInProgressEvent{
					ItemID:         "item_1",
					OutputIndex:    0,
					SequenceNumber: 48,
					Type:           "response.mcp_list_tools.in_progress",
				},
			},
		},
		{
			name:  "response mcp list tools completed event",
			input: []byte(`{"item_id":"item_1","output_index":0,"sequence_number":49,"type":"response.mcp_list_tools.completed"}`),
			expect: ResponseStreamEventUnion{
				OfResponseMcpListToolsCompleted: &ResponseMcpListToolsCompletedEvent{
					ItemID:         "item_1",
					OutputIndex:    0,
					SequenceNumber: 49,
					Type:           "response.mcp_list_tools.completed",
				},
			},
		},
		{
			name:  "response mcp list tools failed event",
			input: []byte(`{"item_id":"item_1","output_index":0,"sequence_number":50,"type":"response.mcp_list_tools.failed"}`),
			expect: ResponseStreamEventUnion{
				OfResponseMcpListToolsFailed: &ResponseMcpListToolsFailedEvent{
					ItemID:         "item_1",
					OutputIndex:    0,
					SequenceNumber: 50,
					Type:           "response.mcp_list_tools.failed",
				},
			},
		},
		{
			name:  "response output text annotation added event",
			input: []byte(`{"annotation":{"type":"test"},"annotation_index":0,"content_index":0,"item_id":"item_1","output_index":0,"sequence_number":51,"type":"response.output_text.annotation.added"}`),
			expect: ResponseStreamEventUnion{
				OfResponseOutputTextAnnotationAdded: &ResponseOutputTextAnnotationAddedEvent{
					Annotation:      map[string]any{"type": "test"},
					AnnotationIndex: 0,
					ContentIndex:    0,
					ItemID:          "item_1",
					OutputIndex:     0,
					SequenceNumber:  51,
					Type:            "response.output_text.annotation.added",
				},
			},
		},
		{
			name:  "response custom tool call input delta event",
			input: []byte(`{"delta":"{","item_id":"item_1","output_index":0,"sequence_number":52,"type":"response.custom_tool_call_input.delta"}`),
			expect: ResponseStreamEventUnion{
				OfResponseCustomToolCallInputDelta: &ResponseCustomToolCallInputDeltaEvent{
					Delta:          "{",
					ItemID:         "item_1",
					OutputIndex:    0,
					SequenceNumber: 52,
					Type:           "response.custom_tool_call_input.delta",
				},
			},
		},
		{
			name:  "response custom tool call input done event",
			input: []byte(`{"input":"{}","item_id":"item_1","output_index":0,"sequence_number":53,"type":"response.custom_tool_call_input.done"}`),
			expect: ResponseStreamEventUnion{
				OfResponseCustomToolCallInputDone: &ResponseCustomToolCallInputDoneEvent{
					Input:          "{}",
					ItemID:         "item_1",
					OutputIndex:    0,
					SequenceNumber: 53,
					Type:           "response.custom_tool_call_input.done",
				},
			},
		},
		{
			name:   "empty type",
			input:  []byte(`{"type": ""}`),
			expErr: "unknown type field value '' for response stream event",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var result ResponseStreamEventUnion
			err := json.Unmarshal(tc.input, &result)
			if tc.expErr != "" {
				require.ErrorContains(t, err, tc.expErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.expect, result)
		})
	}
}

func TestResponseStreamEventUnionMarshalJSON(t *testing.T) {
	tests := []struct {
		name   string
		expect []byte
		input  ResponseStreamEventUnion
		expErr string
	}{
		{
			name:   "audio delta event",
			expect: []byte(`{"delta":"base64data","sequence_number":1,"type":"response.audio.delta"}`),
			input: ResponseStreamEventUnion{
				OfAudioDelta: &ResponseAudioDeltaEvent{
					Delta:          "base64data",
					SequenceNumber: 1,
					Type:           "response.audio.delta",
				},
			},
		},
		{
			name:   "audio done event",
			expect: []byte(`{"sequence_number":2,"type":"response.audio.done"}`),
			input: ResponseStreamEventUnion{
				OfAudioDone: &ResponseAudioDoneEvent{
					SequenceNumber: 2,
					Type:           "response.audio.done",
				},
			},
		},
		{
			name:   "audio transcript delta event",
			expect: []byte(`{"delta":"hello world","sequence_number":3,"type":"response.audio.transcript.delta"}`),
			input: ResponseStreamEventUnion{
				OfAudioTranscriptDelta: &ResponseAudioTranscriptDeltaEvent{
					Delta:          "hello world",
					SequenceNumber: 3,
					Type:           "response.audio.transcript.delta",
				},
			},
		},
		{
			name:   "audio transcript done event",
			expect: []byte(`{"sequence_number":4,"type":"response.audio.transcript.done"}`),
			input: ResponseStreamEventUnion{
				OfAudioTranscriptDone: &ResponseAudioTranscriptDoneEvent{
					SequenceNumber: 4,
					Type:           "response.audio.transcript.done",
				},
			},
		},
		{
			name:   "response created event",
			expect: []byte(`{"response":{"id":"resp_123", "object": "response","created_at": 1741487325,"model": "gpt-4o-2024-08-06","status": "in_progress","temperature":1,"parallel_tool_calls":true,"top_p":1,"text":{"format": {"type": "text"}}},"sequence_number":1,"type":"response.created"}`),
			input: ResponseStreamEventUnion{
				OfResponseCreated: &ResponseCreatedEvent{
					Response: Response{
						ID:                "resp_123",
						Object:            "response",
						Model:             "gpt-4o-2024-08-06",
						CreatedAt:         JSONUNIXTime(time.Unix(int64(1741487325), 0).UTC()),
						ParallelToolCalls: ptr.To(true),
						Temperature:       1,
						Status:            "in_progress",
						TopP:              1,
						Text: ResponseTextConfig{
							Format: ResponseFormatTextConfigUnionParam{
								OfText: &ResponseFormatTextParam{
									Type: "text",
								},
							},
						},
					},
					SequenceNumber: 1,
					Type:           "response.created",
				},
			},
		},
		{
			name:   "response completed event",
			expect: []byte(`{"response":{"id":"resp_123","object": "response","created_at": 1741487325,"completed_at": 1741487326,"model":"gpt-4o-2024-08-06","status":"completed","temperature":1,"parallel_tool_calls":true,"top_p":1,"output":[{"type":"message","id":"msg-123","role":"assistant","content":[{"type":"output_text","text":"Hello World!"}]}],"text":{"format": {"type": "text"}}},"sequence_number":6,"type":"response.completed"}`),
			input: ResponseStreamEventUnion{
				OfResponseCompleted: &ResponseCompletedEvent{
					Response: Response{
						ID:                "resp_123",
						Object:            "response",
						Model:             "gpt-4o-2024-08-06",
						CreatedAt:         JSONUNIXTime(time.Unix(int64(1741487325), 0).UTC()),
						CompletedAt:       ptr.To(JSONUNIXTime(time.Unix(int64(1741487326), 0).UTC())),
						ParallelToolCalls: ptr.To(true),
						Temperature:       1,
						Status:            "completed",
						TopP:              1,
						Output: []ResponseOutputItemUnion{
							{
								OfOutputMessage: &ResponseOutputMessage{
									Type: "message",
									ID:   "msg-123",
									Role: "assistant",
									Content: ResponseOutputMessageContentUnion{
										OfContentArray: []ResponseOutputMessageContentArrayUnion{
											{
												OfOutputText: &ResponseOutputTextParam{
													Type: "output_text",
													Text: "Hello World!",
												},
											},
										},
									},
								},
							},
						},
						Text: ResponseTextConfig{
							Format: ResponseFormatTextConfigUnionParam{
								OfText: &ResponseFormatTextParam{
									Type: "text",
								},
							},
						},
					},
					SequenceNumber: 6,
					Type:           "response.completed",
				},
			},
		},
		{
			name:   "response failed event",
			expect: []byte(`{"sequence_number":7,"type":"response.failed","response":{"id":"resp_123","object": "response","created_at": 1741487325,"model":"gpt-4o-2024-08-06","status":"failed","temperature":1,"parallel_tool_calls":true,"top_p":1,"error":{"code":"server_error","message":"The model failed to generate a response."},"text":{"format": {"type": "text"}}}}`),
			input: ResponseStreamEventUnion{
				OfResponseFailed: &ResponseFailedEvent{
					SequenceNumber: 7,
					Type:           "response.failed",
					Response: Response{
						ID:        "resp_123",
						Object:    "response",
						Model:     "gpt-4o-2024-08-06",
						CreatedAt: JSONUNIXTime(time.Unix(int64(1741487325), 0).UTC()),
						Error: ResponseError{
							Code:    "server_error",
							Message: "The model failed to generate a response.",
						},
						ParallelToolCalls: ptr.To(true),
						Temperature:       1,
						Status:            "failed",
						TopP:              1,
						Text: ResponseTextConfig{
							Format: ResponseFormatTextConfigUnionParam{
								OfText: &ResponseFormatTextParam{
									Type: "text",
								},
							},
						},
					},
				},
			},
		},
		{
			name:   "response incomplete event",
			expect: []byte(`{"response":{"id":"resp_123","object": "response","created_at": 1741487325,"model":"gpt-4o-2024-08-06","status":"incomplete","temperature":1,"parallel_tool_calls":true,"top_p":1,"incomplete_details":{"reason":"max_output_tokens"},"text":{"format": {"type": "text"}}},"sequence_number":8,"type":"response.incomplete"}`),
			input: ResponseStreamEventUnion{
				OfResponseIncomplete: &ResponseIncompleteEvent{
					Response: Response{
						ID:        "resp_123",
						Object:    "response",
						Model:     "gpt-4o-2024-08-06",
						CreatedAt: JSONUNIXTime(time.Unix(int64(1741487325), 0).UTC()),
						IncompleteDetails: ResponseIncompleteDetails{
							Reason: "max_output_tokens",
						},
						ParallelToolCalls: ptr.To(true),
						Temperature:       1,
						Status:            "incomplete",
						TopP:              1,
						Text: ResponseTextConfig{
							Format: ResponseFormatTextConfigUnionParam{
								OfText: &ResponseFormatTextParam{
									Type: "text",
								},
							},
						},
					},
					SequenceNumber: 8,
					Type:           "response.incomplete",
				},
			},
		},
		{
			name:   "response in progress event",
			expect: []byte(`{"sequence_number":9,"type":"response.in_progress","response":{"id":"resp_123", "object": "response","created_at": 1741487325,"model": "gpt-4o-2024-08-06","status": "in_progress","temperature":1,"parallel_tool_calls":true,"top_p":1,"text":{"format": {"type": "text"}}}}`),
			input: ResponseStreamEventUnion{
				OfResponseInProgress: &ResponseInProgressEvent{
					SequenceNumber: 9,
					Type:           "response.in_progress",
					Response: Response{
						ID:                "resp_123",
						Object:            "response",
						Model:             "gpt-4o-2024-08-06",
						CreatedAt:         JSONUNIXTime(time.Unix(int64(1741487325), 0).UTC()),
						ParallelToolCalls: ptr.To(true),
						Temperature:       1,
						Status:            "in_progress",
						TopP:              1,
						Text: ResponseTextConfig{
							Format: ResponseFormatTextConfigUnionParam{
								OfText: &ResponseFormatTextParam{
									Type: "text",
								},
							},
						},
					},
				},
			},
		},
		{
			name:   "response queued event",
			expect: []byte(`{"response":{"id":"resp_123", "object": "response","created_at": 1741487325,"model": "gpt-4o-2024-08-06","status": "queued","temperature":1,"parallel_tool_calls":true,"top_p":1,"text":{"format": {"type": "text"}}},"sequence_number":10,"type":"response.queued"}`),
			input: ResponseStreamEventUnion{
				OfResponseQueued: &ResponseQueuedEvent{
					Response: Response{
						ID:                "resp_123",
						Object:            "response",
						Model:             "gpt-4o-2024-08-06",
						CreatedAt:         JSONUNIXTime(time.Unix(int64(1741487325), 0).UTC()),
						ParallelToolCalls: ptr.To(true),
						Temperature:       1,
						Status:            "queued",
						TopP:              1,
						Text: ResponseTextConfig{
							Format: ResponseFormatTextConfigUnionParam{
								OfText: &ResponseFormatTextParam{
									Type: "text",
								},
							},
						},
					},
					SequenceNumber: 10,
					Type:           "response.queued",
				},
			},
		},
		{
			name:   "error event",
			expect: []byte(`{"code":"invalid_request_error","message":"Invalid request","param":"model","sequence_number":11,"type":"error"}`),
			input: ResponseStreamEventUnion{
				OfError: &ResponseErrorEvent{
					Code:           "invalid_request_error",
					Message:        "Invalid request",
					Param:          "model",
					SequenceNumber: 11,
					Type:           "error",
				},
			},
		},
		{
			name:   "response text delta event",
			expect: []byte(`{"delta":"test","sequence_number":1,"content_index":0,"item_id":"msg-1","output_index":0,"type":"response.output_text.delta"}`),
			input: ResponseStreamEventUnion{
				OfResponseTextDelta: &ResponseTextDeltaEvent{
					Delta:          "test",
					SequenceNumber: 1,
					Type:           "response.output_text.delta",
					ContentIndex:   0,
					ItemID:         "msg-1",
					OutputIndex:    0,
				},
			},
		},
		{
			name:   "response text done event",
			expect: []byte(`{"sequence_number":13,"text":"Hello World!","content_index":0,"output_index":0,"item_id":"msg_123","type":"response.output_text.done"}`),
			input: ResponseStreamEventUnion{
				OfResponseTextDone: &ResponseTextDoneEvent{
					SequenceNumber: 13,
					Type:           "response.output_text.done",
					Text:           "Hello World!",
					ItemID:         "msg_123",
					ContentIndex:   0,
					OutputIndex:    0,
				},
			},
		},
		{
			name:   "response refusal delta event",
			expect: []byte(`{"delta":"I can't","sequence_number":14,"content_index":0,"output_index":0,"item_id":"msg_123","type":"response.refusal.delta"}`),
			input: ResponseStreamEventUnion{
				OfResponseRefusalDelta: &ResponseRefusalDeltaEvent{
					Delta:          "I can't",
					SequenceNumber: 14,
					Type:           "response.refusal.delta",
					ContentIndex:   0,
					OutputIndex:    0,
					ItemID:         "msg_123",
				},
			},
		},
		{
			name:   "response refusal done event",
			expect: []byte(`{"sequence_number":15,"content_index":0,"output_index":0,"item_id":"msg_123","refusal":"final refusal text","type":"response.refusal.done"}`),
			input: ResponseStreamEventUnion{
				OfResponseRefusalDone: &ResponseRefusalDoneEvent{
					SequenceNumber: 15,
					Type:           "response.refusal.done",
					ContentIndex:   0,
					OutputIndex:    0,
					ItemID:         "msg_123",
					Refusal:        "final refusal text",
				},
			},
		},
		{
			name:   "response content part added event",
			expect: []byte(`{"content_index":0,"item_id":"item_1","output_index":0,"part":{"type":"output_text","text":""},"sequence_number":16,"type":"response.content_part.added"}`),
			input: ResponseStreamEventUnion{
				OfResponseContentPartAdded: &ResponseContentPartAddedEvent{
					ContentIndex: 0,
					ItemID:       "item_1",
					OutputIndex:  0,
					Part: ResponseContentPartAddedEventPartUnion{
						OfResponseOutputText: &ResponseOutputTextParam{
							Type: "output_text",
							Text: "",
						},
					},
					SequenceNumber: 16,
					Type:           "response.content_part.added",
				},
			},
		},
		{
			name:   "response content part done event",
			expect: []byte(`{"content_index":0,"item_id":"item_1","output_index":0,"part":{"type":"output_text","text":"Hello World!"},"sequence_number":17,"type":"response.content_part.done"}`),
			input: ResponseStreamEventUnion{
				OfResponseContentPartDone: &ResponseContentPartDoneEvent{
					ContentIndex: 0,
					ItemID:       "item_1",
					OutputIndex:  0,
					Part: ResponseContentPartDoneEventPartUnion{
						OfResponseOutputText: &ResponseOutputTextParam{
							Text: "Hello World!",
							Type: "output_text",
						},
					},
					SequenceNumber: 17,
					Type:           "response.content_part.done",
				},
			},
		},
		{
			name:   "response output item added event",
			expect: []byte(`{"item":{"type":"message","id":"msg_123","status":"in_progress","role":"assistant"},"output_index":0,"sequence_number":18,"type":"response.output_item.added"}`),
			input: ResponseStreamEventUnion{
				OfResponseOutputItemAdded: &ResponseOutputItemAddedEvent{
					Item: ResponseOutputItemUnion{
						OfOutputMessage: &ResponseOutputMessage{
							Type:   "message",
							ID:     "msg_123",
							Status: "in_progress",
							Role:   "assistant",
						},
					},
					OutputIndex:    0,
					SequenceNumber: 18,
					Type:           "response.output_item.added",
				},
			},
		},
		{
			name:   "response output item done event",
			expect: []byte(`{"item":{"type":"message","id":"msg_123","status":"in_progress","role":"assistant","content":[{"type":"output_text", "text":"Hello World!"}]},"output_index":1,"sequence_number":19,"type":"response.output_item.done"}`),
			input: ResponseStreamEventUnion{
				OfResponseOutputItemDone: &ResponseOutputItemDoneEvent{
					Item: ResponseOutputItemUnion{
						OfOutputMessage: &ResponseOutputMessage{
							Type:   "message",
							ID:     "msg_123",
							Status: "in_progress",
							Role:   "assistant",
							Content: ResponseOutputMessageContentUnion{
								OfContentArray: []ResponseOutputMessageContentArrayUnion{
									{
										OfOutputText: &ResponseOutputTextParam{
											Type: "output_text",
											Text: "Hello World!",
										},
									},
								},
							},
						},
					},
					OutputIndex:    1,
					SequenceNumber: 19,
					Type:           "response.output_item.done",
				},
			},
		},
		{
			name:   "response function call arguments delta event",
			expect: []byte(`{"delta":"{","item_id":"item_1","output_index":0,"sequence_number":20,"type":"response.function_call_arguments.delta"}`),
			input: ResponseStreamEventUnion{
				OfResponseFunctionCallArgumentsDelta: &ResponseFunctionCallArgumentsDeltaEvent{
					Delta:          "{",
					ItemID:         "item_1",
					OutputIndex:    0,
					SequenceNumber: 20,
					Type:           "response.function_call_arguments.delta",
				},
			},
		},
		{
			name:   "response function call arguments done event",
			expect: []byte(`{"arguments":"{}","item_id":"item_1","name":"test_function","output_index":0,"sequence_number":21,"type":"response.function_call_arguments.done"}`),
			input: ResponseStreamEventUnion{
				OfResponseFunctionCallArgumentsDone: &ResponseFunctionCallArgumentsDoneEvent{
					Arguments:      "{}",
					ItemID:         "item_1",
					Name:           "test_function",
					OutputIndex:    0,
					SequenceNumber: 21,
					Type:           "response.function_call_arguments.done",
				},
			},
		},
		{
			name:   "response reasoning text delta event",
			expect: []byte(`{"content_index":0,"delta":"thinking","item_id":"item_1","output_index":0,"sequence_number":22,"type":"response.reasoning_text.delta"}`),
			input: ResponseStreamEventUnion{
				OfResponseReasoningTextDelta: &ResponseReasoningTextDeltaEvent{
					ContentIndex:   0,
					Delta:          "thinking",
					ItemID:         "item_1",
					OutputIndex:    0,
					SequenceNumber: 22,
					Type:           "response.reasoning_text.delta",
				},
			},
		},
		{
			name:   "response reasoning text done event",
			expect: []byte(`{"content_index":0,"item_id":"item_1","output_index":0,"sequence_number":23,"text":"reasoning text","type":"response.reasoning_text.done"}`),
			input: ResponseStreamEventUnion{
				OfResponseReasoningTextDone: &ResponseReasoningTextDoneEvent{
					ContentIndex:   0,
					ItemID:         "item_1",
					OutputIndex:    0,
					SequenceNumber: 23,
					Text:           "reasoning text",
					Type:           "response.reasoning_text.done",
				},
			},
		},
		{
			name:   "response reasoning summary text delta event",
			expect: []byte(`{"delta":"summary","item_id":"item_1","output_index":0,"summary_index":0,"sequence_number":24,"type":"response.reasoning_summary_text.delta"}`),
			input: ResponseStreamEventUnion{
				OfResponseReasoningSummaryTextDelta: &ResponseReasoningSummaryTextDeltaEvent{
					Delta:          "summary",
					ItemID:         "item_1",
					OutputIndex:    0,
					SequenceNumber: 24,
					SummaryIndex:   0,
					Type:           "response.reasoning_summary_text.delta",
				},
			},
		},
		{
			name:   "response reasoning summary text done event",
			expect: []byte(`{"item_id":"item_1","output_index":0,"summary_index":0,"sequence_number":25,"text":"summary done","type":"response.reasoning_summary_text.done"}`),
			input: ResponseStreamEventUnion{
				OfResponseReasoningSummaryTextDone: &ResponseReasoningSummaryTextDoneEvent{
					ItemID:         "item_1",
					OutputIndex:    0,
					SequenceNumber: 25,
					Text:           "summary done",
					SummaryIndex:   0,
					Type:           "response.reasoning_summary_text.done",
				},
			},
		},
		{
			name:   "response reasoning summary part added event",
			expect: []byte(`{"item_id":"item_1","output_index":0,"part":{"text":"text","type":"summary_text"},"sequence_number":26,"summary_index":0,"type":"response.reasoning_summary_part.added"}`),
			input: ResponseStreamEventUnion{
				OfResponseReasoningSummaryPartAdded: &ResponseReasoningSummaryPartAddedEvent{
					ItemID:         "item_1",
					OutputIndex:    0,
					Part:           ResponseReasoningSummaryPartAddedEventPart{Text: "text", Type: "summary_text"},
					SequenceNumber: 26,
					SummaryIndex:   0,
					Type:           "response.reasoning_summary_part.added",
				},
			},
		},
		{
			name:   "response reasoning summary part done event",
			expect: []byte(`{"item_id":"item_1","output_index":0,"part":{"text":"text","type":"summary_text"},"sequence_number":27,"summary_index":0,"type":"response.reasoning_summary_part.done"}`),
			input: ResponseStreamEventUnion{
				OfResponseReasoningSummaryPartDone: &ResponseReasoningSummaryPartDoneEvent{
					ItemID:         "item_1",
					OutputIndex:    0,
					Part:           ResponseReasoningSummaryPartDoneEventPart{Text: "text", Type: "summary_text"},
					SequenceNumber: 27,
					SummaryIndex:   0,
					Type:           "response.reasoning_summary_part.done",
				},
			},
		},
		{
			name:   "code interpreter call code delta event",
			expect: []byte(`{"delta":"code","item_id":"item_1","output_index":0,"sequence_number":28,"type":"response.code_interpreter_call_code.delta"}`),
			input: ResponseStreamEventUnion{
				OfCodeInterpreterCallCodeDelta: &ResponseCodeInterpreterCallCodeDeltaEvent{
					Delta:          "code",
					ItemID:         "item_1",
					OutputIndex:    0,
					SequenceNumber: 28,
					Type:           "response.code_interpreter_call_code.delta",
				},
			},
		},
		{
			name:   "code interpreter call code done event",
			expect: []byte(`{"code":"print('hello')","item_id":"item_1","output_index":0,"sequence_number":29,"type":"response.code_interpreter_call_code.done"}`),
			input: ResponseStreamEventUnion{
				OfCodeInterpreterCallCodeDone: &ResponseCodeInterpreterCallCodeDoneEvent{
					Code:           "print('hello')",
					ItemID:         "item_1",
					OutputIndex:    0,
					SequenceNumber: 29,
					Type:           "response.code_interpreter_call_code.done",
				},
			},
		},
		{
			name:   "code interpreter call in progress event",
			expect: []byte(`{"item_id":"item_1","output_index":0,"sequence_number":30,"type":"response.code_interpreter_call.in_progress"}`),
			input: ResponseStreamEventUnion{
				OfCodeInterpreterCallInprogress: &ResponseCodeInterpreterCallInProgressEvent{
					ItemID:         "item_1",
					OutputIndex:    0,
					SequenceNumber: 30,
					Type:           "response.code_interpreter_call.in_progress",
				},
			},
		},
		{
			name:   "code interpreter call interpreting event",
			expect: []byte(`{"item_id":"item_1","output_index":0,"sequence_number":31,"type":"response.code_interpreter_call.interpreting"}`),
			input: ResponseStreamEventUnion{
				OfCodeInterpreterCallInterpreting: &ResponseCodeInterpreterCallInterpretingEvent{
					ItemID:         "item_1",
					OutputIndex:    0,
					SequenceNumber: 31,
					Type:           "response.code_interpreter_call.interpreting",
				},
			},
		},
		{
			name:   "code interpreter call completed event",
			expect: []byte(`{"item_id":"item_1","output_index":0,"sequence_number":32,"type":"response.code_interpreter_call.completed"}`),
			input: ResponseStreamEventUnion{
				OfCodeInterpreterCallCompleted: &ResponseCodeInterpreterCallCompletedEvent{
					ItemID:         "item_1",
					OutputIndex:    0,
					SequenceNumber: 32,
					Type:           "response.code_interpreter_call.completed",
				},
			},
		},
		{
			name:   "response file search call searching event",
			expect: []byte(`{"item_id":"item_1","output_index":0,"sequence_number":33,"type":"response.file_search_call.searching"}`),
			input: ResponseStreamEventUnion{
				OfResponseFileSearchCallSearching: &ResponseFileSearchCallSearchingEvent{
					ItemID:         "item_1",
					OutputIndex:    0,
					SequenceNumber: 33,
					Type:           "response.file_search_call.searching",
				},
			},
		},
		{
			name:   "response file search call in progress event",
			expect: []byte(`{"item_id":"item_1","output_index":0,"sequence_number":34,"type":"response.file_search_call.in_progress"}`),
			input: ResponseStreamEventUnion{
				OfResponseFileSearchCallInProgress: &ResponseFileSearchCallInProgressEvent{
					ItemID:         "item_1",
					OutputIndex:    0,
					SequenceNumber: 34,
					Type:           "response.file_search_call.in_progress",
				},
			},
		},
		{
			name:   "response file search call completed event",
			expect: []byte(`{"item_id":"item_1","output_index":0,"sequence_number":35,"type":"response.file_search_call.completed"}`),
			input: ResponseStreamEventUnion{
				OfResponseFileSearchCallCompleted: &ResponseFileSearchCallCompletedEvent{
					ItemID:         "item_1",
					OutputIndex:    0,
					SequenceNumber: 35,
					Type:           "response.file_search_call.completed",
				},
			},
		},
		{
			name:   "response web search call searching event",
			expect: []byte(`{"item_id":"item_1","output_index":0,"sequence_number":36,"type":"response.web_search_call.searching"}`),
			input: ResponseStreamEventUnion{
				OfResponseWebSearchCallSearching: &ResponseWebSearchCallSearchingEvent{
					ItemID:         "item_1",
					OutputIndex:    0,
					SequenceNumber: 36,
					Type:           "response.web_search_call.searching",
				},
			},
		},
		{
			name:   "response web search call in progress event",
			expect: []byte(`{"item_id":"item_1","output_index":0,"sequence_number":37,"type":"response.web_search_call.in_progress"}`),
			input: ResponseStreamEventUnion{
				OfResponseWebSearchCallInProgress: &ResponseWebSearchCallInProgressEvent{
					ItemID:         "item_1",
					OutputIndex:    0,
					SequenceNumber: 37,
					Type:           "response.web_search_call.in_progress",
				},
			},
		},
		{
			name:   "response web search call completed event",
			expect: []byte(`{"item_id":"item_1","output_index":0,"sequence_number":38,"type":"response.web_search_call.completed"}`),
			input: ResponseStreamEventUnion{
				OfResponseWebSearchCallCompleted: &ResponseWebSearchCallCompletedEvent{
					ItemID:         "item_1",
					OutputIndex:    0,
					SequenceNumber: 38,
					Type:           "response.web_search_call.completed",
				},
			},
		},
		{
			name:   "response image gen call in progress event",
			expect: []byte(`{"item_id":"item_1","output_index":0,"sequence_number":39,"type":"response.image_generation_call.in_progress"}`),
			input: ResponseStreamEventUnion{
				OfResponseImageGenCallInProgress: &ResponseImageGenCallInProgressEvent{
					ItemID:         "item_1",
					OutputIndex:    0,
					SequenceNumber: 39,
					Type:           "response.image_generation_call.in_progress",
				},
			},
		},
		{
			name:   "response image gen call generating event",
			expect: []byte(`{"item_id":"item_1","output_index":0,"sequence_number":40,"type":"response.image_generation_call.generating"}`),
			input: ResponseStreamEventUnion{
				OfResponseImageGenCallGenerating: &ResponseImageGenCallGeneratingEvent{
					ItemID:         "item_1",
					OutputIndex:    0,
					SequenceNumber: 40,
					Type:           "response.image_generation_call.generating",
				},
			},
		},
		{
			name:   "response image gen call partial image event",
			expect: []byte(`{"item_id":"item_1","output_index":0,"partial_image_index":0,"partial_image_b64":"bas64encodedImage","sequence_number":41,"type":"response.image_generation_call.partial_image"}`),
			input: ResponseStreamEventUnion{
				OfResponseImageGenCallPartialImage: &ResponseImageGenCallPartialImageEvent{
					ItemID:            "item_1",
					OutputIndex:       0,
					SequenceNumber:    41,
					PartialImageIndex: 0,
					PartialImageB64:   "bas64encodedImage",
					Type:              "response.image_generation_call.partial_image",
				},
			},
		},
		{
			name:   "response image gen call completed event",
			expect: []byte(`{"item_id":"item_1","output_index":0,"sequence_number":42,"type":"response.image_generation_call.completed"}`),
			input: ResponseStreamEventUnion{
				OfResponseImageGenCallCompleted: &ResponseImageGenCallCompletedEvent{
					ItemID:         "item_1",
					OutputIndex:    0,
					SequenceNumber: 42,
					Type:           "response.image_generation_call.completed",
				},
			},
		},
		{
			name:   "response mcp call arguments delta event",
			expect: []byte(`{"delta":"{","item_id":"item_1","output_index":0,"sequence_number":43,"type":"response.mcp_call_arguments.delta"}`),
			input: ResponseStreamEventUnion{
				OfResponseMcpCallArgumentsDelta: &ResponseMcpCallArgumentsDeltaEvent{
					Delta:          "{",
					ItemID:         "item_1",
					OutputIndex:    0,
					SequenceNumber: 43,
					Type:           "response.mcp_call_arguments.delta",
				},
			},
		},
		{
			name:   "response mcp call arguments done event",
			expect: []byte(`{"arguments":"{}","item_id":"item_1","output_index":0,"sequence_number":44,"type":"response.mcp_call_arguments.done"}`),
			input: ResponseStreamEventUnion{
				OfResponseMcpCallArgumentsDone: &ResponseMcpCallArgumentsDoneEvent{
					Arguments:      "{}",
					ItemID:         "item_1",
					OutputIndex:    0,
					SequenceNumber: 44,
					Type:           "response.mcp_call_arguments.done",
				},
			},
		},
		{
			name:   "response mcp call in progress event",
			expect: []byte(`{"item_id":"item_1","output_index":0,"sequence_number":45,"type":"response.mcp_call.in_progress"}`),
			input: ResponseStreamEventUnion{
				OfResponseMcpCallInProgress: &ResponseMcpCallInProgressEvent{
					ItemID:         "item_1",
					OutputIndex:    0,
					SequenceNumber: 45,
					Type:           "response.mcp_call.in_progress",
				},
			},
		},
		{
			name:   "response mcp call completed event",
			expect: []byte(`{"item_id":"item_1","output_index":0,"sequence_number":46,"type":"response.mcp_call.completed"}`),
			input: ResponseStreamEventUnion{
				OfResponseMcpCallCompleted: &ResponseMcpCallCompletedEvent{
					ItemID:         "item_1",
					OutputIndex:    0,
					SequenceNumber: 46,
					Type:           "response.mcp_call.completed",
				},
			},
		},
		{
			name:   "response mcp call failed event",
			expect: []byte(`{"item_id":"item_1","output_index":0,"sequence_number":47,"type":"response.mcp_call.failed"}`),
			input: ResponseStreamEventUnion{
				OfResponseMcpCallFailed: &ResponseMcpCallFailedEvent{
					ItemID:         "item_1",
					OutputIndex:    0,
					SequenceNumber: 47,
					Type:           "response.mcp_call.failed",
				},
			},
		},
		{
			name:   "response mcp list tools in progress event",
			expect: []byte(`{"item_id":"item_1","output_index":0,"sequence_number":48,"type":"response.mcp_list_tools.in_progress"}`),
			input: ResponseStreamEventUnion{
				OfResponseMcpListToolsInProgress: &ResponseMcpListToolsInProgressEvent{
					ItemID:         "item_1",
					OutputIndex:    0,
					SequenceNumber: 48,
					Type:           "response.mcp_list_tools.in_progress",
				},
			},
		},
		{
			name:   "response mcp list tools completed event",
			expect: []byte(`{"item_id":"item_1","output_index":0,"sequence_number":49,"type":"response.mcp_list_tools.completed"}`),
			input: ResponseStreamEventUnion{
				OfResponseMcpListToolsCompleted: &ResponseMcpListToolsCompletedEvent{
					ItemID:         "item_1",
					OutputIndex:    0,
					SequenceNumber: 49,
					Type:           "response.mcp_list_tools.completed",
				},
			},
		},
		{
			name:   "response mcp list tools failed event",
			expect: []byte(`{"item_id":"item_1","output_index":0,"sequence_number":50,"type":"response.mcp_list_tools.failed"}`),
			input: ResponseStreamEventUnion{
				OfResponseMcpListToolsFailed: &ResponseMcpListToolsFailedEvent{
					ItemID:         "item_1",
					OutputIndex:    0,
					SequenceNumber: 50,
					Type:           "response.mcp_list_tools.failed",
				},
			},
		},
		{
			name:   "response output text annotation added event",
			expect: []byte(`{"annotation":{"type":"test"},"annotation_index":0,"content_index":0,"item_id":"item_1","output_index":0,"sequence_number":51,"type":"response.output_text.annotation.added"}`),
			input: ResponseStreamEventUnion{
				OfResponseOutputTextAnnotationAdded: &ResponseOutputTextAnnotationAddedEvent{
					Annotation:      map[string]any{"type": "test"},
					AnnotationIndex: 0,
					ContentIndex:    0,
					ItemID:          "item_1",
					OutputIndex:     0,
					SequenceNumber:  51,
					Type:            "response.output_text.annotation.added",
				},
			},
		},
		{
			name:   "response custom tool call input delta event",
			expect: []byte(`{"delta":"{","item_id":"item_1","output_index":0,"sequence_number":52,"type":"response.custom_tool_call_input.delta"}`),
			input: ResponseStreamEventUnion{
				OfResponseCustomToolCallInputDelta: &ResponseCustomToolCallInputDeltaEvent{
					Delta:          "{",
					ItemID:         "item_1",
					OutputIndex:    0,
					SequenceNumber: 52,
					Type:           "response.custom_tool_call_input.delta",
				},
			},
		},
		{
			name:   "response custom tool call input done event",
			expect: []byte(`{"input":"{}","item_id":"item_1","output_index":0,"sequence_number":53,"type":"response.custom_tool_call_input.done"}`),
			input: ResponseStreamEventUnion{
				OfResponseCustomToolCallInputDone: &ResponseCustomToolCallInputDoneEvent{
					Input:          "{}",
					ItemID:         "item_1",
					OutputIndex:    0,
					SequenceNumber: 53,
					Type:           "response.custom_tool_call_input.done",
				},
			},
		},
		{
			name:   "nil union",
			input:  ResponseStreamEventUnion{},
			expErr: "no response stream event to marshal",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			data, err := json.Marshal(tc.input)
			if tc.expErr != "" {
				require.ErrorContains(t, err, tc.expErr)
				return
			}
			require.NoError(t, err)
			require.JSONEq(t, string(tc.expect), string(data))
		})
	}
}

func TestResponseStreamEventUnionGetEventType(t *testing.T) {
	tests := []struct {
		name     string
		input    ResponseStreamEventUnion
		expected string
	}{
		{
			name: "audio delta event",
			input: ResponseStreamEventUnion{
				OfAudioDelta: &ResponseAudioDeltaEvent{
					Type: "response.audio.delta",
				},
			},
			expected: "response.audio.delta",
		},
		{
			name: "audio done event",
			input: ResponseStreamEventUnion{
				OfAudioDone: &ResponseAudioDoneEvent{
					Type: "response.audio.done",
				},
			},
			expected: "response.audio.done",
		},
		{
			name: "audio transcript delta event",
			input: ResponseStreamEventUnion{
				OfAudioTranscriptDelta: &ResponseAudioTranscriptDeltaEvent{
					Type: "response.audio.transcript.delta",
				},
			},
			expected: "response.audio.transcript.delta",
		},
		{
			name: "audio transcript done event",
			input: ResponseStreamEventUnion{
				OfAudioTranscriptDone: &ResponseAudioTranscriptDoneEvent{
					Type: "response.audio.transcript.done",
				},
			},
			expected: "response.audio.transcript.done",
		},
		{
			name: "code interpreter call code delta event",
			input: ResponseStreamEventUnion{
				OfCodeInterpreterCallCodeDelta: &ResponseCodeInterpreterCallCodeDeltaEvent{
					Type: "response.code_interpreter_call_code.delta",
				},
			},
			expected: "response.code_interpreter_call_code.delta",
		},
		{
			name: "code interpreter call code done event",
			input: ResponseStreamEventUnion{
				OfCodeInterpreterCallCodeDone: &ResponseCodeInterpreterCallCodeDoneEvent{
					Type: "response.code_interpreter_call_code.done",
				},
			},
			expected: "response.code_interpreter_call_code.done",
		},
		{
			name: "code interpreter call completed event",
			input: ResponseStreamEventUnion{
				OfCodeInterpreterCallCompleted: &ResponseCodeInterpreterCallCompletedEvent{
					Type: "response.code_interpreter_call.completed",
				},
			},
			expected: "response.code_interpreter_call.completed",
		},
		{
			name: "code interpreter call in progress event",
			input: ResponseStreamEventUnion{
				OfCodeInterpreterCallInprogress: &ResponseCodeInterpreterCallInProgressEvent{
					Type: "response.code_interpreter_call.in_progress",
				},
			},
			expected: "response.code_interpreter_call.in_progress",
		},
		{
			name: "code interpreter call interpreting event",
			input: ResponseStreamEventUnion{
				OfCodeInterpreterCallInterpreting: &ResponseCodeInterpreterCallInterpretingEvent{
					Type: "response.code_interpreter_call.interpreting",
				},
			},
			expected: "response.code_interpreter_call.interpreting",
		},
		{
			name: "response completed event",
			input: ResponseStreamEventUnion{
				OfResponseCompleted: &ResponseCompletedEvent{
					Type: "response.completed",
				},
			},
			expected: "response.completed",
		},
		{
			name: "response content part added event",
			input: ResponseStreamEventUnion{
				OfResponseContentPartAdded: &ResponseContentPartAddedEvent{
					Type: "response.content_part.added",
				},
			},
			expected: "response.content_part.added",
		},
		{
			name: "response content part done event",
			input: ResponseStreamEventUnion{
				OfResponseContentPartDone: &ResponseContentPartDoneEvent{
					Type: "response.content_part.done",
				},
			},
			expected: "response.content_part.done",
		},
		{
			name: "response created event",
			input: ResponseStreamEventUnion{
				OfResponseCreated: &ResponseCreatedEvent{
					Type: "response.created",
				},
			},
			expected: "response.created",
		},
		{
			name: "error event",
			input: ResponseStreamEventUnion{
				OfError: &ResponseErrorEvent{
					Type: "error",
				},
			},
			expected: "error",
		},
		{
			name: "response file search call completed event",
			input: ResponseStreamEventUnion{
				OfResponseFileSearchCallCompleted: &ResponseFileSearchCallCompletedEvent{
					Type: "response.file_search_call.completed",
				},
			},
			expected: "response.file_search_call.completed",
		},
		{
			name: "response file search call in progress event",
			input: ResponseStreamEventUnion{
				OfResponseFileSearchCallInProgress: &ResponseFileSearchCallInProgressEvent{
					Type: "response.file_search_call.in_progress",
				},
			},
			expected: "response.file_search_call.in_progress",
		},
		{
			name: "response file search call searching event",
			input: ResponseStreamEventUnion{
				OfResponseFileSearchCallSearching: &ResponseFileSearchCallSearchingEvent{
					Type: "response.file_search_call.searching",
				},
			},
			expected: "response.file_search_call.searching",
		},
		{
			name: "response function call arguments delta event",
			input: ResponseStreamEventUnion{
				OfResponseFunctionCallArgumentsDelta: &ResponseFunctionCallArgumentsDeltaEvent{
					Type: "response.function_call_arguments.delta",
				},
			},
			expected: "response.function_call_arguments.delta",
		},
		{
			name: "response function call arguments done event",
			input: ResponseStreamEventUnion{
				OfResponseFunctionCallArgumentsDone: &ResponseFunctionCallArgumentsDoneEvent{
					Type: "response.function_call_arguments.done",
				},
			},
			expected: "response.function_call_arguments.done",
		},
		{
			name: "response in progress event",
			input: ResponseStreamEventUnion{
				OfResponseInProgress: &ResponseInProgressEvent{
					Type: "response.in_progress",
				},
			},
			expected: "response.in_progress",
		},
		{
			name: "response failed event",
			input: ResponseStreamEventUnion{
				OfResponseFailed: &ResponseFailedEvent{
					Type: "response.failed",
				},
			},
			expected: "response.failed",
		},
		{
			name: "response incomplete event",
			input: ResponseStreamEventUnion{
				OfResponseIncomplete: &ResponseIncompleteEvent{
					Type: "response.incomplete",
				},
			},
			expected: "response.incomplete",
		},
		{
			name: "response output item added event",
			input: ResponseStreamEventUnion{
				OfResponseOutputItemAdded: &ResponseOutputItemAddedEvent{
					Type: "response.output_item.added",
				},
			},
			expected: "response.output_item.added",
		},
		{
			name: "response output item done event",
			input: ResponseStreamEventUnion{
				OfResponseOutputItemDone: &ResponseOutputItemDoneEvent{
					Type: "response.output_item.done",
				},
			},
			expected: "response.output_item.done",
		},
		{
			name: "response reasoning summary part added event",
			input: ResponseStreamEventUnion{
				OfResponseReasoningSummaryPartAdded: &ResponseReasoningSummaryPartAddedEvent{
					Type: "response.reasoning_summary_part.added",
				},
			},
			expected: "response.reasoning_summary_part.added",
		},
		{
			name: "response reasoning summary part done event",
			input: ResponseStreamEventUnion{
				OfResponseReasoningSummaryPartDone: &ResponseReasoningSummaryPartDoneEvent{
					Type: "response.reasoning_summary_part.done",
				},
			},
			expected: "response.reasoning_summary_part.done",
		},
		{
			name: "response reasoning summary text delta event",
			input: ResponseStreamEventUnion{
				OfResponseReasoningSummaryTextDelta: &ResponseReasoningSummaryTextDeltaEvent{
					Type: "response.reasoning_summary_text.delta",
				},
			},
			expected: "response.reasoning_summary_text.delta",
		},
		{
			name: "response reasoning summary text done event",
			input: ResponseStreamEventUnion{
				OfResponseReasoningSummaryTextDone: &ResponseReasoningSummaryTextDoneEvent{
					Type: "response.reasoning_summary_text.done",
				},
			},
			expected: "response.reasoning_summary_text.done",
		},
		{
			name: "response reasoning text delta event",
			input: ResponseStreamEventUnion{
				OfResponseReasoningTextDelta: &ResponseReasoningTextDeltaEvent{
					Type: "response.reasoning_text.delta",
				},
			},
			expected: "response.reasoning_text.delta",
		},
		{
			name: "response reasoning text done event",
			input: ResponseStreamEventUnion{
				OfResponseReasoningTextDone: &ResponseReasoningTextDoneEvent{
					Type: "response.reasoning_text.done",
				},
			},
			expected: "response.reasoning_text.done",
		},
		{
			name: "response refusal delta event",
			input: ResponseStreamEventUnion{
				OfResponseRefusalDelta: &ResponseRefusalDeltaEvent{
					Type: "response.refusal.delta",
				},
			},
			expected: "response.refusal.delta",
		},
		{
			name: "response refusal done event",
			input: ResponseStreamEventUnion{
				OfResponseRefusalDone: &ResponseRefusalDoneEvent{
					Type: "response.refusal.done",
				},
			},
			expected: "response.refusal.done",
		},
		{
			name: "response text delta event",
			input: ResponseStreamEventUnion{
				OfResponseTextDelta: &ResponseTextDeltaEvent{
					Type: "response.output_text.delta",
				},
			},
			expected: "response.output_text.delta",
		},
		{
			name: "response text done event",
			input: ResponseStreamEventUnion{
				OfResponseTextDone: &ResponseTextDoneEvent{
					Type: "response.output_text.done",
				},
			},
			expected: "response.output_text.done",
		},
		{
			name: "response web search call completed event",
			input: ResponseStreamEventUnion{
				OfResponseWebSearchCallCompleted: &ResponseWebSearchCallCompletedEvent{
					Type: "response.web_search_call.completed",
				},
			},
			expected: "response.web_search_call.completed",
		},
		{
			name: "response web search call in progress event",
			input: ResponseStreamEventUnion{
				OfResponseWebSearchCallInProgress: &ResponseWebSearchCallInProgressEvent{
					Type: "response.web_search_call.in_progress",
				},
			},
			expected: "response.web_search_call.in_progress",
		},
		{
			name: "response web search call searching event",
			input: ResponseStreamEventUnion{
				OfResponseWebSearchCallSearching: &ResponseWebSearchCallSearchingEvent{
					Type: "response.web_search_call.searching",
				},
			},
			expected: "response.web_search_call.searching",
		},
		{
			name: "response image gen call completed event",
			input: ResponseStreamEventUnion{
				OfResponseImageGenCallCompleted: &ResponseImageGenCallCompletedEvent{
					Type: "response.image_generation_call.completed",
				},
			},
			expected: "response.image_generation_call.completed",
		},
		{
			name: "response image gen call generating event",
			input: ResponseStreamEventUnion{
				OfResponseImageGenCallGenerating: &ResponseImageGenCallGeneratingEvent{
					Type: "response.image_generation_call.generating",
				},
			},
			expected: "response.image_generation_call.generating",
		},
		{
			name: "response image gen call in progress event",
			input: ResponseStreamEventUnion{
				OfResponseImageGenCallInProgress: &ResponseImageGenCallInProgressEvent{
					Type: "response.image_generation_call.in_progress",
				},
			},
			expected: "response.image_generation_call.in_progress",
		},
		{
			name: "response image gen call partial image event",
			input: ResponseStreamEventUnion{
				OfResponseImageGenCallPartialImage: &ResponseImageGenCallPartialImageEvent{
					Type: "response.image_generation_call.partial_image",
				},
			},
			expected: "response.image_generation_call.partial_image",
		},
		{
			name: "response mcp call arguments delta event",
			input: ResponseStreamEventUnion{
				OfResponseMcpCallArgumentsDelta: &ResponseMcpCallArgumentsDeltaEvent{
					Type: "response.mcp_call_arguments.delta",
				},
			},
			expected: "response.mcp_call_arguments.delta",
		},
		{
			name: "response mcp call arguments done event",
			input: ResponseStreamEventUnion{
				OfResponseMcpCallArgumentsDone: &ResponseMcpCallArgumentsDoneEvent{
					Type: "response.mcp_call_arguments.done",
				},
			},
			expected: "response.mcp_call_arguments.done",
		},
		{
			name: "response mcp call completed event",
			input: ResponseStreamEventUnion{
				OfResponseMcpCallCompleted: &ResponseMcpCallCompletedEvent{
					Type: "response.mcp_call.completed",
				},
			},
			expected: "response.mcp_call.completed",
		},
		{
			name: "response mcp call failed event",
			input: ResponseStreamEventUnion{
				OfResponseMcpCallFailed: &ResponseMcpCallFailedEvent{
					Type: "response.mcp_call.failed",
				},
			},
			expected: "response.mcp_call.failed",
		},
		{
			name: "response mcp call in progress event",
			input: ResponseStreamEventUnion{
				OfResponseMcpCallInProgress: &ResponseMcpCallInProgressEvent{
					Type: "response.mcp_call.in_progress",
				},
			},
			expected: "response.mcp_call.in_progress",
		},
		{
			name: "response mcp list tools completed event",
			input: ResponseStreamEventUnion{
				OfResponseMcpListToolsCompleted: &ResponseMcpListToolsCompletedEvent{
					Type: "response.mcp_list_tools.completed",
				},
			},
			expected: "response.mcp_list_tools.completed",
		},
		{
			name: "response mcp list tools failed event",
			input: ResponseStreamEventUnion{
				OfResponseMcpListToolsFailed: &ResponseMcpListToolsFailedEvent{
					Type: "response.mcp_list_tools.failed",
				},
			},
			expected: "response.mcp_list_tools.failed",
		},
		{
			name: "response mcp list tools in progress event",
			input: ResponseStreamEventUnion{
				OfResponseMcpListToolsInProgress: &ResponseMcpListToolsInProgressEvent{
					Type: "response.mcp_list_tools.in_progress",
				},
			},
			expected: "response.mcp_list_tools.in_progress",
		},
		{
			name: "response output text annotation added event",
			input: ResponseStreamEventUnion{
				OfResponseOutputTextAnnotationAdded: &ResponseOutputTextAnnotationAddedEvent{
					Type: "response.output_text.annotation.added",
				},
			},
			expected: "response.output_text.annotation.added",
		},
		{
			name: "response queued event",
			input: ResponseStreamEventUnion{
				OfResponseQueued: &ResponseQueuedEvent{
					Type: "response.queued",
				},
			},
			expected: "response.queued",
		},
		{
			name: "response custom tool call input delta event",
			input: ResponseStreamEventUnion{
				OfResponseCustomToolCallInputDelta: &ResponseCustomToolCallInputDeltaEvent{
					Type: "response.custom_tool_call_input.delta",
				},
			},
			expected: "response.custom_tool_call_input.delta",
		},
		{
			name: "response custom tool call input done event",
			input: ResponseStreamEventUnion{
				OfResponseCustomToolCallInputDone: &ResponseCustomToolCallInputDoneEvent{
					Type: "response.custom_tool_call_input.done",
				},
			},
			expected: "response.custom_tool_call_input.done",
		},
		{
			name:     "nil union",
			input:    ResponseStreamEventUnion{},
			expected: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := tc.input.GetEventType()
			require.Equal(t, tc.expected, result)
		})
	}
}

func TestResponseCodeInterpreterToolCallOutputUnionMarshalJSON(t *testing.T) {
	tests := []struct {
		name   string
		expect []byte
		input  ResponseCodeInterpreterToolCallOutputUnion
		expErr string
	}{
		{
			name:   "logs output",
			expect: []byte(`{"logs":"output from code execution","type":"logs"}`),
			input: ResponseCodeInterpreterToolCallOutputUnion{
				OfResponseCodeInterpreterToolCallOutputLogs: &ResponseCodeInterpreterToolCallOutputLogs{
					Logs: "output from code execution",
					Type: "logs",
				},
			},
		},
		{
			name:   "logs output with empty value",
			expect: []byte(`{"logs":"","type":"logs"}`),
			input: ResponseCodeInterpreterToolCallOutputUnion{
				OfResponseCodeInterpreterToolCallOutputLogs: &ResponseCodeInterpreterToolCallOutputLogs{
					Logs: "",
					Type: "logs",
				},
			},
		},
		{
			name:   "logs output with special characters",
			expect: []byte(`{"logs":"error: \"file not found\"\nstack trace:\nline 1","type":"logs"}`),
			input: ResponseCodeInterpreterToolCallOutputUnion{
				OfResponseCodeInterpreterToolCallOutputLogs: &ResponseCodeInterpreterToolCallOutputLogs{
					Logs: "error: \"file not found\"\nstack trace:\nline 1",
					Type: "logs",
				},
			},
		},
		{
			name:   "logs output with multiline content",
			expect: []byte(`{"logs":"line1\nline2\nline3","type":"logs"}`),
			input: ResponseCodeInterpreterToolCallOutputUnion{
				OfResponseCodeInterpreterToolCallOutputLogs: &ResponseCodeInterpreterToolCallOutputLogs{
					Logs: "line1\nline2\nline3",
					Type: "logs",
				},
			},
		},
		{
			name:   "image output",
			expect: []byte(`{"type":"image","url":"https://example.com/image.png"}`),
			input: ResponseCodeInterpreterToolCallOutputUnion{
				OfResponseCodeInterpreterToolCallOutputImage: &ResponseCodeInterpreterToolCallOutputImage{
					Type: "image",
					URL:  "https://example.com/image.png",
				},
			},
		},
		{
			name:   "image output with data url",
			expect: []byte(`{"type":"image","url":"data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg=="}`),
			input: ResponseCodeInterpreterToolCallOutputUnion{
				OfResponseCodeInterpreterToolCallOutputImage: &ResponseCodeInterpreterToolCallOutputImage{
					Type: "image",
					URL:  "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg==",
				},
			},
		},
		{
			name:   "nil union",
			input:  ResponseCodeInterpreterToolCallOutputUnion{},
			expErr: "no output to marshal in code interpreter tool call output",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			data, err := json.Marshal(tc.input)
			if tc.expErr != "" {
				require.ErrorContains(t, err, tc.expErr)
				return
			}
			require.NoError(t, err)
			require.JSONEq(t, string(tc.expect), string(data))
		})
	}
}

func TestResponseCodeInterpreterToolCallOutputUnionUnmarshalJSON(t *testing.T) {
	tests := []struct {
		name   string
		input  []byte
		expect ResponseCodeInterpreterToolCallOutputUnion
		expErr string
	}{
		{
			name:  "logs output",
			input: []byte(`{"logs":"output from code execution","type":"logs"}`),
			expect: ResponseCodeInterpreterToolCallOutputUnion{
				OfResponseCodeInterpreterToolCallOutputLogs: &ResponseCodeInterpreterToolCallOutputLogs{
					Logs: "output from code execution",
					Type: "logs",
				},
			},
		},
		{
			name:  "logs output with empty value",
			input: []byte(`{"logs":"","type":"logs"}`),
			expect: ResponseCodeInterpreterToolCallOutputUnion{
				OfResponseCodeInterpreterToolCallOutputLogs: &ResponseCodeInterpreterToolCallOutputLogs{
					Logs: "",
					Type: "logs",
				},
			},
		},
		{
			name:  "logs output with special characters",
			input: []byte(`{"logs":"error: \"file not found\"\nstack trace:\nline 1","type":"logs"}`),
			expect: ResponseCodeInterpreterToolCallOutputUnion{
				OfResponseCodeInterpreterToolCallOutputLogs: &ResponseCodeInterpreterToolCallOutputLogs{
					Logs: "error: \"file not found\"\nstack trace:\nline 1",
					Type: "logs",
				},
			},
		},
		{
			name:  "logs output with multiline content",
			input: []byte(`{"logs":"line1\nline2\nline3","type":"logs"}`),
			expect: ResponseCodeInterpreterToolCallOutputUnion{
				OfResponseCodeInterpreterToolCallOutputLogs: &ResponseCodeInterpreterToolCallOutputLogs{
					Logs: "line1\nline2\nline3",
					Type: "logs",
				},
			},
		},
		{
			name:  "image output",
			input: []byte(`{"type":"image","url":"https://example.com/image.png"}`),
			expect: ResponseCodeInterpreterToolCallOutputUnion{
				OfResponseCodeInterpreterToolCallOutputImage: &ResponseCodeInterpreterToolCallOutputImage{
					Type: "image",
					URL:  "https://example.com/image.png",
				},
			},
		},
		{
			name:  "image output with data url",
			input: []byte(`{"type":"image","url":"data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg=="}`),
			expect: ResponseCodeInterpreterToolCallOutputUnion{
				OfResponseCodeInterpreterToolCallOutputImage: &ResponseCodeInterpreterToolCallOutputImage{
					Type: "image",
					URL:  "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg==",
				},
			},
		},
		{
			name:   "empty type",
			input:  []byte(`{"type": ""}`),
			expErr: "unknown type for code interpreter tool call output: ",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var result ResponseCodeInterpreterToolCallOutputUnion
			err := json.Unmarshal(tc.input, &result)
			if tc.expErr != "" {
				require.ErrorContains(t, err, tc.expErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.expect, result)
		})
	}
}

func TestResponseContentPartAddedEventPartUnionMarshalJSON(t *testing.T) {
	tests := []struct {
		name   string
		expect []byte
		input  ResponseContentPartAddedEventPartUnion
		expErr string
	}{
		{
			name:   "response output text part",
			expect: []byte(`{"text":"Hello World!","type":"output_text"}`),
			input: ResponseContentPartAddedEventPartUnion{
				OfResponseOutputText: &ResponseOutputTextParam{
					Text: "Hello World!",
					Type: "output_text",
				},
			},
		},
		{
			name:   "response output text part with annotations",
			expect: []byte(`{"text":"Test content","type":"output_text","annotations":[{"type":"file_citation","file_id":"file_123","index":0,"filename":"test.txt"}]}`),
			input: ResponseContentPartAddedEventPartUnion{
				OfResponseOutputText: &ResponseOutputTextParam{
					Text: "Test content",
					Type: "output_text",
					Annotations: []ResponseOutputTextAnnotationUnionParam{
						{
							OfFileCitation: &ResponseOutputTextAnnotationFileCitationParam{
								Type:     "file_citation",
								FileID:   "file_123",
								Index:    0,
								Filename: "test.txt",
							},
						},
					},
				},
			},
		},
		{
			name:   "response output text part with logprobs",
			expect: []byte(`{"text":"token","type":"output_text","logprobs":[{"token":"a","logprob":0.5}]}`),
			input: ResponseContentPartAddedEventPartUnion{
				OfResponseOutputText: &ResponseOutputTextParam{
					Text: "token",
					Type: "output_text",
					Logprobs: []ResponseOutputTextLogprobParam{
						{
							Token:   "a",
							Logprob: 0.5,
						},
					},
				},
			},
		},
		{
			name:   "response output refusal part",
			expect: []byte(`{"refusal":"I can't do that","type":"refusal"}`),
			input: ResponseContentPartAddedEventPartUnion{
				OfResponseOutputRefusal: &ResponseOutputRefusalParam{
					Refusal: "I can't do that",
					Type:    "refusal",
				},
			},
		},
		{
			name:   "response reasoning text part",
			expect: []byte(`{"text":"thinking about this...","type":"reasoning_text"}`),
			input: ResponseContentPartAddedEventPartUnion{
				OfResponsContentPartAddedEventPartReasoningText: &ResponseContentPartAddedEventPartReasoningText{
					Text: "thinking about this...",
					Type: "reasoning_text",
				},
			},
		},
		{
			name:   "response output text with empty string",
			expect: []byte(`{"text":"","type":"output_text"}`),
			input: ResponseContentPartAddedEventPartUnion{
				OfResponseOutputText: &ResponseOutputTextParam{
					Text: "",
					Type: "output_text",
				},
			},
		},
		{
			name:   "response reasoning text with empty string",
			expect: []byte(`{"text":"","type":"reasoning_text"}`),
			input: ResponseContentPartAddedEventPartUnion{
				OfResponsContentPartAddedEventPartReasoningText: &ResponseContentPartAddedEventPartReasoningText{
					Text: "",
					Type: "reasoning_text",
				},
			},
		},
		{
			name:   "response refusal with empty string",
			expect: []byte(`{"refusal":"","type":"refusal"}`),
			input: ResponseContentPartAddedEventPartUnion{
				OfResponseOutputRefusal: &ResponseOutputRefusalParam{
					Refusal: "",
					Type:    "refusal",
				},
			},
		},
		{
			name:   "nil union",
			input:  ResponseContentPartAddedEventPartUnion{},
			expErr: "no part to marshal in content part added event",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			data, err := json.Marshal(tc.input)
			if tc.expErr != "" {
				require.ErrorContains(t, err, tc.expErr)
				return
			}
			require.NoError(t, err)
			require.JSONEq(t, string(tc.expect), string(data))
		})
	}
}

func TestResponseContentPartAddedEventPartUnionUnmarshalJSON(t *testing.T) {
	tests := []struct {
		name   string
		input  []byte
		expect ResponseContentPartAddedEventPartUnion
		expErr string
	}{
		{
			name:  "response output text part",
			input: []byte(`{"text":"Hello World!","type":"output_text"}`),
			expect: ResponseContentPartAddedEventPartUnion{
				OfResponseOutputText: &ResponseOutputTextParam{
					Text: "Hello World!",
					Type: "output_text",
				},
			},
		},
		{
			name:  "response output text part with annotations",
			input: []byte(`{"text":"Test content","type":"output_text","annotations":[{"type":"file_citation","file_id":"file_123","index":0,"filename":"test.txt"}]}`),
			expect: ResponseContentPartAddedEventPartUnion{
				OfResponseOutputText: &ResponseOutputTextParam{
					Text: "Test content",
					Type: "output_text",
					Annotations: []ResponseOutputTextAnnotationUnionParam{
						{
							OfFileCitation: &ResponseOutputTextAnnotationFileCitationParam{
								Type:     "file_citation",
								FileID:   "file_123",
								Index:    0,
								Filename: "test.txt",
							},
						},
					},
				},
			},
		},
		{
			name:  "response output text part with logprobs",
			input: []byte(`{"text":"token","type":"output_text","logprobs":[{"token":"a","logprob":0.5}]}`),
			expect: ResponseContentPartAddedEventPartUnion{
				OfResponseOutputText: &ResponseOutputTextParam{
					Text: "token",
					Type: "output_text",
					Logprobs: []ResponseOutputTextLogprobParam{
						{
							Token:   "a",
							Logprob: 0.5,
						},
					},
				},
			},
		},
		{
			name:  "response output refusal part",
			input: []byte(`{"refusal":"I can't do that","type":"refusal"}`),
			expect: ResponseContentPartAddedEventPartUnion{
				OfResponseOutputRefusal: &ResponseOutputRefusalParam{
					Refusal: "I can't do that",
					Type:    "refusal",
				},
			},
		},
		{
			name:  "response reasoning text part",
			input: []byte(`{"text":"thinking about this...","type":"reasoning_text"}`),
			expect: ResponseContentPartAddedEventPartUnion{
				OfResponsContentPartAddedEventPartReasoningText: &ResponseContentPartAddedEventPartReasoningText{
					Text: "thinking about this...",
					Type: "reasoning_text",
				},
			},
		},
		{
			name:  "response output text with empty string",
			input: []byte(`{"text":"","type":"output_text"}`),
			expect: ResponseContentPartAddedEventPartUnion{
				OfResponseOutputText: &ResponseOutputTextParam{
					Text: "",
					Type: "output_text",
				},
			},
		},
		{
			name:  "response reasoning text with empty string",
			input: []byte(`{"text":"","type":"reasoning_text"}`),
			expect: ResponseContentPartAddedEventPartUnion{
				OfResponsContentPartAddedEventPartReasoningText: &ResponseContentPartAddedEventPartReasoningText{
					Text: "",
					Type: "reasoning_text",
				},
			},
		},
		{
			name:  "response refusal with empty string",
			input: []byte(`{"refusal":"","type":"refusal"}`),
			expect: ResponseContentPartAddedEventPartUnion{
				OfResponseOutputRefusal: &ResponseOutputRefusalParam{
					Refusal: "",
					Type:    "refusal",
				},
			},
		},
		{
			name:   "empty type",
			input:  []byte(`{"type": ""}`),
			expErr: "unknown type for content part added event part: ",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var result ResponseContentPartAddedEventPartUnion
			err := json.Unmarshal(tc.input, &result)
			if tc.expErr != "" {
				require.ErrorContains(t, err, tc.expErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.expect, result)
		})
	}
}

func TestResponseContentPartDoneEventPartUnionMarshalJSON(t *testing.T) {
	tests := []struct {
		name   string
		expect []byte
		input  ResponseContentPartDoneEventPartUnion
		expErr string
	}{
		{
			name:   "response output text part",
			expect: []byte(`{"text":"Hello World!","type":"output_text"}`),
			input: ResponseContentPartDoneEventPartUnion{
				OfResponseOutputText: &ResponseOutputTextParam{
					Text: "Hello World!",
					Type: "output_text",
				},
			},
		},
		{
			name:   "response output text part with empty string",
			expect: []byte(`{"text":"","type":"output_text"}`),
			input: ResponseContentPartDoneEventPartUnion{
				OfResponseOutputText: &ResponseOutputTextParam{
					Text: "",
					Type: "output_text",
				},
			},
		},
		{
			name:   "response output refusal part",
			expect: []byte(`{"refusal":"I can't do that","type":"refusal"}`),
			input: ResponseContentPartDoneEventPartUnion{
				OfResponseOutputRefusal: &ResponseOutputRefusalParam{
					Refusal: "I can't do that",
					Type:    "refusal",
				},
			},
		},
		{
			name:   "response output refusal part with empty string",
			expect: []byte(`{"refusal":"","type":"refusal"}`),
			input: ResponseContentPartDoneEventPartUnion{
				OfResponseOutputRefusal: &ResponseOutputRefusalParam{
					Refusal: "",
					Type:    "refusal",
				},
			},
		},
		{
			name:   "response reasoning text part",
			expect: []byte(`{"text":"thinking about this...","type":"reasoning_text"}`),
			input: ResponseContentPartDoneEventPartUnion{
				OfResponsContentPartDoneEventPartReasoningText: &ResponseContentPartDoneEventPartReasoningText{
					Text: "thinking about this...",
					Type: "reasoning_text",
				},
			},
		},
		{
			name:   "response reasoning text with empty string",
			expect: []byte(`{"text":"","type":"reasoning_text"}`),
			input: ResponseContentPartDoneEventPartUnion{
				OfResponsContentPartDoneEventPartReasoningText: &ResponseContentPartDoneEventPartReasoningText{
					Text: "",
					Type: "reasoning_text",
				},
			},
		},
		{
			name:   "response output text with special characters",
			expect: []byte(`{"text":"Line 1\nLine 2\nLine 3","type":"output_text"}`),
			input: ResponseContentPartDoneEventPartUnion{
				OfResponseOutputText: &ResponseOutputTextParam{
					Text: "Line 1\nLine 2\nLine 3",
					Type: "output_text",
				},
			},
		},
		{
			name:   "response reasoning text with special characters",
			expect: []byte(`{"text":"error: \"test\"\nstack trace","type":"reasoning_text"}`),
			input: ResponseContentPartDoneEventPartUnion{
				OfResponsContentPartDoneEventPartReasoningText: &ResponseContentPartDoneEventPartReasoningText{
					Text: "error: \"test\"\nstack trace",
					Type: "reasoning_text",
				},
			},
		},
		{
			name:   "response output text with annotations",
			expect: []byte(`{"text":"This is a very long text content that contains multiple sentences and should be properly marshaled into JSON format without any issues or truncation.","type":"output_text","annotations":[{"type":"file_citation","file_id":"file_123","index":0,"filename":"test.txt"}]}`),
			input: ResponseContentPartDoneEventPartUnion{
				OfResponseOutputText: &ResponseOutputTextParam{
					Text: "This is a very long text content that contains multiple sentences and should be properly marshaled into JSON format without any issues or truncation.",
					Type: "output_text",
					Annotations: []ResponseOutputTextAnnotationUnionParam{
						{
							OfFileCitation: &ResponseOutputTextAnnotationFileCitationParam{
								Type:     "file_citation",
								FileID:   "file_123",
								Index:    0,
								Filename: "test.txt",
							},
						},
					},
				},
			},
		},
		{
			name:   "nil union",
			input:  ResponseContentPartDoneEventPartUnion{},
			expErr: "no part to marshal in content part done event",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			data, err := json.Marshal(tc.input)
			if tc.expErr != "" {
				require.ErrorContains(t, err, tc.expErr)
				return
			}
			require.NoError(t, err)
			require.JSONEq(t, string(tc.expect), string(data))
		})
	}
}

func TestResponseContentPartDoneEventPartUnionUnmarshalJSON(t *testing.T) {
	tests := []struct {
		name   string
		input  []byte
		expect ResponseContentPartDoneEventPartUnion
		expErr string
	}{
		{
			name:  "response output text part",
			input: []byte(`{"text":"Hello World!","type":"output_text"}`),
			expect: ResponseContentPartDoneEventPartUnion{
				OfResponseOutputText: &ResponseOutputTextParam{
					Text: "Hello World!",
					Type: "output_text",
				},
			},
		},
		{
			name:  "response output text part with empty string",
			input: []byte(`{"text":"","type":"output_text"}`),
			expect: ResponseContentPartDoneEventPartUnion{
				OfResponseOutputText: &ResponseOutputTextParam{
					Text: "",
					Type: "output_text",
				},
			},
		},
		{
			name:  "response output refusal part",
			input: []byte(`{"refusal":"I can't do that","type":"refusal"}`),
			expect: ResponseContentPartDoneEventPartUnion{
				OfResponseOutputRefusal: &ResponseOutputRefusalParam{
					Refusal: "I can't do that",
					Type:    "refusal",
				},
			},
		},
		{
			name:  "response output refusal part with empty string",
			input: []byte(`{"refusal":"","type":"refusal"}`),
			expect: ResponseContentPartDoneEventPartUnion{
				OfResponseOutputRefusal: &ResponseOutputRefusalParam{
					Refusal: "",
					Type:    "refusal",
				},
			},
		},
		{
			name:  "response reasoning text part",
			input: []byte(`{"text":"thinking about this...","type":"reasoning_text"}`),
			expect: ResponseContentPartDoneEventPartUnion{
				OfResponsContentPartDoneEventPartReasoningText: &ResponseContentPartDoneEventPartReasoningText{
					Text: "thinking about this...",
					Type: "reasoning_text",
				},
			},
		},
		{
			name:  "response reasoning text with empty string",
			input: []byte(`{"text":"","type":"reasoning_text"}`),
			expect: ResponseContentPartDoneEventPartUnion{
				OfResponsContentPartDoneEventPartReasoningText: &ResponseContentPartDoneEventPartReasoningText{
					Text: "",
					Type: "reasoning_text",
				},
			},
		},
		{
			name:  "response output text with special characters",
			input: []byte(`{"text":"Line 1\nLine 2\nLine 3","type":"output_text"}`),
			expect: ResponseContentPartDoneEventPartUnion{
				OfResponseOutputText: &ResponseOutputTextParam{
					Text: "Line 1\nLine 2\nLine 3",
					Type: "output_text",
				},
			},
		},
		{
			name:  "response reasoning text with special characters",
			input: []byte(`{"text":"error: \"test\"\nstack trace","type":"reasoning_text"}`),
			expect: ResponseContentPartDoneEventPartUnion{
				OfResponsContentPartDoneEventPartReasoningText: &ResponseContentPartDoneEventPartReasoningText{
					Text: "error: \"test\"\nstack trace",
					Type: "reasoning_text",
				},
			},
		},
		{
			name:  "response output text with annotations",
			input: []byte(`{"text":"This is a very long text content that contains multiple sentences and should be properly marshaled into JSON format without any issues or truncation.","type":"output_text","annotations":[{"type":"file_citation","file_id":"file_123","index":0,"filename":"test.txt"}]}`),
			expect: ResponseContentPartDoneEventPartUnion{
				OfResponseOutputText: &ResponseOutputTextParam{
					Text: "This is a very long text content that contains multiple sentences and should be properly marshaled into JSON format without any issues or truncation.",
					Type: "output_text",
					Annotations: []ResponseOutputTextAnnotationUnionParam{
						{
							OfFileCitation: &ResponseOutputTextAnnotationFileCitationParam{
								Type:     "file_citation",
								FileID:   "file_123",
								Index:    0,
								Filename: "test.txt",
							},
						},
					},
				},
			},
		},
		{
			name:   "empty type",
			input:  []byte(`{"type": ""}`),
			expErr: "unknown type for content part done event part: ",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var result ResponseContentPartDoneEventPartUnion
			err := json.Unmarshal(tc.input, &result)
			if tc.expErr != "" {
				require.ErrorContains(t, err, tc.expErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.expect, result)
		})
	}
}
