// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package endpointspec

import (
	"bytes"
	"errors"
	"mime/multipart"
	"testing"

	"github.com/stretchr/testify/require"
	"k8s.io/utils/ptr"

	cohereschema "github.com/envoyproxy/ai-gateway/internal/apischema/cohere"
	"github.com/envoyproxy/ai-gateway/internal/apischema/openai"
	"github.com/envoyproxy/ai-gateway/internal/apischema/openai/tokenize"
	"github.com/envoyproxy/ai-gateway/internal/filterapi"
	"github.com/envoyproxy/ai-gateway/internal/internalapi"
	"github.com/envoyproxy/ai-gateway/internal/json"
	"github.com/envoyproxy/ai-gateway/internal/redaction"
)

func TestChatCompletionsEndpointSpec_ParseBody(t *testing.T) {
	spec := ChatCompletionsEndpointSpec{}

	t.Run("invalid json", func(t *testing.T) {
		_, _, _, _, err := spec.ParseBody([]byte("not-json"), false)
		require.ErrorContains(t, err, "malformed request")
	})

	t.Run("streaming_without_include_usage", func(t *testing.T) {
		req := openai.ChatCompletionRequest{Model: "gpt-4o", Stream: true}
		body, err := json.Marshal(req)
		require.NoError(t, err)

		model, parsed, stream, mutated, err := spec.ParseBody(body, true)
		require.NoError(t, err)
		require.Equal(t, "gpt-4o", model)
		require.True(t, stream)
		require.NotNil(t, parsed)
		require.NotNil(t, parsed.StreamOptions)
		require.True(t, parsed.StreamOptions.IncludeUsage)
		require.NotNil(t, mutated)

		var mutatedReq openai.ChatCompletionRequest
		require.NoError(t, json.Unmarshal(mutated, &mutatedReq))
		require.NotNil(t, mutatedReq.StreamOptions)
		require.True(t, mutatedReq.StreamOptions.IncludeUsage)
	})

	t.Run("streaming_with_include_usage_already_true", func(t *testing.T) {
		req := openai.ChatCompletionRequest{
			Model:         "gpt-4.1",
			Stream:        true,
			StreamOptions: &openai.StreamOptions{IncludeUsage: true},
		}
		body, err := json.Marshal(req)
		require.NoError(t, err)

		_, parsed, _, mutated, err := spec.ParseBody(body, false)
		require.NoError(t, err)
		require.NotNil(t, parsed)
		require.True(t, parsed.StreamOptions.IncludeUsage)
		require.Nil(t, mutated)
	})

	t.Run("non_streaming", func(t *testing.T) {
		req := openai.ChatCompletionRequest{Model: "gpt-4-mini", Stream: false}
		body, err := json.Marshal(req)
		require.NoError(t, err)

		model, parsed, stream, mutated, err := spec.ParseBody(body, false)
		require.NoError(t, err)
		require.Equal(t, "gpt-4-mini", model)
		require.False(t, stream)
		require.NotNil(t, parsed)
		require.Nil(t, mutated)
	})
}

func TestChatCompletionsEndpointSpec_GetTranslator(t *testing.T) {
	spec := ChatCompletionsEndpointSpec{}
	supported := []filterapi.VersionedAPISchema{
		{Name: filterapi.APISchemaOpenAI, Prefix: "v1"},
		{Name: filterapi.APISchemaAWSOpenAI},
		{Name: filterapi.APISchemaAWSBedrock},
		{Name: filterapi.APISchemaAWSAnthropic},
		{Name: filterapi.APISchemaAzureOpenAI, Version: "2024-02-01"},
		{Name: filterapi.APISchemaGCPVertexAI},
		{Name: filterapi.APISchemaGCPAnthropic, Version: "2024-05-01"},
	}

	for _, schema := range supported {
		s := schema
		t.Run("supported_"+string(s.Name), func(t *testing.T) {
			t.Parallel()
			translator, err := spec.GetTranslator(s, "override")
			require.NoError(t, err)
			require.NotNil(t, translator)
		})
	}

	t.Run("unsupported", func(t *testing.T) {
		_, err := spec.GetTranslator(filterapi.VersionedAPISchema{Name: "Unknown"}, "override")
		require.ErrorContains(t, err, "unsupported API schema")
	})

	t.Run("AWSOpenAI request", func(t *testing.T) {
		awsTranslator, err := spec.GetTranslator(filterapi.VersionedAPISchema{Name: filterapi.APISchemaAWSOpenAI}, "")
		require.NoError(t, err)
		original := []byte(`{"model":"us.openai.gpt-5.6-luna","messages":[]}`)
		headers, body, err := awsTranslator.RequestBody(original, &openai.ChatCompletionRequest{Model: "us.openai.gpt-5.6-luna"}, false)
		require.NoError(t, err)
		require.Equal(t, internalapi.Header{":path", "/openai/v1/chat/completions"}, headers[0])
		require.Equal(t, original, body)
	})
}

func TestCompletionsEndpointSpec_ParseBody(t *testing.T) {
	spec := CompletionsEndpointSpec{}

	t.Run("invalid json", func(t *testing.T) {
		_, _, _, _, err := spec.ParseBody([]byte("{bad"), false)
		require.ErrorContains(t, err, "malformed request")
	})

	t.Run("streaming", func(t *testing.T) {
		req := openai.CompletionRequest{Model: "text-davinci-003", Stream: true, Prompt: openai.PromptUnion{Value: "say hi"}}
		body, err := json.Marshal(req)
		require.NoError(t, err)

		model, parsed, stream, mutated, err := spec.ParseBody(body, false)
		require.NoError(t, err)
		require.Equal(t, "text-davinci-003", model)
		require.True(t, stream)
		require.NotNil(t, parsed)
		require.Nil(t, mutated)
	})
}

func TestCompletionsEndpointSpec_GetTranslator(t *testing.T) {
	spec := CompletionsEndpointSpec{}

	_, err := spec.GetTranslator(filterapi.VersionedAPISchema{Name: filterapi.APISchemaOpenAI}, "override")
	require.NoError(t, err)

	_, err = spec.GetTranslator(filterapi.VersionedAPISchema{Name: filterapi.APISchemaAWSBedrock}, "override")
	require.ErrorContains(t, err, "unsupported API schema")
}

func TestEmbeddingsEndpointSpec_ParseBody(t *testing.T) {
	spec := EmbeddingsEndpointSpec{}

	t.Run("invalid json", func(t *testing.T) {
		_, _, _, _, err := spec.ParseBody([]byte("{"), false)
		require.ErrorContains(t, err, "malformed request")
	})

	t.Run("success with input", func(t *testing.T) {
		req := openai.EmbeddingRequest{
			EmbeddingBaseRequest: openai.EmbeddingBaseRequest{Model: "text-embedding-3-large"},
			OfCompletion: &openai.EmbeddingCompletionRequest{
				EmbeddingBaseRequest: openai.EmbeddingBaseRequest{Model: "text-embedding-3-large"},
				Input:                openai.EmbeddingRequestInput{Value: "input"},
			},
		}
		body, err := json.Marshal(req)
		require.NoError(t, err)

		model, parsed, stream, mutated, err := spec.ParseBody(body, false)
		require.NoError(t, err)
		require.Equal(t, "text-embedding-3-large", model)
		require.False(t, stream)
		require.NotNil(t, parsed)
		require.Nil(t, mutated)
	})

	t.Run("success with messages", func(t *testing.T) {
		body := []byte(`{"model":"gemini-embedding-2","messages":[{"role":"user","content":"embed this"}]}`)

		model, parsed, stream, mutated, err := spec.ParseBody(body, false)
		require.NoError(t, err)
		require.Equal(t, "gemini-embedding-2", model)
		require.False(t, stream)
		require.NotNil(t, parsed)
		require.Nil(t, mutated)
	})
}

func TestEmbeddingsEndpointSpec_GetTranslator(t *testing.T) {
	spec := EmbeddingsEndpointSpec{}

	supported := []filterapi.VersionedAPISchema{
		{Name: filterapi.APISchemaOpenAI},
		{Name: filterapi.APISchemaAzureOpenAI},
		{Name: filterapi.APISchemaGCPVertexAI},
		{Name: filterapi.APISchemaAWSBedrock},
	}
	for _, schema := range supported {
		s := schema
		t.Run("supported_"+string(s.Name), func(t *testing.T) {
			t.Parallel()
			tr, err := spec.GetTranslator(s, "override")
			require.NoError(t, err)
			require.NotNil(t, tr)
		})
	}

	t.Run("unsupported", func(t *testing.T) {
		_, err := spec.GetTranslator(filterapi.VersionedAPISchema{Name: filterapi.APISchemaCohere}, "override")
		require.ErrorContains(t, err, "unsupported API schema")
	})
}

func TestImageGenerationEndpointSpec_ParseBody(t *testing.T) {
	spec := ImageGenerationEndpointSpec{}

	t.Run("invalid json", func(t *testing.T) {
		_, _, _, _, err := spec.ParseBody([]byte("{"), false)
		require.ErrorContains(t, err, "malformed request")
	})

	t.Run("success", func(t *testing.T) {
		body, err := json.Marshal(map[string]any{"model": "gpt-image-1", "prompt": "cat"})
		require.NoError(t, err)

		model, parsed, stream, mutated, err := spec.ParseBody(body, false)
		require.NoError(t, err)
		require.Equal(t, "gpt-image-1", model)
		require.False(t, stream)
		require.NotNil(t, parsed)
		require.Nil(t, mutated)
	})
}

func TestImageGenerationEndpointSpec_GetTranslator(t *testing.T) {
	spec := ImageGenerationEndpointSpec{}

	_, err := spec.GetTranslator(filterapi.VersionedAPISchema{Name: filterapi.APISchemaOpenAI}, "override")
	require.NoError(t, err)

	_, err = spec.GetTranslator(filterapi.VersionedAPISchema{Name: filterapi.APISchemaAzureOpenAI}, "override")
	require.ErrorContains(t, err, "unsupported API schema")
}

func TestMessagesEndpointSpec_ParseBody(t *testing.T) {
	spec := MessagesEndpointSpec{}

	t.Run("invalid json", func(t *testing.T) {
		_, _, _, _, err := spec.ParseBody([]byte("["), false)
		require.ErrorContains(t, err, "malformed request")
	})

	t.Run("missing model", func(t *testing.T) {
		body, err := json.Marshal(map[string]any{"stream": true})
		require.NoError(t, err)

		_, _, _, _, err = spec.ParseBody(body, false)
		require.ErrorContains(t, err, "model field is required")
	})

	t.Run("success", func(t *testing.T) {
		body, err := json.Marshal(map[string]any{"model": "claude-3", "stream": true})
		require.NoError(t, err)

		model, parsed, stream, mutated, err := spec.ParseBody(body, false)
		require.NoError(t, err)
		require.Equal(t, "claude-3", model)
		require.True(t, stream)
		require.NotNil(t, parsed)
		require.Nil(t, mutated)
	})
}

func TestMessagesEndpointSpec_GetTranslator(t *testing.T) {
	spec := MessagesEndpointSpec{}
	for _, schema := range []filterapi.VersionedAPISchema{
		{Name: filterapi.APISchemaGCPAnthropic},
		{Name: filterapi.APISchemaAWSAnthropic},
		{Name: filterapi.APISchemaAnthropic},
		{Name: filterapi.APISchemaOpenAI},     // This is for OpenAI-schema backends like vLLM that support the /v1/messages endpoint
		{Name: filterapi.APISchemaAWSBedrock}, // This is for AWS Bedrock translation from the /v1/messages endpoint
	} {
		translator, err := spec.GetTranslator(schema, "override")
		require.NoError(t, err)
		require.NotNil(t, translator)
	}

	_, err := spec.GetTranslator(filterapi.VersionedAPISchema{Name: filterapi.APISchemaCohere}, "override")
	require.ErrorContains(t, err, "only supports")
}

func TestRerankEndpointSpec_ParseBody(t *testing.T) {
	spec := RerankEndpointSpec{}
	t.Run("invalid json", func(t *testing.T) {
		_, _, _, _, err := spec.ParseBody([]byte("{"), false)
		require.ErrorContains(t, err, "malformed request")
	})

	t.Run("success", func(t *testing.T) {
		req := cohereschema.RerankV2Request{Model: "rerank-v3.5", Query: "foo", Documents: []string{"bar"}}
		body, err := json.Marshal(req)
		require.NoError(t, err)

		model, parsed, stream, mutated, err := spec.ParseBody(body, false)
		require.NoError(t, err)
		require.Equal(t, "rerank-v3.5", model)
		require.False(t, stream)
		require.NotNil(t, parsed)
		require.Nil(t, mutated)
	})
}

func TestRerankEndpointSpec_GetTranslator(t *testing.T) {
	spec := RerankEndpointSpec{}

	_, err := spec.GetTranslator(filterapi.VersionedAPISchema{Name: filterapi.APISchemaCohere}, "override")
	require.NoError(t, err)

	_, err = spec.GetTranslator(filterapi.VersionedAPISchema{Name: filterapi.APISchemaOpenAI}, "override")
	require.ErrorContains(t, err, "unsupported API schema")
}

func TestResponsesEndpointSpec_ParseBody(t *testing.T) {
	spec := ResponsesEndpointSpec{}
	t.Run("invalid json", func(t *testing.T) {
		_, _, _, _, err := spec.ParseBody([]byte("{"), false)
		require.ErrorContains(t, err, "malformed request")
	})

	t.Run("success", func(t *testing.T) {
		req := openai.ResponseRequest{Model: "gpt-4o", Input: openai.ResponseNewParamsInputUnion{
			OfString: ptr.To("Hi"),
		}, Stream: true}
		body, err := json.Marshal(req)
		require.NoError(t, err)

		model, parsed, stream, mutated, err := spec.ParseBody(body, false)
		require.NoError(t, err)
		require.Equal(t, "gpt-4o", model)
		require.True(t, stream)
		require.NotNil(t, parsed)
		require.Nil(t, mutated)
	})

	t.Run("array_input_without_type_field", func(t *testing.T) {
		body := []byte(`{
			"model": "gpt-4.7",
			"input": [{"role": "user", "content": "Hello"}],
			"max_tokens": 50
		}`)

		model, parsed, stream, mutated, err := spec.ParseBody(body, false)
		require.NoError(t, err)
		require.Equal(t, "gpt-4.7", model)
		require.False(t, stream)
		require.NotNil(t, parsed)
		require.NotNil(t, parsed.Input.OfInputItemList)
		require.Len(t, parsed.Input.OfInputItemList, 1)
		require.NotNil(t, parsed.Input.OfInputItemList[0].OfMessage)
		require.Equal(t, "user", parsed.Input.OfInputItemList[0].OfMessage.Role)
		require.Nil(t, mutated)
	})

	t.Run("multi_turn_input_with_assistant_output_without_type_field", func(t *testing.T) {
		body := []byte(`{
			"model": "gpt-4.7",
			"input": [
				{"role": "user", "content": "Hello"},
				{"role": "assistant", "content": [{"type": "output_text", "text": "Hi there"}]},
				{"role": "user", "content": "Continue"}
			]
		}`)

		model, parsed, stream, mutated, err := spec.ParseBody(body, false)
		require.NoError(t, err)
		require.Equal(t, "gpt-4.7", model)
		require.False(t, stream)
		require.NotNil(t, parsed)
		require.NotNil(t, parsed.Input.OfInputItemList)
		require.Len(t, parsed.Input.OfInputItemList, 3)
		require.NotNil(t, parsed.Input.OfInputItemList[1].OfOutputMessage)
		require.Equal(t, "assistant", parsed.Input.OfInputItemList[1].OfOutputMessage.Role)
		require.Nil(t, mutated)
	})
}

func TestResponsesEndpointSpec_GetTranslator(t *testing.T) {
	spec := ResponsesEndpointSpec{}

	_, err := spec.GetTranslator(filterapi.VersionedAPISchema{Name: filterapi.APISchemaOpenAI}, "override")
	require.NoError(t, err)

	_, err = spec.GetTranslator(filterapi.VersionedAPISchema{Name: filterapi.APISchemaAzureOpenAI}, "override")
	require.NoError(t, err)

	awsTranslator, err := spec.GetTranslator(filterapi.VersionedAPISchema{Name: filterapi.APISchemaAWSOpenAI}, "us.openai.gpt-5.6-luna")
	require.NoError(t, err)
	headers, body, err := awsTranslator.RequestBody(
		[]byte(`{"model":"gpt-5.6-luna","input":"hello"}`),
		&openai.ResponseRequest{Model: "gpt-5.6-luna"},
		false,
	)
	require.NoError(t, err)
	require.Equal(t, internalapi.Header{":path", "/openai/v1/responses"}, headers[0])
	require.JSONEq(t, `{"model":"us.openai.gpt-5.6-luna","input":"hello"}`, string(body))

	awsTranslator, err = spec.GetTranslator(filterapi.VersionedAPISchema{Name: filterapi.APISchemaAWSOpenAI}, "")
	require.NoError(t, err)
	original := []byte(`{"model":"us.openai.gpt-5.6-luna","input":"hello"}`)
	_, body, err = awsTranslator.RequestBody(original, &openai.ResponseRequest{Model: "us.openai.gpt-5.6-luna"}, false)
	require.NoError(t, err)
	require.Equal(t, original, body)
}

func TestTokenizeEndpointSpec_ParseBody(t *testing.T) {
	spec := TokenizeEndpointSpec{}

	t.Run("invalid json", func(t *testing.T) {
		_, _, _, _, err := spec.ParseBody([]byte("not-json"), false)
		require.ErrorIs(t, err, internalapi.ErrMalformedRequest)
		require.ErrorContains(t, err, "failed to parse JSON for /tokenize")
	})

	t.Run("chat request", func(t *testing.T) {
		chatReq := tokenize.ChatRequest{
			Model: "gpt-4o",
			Messages: []openai.ChatCompletionMessageParamUnion{
				{OfUser: &openai.ChatCompletionUserMessageParam{
					Role:    openai.ChatMessageRoleUser,
					Content: openai.StringOrUserRoleContentUnion{Value: "Hello"},
				}},
			},
		}
		body, err := json.Marshal(chatReq)
		require.NoError(t, err)

		model, parsed, stream, mutated, err := spec.ParseBody(body, false)
		require.NoError(t, err)
		require.Equal(t, "gpt-4o", model)
		require.False(t, stream)
		require.NotNil(t, parsed.ChatRequest)
		require.Nil(t, parsed.CompletionRequest)
		require.Nil(t, mutated)
	})

	t.Run("completion request", func(t *testing.T) {
		compReq := tokenize.CompletionRequest{
			Model:  "gpt-4o",
			Prompt: "Hello world",
		}
		body, err := json.Marshal(compReq)
		require.NoError(t, err)

		model, parsed, stream, mutated, err := spec.ParseBody(body, false)
		require.NoError(t, err)
		require.Equal(t, "gpt-4o", model)
		require.False(t, stream)
		require.NotNil(t, parsed.CompletionRequest)
		require.Nil(t, parsed.ChatRequest)
		require.Nil(t, mutated)
	})

	t.Run("chat request with conflicting flags", func(t *testing.T) {
		chatReq := tokenize.ChatRequest{
			Model: "gpt-4o",
			Messages: []openai.ChatCompletionMessageParamUnion{
				{OfUser: &openai.ChatCompletionUserMessageParam{
					Role:    openai.ChatMessageRoleUser,
					Content: openai.StringOrUserRoleContentUnion{Value: "Hello"},
				}},
			},
			AddGenerationPrompt:  ptr.To(true),
			ContinueFinalMessage: true,
		}
		body, err := json.Marshal(chatReq)
		require.NoError(t, err)

		_, _, _, _, err = spec.ParseBody(body, false)
		require.ErrorIs(t, err, internalapi.ErrMalformedRequest)
		require.ErrorContains(t, err, "continue_final_message")
	})

	t.Run("empty object rejected - model required", func(t *testing.T) {
		_, _, _, _, err := spec.ParseBody([]byte("{}"), false)
		require.ErrorIs(t, err, internalapi.ErrMalformedRequest)
		require.ErrorContains(t, err, "model is required")
	})

	t.Run("never streaming", func(t *testing.T) {
		compReq := tokenize.CompletionRequest{
			Model:  "gpt-4o",
			Prompt: "Hello",
		}
		body, err := json.Marshal(compReq)
		require.NoError(t, err)

		_, _, stream, _, err := spec.ParseBody(body, true)
		require.NoError(t, err)
		require.False(t, stream)
	})
}

func TestTokenizeEndpointSpec_GetTranslator(t *testing.T) {
	spec := TokenizeEndpointSpec{}
	supported := []filterapi.VersionedAPISchema{
		{Name: filterapi.APISchemaOpenAI},
		{Name: filterapi.APISchemaGCPVertexAI},
		{Name: filterapi.APISchemaGCPAnthropic},
		{Name: filterapi.APISchemaAWSAnthropic},
		{Name: filterapi.APISchemaAWSBedrock},
	}

	for _, schema := range supported {
		s := schema
		t.Run("supported_"+string(s.Name), func(t *testing.T) {
			t.Parallel()
			translator, err := spec.GetTranslator(s, "override")
			require.NoError(t, err)
			require.NotNil(t, translator)
		})
	}

	t.Run("unsupported", func(t *testing.T) {
		_, err := spec.GetTranslator(filterapi.VersionedAPISchema{Name: "Unknown"}, "override")
		require.ErrorContains(t, err, "unsupported API schema for tokenize endpoint")
	})
}

func TestChatCompletionsEndpointSpec_RedactSensitiveInfoFromRequest(t *testing.T) {
	spec := ChatCompletionsEndpointSpec{}

	t.Run("redact_simple_user_message", func(t *testing.T) {
		req := &openai.ChatCompletionRequest{
			Model: "gpt-4o",
			Messages: []openai.ChatCompletionMessageParamUnion{
				{
					OfUser: &openai.ChatCompletionUserMessageParam{
						Role:    "user",
						Content: openai.StringOrUserRoleContentUnion{Value: "Hello, this is sensitive data"},
					},
				},
			},
		}

		redacted, err := spec.RedactSensitiveInfoFromRequest(req)
		require.NoError(t, err)
		require.NotNil(t, redacted)

		// Verify the message content is redacted
		require.Len(t, redacted.Messages, 1)
		require.NotNil(t, redacted.Messages[0].OfUser)

		redactedContent := redacted.Messages[0].OfUser.Content.Value.(string)
		require.Contains(t, redactedContent, "[REDACTED LENGTH=")
		require.Contains(t, redactedContent, "HASH=")
	})

	t.Run("redact_user_message_with_image", func(t *testing.T) {
		req := &openai.ChatCompletionRequest{
			Model: "gpt-4o",
			Messages: []openai.ChatCompletionMessageParamUnion{
				{
					OfUser: &openai.ChatCompletionUserMessageParam{
						Role: "user",
						Content: openai.StringOrUserRoleContentUnion{
							Value: []openai.ChatCompletionContentPartUserUnionParam{
								{
									OfText: &openai.ChatCompletionContentPartTextParam{
										Text: "Describe this image",
										Type: "text",
									},
								},
								{
									OfImageURL: &openai.ChatCompletionContentPartImageParam{
										ImageURL: openai.ChatCompletionContentPartImageImageURLParam{
											URL: "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg==",
										},
										Type: "image_url",
									},
								},
							},
						},
					},
				},
			},
		}

		redacted, err := spec.RedactSensitiveInfoFromRequest(req)
		require.NoError(t, err)
		require.NotNil(t, redacted)

		// Verify both text and image are redacted
		require.Len(t, redacted.Messages, 1)
		require.NotNil(t, redacted.Messages[0].OfUser)

		parts := redacted.Messages[0].OfUser.Content.Value.([]openai.ChatCompletionContentPartUserUnionParam)
		require.Len(t, parts, 2)

		// Check text redaction
		require.NotNil(t, parts[0].OfText)
		require.Contains(t, parts[0].OfText.Text, "[REDACTED LENGTH=")

		// Check image redaction
		require.NotNil(t, parts[1].OfImageURL)
		require.Contains(t, parts[1].OfImageURL.ImageURL.URL, "[REDACTED LENGTH=")
	})

	t.Run("redact_assistant_message_with_tool_calls", func(t *testing.T) {
		req := &openai.ChatCompletionRequest{
			Model: "gpt-4o",
			Messages: []openai.ChatCompletionMessageParamUnion{
				{
					OfAssistant: &openai.ChatCompletionAssistantMessageParam{
						Role:    "assistant",
						Content: openai.StringOrAssistantRoleContentUnion{Value: "I'll call the function"},
						ToolCalls: []openai.ChatCompletionMessageToolCallParam{
							{
								Function: openai.ChatCompletionMessageToolCallFunctionParam{
									Name:      "get_weather",
									Arguments: `{"location": "San Francisco"}`,
								},
								Type: "function",
							},
						},
					},
				},
			},
		}

		redacted, err := spec.RedactSensitiveInfoFromRequest(req)
		require.NoError(t, err)
		require.NotNil(t, redacted)

		// Verify tool call arguments are redacted but function name is kept
		require.Len(t, redacted.Messages, 1)
		require.NotNil(t, redacted.Messages[0].OfAssistant)
		require.Len(t, redacted.Messages[0].OfAssistant.ToolCalls, 1)
		require.Equal(t, "get_weather", redacted.Messages[0].OfAssistant.ToolCalls[0].Function.Name)
		require.Contains(t, redacted.Messages[0].OfAssistant.ToolCalls[0].Function.Arguments, "[REDACTED LENGTH=")
	})

	t.Run("redact_tools", func(t *testing.T) {
		req := &openai.ChatCompletionRequest{
			Model: "gpt-4o",
			Messages: []openai.ChatCompletionMessageParamUnion{
				{
					OfUser: &openai.ChatCompletionUserMessageParam{
						Role:    "user",
						Content: openai.StringOrUserRoleContentUnion{Value: "What's the weather?"},
					},
				},
			},
			Tools: []openai.Tool{
				{
					Type: "function",
					Function: &openai.FunctionDefinition{
						Name:        "get_weather",
						Description: "Get the current weather in a given location",
						Parameters:  map[string]interface{}{"type": "object", "properties": map[string]interface{}{"location": map[string]interface{}{"type": "string"}}},
					},
				},
			},
		}

		redacted, err := spec.RedactSensitiveInfoFromRequest(req)
		require.NoError(t, err)
		require.NotNil(t, redacted)

		// Tool definitions are developer-authored schema metadata — kept as-is
		require.Len(t, redacted.Tools, 1)
		require.NotNil(t, redacted.Tools[0].Function)
		require.Equal(t, "get_weather", redacted.Tools[0].Function.Name)
		require.Equal(t, "Get the current weather in a given location", redacted.Tools[0].Function.Description)
		require.Equal(t, req.Tools[0].Function.Parameters, redacted.Tools[0].Function.Parameters)
	})

	t.Run("empty_request", func(t *testing.T) {
		req := &openai.ChatCompletionRequest{
			Model:    "gpt-4o",
			Messages: []openai.ChatCompletionMessageParamUnion{},
		}

		redacted, err := spec.RedactSensitiveInfoFromRequest(req)
		require.NoError(t, err)
		require.NotNil(t, redacted)
		require.Equal(t, "gpt-4o", redacted.Model)
		require.Empty(t, redacted.Messages)
	})

	t.Run("redact_response_format_json_schema", func(t *testing.T) {
		req := &openai.ChatCompletionRequest{
			Model: "gpt-4o",
			Messages: []openai.ChatCompletionMessageParamUnion{
				{
					OfUser: &openai.ChatCompletionUserMessageParam{
						Role:    "user",
						Content: openai.StringOrUserRoleContentUnion{Value: "Generate a response"},
					},
				},
			},
			ResponseFormat: &openai.ChatCompletionResponseFormatUnion{
				OfJSONSchema: &openai.ChatCompletionResponseFormatJSONSchema{
					Type: "json_schema",
					JSONSchema: openai.ChatCompletionResponseFormatJSONSchemaJSONSchema{
						Name:        "math_response",
						Description: "A response containing mathematical steps",
						Schema:      []byte(`{"type":"object","properties":{"steps":{"type":"array"}},"required":["steps"]}`),
						Strict:      true,
					},
				},
			},
		}

		redacted, err := spec.RedactSensitiveInfoFromRequest(req)
		require.NoError(t, err)
		require.NotNil(t, redacted)

		// Response format schema is developer-authored — kept as-is
		require.NotNil(t, redacted.ResponseFormat)
		require.NotNil(t, redacted.ResponseFormat.OfJSONSchema)
		require.Equal(t, "math_response", redacted.ResponseFormat.OfJSONSchema.JSONSchema.Name)
		require.Equal(t, "A response containing mathematical steps", redacted.ResponseFormat.OfJSONSchema.JSONSchema.Description)
		require.Equal(t, req.ResponseFormat.OfJSONSchema.JSONSchema.Schema, redacted.ResponseFormat.OfJSONSchema.JSONSchema.Schema)
	})

	t.Run("redact_guided_json", func(t *testing.T) {
		guidedSchema := []byte(`{"type":"object","properties":{"answer":{"type":"string"}},"required":["answer"]}`)
		req := &openai.ChatCompletionRequest{
			Model: "gpt-4o",
			Messages: []openai.ChatCompletionMessageParamUnion{
				{
					OfUser: &openai.ChatCompletionUserMessageParam{
						Role:    "user",
						Content: openai.StringOrUserRoleContentUnion{Value: "Answer the question"},
					},
				},
			},
			GuidedJSON: guidedSchema,
		}

		redacted, err := spec.RedactSensitiveInfoFromRequest(req)
		require.NoError(t, err)
		require.NotNil(t, redacted)

		// guided_json is developer-authored schema — kept as-is
		require.Equal(t, string(guidedSchema), string(redacted.GuidedJSON))
	})

	t.Run("redact_various_message_types", func(t *testing.T) {
		req := &openai.ChatCompletionRequest{
			Model: "gpt-4o",
			Messages: []openai.ChatCompletionMessageParamUnion{
				{
					OfSystem: &openai.ChatCompletionSystemMessageParam{
						Role:    "system",
						Content: openai.ContentUnion{Value: "System message with sensitive data"},
					},
				},
				{
					OfDeveloper: &openai.ChatCompletionDeveloperMessageParam{
						Role:    "developer",
						Content: openai.ContentUnion{Value: "Developer instructions"},
					},
				},
				{
					OfTool: &openai.ChatCompletionToolMessageParam{
						Role:       "tool",
						Content:    openai.ContentUnion{Value: "Tool result: API_KEY_12345"},
						ToolCallID: "call_123",
					},
				},
			},
		}

		redacted, err := spec.RedactSensitiveInfoFromRequest(req)
		require.NoError(t, err)
		require.NotNil(t, redacted)

		// Verify all message types are redacted
		require.Len(t, redacted.Messages, 3)

		// System message
		require.NotNil(t, redacted.Messages[0].OfSystem)
		systemContent := redacted.Messages[0].OfSystem.Content.Value.(string)
		require.Contains(t, systemContent, "[REDACTED LENGTH=")

		// Developer message
		require.NotNil(t, redacted.Messages[1].OfDeveloper)
		devContent := redacted.Messages[1].OfDeveloper.Content.Value.(string)
		require.Contains(t, devContent, "[REDACTED LENGTH=")

		// Tool message
		require.NotNil(t, redacted.Messages[2].OfTool)
		toolContent := redacted.Messages[2].OfTool.Content.Value.(string)
		require.Contains(t, toolContent, "[REDACTED LENGTH=")
	})

	t.Run("redact_content_union_with_text_parts", func(t *testing.T) {
		req := &openai.ChatCompletionRequest{
			Model: "gpt-4o",
			Messages: []openai.ChatCompletionMessageParamUnion{
				{
					OfSystem: &openai.ChatCompletionSystemMessageParam{
						Role: "system",
						Content: openai.ContentUnion{
							Value: []openai.ChatCompletionContentPartTextParam{
								{Type: "text", Text: "First part with sensitive data"},
								{Type: "text", Text: "Second part with more sensitive data"},
							},
						},
					},
				},
			},
		}

		redacted, err := spec.RedactSensitiveInfoFromRequest(req)
		require.NoError(t, err)
		require.NotNil(t, redacted)

		// Verify content parts are redacted
		require.Len(t, redacted.Messages, 1)
		require.NotNil(t, redacted.Messages[0].OfSystem)
		parts := redacted.Messages[0].OfSystem.Content.Value.([]openai.ChatCompletionContentPartTextParam)
		require.Len(t, parts, 2)
		require.Contains(t, parts[0].Text, "[REDACTED LENGTH=")
		require.Contains(t, parts[1].Text, "[REDACTED LENGTH=")
	})

	t.Run("redact_assistant_message_with_content_array", func(t *testing.T) {
		textContent := "Assistant response text"
		refusalContent := "I cannot help with that"
		req := &openai.ChatCompletionRequest{
			Model: "gpt-4o",
			Messages: []openai.ChatCompletionMessageParamUnion{
				{
					OfAssistant: &openai.ChatCompletionAssistantMessageParam{
						Role: "assistant",
						Content: openai.StringOrAssistantRoleContentUnion{
							Value: []openai.ChatCompletionAssistantMessageParamContent{
								{Type: "text", Text: &textContent},
								{Type: "refusal", Refusal: &refusalContent},
							},
						},
					},
				},
			},
		}

		redacted, err := spec.RedactSensitiveInfoFromRequest(req)
		require.NoError(t, err)
		require.NotNil(t, redacted)

		// Verify content array is redacted
		require.Len(t, redacted.Messages, 1)
		require.NotNil(t, redacted.Messages[0].OfAssistant)
		parts := redacted.Messages[0].OfAssistant.Content.Value.([]openai.ChatCompletionAssistantMessageParamContent)
		require.Len(t, parts, 2)
		require.NotNil(t, parts[0].Text)
		require.Contains(t, *parts[0].Text, "[REDACTED LENGTH=")
		require.NotNil(t, parts[1].Refusal)
		require.Contains(t, *parts[1].Refusal, "[REDACTED LENGTH=")
	})

	t.Run("redact_assistant_message_with_single_content_object", func(t *testing.T) {
		textContent := "Single content object"
		req := &openai.ChatCompletionRequest{
			Model: "gpt-4o",
			Messages: []openai.ChatCompletionMessageParamUnion{
				{
					OfAssistant: &openai.ChatCompletionAssistantMessageParam{
						Role: "assistant",
						Content: openai.StringOrAssistantRoleContentUnion{
							Value: openai.ChatCompletionAssistantMessageParamContent{
								Type: "text",
								Text: &textContent,
							},
						},
					},
				},
			},
		}

		redacted, err := spec.RedactSensitiveInfoFromRequest(req)
		require.NoError(t, err)
		require.NotNil(t, redacted)

		// Verify single content object is redacted
		require.Len(t, redacted.Messages, 1)
		require.NotNil(t, redacted.Messages[0].OfAssistant)
		part := redacted.Messages[0].OfAssistant.Content.Value.(openai.ChatCompletionAssistantMessageParamContent)
		require.NotNil(t, part.Text)
		require.Contains(t, *part.Text, "[REDACTED LENGTH=")
	})

	t.Run("redact_user_content_with_audio", func(t *testing.T) {
		req := &openai.ChatCompletionRequest{
			Model: "gpt-4o",
			Messages: []openai.ChatCompletionMessageParamUnion{
				{
					OfUser: &openai.ChatCompletionUserMessageParam{
						Role: "user",
						Content: openai.StringOrUserRoleContentUnion{
							Value: []openai.ChatCompletionContentPartUserUnionParam{
								{
									OfInputAudio: &openai.ChatCompletionContentPartInputAudioParam{
										Type: "input_audio",
										InputAudio: openai.ChatCompletionContentPartInputAudioInputAudioParam{
											Data:   "base64encodedaudiodata==",
											Format: "wav",
										},
									},
								},
							},
						},
					},
				},
			},
		}

		redacted, err := spec.RedactSensitiveInfoFromRequest(req)
		require.NoError(t, err)
		require.NotNil(t, redacted)

		// Verify audio data is redacted
		require.Len(t, redacted.Messages, 1)
		require.NotNil(t, redacted.Messages[0].OfUser)
		parts := redacted.Messages[0].OfUser.Content.Value.([]openai.ChatCompletionContentPartUserUnionParam)
		require.Len(t, parts, 1)
		require.NotNil(t, parts[0].OfInputAudio)
		require.Contains(t, parts[0].OfInputAudio.InputAudio.Data, "[REDACTED LENGTH=")
	})

	t.Run("redact_user_content_with_file", func(t *testing.T) {
		req := &openai.ChatCompletionRequest{
			Model: "gpt-4o",
			Messages: []openai.ChatCompletionMessageParamUnion{
				{
					OfUser: &openai.ChatCompletionUserMessageParam{
						Role: "user",
						Content: openai.StringOrUserRoleContentUnion{
							Value: []openai.ChatCompletionContentPartUserUnionParam{
								{
									OfFile: &openai.ChatCompletionContentPartFileParam{
										Type: "file",
										File: openai.ChatCompletionContentPartFileFileParam{
											FileData: "base64encodedfiledata==",
											Filename: "document.pdf",
										},
									},
								},
							},
						},
					},
				},
			},
		}

		redacted, err := spec.RedactSensitiveInfoFromRequest(req)
		require.NoError(t, err)
		require.NotNil(t, redacted)

		// Verify file data is redacted
		require.Len(t, redacted.Messages, 1)
		require.NotNil(t, redacted.Messages[0].OfUser)
		parts := redacted.Messages[0].OfUser.Content.Value.([]openai.ChatCompletionContentPartUserUnionParam)
		require.Len(t, parts, 1)
		require.NotNil(t, parts[0].OfFile)
		require.Contains(t, parts[0].OfFile.File.FileData, "[REDACTED LENGTH=")
	})

	t.Run("redact_prediction_content", func(t *testing.T) {
		req := &openai.ChatCompletionRequest{
			Model: "gpt-4o",
			Messages: []openai.ChatCompletionMessageParamUnion{
				{
					OfUser: &openai.ChatCompletionUserMessageParam{
						Role:    "user",
						Content: openai.StringOrUserRoleContentUnion{Value: "Test message"},
					},
				},
			},
			PredictionContent: &openai.PredictionContent{
				Type:    openai.PredictionContentTypeContent,
				Content: openai.ContentUnion{Value: "Predicted content with sensitive information"},
			},
		}

		redacted, err := spec.RedactSensitiveInfoFromRequest(req)
		require.NoError(t, err)
		require.NotNil(t, redacted)

		// Verify prediction content is redacted
		require.NotNil(t, redacted.PredictionContent)
		redactedContent := redacted.PredictionContent.Content.Value.(string)
		require.Contains(t, redactedContent, "[REDACTED LENGTH=")
		require.Contains(t, redactedContent, "HASH=")
	})
}

func TestRedactString(t *testing.T) {
	t.Run("redact_non_empty_string", func(t *testing.T) {
		result := redaction.RedactString("sensitive data")
		require.Contains(t, result, "[REDACTED LENGTH=14")
		require.Contains(t, result, "HASH=")
	})
}

func TestSpeechEndpointSpec_ParseBody(t *testing.T) {
	spec := SpeechEndpointSpec{}

	t.Run("invalid json", func(t *testing.T) {
		_, _, _, _, err := spec.ParseBody([]byte("{"), false)
		require.ErrorContains(t, err, "failed to unmarshal speech request")
	})

	t.Run("binary_mode", func(t *testing.T) {
		req := openai.SpeechRequest{
			Model: "tts-1",
			Input: "Hello world",
			Voice: "alloy",
		}
		body, err := json.Marshal(req)
		require.NoError(t, err)

		model, parsed, stream, mutated, err := spec.ParseBody(body, false)
		require.NoError(t, err)
		require.Equal(t, "tts-1", model)
		require.False(t, stream)
		require.NotNil(t, parsed)
		require.Nil(t, mutated)
	})

	t.Run("sse_streaming_mode", func(t *testing.T) {
		sseFormat := "sse"
		req := openai.SpeechRequest{
			Model:        "gpt-4o-mini-tts",
			Input:        "Hello streaming",
			Voice:        "nova",
			StreamFormat: &sseFormat,
		}
		body, err := json.Marshal(req)
		require.NoError(t, err)

		model, parsed, stream, mutated, err := spec.ParseBody(body, false)
		require.NoError(t, err)
		require.Equal(t, "gpt-4o-mini-tts", model)
		require.True(t, stream)
		require.NotNil(t, parsed)
		require.Equal(t, "sse", *parsed.StreamFormat)
		require.Nil(t, mutated)
	})

	t.Run("audio_format_mode", func(t *testing.T) {
		audioFormat := "audio"
		req := openai.SpeechRequest{
			Model:        "tts-1-hd",
			Input:        "Test",
			Voice:        "alloy",
			StreamFormat: &audioFormat,
		}
		body, err := json.Marshal(req)
		require.NoError(t, err)

		_, _, stream, _, err := spec.ParseBody(body, false)
		require.NoError(t, err)
		require.False(t, stream, "audio format should not be treated as streaming")
	})

	t.Run("with_all_optional_params", func(t *testing.T) {
		instructions := "Speak clearly"
		responseFormat := "mp3"
		speed := 1.5
		req := openai.SpeechRequest{
			Model:          "tts-1",
			Input:          "Test with options",
			Voice:          "shimmer",
			Instructions:   &instructions,
			ResponseFormat: &responseFormat,
			Speed:          &speed,
		}
		body, err := json.Marshal(req)
		require.NoError(t, err)

		model, parsed, stream, mutated, err := spec.ParseBody(body, false)
		require.NoError(t, err)
		require.Equal(t, "tts-1", model)
		require.False(t, stream)
		require.NotNil(t, parsed)
		require.Equal(t, "Speak clearly", *parsed.Instructions)
		require.Equal(t, "mp3", *parsed.ResponseFormat)
		require.Equal(t, 1.5, *parsed.Speed)
		require.Nil(t, mutated)
	})
}

func TestSpeechEndpointSpec_GetTranslator(t *testing.T) {
	spec := SpeechEndpointSpec{}

	_, err := spec.GetTranslator(filterapi.VersionedAPISchema{Name: filterapi.APISchemaOpenAI}, "override")
	require.NoError(t, err)

	_, err = spec.GetTranslator(filterapi.VersionedAPISchema{Name: filterapi.APISchemaAzureOpenAI}, "override")
	require.ErrorContains(t, err, "unsupported API schema for speech")
}

func TestSpeechEndpointSpec_RedactSensitiveInfoFromRequest(t *testing.T) {
	spec := SpeechEndpointSpec{}

	t.Run("redact_input_text", func(t *testing.T) {
		req := &openai.SpeechRequest{
			Model: "tts-1",
			Input: "This is sensitive text that should be redacted",
			Voice: "alloy",
		}

		redacted, err := spec.RedactSensitiveInfoFromRequest(req)
		require.NoError(t, err)
		require.NotNil(t, redacted)
		require.Contains(t, redacted.Input, "[REDACTED LENGTH=")
		require.Equal(t, "tts-1", redacted.Model)
		require.Equal(t, "alloy", redacted.Voice)
	})

	t.Run("redact_instructions", func(t *testing.T) {
		instructions := "Speak with a British accent and emphasize certain words"
		req := &openai.SpeechRequest{
			Model:        "gpt-4o-mini-tts",
			Input:        "Hello world",
			Voice:        "nova",
			Instructions: &instructions,
		}

		redacted, err := spec.RedactSensitiveInfoFromRequest(req)
		require.NoError(t, err)
		require.NotNil(t, redacted)
		require.Contains(t, redacted.Input, "[REDACTED LENGTH=")
		require.NotNil(t, redacted.Instructions)
		require.Contains(t, *redacted.Instructions, "[REDACTED LENGTH=")
	})

	t.Run("no_instructions", func(t *testing.T) {
		req := &openai.SpeechRequest{
			Model: "tts-1-hd",
			Input: "Test input",
			Voice: "echo",
		}

		redacted, err := spec.RedactSensitiveInfoFromRequest(req)
		require.NoError(t, err)
		require.NotNil(t, redacted)
		require.Contains(t, redacted.Input, "[REDACTED LENGTH=")
		require.Nil(t, redacted.Instructions)
	})

	t.Run("preserve_other_fields", func(t *testing.T) {
		responseFormat := "wav"
		speed := 1.5
		streamFormat := "sse"
		req := &openai.SpeechRequest{
			Model:          "gpt-4o-mini-tts",
			Input:          "Sensitive content",
			Voice:          "shimmer",
			ResponseFormat: &responseFormat,
			Speed:          &speed,
			StreamFormat:   &streamFormat,
		}

		redacted, err := spec.RedactSensitiveInfoFromRequest(req)
		require.NoError(t, err)
		require.NotNil(t, redacted)
		require.Contains(t, redacted.Input, "[REDACTED LENGTH=")
		require.Equal(t, "shimmer", redacted.Voice)
		require.NotNil(t, redacted.ResponseFormat)
		require.Equal(t, "wav", *redacted.ResponseFormat)
		require.NotNil(t, redacted.Speed)
		require.Equal(t, 1.5, *redacted.Speed)
		require.NotNil(t, redacted.StreamFormat)
		require.Equal(t, "sse", *redacted.StreamFormat)
	})
}

// --- Transcription endpoint spec tests ---

func buildMultipartBody(t *testing.T, fields map[string]string, fileName string, fileData []byte) ([]byte, string) {
	t.Helper()
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	for k, v := range fields {
		require.NoError(t, writer.WriteField(k, v))
	}
	if fileName != "" {
		part, err := writer.CreateFormFile("file", fileName)
		require.NoError(t, err)
		_, err = part.Write(fileData)
		require.NoError(t, err)
	}
	require.NoError(t, writer.Close())
	return buf.Bytes(), writer.FormDataContentType()
}

func TestTranscriptionEndpointSpec_ParseBody_RejectsJSON(t *testing.T) {
	spec := TranscriptionEndpointSpec{}
	_, _, _, _, err := spec.ParseBody([]byte(`{"model":"whisper-1"}`), false)
	require.ErrorContains(t, err, "expected multipart/form-data")
}

func TestTranscriptionEndpointSpec_ParseMultipartBody(t *testing.T) {
	spec := TranscriptionEndpointSpec{}

	t.Run("valid request", func(t *testing.T) {
		body, ct := buildMultipartBody(t, map[string]string{
			"model":    "whisper-1",
			"language": "en",
			"prompt":   "test prompt",
		}, "test.mp3", []byte("audio-data"))

		model, req, stream, mutated, err := spec.ParseMultipartBody(body, ct, false)
		require.NoError(t, err)
		require.Equal(t, "whisper-1", model)
		require.NotNil(t, req)
		require.Equal(t, "whisper-1", req.Model)
		require.Equal(t, "en", req.Language)
		require.Equal(t, "test prompt", req.Prompt)
		require.Equal(t, "test.mp3", req.FileName)
		require.Equal(t, int64(10), req.FileSize)
		require.False(t, stream)
		require.Nil(t, mutated)
	})

	t.Run("missing model", func(t *testing.T) {
		body, ct := buildMultipartBody(t, map[string]string{}, "test.mp3", []byte("audio"))
		_, _, _, _, err := spec.ParseMultipartBody(body, ct, false)
		require.ErrorContains(t, err, "missing required field 'model'")
	})

	t.Run("missing file", func(t *testing.T) {
		body, ct := buildMultipartBody(t, map[string]string{"model": "whisper-1"}, "", nil)
		_, _, _, _, err := spec.ParseMultipartBody(body, ct, false)
		require.ErrorContains(t, err, "missing required field 'file'")
	})

	t.Run("invalid content type", func(t *testing.T) {
		_, _, _, _, err := spec.ParseMultipartBody([]byte("data"), "text/plain", false)
		require.ErrorContains(t, err, "failed to parse multipart form data")
	})

	t.Run("temperature and response_format", func(t *testing.T) {
		body, ct := buildMultipartBody(t, map[string]string{
			"model":           "whisper-1",
			"temperature":     "0.5",
			"response_format": "verbose_json",
		}, "test.wav", []byte("wav-data"))

		_, req, _, _, err := spec.ParseMultipartBody(body, ct, false)
		require.NoError(t, err)
		require.NotNil(t, req.Temperature)
		require.Equal(t, 0.5, *req.Temperature)
		require.Equal(t, "verbose_json", req.ResponseFormat)
	})

	t.Run("invalid temperature rejected as malformed request", func(t *testing.T) {
		body, ct := buildMultipartBody(t, map[string]string{
			"model":       "whisper-1",
			"temperature": "not-a-number",
		}, "test.mp3", []byte("audio-data"))

		_, _, _, _, err := spec.ParseMultipartBody(body, ct, false)
		require.ErrorContains(t, err, "invalid temperature value")
		require.ErrorContains(t, err, "malformed request")
	})

	t.Run("stream field true", func(t *testing.T) {
		body, ct := buildMultipartBody(t, map[string]string{
			"model":  "whisper-1",
			"stream": "true",
		}, "test.mp3", []byte("audio-data"))

		// req.Stream is propagated as the third return value so the upstream filter
		// switches Envoy to STREAMED response mode. The translator still branches on
		// the actual response Content-Type at response time, so whisper-1+stream=true
		// (which OpenAI silently treats as non-streaming) is handled correctly.
		_, req, stream, _, err := spec.ParseMultipartBody(body, ct, false)
		require.NoError(t, err)
		require.True(t, req.Stream)
		require.True(t, stream)
	})

	t.Run("stream field false", func(t *testing.T) {
		body, ct := buildMultipartBody(t, map[string]string{
			"model":  "whisper-1",
			"stream": "false",
		}, "test.mp3", []byte("audio-data"))

		_, req, stream, _, err := spec.ParseMultipartBody(body, ct, false)
		require.NoError(t, err)
		require.False(t, req.Stream)
		require.False(t, stream)
	})

	t.Run("stream field omitted defaults to false", func(t *testing.T) {
		body, ct := buildMultipartBody(t, map[string]string{
			"model": "gpt-4o-transcribe",
		}, "test.mp3", []byte("audio-data"))

		_, req, stream, _, err := spec.ParseMultipartBody(body, ct, false)
		require.NoError(t, err)
		require.False(t, req.Stream)
		require.False(t, stream)
	})

	t.Run("timestamp_granularities", func(t *testing.T) {
		var buf bytes.Buffer
		writer := multipart.NewWriter(&buf)
		require.NoError(t, writer.WriteField("model", "whisper-1"))
		require.NoError(t, writer.WriteField("timestamp_granularities[]", "word"))
		require.NoError(t, writer.WriteField("timestamp_granularities[]", "segment"))
		part, err := writer.CreateFormFile("file", "test.mp3")
		require.NoError(t, err)
		_, err = part.Write([]byte("audio"))
		require.NoError(t, err)
		require.NoError(t, writer.Close())

		_, req, _, _, err := spec.ParseMultipartBody(buf.Bytes(), writer.FormDataContentType(), false)
		require.NoError(t, err)
		require.Equal(t, []string{"word", "segment"}, req.TimestampGranularities)
	})

	t.Run("missing boundary in content-type", func(t *testing.T) {
		_, _, _, _, err := spec.ParseMultipartBody([]byte("data"), "multipart/form-data", false)
		require.ErrorContains(t, err, "missing boundary")
	})

	t.Run("prompt field", func(t *testing.T) {
		body, ct := buildMultipartBody(t, map[string]string{
			"model":  "whisper-1",
			"prompt": "This is a transcription prompt",
		}, "test.mp3", []byte("audio"))

		_, req, _, _, err := spec.ParseMultipartBody(body, ct, false)
		require.NoError(t, err)
		require.Equal(t, "This is a transcription prompt", req.Prompt)
	})

	t.Run("language field", func(t *testing.T) {
		body, ct := buildMultipartBody(t, map[string]string{
			"model":    "whisper-1",
			"language": "fr",
		}, "test.mp3", []byte("audio"))

		_, req, _, _, err := spec.ParseMultipartBody(body, ct, false)
		require.NoError(t, err)
		require.Equal(t, "fr", req.Language)
	})
}

func TestTranscriptionEndpointSpec_GetTranslator(t *testing.T) {
	spec := TranscriptionEndpointSpec{}

	_, err := spec.GetTranslator(filterapi.VersionedAPISchema{Name: filterapi.APISchemaOpenAI}, "override")
	require.NoError(t, err)

	_, err = spec.GetTranslator(filterapi.VersionedAPISchema{Name: filterapi.APISchemaAzureOpenAI}, "override")
	require.ErrorContains(t, err, "unsupported API schema for audio transcription")
}

func TestTranscriptionEndpointSpec_RedactSensitiveInfoFromRequest(t *testing.T) {
	spec := TranscriptionEndpointSpec{}

	t.Run("no prompt", func(t *testing.T) {
		req := &openai.TranscriptionRequest{Model: "whisper-1"}
		redacted, err := spec.RedactSensitiveInfoFromRequest(req)
		require.NoError(t, err)
		require.Empty(t, redacted.Prompt)
	})

	t.Run("with prompt", func(t *testing.T) {
		req := &openai.TranscriptionRequest{Model: "whisper-1", Prompt: "sensitive transcription context"}
		redacted, err := spec.RedactSensitiveInfoFromRequest(req)
		require.NoError(t, err)
		require.Contains(t, redacted.Prompt, "[REDACTED LENGTH=")
		require.Contains(t, redacted.Prompt, "HASH=")
		require.NotContains(t, redacted.Prompt, "sensitive transcription context")
		require.Equal(t, "whisper-1", redacted.Model)
	})
}

// --- Translation endpoint spec tests ---

func TestTranslationEndpointSpec_ParseBody_RejectsJSON(t *testing.T) {
	spec := TranslationEndpointSpec{}
	_, _, _, _, err := spec.ParseBody([]byte(`{"model":"whisper-1"}`), false)
	require.ErrorContains(t, err, "expected multipart/form-data")
}

func TestTranslationEndpointSpec_ParseMultipartBody(t *testing.T) {
	spec := TranslationEndpointSpec{}

	t.Run("valid request", func(t *testing.T) {
		body, ct := buildMultipartBody(t, map[string]string{
			"model":  "whisper-1",
			"prompt": "translate this",
		}, "test.mp3", []byte("audio-data"))

		model, req, stream, mutated, err := spec.ParseMultipartBody(body, ct, false)
		require.NoError(t, err)
		require.Equal(t, "whisper-1", model)
		require.NotNil(t, req)
		require.Equal(t, "whisper-1", req.Model)
		require.Equal(t, "translate this", req.Prompt)
		require.Equal(t, "test.mp3", req.FileName)
		require.Equal(t, int64(10), req.FileSize)
		require.False(t, stream)
		require.Nil(t, mutated)
	})

	t.Run("missing model", func(t *testing.T) {
		body, ct := buildMultipartBody(t, map[string]string{}, "test.mp3", []byte("audio"))
		_, _, _, _, err := spec.ParseMultipartBody(body, ct, false)
		require.ErrorContains(t, err, "missing required field 'model'")
	})

	t.Run("missing file", func(t *testing.T) {
		body, ct := buildMultipartBody(t, map[string]string{"model": "whisper-1"}, "", nil)
		_, _, _, _, err := spec.ParseMultipartBody(body, ct, false)
		require.ErrorContains(t, err, "missing required field 'file'")
	})

	t.Run("temperature and response_format", func(t *testing.T) {
		body, ct := buildMultipartBody(t, map[string]string{
			"model":           "whisper-1",
			"temperature":     "0.3",
			"response_format": "srt",
		}, "test.mp3", []byte("audio-data"))

		_, req, _, _, err := spec.ParseMultipartBody(body, ct, false)
		require.NoError(t, err)
		require.NotNil(t, req.Temperature)
		require.Equal(t, 0.3, *req.Temperature)
		require.Equal(t, "srt", req.ResponseFormat)
	})

	t.Run("invalid temperature rejected as malformed request", func(t *testing.T) {
		body, ct := buildMultipartBody(t, map[string]string{
			"model":       "whisper-1",
			"temperature": "abc",
		}, "test.mp3", []byte("audio-data"))

		_, _, _, _, err := spec.ParseMultipartBody(body, ct, false)
		require.ErrorContains(t, err, "invalid temperature value")
		require.ErrorContains(t, err, "malformed request")
	})

	t.Run("invalid content type", func(t *testing.T) {
		_, _, _, _, err := spec.ParseMultipartBody([]byte("data"), "text/plain", false)
		require.ErrorContains(t, err, "failed to parse multipart form data")
	})

	t.Run("missing boundary in content-type", func(t *testing.T) {
		_, _, _, _, err := spec.ParseMultipartBody([]byte("data"), "multipart/form-data", false)
		require.ErrorContains(t, err, "missing boundary")
	})
}

func TestTranslationEndpointSpec_GetTranslator(t *testing.T) {
	spec := TranslationEndpointSpec{}

	_, err := spec.GetTranslator(filterapi.VersionedAPISchema{Name: filterapi.APISchemaOpenAI}, "override")
	require.NoError(t, err)

	_, err = spec.GetTranslator(filterapi.VersionedAPISchema{Name: filterapi.APISchemaAzureOpenAI}, "override")
	require.ErrorContains(t, err, "unsupported API schema for audio translation")
}

func TestTranslationEndpointSpec_RedactSensitiveInfoFromRequest(t *testing.T) {
	spec := TranslationEndpointSpec{}

	t.Run("no prompt", func(t *testing.T) {
		req := &openai.TranslationRequest{Model: "whisper-1"}
		redacted, err := spec.RedactSensitiveInfoFromRequest(req)
		require.NoError(t, err)
		require.Empty(t, redacted.Prompt)
	})

	t.Run("with prompt", func(t *testing.T) {
		req := &openai.TranslationRequest{Model: "whisper-1", Prompt: "sensitive translation context"}
		redacted, err := spec.RedactSensitiveInfoFromRequest(req)
		require.NoError(t, err)
		require.Contains(t, redacted.Prompt, "[REDACTED LENGTH=")
		require.Contains(t, redacted.Prompt, "HASH=")
		require.NotContains(t, redacted.Prompt, "sensitive translation context")
		require.Equal(t, "whisper-1", redacted.Model)
	})
}

// --- ParseMultipartBody defaults for JSON-only endpoints ---

func TestParseMultipartBody_RejectsJSONOnlyEndpoints(t *testing.T) {
	_, _, _, _, err := ChatCompletionsEndpointSpec{}.ParseMultipartBody(nil, "", false)
	require.ErrorContains(t, err, "multipart body not supported")

	_, _, _, _, err = CompletionsEndpointSpec{}.ParseMultipartBody(nil, "", false)
	require.ErrorContains(t, err, "multipart body not supported")

	_, _, _, _, err = EmbeddingsEndpointSpec{}.ParseMultipartBody(nil, "", false)
	require.ErrorContains(t, err, "multipart body not supported")

	_, _, _, _, err = ImageGenerationEndpointSpec{}.ParseMultipartBody(nil, "", false)
	require.ErrorContains(t, err, "multipart body not supported")

	_, _, _, _, err = SpeechEndpointSpec{}.ParseMultipartBody(nil, "", false)
	require.ErrorContains(t, err, "multipart body not supported")

	_, _, _, _, err = ResponsesInputTokensEndpointSpec{}.ParseMultipartBody(nil, "", false)
	require.ErrorContains(t, err, "multipart body not supported")
}

func TestResponsesInputTokensEndpointSpec_ParseBody(t *testing.T) {
	spec := ResponsesInputTokensEndpointSpec{}

	t.Run("invalid json", func(t *testing.T) {
		_, _, _, _, err := spec.ParseBody([]byte("{"), false)
		require.ErrorContains(t, err, "malformed request")
	})

	t.Run("empty model allowed", func(t *testing.T) {
		model, parsed, stream, _, err := spec.ParseBody([]byte(`{"input":"hello"}`), false)
		require.NoError(t, err)
		require.Empty(t, model)
		require.NotNil(t, parsed)
		require.False(t, stream)
	})

	t.Run("success", func(t *testing.T) {
		body := []byte(`{"model":"gpt-4.1","input":"Count these tokens please"}`)
		model, parsed, stream, mutated, err := spec.ParseBody(body, false)
		require.NoError(t, err)
		require.Equal(t, "gpt-4.1", model)
		require.False(t, stream)
		require.NotNil(t, parsed)
		require.Nil(t, mutated)
	})
}

func TestResponsesInputTokensEndpointSpec_GetTranslator(t *testing.T) {
	spec := ResponsesInputTokensEndpointSpec{}

	_, err := spec.GetTranslator(filterapi.VersionedAPISchema{Name: filterapi.APISchemaOpenAI}, "override")
	require.NoError(t, err)

	_, err = spec.GetTranslator(filterapi.VersionedAPISchema{Name: filterapi.APISchemaAzureOpenAI, Version: "2025-01-01-preview"}, "override")
	require.NoError(t, err)

	_, err = spec.GetTranslator(filterapi.VersionedAPISchema{Name: filterapi.APISchemaAnthropic}, "override")
	require.ErrorContains(t, err, "unsupported API schema")
}

func TestCompletionsEndpointSpec_RedactSensitiveInfoFromRequest(t *testing.T) {
	t.Run("string prompt", func(t *testing.T) {
		const marker = "my-marker-completion-prompt"
		_, req, _, _, err := CompletionsEndpointSpec{}.ParseBody([]byte(`{"model":"gpt-3.5-turbo-instruct","prompt":"`+marker+`"}`), false)
		require.NoError(t, err)
		redacted, err := CompletionsEndpointSpec{}.RedactSensitiveInfoFromRequest(req)
		require.NoError(t, err)
		out := mustMarshal(t, redacted)
		require.NotContains(t, out, marker)
		require.Contains(t, out, "[REDACTED")
		require.Contains(t, mustMarshal(t, req), marker, "original must not be mutated")
	})

	t.Run("array prompt", func(t *testing.T) {
		const marker = "array-marker-prompt"
		_, req, _, _, err := CompletionsEndpointSpec{}.ParseBody([]byte(`{"model":"m","prompt":["`+marker+`","second"]}`), false)
		require.NoError(t, err)
		redacted, err := CompletionsEndpointSpec{}.RedactSensitiveInfoFromRequest(req)
		require.NoError(t, err)
		out := mustMarshal(t, redacted)
		require.NotContains(t, out, marker)
		require.Contains(t, out, "[REDACTED")
	})
}

func TestEmbeddingsEndpointSpec_RedactSensitiveInfoFromRequest(t *testing.T) {
	t.Run("completion input string", func(t *testing.T) {
		const marker = "embed-this-marker"
		_, req, _, _, err := EmbeddingsEndpointSpec{}.ParseBody([]byte(`{"model":"text-embedding-3-small","input":"`+marker+`"}`), false)
		require.NoError(t, err)
		redacted, err := EmbeddingsEndpointSpec{}.RedactSensitiveInfoFromRequest(req)
		require.NoError(t, err)
		out := mustMarshal(t, redacted)
		require.NotContains(t, out, marker)
		require.Contains(t, out, "[REDACTED")
		require.Contains(t, mustMarshal(t, req), marker, "original must not be mutated")
	})

	t.Run("chat messages", func(t *testing.T) {
		const marker = "chat-embed-marker"
		_, req, _, _, err := EmbeddingsEndpointSpec{}.ParseBody([]byte(`{"model":"m","messages":[{"role":"user","content":"`+marker+`"}]}`), false)
		require.NoError(t, err)
		redacted, err := EmbeddingsEndpointSpec{}.RedactSensitiveInfoFromRequest(req)
		require.NoError(t, err)
		out := mustMarshal(t, redacted)
		require.NotContains(t, out, marker)
		require.Contains(t, out, "[REDACTED")
	})
}

func TestImageGenerationEndpointSpec_RedactSensitiveInfoFromRequest(t *testing.T) {
	const marker = "draw a marker image of the plans"
	req := &openai.ImageGenerationRequest{Model: "dall-e-3", Prompt: marker}
	redacted, err := ImageGenerationEndpointSpec{}.RedactSensitiveInfoFromRequest(req)
	require.NoError(t, err)
	require.Contains(t, redacted.Prompt, "[REDACTED LENGTH=")
	require.NotEqual(t, marker, redacted.Prompt)
	require.Equal(t, marker, req.Prompt, "original must not be mutated")
}

func TestResponsesEndpointSpec_RedactSensitiveInfoFromRequest(t *testing.T) {
	t.Run("instructions/user/string input", func(t *testing.T) {
		const markerInstr = "marker-instructions"
		const markerUser = "user-pii-123"
		const markerInput = "marker-input-text"
		body := `{"model":"gpt-4o","instructions":"` + markerInstr + `","user":"` + markerUser + `","input":"` + markerInput + `"}`
		_, req, _, _, err := ResponsesEndpointSpec{}.ParseBody([]byte(body), false)
		require.NoError(t, err)
		redacted, err := ResponsesEndpointSpec{}.RedactSensitiveInfoFromRequest(req)
		require.NoError(t, err)
		out := mustMarshal(t, redacted)
		require.NotContains(t, out, markerInstr)
		require.NotContains(t, out, markerUser)
		require.NotContains(t, out, markerInput)
		require.Contains(t, out, "[REDACTED")
		require.Contains(t, mustMarshal(t, req), markerInstr, "original must not be mutated")
	})

	t.Run("input as item array", func(t *testing.T) {
		const marker = "marker-array-input-content"
		body := `{"model":"gpt-4o","input":[{"type":"message","role":"user","content":"` + marker + `"}]}`
		_, req, _, _, err := ResponsesEndpointSpec{}.ParseBody([]byte(body), false)
		require.NoError(t, err)
		redacted, err := ResponsesEndpointSpec{}.RedactSensitiveInfoFromRequest(req)
		require.NoError(t, err)
		out := mustMarshal(t, redacted)
		require.NotContains(t, out, marker)
		require.Contains(t, out, "[REDACTED")
	})
}

func TestMessagesEndpointSpec_RedactSensitiveInfoFromRequest(t *testing.T) {
	t.Run("string content + string system", func(t *testing.T) {
		const markerContent = "marker-user-message"
		const markerSystem = "marker-system-prompt"
		body := `{"model":"claude-3-5-sonnet","max_tokens":10,"messages":[{"role":"user","content":"` + markerContent + `"}],"system":"` + markerSystem + `"}`
		_, req, _, _, err := MessagesEndpointSpec{}.ParseBody([]byte(body), false)
		require.NoError(t, err)
		redacted, err := MessagesEndpointSpec{}.RedactSensitiveInfoFromRequest(req)
		require.NoError(t, err)
		out := mustMarshal(t, redacted)
		require.NotContains(t, out, markerContent)
		require.NotContains(t, out, markerSystem)
		require.Contains(t, out, "[REDACTED")
		require.Contains(t, mustMarshal(t, req), markerContent, "original must not be mutated")
	})

	t.Run("array content blocks", func(t *testing.T) {
		const marker = "marker-block-text"
		body := `{"model":"claude-3-5-sonnet","max_tokens":10,"messages":[{"role":"user","content":[{"type":"text","text":"` + marker + `"}]}]}`
		_, req, _, _, err := MessagesEndpointSpec{}.ParseBody([]byte(body), false)
		require.NoError(t, err)
		redacted, err := MessagesEndpointSpec{}.RedactSensitiveInfoFromRequest(req)
		require.NoError(t, err)
		out := mustMarshal(t, redacted)
		require.NotContains(t, out, marker)
		require.Contains(t, out, "[REDACTED")
	})
}

func TestRerankEndpointSpec_RedactSensitiveInfoFromRequest(t *testing.T) {
	const markerQ = "marker-query"
	const markerDoc = "marker-document-content"
	req := &cohereschema.RerankV2Request{Model: "rerank-v3.5", Query: markerQ, Documents: []string{markerDoc, "another"}}
	redacted, err := RerankEndpointSpec{}.RedactSensitiveInfoFromRequest(req)
	require.NoError(t, err)
	require.Contains(t, redacted.Query, "[REDACTED LENGTH=")
	require.Len(t, redacted.Documents, 2)
	require.Contains(t, redacted.Documents[0], "[REDACTED LENGTH=")
	require.NotEqual(t, markerQ, redacted.Query)
	require.NotEqual(t, markerDoc, redacted.Documents[0])
	require.Equal(t, markerQ, req.Query, "original must not be mutated")
	require.Equal(t, markerDoc, req.Documents[0], "original must not be mutated")
}

func TestTokenizeEndpointSpec_RedactSensitiveInfoFromRequest(t *testing.T) {
	t.Run("completion prompt", func(t *testing.T) {
		const marker = "tokenize-this-marker-prompt"
		_, req, _, _, err := TokenizeEndpointSpec{}.ParseBody([]byte(`{"model":"m","prompt":"`+marker+`"}`), false)
		require.NoError(t, err)
		redacted, err := TokenizeEndpointSpec{}.RedactSensitiveInfoFromRequest(req)
		require.NoError(t, err)
		out := mustMarshal(t, redacted)
		require.NotContains(t, out, marker)
		require.Contains(t, out, "[REDACTED")
		require.Contains(t, mustMarshal(t, req), marker, "original must not be mutated")
	})

	t.Run("chat messages", func(t *testing.T) {
		const marker = "tokenize-chat-marker"
		_, req, _, _, err := TokenizeEndpointSpec{}.ParseBody([]byte(`{"model":"m","messages":[{"role":"user","content":"`+marker+`"}]}`), false)
		require.NoError(t, err)
		redacted, err := TokenizeEndpointSpec{}.RedactSensitiveInfoFromRequest(req)
		require.NoError(t, err)
		out := mustMarshal(t, redacted)
		require.NotContains(t, out, marker)
		require.Contains(t, out, "[REDACTED")
	})
}

// mustMarshal marshals v and returns its string form, failing the test on error.
func mustMarshal(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return string(b)
}

// errMarshaler always fails to MarshalJSON, to exercise the fail-safe error
// branches of redactInterfaceValue and redactUnionField.
type errMarshaler struct{}

func (errMarshaler) MarshalJSON() ([]byte, error) { return nil, errors.New("marshal-unsupported") }

func TestRedactInterfaceValue_MarshalError(t *testing.T) {
	// A value that cannot be marshaled must yield a placeholder, never a panic
	// and never the raw value.
	out := redactInterfaceValue(errMarshaler{})
	s, ok := out.(string)
	require.True(t, ok, "expected a placeholder string on marshal error")
	require.Contains(t, s, "[REDACTED")

	// A func value is also unmarshalable to JSON.
	out = redactInterfaceValue(func() {})
	s, ok = out.(string)
	require.True(t, ok)
	require.Contains(t, s, "[REDACTED")
}

func TestRedactUnionField_MarshalError(t *testing.T) {
	// A field whose MarshalJSON errors must yield the zero value of T (fail-safe),
	// so the field logs as absent rather than leaking content.
	out := redactUnionField(errMarshaler{})
	require.Equal(t, errMarshaler{}, out)
}

func TestMessagesCountTokensEndpointSpec_ParseBody(t *testing.T) {
	spec := MessagesCountTokensEndpointSpec{}

	t.Run("invalid json", func(t *testing.T) {
		_, _, _, _, err := spec.ParseBody([]byte("["), false)
		require.ErrorContains(t, err, "malformed request")
	})

	t.Run("missing model", func(t *testing.T) {
		body, err := json.Marshal(map[string]any{"messages": []any{}})
		require.NoError(t, err)

		_, _, _, _, err = spec.ParseBody(body, false)
		require.ErrorContains(t, err, "model field is required")
	})

	t.Run("success", func(t *testing.T) {
		body, err := json.Marshal(map[string]any{
			"model":    "claude-opus-4-6",
			"messages": []any{map[string]any{"role": "user", "content": "hello"}},
		})
		require.NoError(t, err)

		model, parsed, stream, mutated, err := spec.ParseBody(body, false)
		require.NoError(t, err)
		require.Equal(t, "claude-opus-4-6", model)
		require.False(t, stream) // count_tokens is never streaming
		require.NotNil(t, parsed)
		require.Nil(t, mutated)
	})
}

func TestMessagesCountTokensEndpointSpec_GetTranslator(t *testing.T) {
	spec := MessagesCountTokensEndpointSpec{}
	for _, schema := range []filterapi.VersionedAPISchema{
		{Name: filterapi.APISchemaGCPAnthropic},
		{Name: filterapi.APISchemaAWSAnthropic},
		{Name: filterapi.APISchemaAnthropic},
	} {
		translator, err := spec.GetTranslator(schema, "override")
		require.NoError(t, err)
		require.NotNil(t, translator)
	}

	_, err := spec.GetTranslator(filterapi.VersionedAPISchema{Name: filterapi.APISchemaOpenAI}, "override")
	require.ErrorContains(t, err, "unsupported")
}
