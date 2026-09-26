// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package openai

import (
	"strings"
	"testing"
	"time"

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

// Helper to create int pointers.
//
//go:fix inline

// Test data.
var (
	basicCompletionReq = &openai.CompletionRequest{
		Model:  "gpt-3.5-turbo-instruct",
		Prompt: openai.PromptUnion{Value: "Say this is a test"},
	}

	basicCompletionReqBody = []byte(`{"model":"gpt-3.5-turbo-instruct","prompt":"Say this is a test"}`)

	arrayCompletionReq = &openai.CompletionRequest{
		Model:  "gpt-3.5-turbo-instruct",
		Prompt: openai.PromptUnion{Value: []string{"Say this is a test", "Say hello"}},
	}

	arrayCompletionReqBody = []byte(`{"model":"gpt-3.5-turbo-instruct","prompt":["Say this is a test","Say hello"]}`)

	tokenCompletionReq = &openai.CompletionRequest{
		Model:  "gpt-3.5-turbo-instruct",
		Prompt: openai.PromptUnion{Value: []int64{1, 2, 3}},
	}

	tokenCompletionReqBody = []byte(`{"model":"gpt-3.5-turbo-instruct","prompt":[1,2,3]}`)

	basicCompletionResp = &openai.CompletionResponse{
		ID:      "cmpl-123",
		Object:  "text_completion",
		Created: openai.JSONUNIXTime(time.Unix(1234567890, 0)),
		Model:   "gpt-3.5-turbo-instruct",
		Choices: []openai.CompletionChoice{
			{
				Text:         "This is a test",
				Index:        new(0),
				FinishReason: "stop",
			},
		},
		Usage: &openai.Usage{
			PromptTokens:     5,
			CompletionTokens: 4,
			TotalTokens:      9,
		},
	}

	multiChoiceCompletionResp = &openai.CompletionResponse{
		ID:      "cmpl-456",
		Object:  "text_completion",
		Created: openai.JSONUNIXTime(time.Unix(1234567890, 0)),
		Model:   "gpt-3.5-turbo-instruct",
		Choices: []openai.CompletionChoice{
			{
				Text:         "First choice",
				Index:        new(0),
				FinishReason: "stop",
			},
			{
				Text:         "Second choice",
				Index:        new(1),
				FinishReason: "stop",
			},
		},
		Usage: &openai.Usage{
			PromptTokens:     5,
			CompletionTokens: 6,
			TotalTokens:      11,
		},
	}

	// streamChunks represents delta chunks (incremental text), as returned by the real API.
	streamChunks = []*openai.CompletionResponse{
		{
			ID:      "cmpl-789",
			Object:  "text_completion",
			Created: openai.JSONUNIXTime(time.Unix(1234567890, 0)),
			Model:   "gpt-3.5-turbo-instruct",
			Choices: []openai.CompletionChoice{
				{Text: "This", Index: new(0)},
			},
		},
		{
			ID:      "cmpl-789",
			Object:  "text_completion",
			Created: openai.JSONUNIXTime(time.Unix(1234567890, 0)),
			Model:   "gpt-3.5-turbo-instruct",
			Choices: []openai.CompletionChoice{
				{Text: " is", Index: new(0)},
			},
		},
		{
			ID:      "cmpl-789",
			Object:  "text_completion",
			Created: openai.JSONUNIXTime(time.Unix(1234567890, 0)),
			Model:   "gpt-3.5-turbo-instruct",
			Choices: []openai.CompletionChoice{
				{Text: " a test", Index: new(0), FinishReason: "stop"},
			},
			Usage: &openai.Usage{
				PromptTokens:     5,
				CompletionTokens: 4,
				TotalTokens:      9,
			},
		},
	}
)

func TestCompletionRecorder_StartParams(t *testing.T) {
	tests := []struct {
		name             string
		req              *openai.CompletionRequest
		reqBody          []byte
		expectedSpanName string
	}{
		{
			name:             "basic request",
			req:              basicCompletionReq,
			reqBody:          basicCompletionReqBody,
			expectedSpanName: "Completion",
		},
		{
			name:             "array prompts request",
			req:              arrayCompletionReq,
			reqBody:          arrayCompletionReqBody,
			expectedSpanName: "Completion",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := NewCompletionRecorderFromEnv()

			spanName, opts := recorder.StartParams(tt.req, tt.reqBody)
			actualSpan := testotel.RecordNewSpan(t, spanName, opts...)

			require.Equal(t, tt.expectedSpanName, actualSpan.Name)
			require.Equal(t, oteltrace.SpanKindInternal, actualSpan.SpanKind)
		})
	}
}

func TestCompletionRecorder_RecordRequest(t *testing.T) {
	tests := []struct {
		name          string
		req           *openai.CompletionRequest
		reqBody       []byte
		config        *openinference.TraceConfig
		expectedAttrs []attribute.KeyValue
	}{
		{
			name:    "basic string prompt",
			req:     basicCompletionReq,
			reqBody: basicCompletionReqBody,
			config:  &openinference.TraceConfig{},
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.SpanKind, openinference.SpanKindLLM),
				attribute.String(openinference.LLMSystem, openinference.LLMSystemOpenAI),
				attribute.String(openinference.LLMModelName, "gpt-3.5-turbo-instruct"),
				attribute.String(openinference.InputValue, string(basicCompletionReqBody)),
				attribute.String(openinference.InputMimeType, openinference.MimeTypeJSON),
				attribute.String(openinference.LLMInvocationParameters, `{"model":"gpt-3.5-turbo-instruct"}`),
				attribute.String(openinference.PromptTextAttribute(0), "Say this is a test"),
			},
		},
		{
			name:    "array prompts",
			req:     arrayCompletionReq,
			reqBody: arrayCompletionReqBody,
			config:  &openinference.TraceConfig{},
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.SpanKind, openinference.SpanKindLLM),
				attribute.String(openinference.LLMSystem, openinference.LLMSystemOpenAI),
				attribute.String(openinference.LLMModelName, "gpt-3.5-turbo-instruct"),
				attribute.String(openinference.InputValue, string(arrayCompletionReqBody)),
				attribute.String(openinference.InputMimeType, openinference.MimeTypeJSON),
				attribute.String(openinference.LLMInvocationParameters, `{"model":"gpt-3.5-turbo-instruct"}`),
				attribute.String(openinference.PromptTextAttribute(0), "Say this is a test"),
				attribute.String(openinference.PromptTextAttribute(1), "Say hello"),
			},
		},
		{
			name:    "token prompts not recorded",
			req:     tokenCompletionReq,
			reqBody: tokenCompletionReqBody,
			config:  &openinference.TraceConfig{},
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.SpanKind, openinference.SpanKindLLM),
				attribute.String(openinference.LLMSystem, openinference.LLMSystemOpenAI),
				attribute.String(openinference.LLMModelName, "gpt-3.5-turbo-instruct"),
				attribute.String(openinference.InputValue, string(tokenCompletionReqBody)),
				attribute.String(openinference.InputMimeType, openinference.MimeTypeJSON),
				attribute.String(openinference.LLMInvocationParameters, `{"model":"gpt-3.5-turbo-instruct"}`),
				// No prompt attributes for token arrays
			},
		},
		{
			name:    "hide inputs",
			req:     basicCompletionReq,
			reqBody: basicCompletionReqBody,
			config:  &openinference.TraceConfig{HideInputs: true},
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.SpanKind, openinference.SpanKindLLM),
				attribute.String(openinference.LLMSystem, openinference.LLMSystemOpenAI),
				attribute.String(openinference.LLMModelName, "gpt-3.5-turbo-instruct"),
				attribute.String(openinference.InputValue, openinference.RedactedValue),
				attribute.String(openinference.LLMInvocationParameters, `{"model":"gpt-3.5-turbo-instruct"}`),
				// No prompts when HideInputs is true
			},
		},
		{
			name:    "hide prompts",
			req:     basicCompletionReq,
			reqBody: basicCompletionReqBody,
			config:  &openinference.TraceConfig{HidePrompts: true},
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.SpanKind, openinference.SpanKindLLM),
				attribute.String(openinference.LLMSystem, openinference.LLMSystemOpenAI),
				attribute.String(openinference.LLMModelName, "gpt-3.5-turbo-instruct"),
				attribute.String(openinference.InputValue, string(basicCompletionReqBody)),
				attribute.String(openinference.InputMimeType, openinference.MimeTypeJSON),
				attribute.String(openinference.LLMInvocationParameters, `{"model":"gpt-3.5-turbo-instruct"}`),
				// No prompt attributes when HidePrompts is true
			},
		},
		{
			name:    "hide invocation parameters",
			req:     basicCompletionReq,
			reqBody: basicCompletionReqBody,
			config:  &openinference.TraceConfig{HideLLMInvocationParameters: true},
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.SpanKind, openinference.SpanKindLLM),
				attribute.String(openinference.LLMSystem, openinference.LLMSystemOpenAI),
				attribute.String(openinference.LLMModelName, "gpt-3.5-turbo-instruct"),
				attribute.String(openinference.InputValue, string(basicCompletionReqBody)),
				attribute.String(openinference.InputMimeType, openinference.MimeTypeJSON),
				attribute.String(openinference.PromptTextAttribute(0), "Say this is a test"),
				// No LLMInvocationParameters when HideLLMInvocationParameters is true
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := NewCompletionRecorder(tt.config)

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

func TestCompletionRecorder_RecordResponse(t *testing.T) {
	tests := []struct {
		name           string
		req            *openai.CompletionRequest
		reqBody        []byte
		resp           *openai.CompletionResponse
		config         *openinference.TraceConfig
		expectedAttrs  []attribute.KeyValue
		expectedStatus trace.Status
	}{
		{
			name:    "basic response",
			req:     basicCompletionReq,
			reqBody: basicCompletionReqBody,
			resp:    basicCompletionResp,
			config:  &openinference.TraceConfig{},
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.LLMModelName, "gpt-3.5-turbo-instruct"),
				attribute.String(openinference.OutputMimeType, openinference.MimeTypeJSON),
				attribute.String(openinference.ChoiceTextAttribute(0), "This is a test"),
				attribute.Int(openinference.LLMTokenCountPrompt, 5),
				attribute.Int(openinference.LLMTokenCountCompletion, 4),
				attribute.Int(openinference.LLMTokenCountTotal, 9),
			},
			expectedStatus: trace.Status{Code: codes.Ok, Description: ""},
		},
		{
			name:    "multiple choices",
			req:     basicCompletionReq,
			reqBody: basicCompletionReqBody,
			resp:    multiChoiceCompletionResp,
			config:  &openinference.TraceConfig{},
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.LLMModelName, "gpt-3.5-turbo-instruct"),
				attribute.String(openinference.OutputMimeType, openinference.MimeTypeJSON),
				attribute.String(openinference.ChoiceTextAttribute(0), "First choice"),
				attribute.String(openinference.ChoiceTextAttribute(1), "Second choice"),
				attribute.Int(openinference.LLMTokenCountPrompt, 5),
				attribute.Int(openinference.LLMTokenCountCompletion, 6),
				attribute.Int(openinference.LLMTokenCountTotal, 11),
			},
			expectedStatus: trace.Status{Code: codes.Ok, Description: ""},
		},
		{
			name:    "hide outputs",
			req:     basicCompletionReq,
			reqBody: basicCompletionReqBody,
			resp:    basicCompletionResp,
			config:  &openinference.TraceConfig{HideOutputs: true},
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.LLMModelName, "gpt-3.5-turbo-instruct"),
				// No OutputMimeType when HideOutputs is true
				// No choices when HideOutputs is true
				attribute.Int(openinference.LLMTokenCountPrompt, 5),
				attribute.Int(openinference.LLMTokenCountCompletion, 4),
				attribute.Int(openinference.LLMTokenCountTotal, 9),
				attribute.String(openinference.OutputValue, openinference.RedactedValue),
			},
			expectedStatus: trace.Status{Code: codes.Ok, Description: ""},
		},
		{
			name:    "hide choices",
			req:     basicCompletionReq,
			reqBody: basicCompletionReqBody,
			resp:    basicCompletionResp,
			config:  &openinference.TraceConfig{HideChoices: true},
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.LLMModelName, "gpt-3.5-turbo-instruct"),
				attribute.String(openinference.OutputMimeType, openinference.MimeTypeJSON),
				// No choice text attributes when HideChoices is true
				attribute.Int(openinference.LLMTokenCountPrompt, 5),
				attribute.Int(openinference.LLMTokenCountCompletion, 4),
				attribute.Int(openinference.LLMTokenCountTotal, 9),
			},
			expectedStatus: trace.Status{Code: codes.Ok, Description: ""},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := NewCompletionRecorder(tt.config)

			actualSpan := testotel.RecordWithSpan(t, func(span oteltrace.Span) bool {
				// First record the request.
				recorder.RecordRequest(span, tt.req, tt.reqBody)
				// Then record the response.
				recorder.RecordResponse(span, tt.resp)
				return false
			})

			// Extract just the response attributes (skip request attributes).
			var responseAttrs []attribute.KeyValue
			for _, attr := range actualSpan.Attributes {
				key := string(attr.Key)
				// Skip request-specific attributes.
				if key == openinference.SpanKind ||
					key == openinference.LLMSystem ||
					key == openinference.InputValue ||
					key == openinference.InputMimeType ||
					key == openinference.LLMInvocationParameters ||
					strings.HasPrefix(key, "llm.prompts.") {
					continue
				}
				responseAttrs = append(responseAttrs, attr)
			}

			// Check output value separately if present.
			var outputValueAttr *attribute.KeyValue
			var expectedOutputValueAttr *attribute.KeyValue

			var filteredResponseAttrs []attribute.KeyValue
			for _, attr := range responseAttrs {
				if string(attr.Key) == openinference.OutputValue {
					outputValueAttr = &attr
				} else {
					filteredResponseAttrs = append(filteredResponseAttrs, attr)
				}
			}

			filteredExpectedAttrs := []attribute.KeyValue{}
			for _, attr := range tt.expectedAttrs {
				if string(attr.Key) == openinference.OutputValue {
					expectedOutputValueAttr = &attr
				} else {
					filteredExpectedAttrs = append(filteredExpectedAttrs, attr)
				}
			}

			openinference.RequireAttributesEqual(t, filteredExpectedAttrs, filteredResponseAttrs)

			// Check output value separately.
			if expectedOutputValueAttr != nil {
				require.NotNil(t, outputValueAttr, "expected output value attribute")
				if expectedOutputValueAttr.Value.AsString() == openinference.RedactedValue {
					require.Equal(t, openinference.RedactedValue, outputValueAttr.Value.AsString())
				} else {
					// For non-redacted values, just check it contains the response.
					respJSON, _ := json.Marshal(tt.resp)
					require.JSONEq(t, string(respJSON), outputValueAttr.Value.AsString())
				}
			} else if outputValueAttr != nil {
				// If we have output value but didn't expect it, check it's valid JSON.
				respJSON, _ := json.Marshal(tt.resp)
				require.JSONEq(t, string(respJSON), outputValueAttr.Value.AsString())
			}

			require.Empty(t, actualSpan.Events)
			require.Equal(t, tt.expectedStatus, actualSpan.Status)
		})
	}
}

func TestCompletionRecorder_RecordResponseChunks(t *testing.T) {
	tests := []struct {
		name           string
		chunks         []*openai.CompletionResponse
		config         *openinference.TraceConfig
		expectedAttrs  []attribute.KeyValue
		expectedStatus trace.Status
		expectedEvents int
	}{
		{
			name:   "multiple chunks",
			chunks: streamChunks,
			config: &openinference.TraceConfig{},
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.LLMModelName, "gpt-3.5-turbo-instruct"),
				attribute.String(openinference.OutputMimeType, openinference.MimeTypeJSON),
				attribute.String(openinference.ChoiceTextAttribute(0), "This is a test"),
				attribute.Int(openinference.LLMTokenCountPrompt, 5),
				attribute.Int(openinference.LLMTokenCountCompletion, 4),
				attribute.Int(openinference.LLMTokenCountTotal, 9),
			},
			expectedStatus: trace.Status{Code: codes.Ok, Description: ""},
			expectedEvents: 1,
		},
		{
			name:           "empty chunks",
			chunks:         []*openai.CompletionResponse{},
			config:         &openinference.TraceConfig{},
			expectedAttrs:  []attribute.KeyValue{},
			expectedStatus: trace.Status{Code: codes.Unset, Description: ""},
			expectedEvents: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := NewCompletionRecorder(tt.config)

			actualSpan := testotel.RecordWithSpan(t, func(span oteltrace.Span) bool {
				recorder.RecordResponseChunks(span, tt.chunks)
				return false
			})

			// Extract just the chunk attributes (skip output.value).
			var chunkAttrs []attribute.KeyValue
			for _, attr := range actualSpan.Attributes {
				if string(attr.Key) != openinference.OutputValue {
					chunkAttrs = append(chunkAttrs, attr)
				}
			}

			openinference.RequireAttributesEqual(t, tt.expectedAttrs, chunkAttrs)
			require.Len(t, actualSpan.Events, tt.expectedEvents)
			if tt.expectedEvents > 0 {
				require.Equal(t, "First Token Stream Event", actualSpan.Events[0].Name)
			}
			require.Equal(t, tt.expectedStatus, actualSpan.Status)
		})
	}
}

func TestCompletionRecorder_RecordResponseOnError(t *testing.T) {
	tests := []struct {
		name           string
		statusCode     int
		errorBody      []byte
		expectedStatus trace.Status
		expectedEvents int
	}{
		{
			name:       "404 not found error",
			statusCode: 404,
			errorBody:  []byte(`{"error":{"message":"Model not found","type":"invalid_request_error"}}`),
			expectedStatus: trace.Status{
				Code:        codes.Error,
				Description: "Error code: 404 - {\"error\":{\"message\":\"Model not found\",\"type\":\"invalid_request_error\"}}",
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
			name:       "400 bad request",
			statusCode: 400,
			errorBody:  []byte(`{"error":{"message":"Invalid input"}}`),
			expectedStatus: trace.Status{
				Code:        codes.Error,
				Description: "Error code: 400 - {\"error\":{\"message\":\"Invalid input\"}}",
			},
			expectedEvents: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := NewCompletionRecorderFromEnv()

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

// Ensure CompletionRecorder implements the interface.
var _ interface {
	StartParams(*openai.CompletionRequest, []byte) (string, []oteltrace.SpanStartOption)
	RecordRequest(oteltrace.Span, *openai.CompletionRequest, []byte)
	RecordResponse(oteltrace.Span, *openai.CompletionResponse)
	RecordResponseChunks(oteltrace.Span, []*openai.CompletionResponse)
	RecordResponseOnError(oteltrace.Span, int, []byte)
} = (*CompletionRecorder)(nil)
