// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package openai

import (
	"strings"
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

// Test data.
var (
	basicEmbeddingReq = &openai.EmbeddingRequest{
		Model: "text-embedding-3-small",
		OfCompletion: &openai.EmbeddingCompletionRequest{
			Model: "text-embedding-3-small",
			Input: openai.EmbeddingRequestInput{Value: "How do I reset my password?"},
		},
	}
	basicEmbeddingReqBody = []byte(`{"model":"text-embedding-3-small","input":"How do I reset my password?"}`)

	multiInputEmbeddingReq = &openai.EmbeddingRequest{
		Model: "text-embedding-3-small",
		OfCompletion: &openai.EmbeddingCompletionRequest{
			Model: "text-embedding-3-small",
			Input: openai.EmbeddingRequestInput{Value: []string{"How", "do", "I", "reset", "my", "password?"}},
		},
	}
	multiInputEmbeddingReqBody = []byte(`{"model":"text-embedding-3-small","input":["How","do","I","reset","my","password?"]}`)

	tokenInputEmbeddingReq = &openai.EmbeddingRequest{
		Model: "text-embedding-3-small",
		OfCompletion: &openai.EmbeddingCompletionRequest{
			Model: "text-embedding-3-small",
			Input: openai.EmbeddingRequestInput{Value: []int64{4438, 656, 358, 7738, 856, 3636, 30}},
		},
	}
	tokenInputEmbeddingReqBody = []byte(`{"model":"text-embedding-3-small","input":[4438,656,358,7738,856,3636,30]}`)

	chatEmbeddingReq = &openai.EmbeddingRequest{
		Model: "gemini-embedding-2",
		OfChat: &openai.EmbeddingChatRequest{
			Model: "gemini-embedding-2",
			Messages: []openai.ChatCompletionMessageParamUnion{
				{OfUser: &openai.ChatCompletionUserMessageParam{
					Role:    "user",
					Content: openai.StringOrUserRoleContentUnion{Value: "embed this text"},
				}},
			},
		},
	}
	chatEmbeddingReqBody = []byte(`{"model":"gemini-embedding-2","messages":[{"role":"user","content":"embed this text"}]}`)

	basicEmbeddingResp = &openai.EmbeddingResponse{
		Model: "text-embedding-3-small",
		Usage: openai.EmbeddingUsage{
			PromptTokens: 10,
			TotalTokens:  10,
		},
		Data: []openai.Embedding{
			{Index: 0, Embedding: openai.EmbeddingUnion{Value: []float64{0.1, 0.2, 0.3}}},
		},
	}

	multiEmbeddingResp = &openai.EmbeddingResponse{
		Model: "text-embedding-3-small",
		Usage: openai.EmbeddingUsage{
			PromptTokens: 25,
			TotalTokens:  25,
		},
		Data: []openai.Embedding{
			{Index: 0, Embedding: openai.EmbeddingUnion{Value: []float64{0.1, 0.2}}},
			{Index: 1, Embedding: openai.EmbeddingUnion{Value: []float64{0.3, 0.4}}},
		},
	}

	base64EmbeddingResp = &openai.EmbeddingResponse{
		Model: "text-embedding-3-small",
		Usage: openai.EmbeddingUsage{
			PromptTokens: 5,
			TotalTokens:  5,
		},
		Data: []openai.Embedding{
			{Index: 0, Embedding: openai.EmbeddingUnion{Value: "base64encodedstring"}},
		},
	}
)

func TestEmbeddingsRecorder_StartParams(t *testing.T) {
	tests := []struct {
		name             string
		req              *openai.EmbeddingRequest
		reqBody          []byte
		expectedSpanName string
	}{
		{
			name:             "basic request",
			req:              basicEmbeddingReq,
			reqBody:          basicEmbeddingReqBody,
			expectedSpanName: "CreateEmbeddings",
		},
		{
			name:             "multi input request",
			req:              multiInputEmbeddingReq,
			reqBody:          multiInputEmbeddingReqBody,
			expectedSpanName: "CreateEmbeddings",
		},
		{
			name:             "chat embedding request",
			req:              chatEmbeddingReq,
			reqBody:          chatEmbeddingReqBody,
			expectedSpanName: "CreateEmbeddings",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := NewEmbeddingsRecorderFromEnv()

			spanName, opts := recorder.StartParams(tt.req, tt.reqBody)
			actualSpan := testotel.RecordNewSpan(t, spanName, opts...)

			require.Equal(t, tt.expectedSpanName, actualSpan.Name)
			require.Equal(t, oteltrace.SpanKindInternal, actualSpan.SpanKind)
		})
	}
}

func TestEmbeddingsRecorder_RecordRequest(t *testing.T) {
	tests := []struct {
		name          string
		req           *openai.EmbeddingRequest
		reqBody       []byte
		config        *openinference.TraceConfig
		expectedAttrs []attribute.KeyValue
	}{
		{
			name:    "basic request",
			req:     basicEmbeddingReq,
			reqBody: basicEmbeddingReqBody,
			config:  &openinference.TraceConfig{},
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.SpanKind, openinference.SpanKindEmbedding),
				attribute.String(openinference.InputValue, string(basicEmbeddingReqBody)),
				attribute.String(openinference.InputMimeType, openinference.MimeTypeJSON),
				attribute.String(openinference.EmbeddingInvocationParameters, `{"model":"text-embedding-3-small"}`),
				attribute.String(openinference.EmbeddingTextAttribute(0), "How do I reset my password?"),
			},
		},
		{
			name:    "multiple inputs",
			req:     multiInputEmbeddingReq,
			reqBody: multiInputEmbeddingReqBody,
			config:  &openinference.TraceConfig{},
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.SpanKind, openinference.SpanKindEmbedding),
				attribute.String(openinference.InputValue, string(multiInputEmbeddingReqBody)),
				attribute.String(openinference.InputMimeType, openinference.MimeTypeJSON),
				attribute.String(openinference.EmbeddingInvocationParameters, `{"model":"text-embedding-3-small"}`),
				attribute.String(openinference.EmbeddingTextAttribute(0), "How"),
				attribute.String(openinference.EmbeddingTextAttribute(1), "do"),
				attribute.String(openinference.EmbeddingTextAttribute(2), "I"),
				attribute.String(openinference.EmbeddingTextAttribute(3), "reset"),
				attribute.String(openinference.EmbeddingTextAttribute(4), "my"),
				attribute.String(openinference.EmbeddingTextAttribute(5), "password?"),
			},
		},
		{
			name:    "token inputs",
			req:     tokenInputEmbeddingReq,
			reqBody: tokenInputEmbeddingReqBody,
			config:  &openinference.TraceConfig{},
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.SpanKind, openinference.SpanKindEmbedding),
				attribute.String(openinference.InputValue, string(tokenInputEmbeddingReqBody)),
				attribute.String(openinference.InputMimeType, openinference.MimeTypeJSON),
				attribute.String(openinference.EmbeddingInvocationParameters, `{"model":"text-embedding-3-small"}`),
			},
		},
		{
			name:    "hidden inputs",
			req:     basicEmbeddingReq,
			reqBody: basicEmbeddingReqBody,
			config:  &openinference.TraceConfig{HideInputs: true},
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.SpanKind, openinference.SpanKindEmbedding),
				attribute.String(openinference.InputValue, openinference.RedactedValue),
				attribute.String(openinference.EmbeddingInvocationParameters, `{"model":"text-embedding-3-small"}`),
			},
		},
		{
			name:    "chat embedding request",
			req:     chatEmbeddingReq,
			reqBody: chatEmbeddingReqBody,
			config:  &openinference.TraceConfig{},
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.SpanKind, openinference.SpanKindEmbedding),
				attribute.String(openinference.InputValue, string(chatEmbeddingReqBody)),
				attribute.String(openinference.InputMimeType, openinference.MimeTypeJSON),
				attribute.String(openinference.EmbeddingInvocationParameters, `{"model":"gemini-embedding-2"}`),
				attribute.String(openinference.EmbeddingTextAttribute(0), "embed this text"),
			},
		},
		{
			name:    "hidden invocation parameters",
			req:     basicEmbeddingReq,
			reqBody: basicEmbeddingReqBody,
			config:  &openinference.TraceConfig{HideLLMInvocationParameters: true},
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.SpanKind, openinference.SpanKindEmbedding),
				attribute.String(openinference.InputValue, string(basicEmbeddingReqBody)),
				attribute.String(openinference.InputMimeType, openinference.MimeTypeJSON),
				attribute.String(openinference.EmbeddingTextAttribute(0), "How do I reset my password?"),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := NewEmbeddingsRecorder(tt.config)

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

func TestEmbeddingsRecorder_RecordResponse(t *testing.T) {
	tests := []struct {
		name           string
		req            *openai.EmbeddingRequest
		reqBody        []byte
		resp           *openai.EmbeddingResponse
		config         *openinference.TraceConfig
		expectedAttrs  []attribute.KeyValue
		expectedStatus trace.Status
	}{
		{
			name:    "basic response with vectors",
			req:     basicEmbeddingReq,
			reqBody: basicEmbeddingReqBody,
			resp:    basicEmbeddingResp,
			config:  &openinference.TraceConfig{},
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.EmbeddingModelName, "text-embedding-3-small"),
				attribute.String(openinference.OutputMimeType, openinference.MimeTypeJSON),
				attribute.Float64Slice(openinference.EmbeddingVectorAttribute(0), []float64{0.1, 0.2, 0.3}),
				attribute.Int(openinference.LLMTokenCountPrompt, 10),
				attribute.Int(openinference.LLMTokenCountTotal, 10),
			},
			expectedStatus: trace.Status{Code: codes.Ok, Description: ""},
		},
		{
			name:    "multiple embeddings response",
			req:     multiInputEmbeddingReq,
			reqBody: multiInputEmbeddingReqBody,
			resp:    multiEmbeddingResp,
			config:  &openinference.TraceConfig{},
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.EmbeddingModelName, "text-embedding-3-small"),
				attribute.String(openinference.OutputMimeType, openinference.MimeTypeJSON),
				attribute.Float64Slice(openinference.EmbeddingVectorAttribute(0), []float64{0.1, 0.2}),
				attribute.Float64Slice(openinference.EmbeddingVectorAttribute(1), []float64{0.3, 0.4}),
				attribute.Int(openinference.LLMTokenCountPrompt, 25),
				attribute.Int(openinference.LLMTokenCountTotal, 25),
			},
			expectedStatus: trace.Status{Code: codes.Ok, Description: ""},
		},
		{
			name:    "base64 encoded response",
			req:     basicEmbeddingReq,
			reqBody: basicEmbeddingReqBody,
			resp:    base64EmbeddingResp,
			config:  &openinference.TraceConfig{},
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.EmbeddingModelName, "text-embedding-3-small"),
				attribute.String(openinference.OutputMimeType, openinference.MimeTypeJSON),
				// Base64 encoded embeddings are not included as vectors.
				attribute.Int(openinference.LLMTokenCountPrompt, 5),
				attribute.Int(openinference.LLMTokenCountTotal, 5),
			},
			expectedStatus: trace.Status{Code: codes.Ok, Description: ""},
		},
		{
			name:    "hidden outputs",
			req:     basicEmbeddingReq,
			reqBody: basicEmbeddingReqBody,
			resp:    basicEmbeddingResp,
			config:  &openinference.TraceConfig{HideOutputs: true},
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.EmbeddingModelName, "text-embedding-3-small"),
				// No output mime type or vectors when outputs are hidden.
				attribute.Int(openinference.LLMTokenCountPrompt, 10),
				attribute.Int(openinference.LLMTokenCountTotal, 10),
				attribute.String(openinference.OutputValue, openinference.RedactedValue),
			},
			expectedStatus: trace.Status{Code: codes.Ok, Description: ""},
		},
		{
			name:    "hidden embeddings text",
			req:     basicEmbeddingReq,
			reqBody: basicEmbeddingReqBody,
			resp:    basicEmbeddingResp,
			config:  &openinference.TraceConfig{HideEmbeddingsText: true},
			expectedAttrs: []attribute.KeyValue{
				// No embedding text when embeddings text is hidden.
				attribute.String(openinference.EmbeddingModelName, "text-embedding-3-small"),
				attribute.String(openinference.OutputMimeType, openinference.MimeTypeJSON),
				attribute.Float64Slice(openinference.EmbeddingVectorAttribute(0), []float64{0.1, 0.2, 0.3}),
				attribute.Int(openinference.LLMTokenCountPrompt, 10),
				attribute.Int(openinference.LLMTokenCountTotal, 10),
			},
			expectedStatus: trace.Status{Code: codes.Ok, Description: ""},
		},
		{
			name:    "token input (no text)",
			req:     tokenInputEmbeddingReq,
			reqBody: tokenInputEmbeddingReqBody,
			resp:    basicEmbeddingResp,
			config:  &openinference.TraceConfig{},
			expectedAttrs: []attribute.KeyValue{
				// Token inputs are not decoded to text.
				attribute.String(openinference.EmbeddingModelName, "text-embedding-3-small"),
				attribute.String(openinference.OutputMimeType, openinference.MimeTypeJSON),
				attribute.Float64Slice(openinference.EmbeddingVectorAttribute(0), []float64{0.1, 0.2, 0.3}),
				attribute.Int(openinference.LLMTokenCountPrompt, 10),
				attribute.Int(openinference.LLMTokenCountTotal, 10),
			},
			expectedStatus: trace.Status{Code: codes.Ok, Description: ""},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := NewEmbeddingsRecorder(tt.config)

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
				// NOTE: embedding.text attributes are in request phase (unlike Python OpenInference which has a bug).
				if key == openinference.LLMSystem ||
					key == openinference.SpanKind ||
					key == openinference.InputValue ||
					key == openinference.InputMimeType ||
					key == openinference.EmbeddingInvocationParameters ||
					strings.HasPrefix(key, "embedding.embeddings.") && strings.HasSuffix(key, ".embedding.text") {
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

func TestEmbeddingsRecorder_RecordResponseOnError(t *testing.T) {
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
			recorder := NewEmbeddingsRecorderFromEnv()

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

// Ensure EmbeddingsRecorder implements the interface.
var _ interface {
	StartParams(*openai.EmbeddingRequest, []byte) (string, []oteltrace.SpanStartOption)
	RecordRequest(oteltrace.Span, *openai.EmbeddingRequest, []byte)
	RecordResponse(oteltrace.Span, *openai.EmbeddingResponse)
	RecordResponseOnError(oteltrace.Span, int, []byte)
} = (*EmbeddingsRecorder)(nil)
