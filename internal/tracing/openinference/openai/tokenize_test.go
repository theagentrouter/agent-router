// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package openai

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/codes"
	oteltrace "go.opentelemetry.io/otel/trace"

	"github.com/envoyproxy/ai-gateway/internal/apischema/openai"
	"github.com/envoyproxy/ai-gateway/internal/apischema/openai/tokenize"
	"github.com/envoyproxy/ai-gateway/internal/json"
	"github.com/envoyproxy/ai-gateway/internal/testing/testotel"
	"github.com/envoyproxy/ai-gateway/internal/tracing/openinference"
)

func TestNewTokenizeRecorderFromEnv(t *testing.T) {
	recorder := NewTokenizeRecorderFromEnv()
	require.NotNil(t, recorder)

	// Verify it's the correct concrete type
	concreteRecorder, ok := recorder.(*TokenizeRecorder)
	require.True(t, ok, "Expected *TokenizeRecorder type")
	require.NotNil(t, concreteRecorder.traceConfig, "TraceConfig should be initialized")
}

func TestNewTokenizeRecorder(t *testing.T) {
	t.Run("with nil config", func(t *testing.T) {
		recorder := NewTokenizeRecorder(nil)
		require.NotNil(t, recorder)

		concreteRecorder := recorder.(*TokenizeRecorder)
		require.NotNil(t, concreteRecorder.traceConfig, "Should create default config when nil provided")
	})

	t.Run("with custom config", func(t *testing.T) {
		customConfig := &openinference.TraceConfig{
			HideInputs:  true,
			HideOutputs: true,
		}

		recorder := NewTokenizeRecorder(customConfig)
		require.NotNil(t, recorder)

		concreteRecorder := recorder.(*TokenizeRecorder)
		require.Equal(t, customConfig, concreteRecorder.traceConfig, "Should use provided config")
	})
}

func TestTokenizeRecorder_StartParams(t *testing.T) {
	recorder := NewTokenizeRecorder(nil).(*TokenizeRecorder)

	tests := []struct {
		name string
		req  *tokenize.RequestUnion
		body []byte
	}{
		{
			name: "chat request",
			req: &tokenize.RequestUnion{
				ChatRequest: &tokenize.ChatRequest{
					Model: "gpt-4",
					Messages: []openai.ChatCompletionMessageParamUnion{
						{
							OfUser: &openai.ChatCompletionUserMessageParam{
								Content: openai.StringOrUserRoleContentUnion{Value: "Hello"},
								Role:    openai.ChatMessageRoleUser,
							},
						},
					},
				},
			},
			body: []byte(`{"model":"gpt-4","messages":[{"role":"user","content":"Hello"}]}`),
		},
		{
			name: "completion request",
			req: &tokenize.RequestUnion{
				CompletionRequest: &tokenize.CompletionRequest{
					Model:  "gpt-4",
					Prompt: "Hello world",
				},
			},
			body: []byte(`{"model":"gpt-4","prompt":"Hello world"}`),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spanName, opts := recorder.StartParams(tt.req, tt.body)

			assert.Equal(t, "Tokenize", spanName, "Span name should be 'Tokenize'")
			assert.NotNil(t, opts, "Span options should not be nil")
			assert.Len(t, opts, 1, "Should have exactly one span option (SpanKindInternal)")

			// Test that the span can be created successfully
			actualSpan := testotel.RecordNewSpan(t, spanName, opts...)
			require.Equal(t, "Tokenize", actualSpan.Name)
		})
	}
}

func TestTokenizeRecorder_RecordRequest(t *testing.T) {
	tests := []struct {
		name           string
		req            *tokenize.RequestUnion
		body           string
		config         *openinference.TraceConfig
		expectedAttrs  map[string]interface{}
		shouldHideBody bool
	}{
		{
			name: "chat request with all attributes",
			req: &tokenize.RequestUnion{
				ChatRequest: &tokenize.ChatRequest{
					Model:                "gpt-4",
					AddGenerationPrompt:  boolPtr(true),
					ContinueFinalMessage: false,
					AddSpecialTokens:     true,
					ReturnTokenStrs:      boolPtr(true),
					Messages: []openai.ChatCompletionMessageParamUnion{
						{
							OfUser: &openai.ChatCompletionUserMessageParam{
								Content: openai.StringOrUserRoleContentUnion{Value: "Hello"},
								Role:    openai.ChatMessageRoleUser,
							},
						},
						{
							OfAssistant: &openai.ChatCompletionAssistantMessageParam{
								Content: openai.StringOrAssistantRoleContentUnion{Value: "Hi there!"},
								Role:    openai.ChatMessageRoleAssistant,
							},
						},
					},
				},
			},
			body: `{"model":"gpt-4","messages":[...]}`,
			config: &openinference.TraceConfig{
				HideInputs:  false,
				HideOutputs: false,
			},
			expectedAttrs: map[string]interface{}{
				openinference.SpanKind:            openinference.SpanKindTokenCounter,
				openinference.LLMSystem:           openinference.LLMSystemOpenAI,
				openinference.LLMModelName:        "gpt-4",
				openinference.InputValue:          `{"model":"gpt-4","messages":[...]}`,
				openinference.InputMimeType:       openinference.MimeTypeJSON,
				"tokenize.request_type":           "chat",
				"tokenize.add_generation_prompt":  true,
				"tokenize.continue_final_message": false,
				"tokenize.add_special_tokens":     true,
				"tokenize.return_token_strs":      true,
				"tokenize.message_count":          int64(2), // OpenTelemetry returns int64
			},
			shouldHideBody: false,
		},
		{
			name: "completion request",
			req: &tokenize.RequestUnion{
				CompletionRequest: &tokenize.CompletionRequest{
					Model:            "gpt-3.5-turbo",
					Prompt:           "Complete this",
					AddSpecialTokens: boolPtr(false),
					ReturnTokenStrs:  boolPtr(false),
				},
			},
			body: `{"model":"gpt-3.5-turbo","prompt":"Complete this"}`,
			config: &openinference.TraceConfig{
				HideInputs:  false,
				HideOutputs: false,
			},
			expectedAttrs: map[string]interface{}{
				openinference.SpanKind:        openinference.SpanKindTokenCounter,
				openinference.LLMSystem:       openinference.LLMSystemOpenAI,
				openinference.LLMModelName:    "gpt-3.5-turbo",
				openinference.InputValue:      `{"model":"gpt-3.5-turbo","prompt":"Complete this"}`,
				openinference.InputMimeType:   openinference.MimeTypeJSON,
				"tokenize.request_type":       "completion",
				"tokenize.add_special_tokens": false,
				"tokenize.return_token_strs":  false,
			},
			shouldHideBody: false,
		},
		{
			name: "hidden inputs",
			req: &tokenize.RequestUnion{
				ChatRequest: &tokenize.ChatRequest{
					Model: "gpt-4",
					Messages: []openai.ChatCompletionMessageParamUnion{
						{
							OfUser: &openai.ChatCompletionUserMessageParam{
								Content: openai.StringOrUserRoleContentUnion{Value: "Sensitive data"},
								Role:    openai.ChatMessageRoleUser,
							},
						},
					},
				},
			},
			body: `{"model":"gpt-4","messages":[{"role":"user","content":"Sensitive data"}]}`,
			config: &openinference.TraceConfig{
				HideInputs:  true,
				HideOutputs: false,
			},
			expectedAttrs: map[string]interface{}{
				openinference.SpanKind:     openinference.SpanKindTokenCounter,
				openinference.LLMSystem:    openinference.LLMSystemOpenAI,
				openinference.LLMModelName: "gpt-4",
				"tokenize.request_type":    "chat",
				"tokenize.message_count":   int64(1),
			},
			shouldHideBody: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := NewTokenizeRecorder(tt.config).(*TokenizeRecorder)

			// Use testotel to create a real span for testing
			actualSpan := testotel.RecordWithSpan(t, func(span oteltrace.Span) bool {
				recorder.RecordRequest(span, tt.req, []byte(tt.body))
				return false // Let testotel handle ending the span
			})

			// Convert span attributes to map for easier testing
			attrMap := make(map[string]interface{})
			for _, attr := range actualSpan.Attributes {
				attrMap[string(attr.Key)] = attr.Value.AsInterface()
			}

			// Check all expected attributes
			for key, expectedValue := range tt.expectedAttrs {
				actualValue, exists := attrMap[key]
				require.True(t, exists, "Expected attribute %s not found", key)
				assert.Equal(t, expectedValue, actualValue, "Attribute %s has wrong value", key)
			}

			// Check input hiding behavior
			if tt.shouldHideBody {
				assert.NotContains(t, attrMap, openinference.InputValue, "Input should be hidden")
			}
		})
	}
}

func TestTokenizeRecorder_RecordResponse(t *testing.T) {
	tests := []struct {
		name       string
		config     *openinference.TraceConfig
		resp       *tokenize.Response
		expectBody bool
	}{
		{
			name: "outputs visible",
			config: &openinference.TraceConfig{
				HideOutputs: false,
			},
			resp: &tokenize.Response{
				Count:  25,
				Tokens: []int{1, 2, 3},
			},
			expectBody: true,
		},
		{
			name: "outputs hidden",
			config: &openinference.TraceConfig{
				HideOutputs: true,
			},
			resp: &tokenize.Response{
				Count:  25,
				Tokens: []int{1, 2, 3},
			},
			expectBody: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := NewTokenizeRecorder(tt.config).(*TokenizeRecorder)

			// Use testotel to create a real span for testing
			actualSpan := testotel.RecordWithSpan(t, func(span oteltrace.Span) bool {
				recorder.RecordResponse(span, tt.resp)
				return false // Let testotel handle ending the span
			})

			// Verify basic span properties
			require.Equal(t, codes.Ok, actualSpan.Status.Code, "Status should be OK")

			// Convert span attributes to map for easier testing
			attrMap := make(map[string]interface{})
			for _, attr := range actualSpan.Attributes {
				attrMap[string(attr.Key)] = attr.Value.AsInterface()
			}

			// Check that basic response attributes are set
			assert.Equal(t, int64(tt.resp.Count), attrMap["tokenize.token_count"], "Token count should match")
			assert.Equal(t, openinference.MimeTypeJSON, attrMap[openinference.OutputMimeType], "Output MIME type should be JSON")

			// Check if body is properly handled based on config
			outputValue, hasOutputValue := attrMap[openinference.OutputValue]
			require.True(t, hasOutputValue, "Output value attribute should be set")

			if tt.expectBody {
				// Should contain actual JSON response
				var resp tokenize.Response
				err := json.Unmarshal([]byte(outputValue.(string)), &resp)
				require.NoError(t, err, "Output value should be valid JSON")
				assert.Equal(t, tt.resp.Count, resp.Count, "Response should match")
			} else {
				// Should be redacted
				assert.Equal(t, openinference.RedactedValue, outputValue, "Output should be redacted")
			}
		})
	}
}

func TestTokenizeRecorder_RecordResponseOnError(t *testing.T) {
	recorder := NewTokenizeRecorder(nil).(*TokenizeRecorder)

	// Use testotel to create a real span for testing
	actualSpan := testotel.RecordWithSpan(t, func(span oteltrace.Span) bool {
		recorder.RecordResponseOnError(span, 400, []byte(`{"error":"Bad request"}`))
		return false // Let testotel handle ending the span
	})

	// Verify error status was set
	require.Equal(t, codes.Error, actualSpan.Status.Code, "Status should be Error")

	// Check that an exception event was added
	require.Len(t, actualSpan.Events, 1, "Should have exactly one exception event")
	event := actualSpan.Events[0]
	assert.Equal(t, "exception", event.Name, "Event should be named 'exception'")

	// Convert event attributes to map for easier testing
	eventAttrs := make(map[string]interface{})
	for _, attr := range event.Attributes {
		eventAttrs[string(attr.Key)] = attr.Value.AsInterface()
	}

	// Check exception attributes
	assert.Equal(t, "BadRequestError", eventAttrs["exception.type"], "Exception type should be set")
	assert.Equal(t, `Error code: 400 - {"error":"Bad request"}`, eventAttrs["exception.message"], "Exception message should be set")
}

// Helper function to create bool pointers
func boolPtr(b bool) *bool {
	return &b
}
