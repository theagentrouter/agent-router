// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package openai

import (
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/sdk/trace"
	oteltrace "go.opentelemetry.io/otel/trace"

	"github.com/envoyproxy/ai-gateway/internal/apischema/openai"
	"github.com/envoyproxy/ai-gateway/internal/json"
	"github.com/envoyproxy/ai-gateway/internal/testing/testotel"
	"github.com/envoyproxy/ai-gateway/internal/tracing/openinference"
)

func TestChatCompletionRecorder_WithConfig_HideInputs(t *testing.T) {
	tests := []struct {
		name          string
		config        *openinference.TraceConfig
		req           *openai.ChatCompletionRequest
		reqBody       []byte
		expectedAttrs []attribute.KeyValue
	}{
		{
			name: "hide input value",
			config: &openinference.TraceConfig{
				HideInputs: true,
			},
			req:     basicReq,
			reqBody: basicReqBody,
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.SpanKind, openinference.SpanKindLLM),
				attribute.String(openinference.LLMSystem, openinference.LLMSystemOpenAI),
				attribute.String(openinference.LLMModelName, openai.ModelGPT5Nano),
				attribute.String(openinference.InputValue, openinference.RedactedValue),
				// No InputMimeType when input is hidden.
				attribute.String(openinference.LLMInvocationParameters, `{"model":"gpt-5-nano"}`),
				// No input messages when HideInputs is true.
			},
		},
		{
			name: "hide input messages only",
			config: &openinference.TraceConfig{
				HideInputMessages: true,
			},
			req:     basicReq,
			reqBody: basicReqBody,
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.SpanKind, openinference.SpanKindLLM),
				attribute.String(openinference.LLMSystem, openinference.LLMSystemOpenAI),
				attribute.String(openinference.LLMModelName, openai.ModelGPT5Nano),
				attribute.String(openinference.InputValue, string(basicReqBody)),
				attribute.String(openinference.InputMimeType, openinference.MimeTypeJSON),
				attribute.String(openinference.LLMInvocationParameters, `{"model":"gpt-5-nano"}`),
				// No input messages when HideInputMessages is true.
			},
		},
		{
			name: "hide input text but show messages",
			config: &openinference.TraceConfig{
				HideInputText: true,
			},
			req:     basicReq,
			reqBody: basicReqBody,
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.SpanKind, openinference.SpanKindLLM),
				attribute.String(openinference.LLMSystem, openinference.LLMSystemOpenAI),
				attribute.String(openinference.LLMModelName, openai.ModelGPT5Nano),
				attribute.String(openinference.InputValue, string(basicReqBody)),
				attribute.String(openinference.InputMimeType, openinference.MimeTypeJSON),
				attribute.String(openinference.LLMInvocationParameters, `{"model":"gpt-5-nano"}`),
				attribute.String(openinference.InputMessageAttribute(0, openinference.MessageRole), openai.ChatMessageRoleUser),
				attribute.String(openinference.InputMessageAttribute(0, openinference.MessageContent), openinference.RedactedValue),
			},
		},
		{
			name: "hide invocation parameters",
			config: &openinference.TraceConfig{
				HideLLMInvocationParameters: true,
			},
			req:     basicReq,
			reqBody: basicReqBody,
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.SpanKind, openinference.SpanKindLLM),
				attribute.String(openinference.LLMSystem, openinference.LLMSystemOpenAI),
				attribute.String(openinference.LLMModelName, openai.ModelGPT5Nano),
				attribute.String(openinference.InputValue, string(basicReqBody)),
				attribute.String(openinference.InputMimeType, openinference.MimeTypeJSON),
				// No LLMInvocationParameters when hidden.
				attribute.String(openinference.InputMessageAttribute(0, openinference.MessageRole), openai.ChatMessageRoleUser),
				attribute.String(openinference.InputMessageAttribute(0, openinference.MessageContent), "Hello!"),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := NewChatCompletionRecorder(tt.config)

			actualSpan := testotel.RecordWithSpan(t, func(span oteltrace.Span) bool {
				recorder.RecordRequest(span, tt.req, tt.reqBody)
				return false
			})

			openinference.RequireAttributesEqual(t, tt.expectedAttrs, actualSpan.Attributes)
		})
	}
}

func TestChatCompletionRecorder_WithConfig_HideOutputs(t *testing.T) {
	tests := []struct {
		name           string
		config         *openinference.TraceConfig
		respBody       []byte
		expectedAttrs  []attribute.KeyValue
		expectedStatus trace.Status
	}{
		{
			name: "hide output value",
			config: &openinference.TraceConfig{
				HideOutputs: true,
			},
			respBody: basicRespBody,
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.LLMModelName, openai.ModelGPT5Nano),
				// No OutputMimeType when output is hidden.
				// No output messages when HideOutputs is true.
				// Token counts are still included as metadata.
				attribute.Int(openinference.LLMTokenCountPrompt, 20),
				attribute.Int(openinference.LLMTokenCountCompletion, 10),
				attribute.Int(openinference.LLMTokenCountTotal, 30),
				attribute.String(openinference.OutputValue, openinference.RedactedValue),
			},
			expectedStatus: trace.Status{Code: codes.Ok, Description: ""},
		},
		{
			name: "hide output messages only",
			config: &openinference.TraceConfig{
				HideOutputMessages: true,
			},
			respBody: basicRespBody,
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.LLMModelName, openai.ModelGPT5Nano),
				attribute.String(openinference.OutputMimeType, openinference.MimeTypeJSON),
				// No output messages when HideOutputMessages is true.
				attribute.Int(openinference.LLMTokenCountPrompt, 20),
				attribute.Int(openinference.LLMTokenCountCompletion, 10),
				attribute.Int(openinference.LLMTokenCountTotal, 30),
				attribute.String(openinference.OutputValue, string(basicRespBody)),
			},
			expectedStatus: trace.Status{Code: codes.Ok, Description: ""},
		},
		{
			name: "hide output text but show messages",
			config: &openinference.TraceConfig{
				HideOutputText: true,
			},
			respBody: basicRespBody,
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.LLMModelName, openai.ModelGPT5Nano),
				attribute.String(openinference.OutputMimeType, openinference.MimeTypeJSON),
				attribute.String(openinference.OutputMessageAttribute(0, openinference.MessageRole), "assistant"),
				attribute.String(openinference.OutputMessageAttribute(0, openinference.MessageContent), openinference.RedactedValue),
				attribute.Int(openinference.LLMTokenCountPrompt, 20),
				attribute.Int(openinference.LLMTokenCountCompletion, 10),
				attribute.Int(openinference.LLMTokenCountTotal, 30),
				attribute.String(openinference.OutputValue, string(basicRespBody)),
			},
			expectedStatus: trace.Status{Code: codes.Ok, Description: ""},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := NewChatCompletionRecorder(tt.config)

			actualSpan := testotel.RecordWithSpan(t, func(span oteltrace.Span) bool {
				resp := &openai.ChatCompletionResponse{}
				err := json.Unmarshal(tt.respBody, resp)
				require.NoError(t, err)
				recorder.RecordResponse(span, resp)
				return false
			})

			openinference.RequireAttributesEqual(t, tt.expectedAttrs, actualSpan.Attributes)
			require.Equal(t, tt.expectedStatus, actualSpan.Status)
		})
	}
}

func TestChatCompletionRecorder_WithConfig_HideImages(t *testing.T) {
	// Create a multimodal request with text and image.
	multimodalReq := &openai.ChatCompletionRequest{
		Model: openai.ModelGPT5Nano,
		Messages: []openai.ChatCompletionMessageParamUnion{{
			OfUser: &openai.ChatCompletionUserMessageParam{
				Role: openai.ChatMessageRoleUser,
				Content: openai.StringOrUserRoleContentUnion{
					Value: []openai.ChatCompletionContentPartUserUnionParam{
						{OfText: &openai.ChatCompletionContentPartTextParam{
							Text: "What is in this image?",
							Type: "text",
						}},
						{OfImageURL: &openai.ChatCompletionContentPartImageParam{
							ImageURL: openai.ChatCompletionContentPartImageImageURLParam{
								URL: "https://example.com/image.jpg",
							},
							Type: "image_url",
						}},
					},
				},
			},
		}},
	}
	multimodalReqBody := mustJSON(multimodalReq)

	tests := []struct {
		name          string
		config        *openinference.TraceConfig
		req           *openai.ChatCompletionRequest
		reqBody       []byte
		expectedAttrs []attribute.KeyValue
	}{
		{
			name: "hide input images",
			config: &openinference.TraceConfig{
				HideInputImages: true,
			},
			req:     multimodalReq,
			reqBody: multimodalReqBody,
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.SpanKind, openinference.SpanKindLLM),
				attribute.String(openinference.LLMSystem, openinference.LLMSystemOpenAI),
				attribute.String(openinference.LLMModelName, openai.ModelGPT5Nano),
				attribute.String(openinference.InputValue, string(multimodalReqBody)),
				attribute.String(openinference.InputMimeType, openinference.MimeTypeJSON),
				attribute.String(openinference.LLMInvocationParameters, `{"model":"gpt-5-nano"}`),
				attribute.String(openinference.InputMessageAttribute(0, openinference.MessageRole), openai.ChatMessageRoleUser),
				attribute.String(openinference.InputMessageContentAttribute(0, 0, "text"), "What is in this image?"),
				attribute.String(openinference.InputMessageContentAttribute(0, 0, "type"), "text"),
				// No image content when HideInputImages is true.
			},
		},
		{
			name:    "show input images by default",
			config:  &openinference.TraceConfig{},
			req:     multimodalReq,
			reqBody: multimodalReqBody,
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.SpanKind, openinference.SpanKindLLM),
				attribute.String(openinference.LLMSystem, openinference.LLMSystemOpenAI),
				attribute.String(openinference.LLMModelName, openai.ModelGPT5Nano),
				attribute.String(openinference.InputValue, string(multimodalReqBody)),
				attribute.String(openinference.InputMimeType, openinference.MimeTypeJSON),
				attribute.String(openinference.LLMInvocationParameters, `{"model":"gpt-5-nano"}`),
				attribute.String(openinference.InputMessageAttribute(0, openinference.MessageRole), openai.ChatMessageRoleUser),
				attribute.String(openinference.InputMessageContentAttribute(0, 0, "text"), "What is in this image?"),
				attribute.String(openinference.InputMessageContentAttribute(0, 0, "type"), "text"),
				attribute.String(openinference.InputMessageContentAttribute(0, 1, "image.image.url"), "https://example.com/image.jpg"),
				attribute.String(openinference.InputMessageContentAttribute(0, 1, "type"), "image"),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := NewChatCompletionRecorder(tt.config)

			actualSpan := testotel.RecordWithSpan(t, func(span oteltrace.Span) bool {
				recorder.RecordRequest(span, tt.req, tt.reqBody)
				return false
			})

			openinference.RequireAttributesEqual(t, tt.expectedAttrs, actualSpan.Attributes)
		})
	}
}

func TestChatCompletionRecorder_WithConfig_Base64ImageMaxLength(t *testing.T) {
	// Create a request with a base64 image.
	base64ImageReq := &openai.ChatCompletionRequest{
		Model: openai.ModelGPT5Nano,
		Messages: []openai.ChatCompletionMessageParamUnion{{
			OfUser: &openai.ChatCompletionUserMessageParam{
				Role: openai.ChatMessageRoleUser,
				Content: openai.StringOrUserRoleContentUnion{
					Value: []openai.ChatCompletionContentPartUserUnionParam{
						{OfImageURL: &openai.ChatCompletionContentPartImageParam{
							ImageURL: openai.ChatCompletionContentPartImageImageURLParam{
								URL: "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAUAAAAFCAYAAACNbyblAAAAHElEQVQI12P4//8/w38GIAXDIBKE0DHxgljNBAAO9TXL0Y4OHwAAAABJRU5ErkJggg==",
							},
							Type: "image_url",
						}},
					},
				},
			},
		}},
	}
	base64ImageReqBody := mustJSON(base64ImageReq)

	tests := []struct {
		name          string
		config        *openinference.TraceConfig
		req           *openai.ChatCompletionRequest
		reqBody       []byte
		expectedAttrs []attribute.KeyValue
	}{
		{
			name: "base64 image exceeds max length",
			config: &openinference.TraceConfig{
				Base64ImageMaxLength: 50, // Set very low limit.
			},
			req:     base64ImageReq,
			reqBody: base64ImageReqBody,
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.SpanKind, openinference.SpanKindLLM),
				attribute.String(openinference.LLMSystem, openinference.LLMSystemOpenAI),
				attribute.String(openinference.LLMModelName, openai.ModelGPT5Nano),
				attribute.String(openinference.InputValue, string(base64ImageReqBody)),
				attribute.String(openinference.InputMimeType, openinference.MimeTypeJSON),
				attribute.String(openinference.LLMInvocationParameters, `{"model":"gpt-5-nano"}`),
				attribute.String(openinference.InputMessageAttribute(0, openinference.MessageRole), openai.ChatMessageRoleUser),
				attribute.String(openinference.InputMessageContentAttribute(0, 0, "image.image.url"), openinference.RedactedValue),
				attribute.String(openinference.InputMessageContentAttribute(0, 0, "type"), "image"),
			},
		},
		{
			name: "base64 image within max length",
			config: &openinference.TraceConfig{
				Base64ImageMaxLength: 200, // Set high limit.
			},
			req:     base64ImageReq,
			reqBody: base64ImageReqBody,
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.SpanKind, openinference.SpanKindLLM),
				attribute.String(openinference.LLMSystem, openinference.LLMSystemOpenAI),
				attribute.String(openinference.LLMModelName, openai.ModelGPT5Nano),
				attribute.String(openinference.InputValue, string(base64ImageReqBody)),
				attribute.String(openinference.InputMimeType, openinference.MimeTypeJSON),
				attribute.String(openinference.LLMInvocationParameters, `{"model":"gpt-5-nano"}`),
				attribute.String(openinference.InputMessageAttribute(0, openinference.MessageRole), openai.ChatMessageRoleUser),
				attribute.String(openinference.InputMessageContentAttribute(0, 0, "image.image.url"), "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAUAAAAFCAYAAACNbyblAAAAHElEQVQI12P4//8/w38GIAXDIBKE0DHxgljNBAAO9TXL0Y4OHwAAAABJRU5ErkJggg=="),
				attribute.String(openinference.InputMessageContentAttribute(0, 0, "type"), "image"),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := NewChatCompletionRecorder(tt.config)

			actualSpan := testotel.RecordWithSpan(t, func(span oteltrace.Span) bool {
				recorder.RecordRequest(span, tt.req, tt.reqBody)
				return false
			})

			openinference.RequireAttributesEqual(t, tt.expectedAttrs, actualSpan.Attributes)
		})
	}
}

func TestChatCompletionRecorder_WithConfig_NoJSONMarshalWhenHidden(t *testing.T) {
	// Test that we don't do unnecessary work when attributes are hidden.
	config := &openinference.TraceConfig{
		HideLLMInvocationParameters: true,
		HideInputs:                  true,
		HideOutputs:                 true,
	}

	recorder := NewChatCompletionRecorder(config)

	// Create a request that would fail JSON marshaling if attempted.
	invalidReq := &openai.ChatCompletionRequest{
		Model: openai.ModelGPT5Nano,
		Messages: []openai.ChatCompletionMessageParamUnion{{
			OfUser: &openai.ChatCompletionUserMessageParam{
				Content: openai.StringOrUserRoleContentUnion{Value: "Hello!"},
				Role:    openai.ChatMessageRoleUser,
			},
		}},
	}

	actualSpan := testotel.RecordWithSpan(t, func(span oteltrace.Span) bool {
		// This should not panic or error even though invocation params are hidden.
		recorder.RecordRequest(span, invalidReq, []byte(`{"model":"test"}`))
		return false
	})

	// Verify minimal attributes are set.
	expectedAttrs := []attribute.KeyValue{
		attribute.String(openinference.SpanKind, openinference.SpanKindLLM),
		attribute.String(openinference.LLMSystem, openinference.LLMSystemOpenAI),
		attribute.String(openinference.LLMModelName, openai.ModelGPT5Nano),
		attribute.String(openinference.InputValue, openinference.RedactedValue),
		// No InputMimeType, no invocation params, no messages.
	}

	openinference.RequireAttributesEqual(t, expectedAttrs, actualSpan.Attributes)
}

func TestChatCompletionRecorder_ConfigFromEnvironment(t *testing.T) {
	// Test that recorder uses environment variables when config is nil.
	t.Setenv(openinference.EnvHideInputs, "true")
	t.Setenv(openinference.EnvHideOutputs, "true")

	recorder := NewChatCompletionRecorderFromEnv()

	// Request test.
	reqSpan := testotel.RecordWithSpan(t, func(span oteltrace.Span) bool {
		recorder.RecordRequest(span, basicReq, basicReqBody)
		return false
	})

	// Verify input is hidden.
	attrs := make(map[string]attribute.Value)
	for _, kv := range reqSpan.Attributes {
		attrs[string(kv.Key)] = kv.Value
	}
	require.Equal(t, openinference.RedactedValue, attrs[openinference.InputValue].AsString())

	// Response test.
	respSpan := testotel.RecordWithSpan(t, func(span oteltrace.Span) bool {
		resp := &openai.ChatCompletionResponse{}
		err := json.Unmarshal(basicRespBody, resp)
		require.NoError(t, err)
		recorder.RecordResponse(span, resp)
		return false
	})

	// Verify output is hidden.
	attrs = make(map[string]attribute.Value)
	for _, kv := range respSpan.Attributes {
		attrs[string(kv.Key)] = kv.Value
	}
	require.Equal(t, openinference.RedactedValue, attrs[openinference.OutputValue].AsString())
}

func TestChatCompletionRecorder_WithConfig_Streaming(t *testing.T) {
	config := &openinference.TraceConfig{
		HideOutputs: true,
	}

	recorder := NewChatCompletionRecorder(config)

	sseBody := `data: {"id":"chatcmpl-123","object":"chat.completion.chunk","created":1702000000,"model":"gpt-5-nano","choices":[{"index":0,"delta":{"role":"assistant","content":"Hello"},"finish_reason":null}]}

data: [DONE]`

	actualSpan := testotel.RecordWithSpan(t, func(span oteltrace.Span) bool {
		recorder.RecordResponseChunks(span, parseSSEToChunks(t, sseBody))
		return false
	})

	// Verify output is hidden.
	attrs := make(map[string]attribute.Value)
	for _, kv := range actualSpan.Attributes {
		attrs[string(kv.Key)] = kv.Value
	}
	require.Equal(t, openinference.RedactedValue, attrs[openinference.OutputValue].AsString())
	// Model name should still be present.
	require.Equal(t, openai.ModelGPT5Nano, attrs[openinference.LLMModelName].AsString())
}

func TestChatCompletionRecorder_WithConfig_ErrorBodyRedaction(t *testing.T) {
	const secret = "SENSITIVE-PROMPT-TEXT"

	tests := []struct {
		name             string
		config           *openinference.TraceConfig
		expectedContains bool
	}{
		{name: "default config records the body", config: openinference.NewTraceConfig(), expectedContains: true},
		{name: "hide inputs redacts the body", config: &openinference.TraceConfig{HideInputs: true}},
		{name: "hide outputs redacts the body", config: &openinference.TraceConfig{HideOutputs: true}},
		{name: "hide inputs and outputs redacts the body", config: &openinference.TraceConfig{HideInputs: true, HideOutputs: true}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := NewChatCompletionRecorder(tt.config)

			actualSpan := testotel.RecordWithSpan(t, func(span oteltrace.Span) bool {
				recorder.RecordResponseOnError(span, 400, []byte(`{"error":"`+secret+`"}`))
				return false
			})

			require.Contains(t, actualSpan.Status.Description, "Error code: 400")
			if tt.expectedContains {
				require.Contains(t, actualSpan.Status.Description, secret)
			} else {
				require.NotContains(t, actualSpan.Status.Description, secret)
			}
		})
	}
}
