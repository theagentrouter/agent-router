// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package openai

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/sdk/trace"
	oteltrace "go.opentelemetry.io/otel/trace"

	"github.com/envoyproxy/ai-gateway/internal/apischema/openai"
	"github.com/envoyproxy/ai-gateway/internal/testing/testotel"
	"github.com/envoyproxy/ai-gateway/internal/tracing/openinference"
)

var (
	basicResponseReq = &openai.ResponseRequest{
		Model: openai.ModelGPT5Nano,
		Input: openai.ResponseNewParamsInputUnion{
			OfString: ptr("Hi"),
		},
	}
	basicResponseReqBody = mustJSON(basicResponseReq)

	basicResponseResp = &openai.Response{
		ID:     "resp-123",
		Model:  openai.ModelGPT5Nano,
		Status: "completed",
		Text: openai.ResponseTextConfig{
			Format: openai.ResponseFormatTextConfigUnionParam{
				OfText: &openai.ResponseFormatTextParam{
					Type: "text",
				},
			},
		},
		Output: []openai.ResponseOutputItemUnion{
			{
				OfOutputMessage: &openai.ResponseOutputMessage{
					ID:   "msg_01",
					Type: "message",
					Role: "assistant",
					Content: openai.ResponseOutputMessageContentUnion{
						OfContentArray: []openai.ResponseOutputMessageContentArrayUnion{
							{
								OfOutputText: &openai.ResponseOutputTextParam{
									Type: "output_text",
									Text: "Hello, how can I help?",
								},
							},
						},
					},
				},
			},
		},
		Usage: &openai.ResponseUsage{
			InputTokens: 20,
			InputTokensDetails: openai.ResponseUsageInputTokensDetails{
				CachedTokens:     2,
				CacheWriteTokens: 4,
			},
			OutputTokens: 10,
			TotalTokens:  30,
		},
	}
	basicResponseRespBody = mustJSON(basicResponseResp)

	responseWithCacheWrite = &openai.Response{
		ID:     "resp-456",
		Model:  openai.ModelGPT5Nano,
		Status: "completed",
		Text: openai.ResponseTextConfig{
			Format: openai.ResponseFormatTextConfigUnionParam{
				OfText: &openai.ResponseFormatTextParam{
					Type: "text",
				},
			},
		},
		Output: []openai.ResponseOutputItemUnion{
			{
				OfOutputMessage: &openai.ResponseOutputMessage{
					ID:   "msg_02",
					Type: "message",
					Role: "assistant",
					Content: openai.ResponseOutputMessageContentUnion{
						OfContentArray: []openai.ResponseOutputMessageContentArrayUnion{
							{
								OfOutputText: &openai.ResponseOutputTextParam{
									Type: "output_text",
									Text: "This response includes cache write tokens.",
								},
							},
						},
					},
				},
			},
		},
		Usage: &openai.ResponseUsage{
			InputTokens: 100,
			InputTokensDetails: openai.ResponseUsageInputTokensDetails{
				CachedTokens:     10,
				CacheWriteTokens: 50,
			},
			OutputTokens: 25,
			TotalTokens:  125,
		},
	}
	responseWithCacheWriteBody = mustJSON(responseWithCacheWrite)

	responseReqWithStreaming = &openai.ResponseRequest{
		Model: openai.ModelGPT5Nano,
		Input: openai.ResponseNewParamsInputUnion{
			OfString: ptr("Hi"),
		},
		Stream: true,
	}
	responseReqWithStreamingBody = mustJSON(responseReqWithStreaming)
)

func TestResponsesRecorder_StartParams(t *testing.T) {
	tests := []struct {
		name             string
		req              *openai.ResponseRequest
		reqBody          []byte
		expectedSpanName string
	}{
		{
			name:             "basic request",
			req:              basicResponseReq,
			reqBody:          basicResponseReqBody,
			expectedSpanName: "Responses",
		},
		{
			name:             "streaming request",
			req:              responseReqWithStreaming,
			reqBody:          responseReqWithStreamingBody,
			expectedSpanName: "Responses",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := NewResponsesRecorderFromEnv()

			spanName, opts := recorder.StartParams(tt.req, tt.reqBody)
			actualSpan := testotel.RecordNewSpan(t, spanName, opts...)

			require.Equal(t, tt.expectedSpanName, actualSpan.Name)
			require.Equal(t, oteltrace.SpanKindInternal, actualSpan.SpanKind)
		})
	}
}

func TestResponsesRecorder_RecordRequest(t *testing.T) {
	tests := []struct {
		name          string
		req           *openai.ResponseRequest
		reqBody       []byte
		expectedAttrs []attribute.KeyValue
	}{
		{
			name:    "basic request",
			req:     basicResponseReq,
			reqBody: basicResponseReqBody,
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.SpanKind, openinference.SpanKindLLM),
				attribute.String(openinference.LLMSystem, openinference.LLMSystemOpenAI),
				attribute.String(openinference.LLMModelName, openai.ModelGPT5Nano),
				attribute.String(openinference.InputValue, string(basicResponseReqBody)),
				attribute.String(openinference.InputMimeType, openinference.MimeTypeJSON),
				attribute.String(openinference.LLMInvocationParameters, fmt.Sprintf("{\"model\":\"%s\"}", basicResponseReq.Model)),
				attribute.String(openinference.InputMessageAttribute(0, openinference.MessageRole), "user"),
				attribute.String(openinference.InputMessageAttribute(0, openinference.MessageContent), *basicResponseReq.Input.OfString),
			},
		},
		{
			name:    "streaming request",
			req:     responseReqWithStreaming,
			reqBody: responseReqWithStreamingBody,
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.SpanKind, openinference.SpanKindLLM),
				attribute.String(openinference.LLMSystem, openinference.LLMSystemOpenAI),
				attribute.String(openinference.LLMModelName, openai.ModelGPT5Nano),
				attribute.String(openinference.InputValue, string(responseReqWithStreamingBody)),
				attribute.String(openinference.InputMimeType, openinference.MimeTypeJSON),
				attribute.String(openinference.LLMInvocationParameters, fmt.Sprintf("{\"model\":\"%s\",\"stream\":true}", responseReqWithStreaming.Model)),
				attribute.String(openinference.InputMessageAttribute(0, openinference.MessageRole), "user"),
				attribute.String(openinference.InputMessageAttribute(0, openinference.MessageContent), *responseReqWithStreaming.Input.OfString),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := NewResponsesRecorderFromEnv()

			actualSpan := testotel.RecordWithSpan(t, func(span oteltrace.Span) bool {
				recorder.RecordRequest(span, tt.req, tt.reqBody)
				return false
			})

			openinference.RequireAttributesEqual(t, tt.expectedAttrs, actualSpan.Attributes)
			require.Empty(t, actualSpan.Events)
			require.Equal(t, trace.Status{Code: codes.Unset, Description: ""}, actualSpan.Status)
		})
	}
}

func TestResponsesRecorder_RecordResponse(t *testing.T) {
	tests := []struct {
		name           string
		resp           *openai.Response
		config         *openinference.TraceConfig
		expectedAttrs  []attribute.KeyValue
		expectedStatus trace.Status
	}{
		{
			name:   "successful response",
			resp:   basicResponseResp,
			config: &openinference.TraceConfig{},
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.OutputMimeType, openinference.MimeTypeJSON),
				attribute.String(openinference.LLMModelName, openai.ModelGPT5Nano),
				attribute.Int(openinference.LLMTokenCountPrompt, 20),
				attribute.Int(openinference.LLMTokenCountCompletion, 10),
				attribute.Int(openinference.LLMTokenCountTotal, 30),
				attribute.Int(openinference.LLMTokenCountPromptCacheHit, 2),
				attribute.Int(openinference.LLMTokenCountPromptCacheWrite, 4),
				attribute.String(openinference.OutputValue, string(basicResponseRespBody)),
				attribute.String(openinference.OutputMessageAttribute(0, openinference.MessageRole), "assistant"),
				attribute.String(openinference.OutputMessageContentAttribute(0, 0, "type"), "text"),
				attribute.String(openinference.OutputMessageContentAttribute(0, 0, "text"), basicResponseResp.Output[0].OfOutputMessage.Content.OfContentArray[0].OfOutputText.Text),
			},
			expectedStatus: trace.Status{Code: codes.Ok, Description: ""},
		},
		{
			name:   "response with cache creation",
			resp:   responseWithCacheWrite,
			config: &openinference.TraceConfig{},
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.OutputMimeType, openinference.MimeTypeJSON),
				attribute.String(openinference.LLMModelName, openai.ModelGPT5Nano),
				attribute.Int(openinference.LLMTokenCountPrompt, 100),
				attribute.Int(openinference.LLMTokenCountCompletion, 25),
				attribute.Int(openinference.LLMTokenCountTotal, 125),
				attribute.Int(openinference.LLMTokenCountPromptCacheHit, 10),
				attribute.Int(openinference.LLMTokenCountPromptCacheWrite, 50),
				attribute.String(openinference.OutputValue, string(responseWithCacheWriteBody)),
				attribute.String(openinference.OutputMessageAttribute(0, openinference.MessageRole), "assistant"),
				attribute.String(openinference.OutputMessageContentAttribute(0, 0, "type"), "text"),
				attribute.String(openinference.OutputMessageContentAttribute(0, 0, "text"), responseWithCacheWrite.Output[0].OfOutputMessage.Content.OfContentArray[0].OfOutputText.Text),
			},
			expectedStatus: trace.Status{Code: codes.Ok, Description: ""},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := NewResponsesRecorder(tt.config)

			actualSpan := testotel.RecordWithSpan(t, func(span oteltrace.Span) bool {
				recorder.RecordResponse(span, tt.resp)
				return false
			})

			openinference.RequireAttributesEqual(t, tt.expectedAttrs, actualSpan.Attributes)
			require.Equal(t, tt.expectedStatus, actualSpan.Status)
		})
	}
}

func TestResponsesRecorder_RecordResponseOnError(t *testing.T) {
	tests := []struct {
		name           string
		statusCode     int
		errorBody      []byte
		expectedStatus trace.Status
		expectedEvents int
	}{
		{
			name:       "400 bad request error",
			statusCode: 400,
			errorBody:  []byte(`{"error":{"message":"Invalid request","type":"invalid_request_error"}}`),
			expectedStatus: trace.Status{
				Code:        codes.Error,
				Description: "Error code: 400 - {\"error\":{\"message\":\"Invalid request\",\"type\":\"invalid_request_error\"}}",
			},
			expectedEvents: 1,
		},
		{
			name:       "500 internal server error",
			statusCode: 500,
			errorBody:  []byte(`{"error":{"message":"Internal server error"}}`),
			expectedStatus: trace.Status{
				Code:        codes.Error,
				Description: "Error code: 500 - {\"error\":{\"message\":\"Internal server error\"}}",
			},
			expectedEvents: 1,
		},
		{
			name:       "401 authentication error",
			statusCode: 401,
			errorBody:  []byte(`{"error":{"message":"Unauthorized","type":"authentication_error"}}`),
			expectedStatus: trace.Status{
				Code:        codes.Error,
				Description: "Error code: 401 - {\"error\":{\"message\":\"Unauthorized\",\"type\":\"authentication_error\"}}",
			},
			expectedEvents: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := NewResponsesRecorderFromEnv()

			actualSpan := testotel.RecordWithSpan(t, func(span oteltrace.Span) bool {
				recorder.RecordResponseOnError(span, tt.statusCode, tt.errorBody)
				return false
			})

			require.Equal(t, tt.expectedStatus, actualSpan.Status)
			require.Len(t, actualSpan.Events, tt.expectedEvents)
			if tt.expectedEvents > 0 {
				require.Equal(t, "exception", actualSpan.Events[0].Name)
			}
		})
	}
}

func TestResponsesRecorder_RecordResponseChunks_Empty(t *testing.T) {
	recorder := NewResponsesRecorderFromEnv()

	actualSpan := testotel.RecordWithSpan(t, func(span oteltrace.Span) bool {
		recorder.RecordResponseChunks(span, []*openai.ResponseStreamEventUnion{})
		return false
	})

	// Empty chunks should not produce any events
	openinference.RequireEventsEqual(t, []trace.Event{}, actualSpan.Events)
}

func TestResponsesRecorder_RecordResponseChunks(t *testing.T) {
	recorder := NewResponsesRecorderFromEnv()
	respCmplEventJSON := `{
      "type": "response.completed",
      "response": {
        "id": "resp_123",
        "object": "response",
        "created_at": 1740855869,
        "status": "completed",
        "error": null,
        "incomplete_details": null,
        "input": [],
        "instructions": null,
        "max_output_tokens": null,
        "model": "gpt-4o-mini-2024-07-18",
        "output": [
          {
            "id": "msg_123",
            "type": "message",
            "role": "assistant",
            "content": [
              {
                "type": "output_text",
                "text": "In a shimmering forest under a sky full of stars, a lonely unicorn named Lila discovered a hidden pond that glowed with moonlight. Every night, she would leave sparkling, magical flowers by the water's edge, hoping to share her beauty with others. One enchanting evening, she woke to find a group of friendly animals gathered around, eager to be friends and share in her magic.",
                "annotations": []
              }
            ]
          }
        ],
        "previous_response_id": null,
        "reasoning_effort": null,
        "store": false,
        "temperature": 1,
        "text": {
          "format": {
            "type": "text"
          }
        },
        "tool_choice": "auto",
        "tools": [],
        "top_p": 1,
        "truncation": "disabled",
        "usage": {
          "input_tokens": 0,
          "output_tokens": 0,
          "output_tokens_details": {
            "reasoning_tokens": 0
          },
          "total_tokens": 0
        },
        "user": null,
        "metadata": {}
      },
      "sequence_number": 2
    }`
	var respCmplEvent openai.ResponseStreamEventUnion
	err := respCmplEvent.UnmarshalJSON([]byte(respCmplEventJSON))
	require.NoError(t, err)

	actualSpan := testotel.RecordWithSpan(t, func(span oteltrace.Span) bool {
		recorder.RecordResponseChunks(span, []*openai.ResponseStreamEventUnion{{}, &respCmplEvent})
		return false
	})

	// should produce two events
	openinference.RequireEventsEqual(t, []trace.Event{
		{
			Name: "First Token Stream Event",
			Time: time.Time{},
		},
		{
			Name: "Response Completed Event",
			Time: time.Time{},
		},
	}, actualSpan.Events)
}

func TestResponsesRecorder_WithConfig_HideInputs(t *testing.T) {
	tests := []struct {
		name          string
		config        *openinference.TraceConfig
		expectedAttrs []attribute.KeyValue
	}{
		{
			name: "hide input value",
			config: &openinference.TraceConfig{
				HideInputs: true,
			},
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.SpanKind, openinference.SpanKindLLM),
				attribute.String(openinference.LLMSystem, openinference.LLMSystemOpenAI),
				attribute.String(openinference.LLMModelName, openai.ModelGPT5Nano),
				attribute.String(openinference.InputValue, openinference.RedactedValue),
				attribute.String(openinference.InputMimeType, openinference.MimeTypeJSON),
				attribute.String(openinference.LLMInvocationParameters, `{"model":"gpt-5-nano"}`),
			},
		},
		{
			name:   "show input value",
			config: &openinference.TraceConfig{},
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.SpanKind, openinference.SpanKindLLM),
				attribute.String(openinference.LLMSystem, openinference.LLMSystemOpenAI),
				attribute.String(openinference.LLMModelName, openai.ModelGPT5Nano),
				attribute.String(openinference.InputValue, string(basicResponseReqBody)),
				attribute.String(openinference.InputMimeType, openinference.MimeTypeJSON),
				attribute.String(openinference.LLMInvocationParameters, `{"model":"gpt-5-nano"}`),
				attribute.String("llm.input_messages.0.message.role", "user"),
				attribute.String("llm.input_messages.0.message.content", "Hi"),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := NewResponsesRecorder(tt.config)

			actualSpan := testotel.RecordWithSpan(t, func(span oteltrace.Span) bool {
				recorder.RecordRequest(span, basicResponseReq, basicResponseReqBody)
				return false
			})

			openinference.RequireAttributesEqual(t, tt.expectedAttrs, actualSpan.Attributes)
		})
	}
}

func TestResponsesRecorder_ConfigFromEnvironment(t *testing.T) {
	// Test that recorder uses environment variables when config is nil.
	t.Setenv(openinference.EnvHideInputs, "true")

	recorder := NewResponsesRecorderFromEnv()

	// Request test.
	reqSpan := testotel.RecordWithSpan(t, func(span oteltrace.Span) bool {
		recorder.RecordRequest(span, basicResponseReq, basicResponseReqBody)
		return false
	})

	// Verify span was created with LLM attributes
	attrs := make(map[string]attribute.Value)
	for _, kv := range reqSpan.Attributes {
		attrs[string(kv.Key)] = kv.Value
	}
	require.Equal(t, openinference.SpanKindLLM, attrs[openinference.SpanKind].AsString())
	require.Equal(t, openinference.LLMSystemOpenAI, attrs[openinference.LLMSystem].AsString())
	require.Equal(t, openai.ModelGPT5Nano, attrs[openinference.LLMModelName].AsString())
}

func TestResponsesRecorder_NewResponsesRecorder_NilConfig(t *testing.T) {
	// Test that NewResponsesRecorder uses NewTraceConfigFromEnv() when config is nil
	recorder := NewResponsesRecorder(nil)

	require.NotNil(t, recorder)

	// Verify it's a ResponsesRecorder
	_, ok := recorder.(*ResponsesRecorder)
	require.True(t, ok)
}

func TestResponsesRecorder_EmptyResponseBody(t *testing.T) {
	emptyResp := &openai.Response{
		ID:    "resp-empty",
		Model: openai.ModelGPT5Nano,
	}

	recorder := NewResponsesRecorderFromEnv()

	actualSpan := testotel.RecordWithSpan(t, func(span oteltrace.Span) bool {
		recorder.RecordResponse(span, emptyResp)
		return false
	})

	// Should have LLMModelName and OutputValue (since token counts are 0)
	attrs := make(map[string]attribute.Value)
	for _, kv := range actualSpan.Attributes {
		attrs[string(kv.Key)] = kv.Value
	}
	require.Equal(t, openai.ModelGPT5Nano, attrs[openinference.LLMModelName].AsString())
	require.NotEmpty(t, attrs[openinference.OutputValue].AsString())
	require.Equal(t, codes.Ok, actualSpan.Status.Code)
}
