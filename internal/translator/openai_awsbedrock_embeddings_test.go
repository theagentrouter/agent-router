// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package translator

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/require"

	"github.com/envoyproxy/ai-gateway/internal/apischema/openai"
	"github.com/envoyproxy/ai-gateway/internal/internalapi"
	"github.com/envoyproxy/ai-gateway/internal/json"
	tracingapi "github.com/envoyproxy/ai-gateway/internal/tracing/tracingapi"
)

func TestEmbeddingOpenAIToAWSBedrockTranslator_RequestBody(t *testing.T) {
	tests := []struct {
		name                string
		modelNameOverride   internalapi.ModelNameOverride
		input               openai.EmbeddingRequest
		wantErr             bool
		wantPath            string
		wantBodyContains    []string
		wantBodyNotContains []string
	}{
		{
			// v1 only supports inputText; dimensions/normalize/embeddingTypes must NOT appear in the body.
			name: "v1 model - only inputText sent",
			input: openai.EmbeddingRequest{
				EmbeddingBaseRequest: openai.EmbeddingBaseRequest{Model: "amazon.titan-embed-text-v1:2"},
				OfCompletion: &openai.EmbeddingCompletionRequest{
					Model: "amazon.titan-embed-text-v1:2",
					Input: openai.EmbeddingRequestInput{Value: "hello world"},
				},
			},
			wantPath:         "/model/amazon.titan-embed-text-v1:2/invoke",
			wantBodyContains: []string{`"inputText":"hello world"`},
		},
		{
			name: "string input",
			input: openai.EmbeddingRequest{
				EmbeddingBaseRequest: openai.EmbeddingBaseRequest{Model: "amazon.titan-embed-text-v2:0"},
				OfCompletion: &openai.EmbeddingCompletionRequest{
					Model: "amazon.titan-embed-text-v2:0",
					Input: openai.EmbeddingRequestInput{Value: "hello world"},
				},
			},
			wantPath:         "/model/amazon.titan-embed-text-v2:0/invoke",
			wantBodyContains: []string{`"inputText":"hello world"`},
		},
		{
			name: "single-element string slice input",
			input: openai.EmbeddingRequest{
				EmbeddingBaseRequest: openai.EmbeddingBaseRequest{Model: "amazon.titan-embed-text-v2:0"},
				OfCompletion: &openai.EmbeddingCompletionRequest{
					Model: "amazon.titan-embed-text-v2:0",
					Input: openai.EmbeddingRequestInput{Value: []string{"hello world"}},
				},
			},
			wantPath:         "/model/amazon.titan-embed-text-v2:0/invoke",
			wantBodyContains: []string{`"inputText":"hello world"`},
		},
		{
			name: "batch input rejected",
			input: openai.EmbeddingRequest{
				EmbeddingBaseRequest: openai.EmbeddingBaseRequest{Model: "amazon.titan-embed-text-v2:0"},
				OfCompletion: &openai.EmbeddingCompletionRequest{
					Model: "amazon.titan-embed-text-v2:0",
					Input: openai.EmbeddingRequestInput{Value: []string{"first", "second"}},
				},
			},
			wantErr: true,
		},
		{
			name: "empty string slice rejected",
			input: openai.EmbeddingRequest{
				EmbeddingBaseRequest: openai.EmbeddingBaseRequest{Model: "amazon.titan-embed-text-v2:0"},
				OfCompletion: &openai.EmbeddingCompletionRequest{
					Model: "amazon.titan-embed-text-v2:0",
					Input: openai.EmbeddingRequestInput{Value: []string{}},
				},
			},
			wantErr: true,
		},
		{
			name: "unsupported input type rejected",
			input: openai.EmbeddingRequest{
				EmbeddingBaseRequest: openai.EmbeddingBaseRequest{Model: "amazon.titan-embed-text-v2:0"},
				OfCompletion: &openai.EmbeddingCompletionRequest{
					Model: "amazon.titan-embed-text-v2:0",
					Input: openai.EmbeddingRequestInput{Value: 42},
				},
			},
			wantErr: true,
		},
		{
			// v1 model - when no dimensions are set, the dimensions field must be omitted entirely.
			name: "v1 model - dimensions field omitted from body",
			input: openai.EmbeddingRequest{
				EmbeddingBaseRequest: openai.EmbeddingBaseRequest{Model: "amazon.titan-embed-text-v1:2"},
				OfCompletion: &openai.EmbeddingCompletionRequest{
					Model: "amazon.titan-embed-text-v1:2",
					Input: openai.EmbeddingRequestInput{Value: "test"},
				},
			},
			wantPath:            "/model/amazon.titan-embed-text-v1:2/invoke",
			wantBodyContains:    []string{`"inputText":"test"`},
			wantBodyNotContains: []string{`"dimensions"`, `"normalize"`, `"embeddingTypes"`},
		},
		{
			// v2 model - dimensions is forwarded.
			name: "v2 model - dimensions forwarded to Titan",
			input: openai.EmbeddingRequest{
				EmbeddingBaseRequest: openai.EmbeddingBaseRequest{Model: "amazon.titan-embed-text-v2:0", Dimensions: &[]int{256}[0]},
				OfCompletion: &openai.EmbeddingCompletionRequest{
					Model: "amazon.titan-embed-text-v2:0", Dimensions: &[]int{256}[0],
					Input: openai.EmbeddingRequestInput{Value: "test"},
				},
			},
			wantPath:         "/model/amazon.titan-embed-text-v2:0/invoke",
			wantBodyContains: []string{`"inputText":"test"`, `"dimensions":256`},
		},
		{
			name:              "model name override applied",
			modelNameOverride: "amazon.titan-embed-text-v1:2",
			input: openai.EmbeddingRequest{
				EmbeddingBaseRequest: openai.EmbeddingBaseRequest{Model: "amazon.titan-embed-text-v2:0"},
				OfCompletion: &openai.EmbeddingCompletionRequest{
					Model: "amazon.titan-embed-text-v2:0",
					Input: openai.EmbeddingRequestInput{Value: "test"},
				},
			},
			wantPath:         "/model/amazon.titan-embed-text-v1:2/invoke",
			wantBodyContains: []string{`"inputText":"test"`},
		},
		{
			name: "model with spaces is path-escaped",
			input: openai.EmbeddingRequest{
				EmbeddingBaseRequest: openai.EmbeddingBaseRequest{Model: "my model v1"},
				OfCompletion: &openai.EmbeddingCompletionRequest{
					Model: "my model v1",
					Input: openai.EmbeddingRequestInput{Value: "escape test"},
				},
			},
			wantPath:         "/model/my%20model%20v1/invoke",
			wantBodyContains: []string{`"inputText":"escape test"`},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			translator := NewEmbeddingOpenAIToAWSBedrockTranslator(tc.modelNameOverride)
			originalBody, _ := json.Marshal(tc.input)

			headers, body, err := translator.RequestBody(originalBody, &tc.input, false)

			if tc.wantErr {
				require.Error(t, err)
				require.ErrorIs(t, err, internalapi.ErrInvalidRequestBody)
				return
			}

			require.NoError(t, err)
			require.Len(t, headers, 2)
			require.Equal(t, pathHeaderName, headers[0].Key())
			require.Equal(t, tc.wantPath, headers[0].Value())
			require.Equal(t, contentLengthHeaderName, headers[1].Key())
			require.Equal(t, fmt.Sprintf("%d", len(body)), headers[1].Value())

			bodyStr := string(body)
			for _, substr := range tc.wantBodyContains {
				require.Contains(t, bodyStr, substr)
			}
			for _, substr := range tc.wantBodyNotContains {
				require.NotContains(t, bodyStr, substr)
			}
		})
	}
}

func TestEmbeddingOpenAIToAWSBedrockTranslator_RequestBody_MarshalError(t *testing.T) {
	orig := json.Marshal
	t.Cleanup(func() { json.Marshal = orig })
	json.Marshal = func(any) ([]byte, error) { return nil, fmt.Errorf("injected marshal error") }

	translator := NewEmbeddingOpenAIToAWSBedrockTranslator("")
	req := openai.EmbeddingRequest{
		Model: "amazon.titan-embed-text-v2:0",
		OfCompletion: &openai.EmbeddingCompletionRequest{
			Model: "amazon.titan-embed-text-v2:0",
			Input: openai.EmbeddingRequestInput{Value: "test"},
		},
	}
	_, _, err := translator.RequestBody(nil, &req, false)
	require.ErrorContains(t, err, "failed to marshal body")
}

func TestEmbeddingOpenAIToAWSBedrockTranslator_ResponseBody_MarshalError(t *testing.T) {
	orig := json.Marshal
	t.Cleanup(func() { json.Marshal = orig })
	json.Marshal = func(any) ([]byte, error) { return nil, errors.New("injected marshal error") }

	translator := NewEmbeddingOpenAIToAWSBedrockTranslator("").(*openAIToAWSBedrockTranslatorV1Embedding)
	translator.requestModel = "amazon.titan-embed-text-v2:0"

	_, _, _, _, err := translator.ResponseBody(
		map[string]string{"content-type": "application/json"},
		strings.NewReader(`{"embedding":[0.1],"inputTextTokenCount":1}`),
		false,
		nil,
	)
	require.ErrorContains(t, err, "failed to marshal OpenAI embedding response")
}

func TestEmbeddingOpenAIToAWSBedrockTranslator_ResponseError_ReadError(t *testing.T) {
	translator := NewEmbeddingOpenAIToAWSBedrockTranslator("").(*openAIToAWSBedrockTranslatorV1Embedding)

	_, _, err := translator.ResponseError(
		map[string]string{statusHeaderName: "500"},
		iotest.ErrReader(errors.New("injected read error")),
	)
	require.ErrorContains(t, err, "read error body")
}

func TestEmbeddingOpenAIToAWSBedrockTranslator_ResponseError_MarshalError(t *testing.T) {
	orig := json.Marshal
	t.Cleanup(func() { json.Marshal = orig })
	json.Marshal = func(any) ([]byte, error) { return nil, errors.New("injected marshal error") }

	translator := NewEmbeddingOpenAIToAWSBedrockTranslator("").(*openAIToAWSBedrockTranslatorV1Embedding)

	// non-JSON body with no content-type → buildGenericError → json.Marshal fails
	_, _, err := translator.ResponseError(
		map[string]string{statusHeaderName: "503"},
		strings.NewReader("Service Unavailable"),
	)
	require.ErrorContains(t, err, "marshal error body")
}

func TestEmbeddingOpenAIToAWSBedrockTranslator_ResponseHeaders(t *testing.T) {
	translator := NewEmbeddingOpenAIToAWSBedrockTranslator("")
	headers, err := translator.ResponseHeaders(map[string]string{"content-type": "application/json"})
	require.NoError(t, err)
	require.Nil(t, headers)
}

func TestEmbeddingOpenAIToAWSBedrockTranslator_ResponseBody(t *testing.T) {
	tests := []struct {
		name             string
		requestModel     string
		titanResponse    string
		wantErr          bool
		wantResponseBody openai.EmbeddingResponse
		wantInputTokens  uint32
	}{
		{
			// v1 response has only "embedding" and "inputTextTokenCount".
			name:         "v1 response format",
			requestModel: "amazon.titan-embed-text-v1:2",
			titanResponse: `{
				"embedding": [0.1, 0.2, 0.3],
				"inputTextTokenCount": 3
			}`,
			wantResponseBody: openai.EmbeddingResponse{
				Object: "list",
				Model:  "amazon.titan-embed-text-v1:2",
				Data: []openai.Embedding{
					{
						Object:    "embedding",
						Index:     0,
						Embedding: openai.EmbeddingUnion{Value: []float64{0.1, 0.2, 0.3}},
					},
				},
				Usage: openai.EmbeddingUsage{PromptTokens: 3, TotalTokens: 3},
			},
			wantInputTokens: 3,
		},
		{
			// v2 response may contain "embeddingsByType" but we only use "embedding" and "inputTextTokenCount".
			name:         "v2 response format with embeddingsByType ignored",
			requestModel: "amazon.titan-embed-text-v2:0",
			titanResponse: `{
				"embedding": [0.1, 0.2, 0.3],
				"inputTextTokenCount": 3,
				"embeddingsByType": {"float": [0.1, 0.2, 0.3], "binary": [1, 0, 1]}
			}`,
			wantResponseBody: openai.EmbeddingResponse{
				Object: "list",
				Model:  "amazon.titan-embed-text-v2:0",
				Data: []openai.Embedding{
					{
						Object:    "embedding",
						Index:     0,
						Embedding: openai.EmbeddingUnion{Value: []float64{0.1, 0.2, 0.3}},
					},
				},
				Usage: openai.EmbeddingUsage{PromptTokens: 3, TotalTokens: 3},
			},
			wantInputTokens: 3,
		},
		{
			name:         "successful response",
			requestModel: "amazon.titan-embed-text-v2:0",
			titanResponse: `{
				"embedding": [0.1, 0.2, 0.3],
				"inputTextTokenCount": 3
			}`,
			wantResponseBody: openai.EmbeddingResponse{
				Object: "list",
				Model:  "amazon.titan-embed-text-v2:0",
				Data: []openai.Embedding{
					{
						Object:    "embedding",
						Index:     0,
						Embedding: openai.EmbeddingUnion{Value: []float64{0.1, 0.2, 0.3}},
					},
				},
				Usage: openai.EmbeddingUsage{
					PromptTokens: 3,
					TotalTokens:  3,
				},
			},
			wantInputTokens: 3,
		},
		{
			name:         "request model echoed in response",
			requestModel: "amazon.titan-embed-text-v1:2",
			titanResponse: `{
				"embedding": [0.5],
				"inputTextTokenCount": 1
			}`,
			wantResponseBody: openai.EmbeddingResponse{
				Object: "list",
				Model:  "amazon.titan-embed-text-v1:2",
				Data: []openai.Embedding{
					{
						Object:    "embedding",
						Index:     0,
						Embedding: openai.EmbeddingUnion{Value: []float64{0.5}},
					},
				},
				Usage: openai.EmbeddingUsage{
					PromptTokens: 1,
					TotalTokens:  1,
				},
			},
			wantInputTokens: 1,
		},
		{
			name:          "invalid JSON response",
			requestModel:  "amazon.titan-embed-text-v2:0",
			titanResponse: `not valid json`,
			wantErr:       true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			translator := NewEmbeddingOpenAIToAWSBedrockTranslator("").(*openAIToAWSBedrockTranslatorV1Embedding)
			translator.requestModel = tc.requestModel

			headers, body, tokenUsage, responseModel, err := translator.ResponseBody(
				map[string]string{"content-type": "application/json"},
				strings.NewReader(tc.titanResponse),
				true,
				nil,
			)

			if tc.wantErr {
				require.Error(t, err)
				return
			}

			require.NoError(t, err)
			require.Len(t, headers, 1)
			require.Equal(t, contentLengthHeaderName, headers[0].Key())
			require.Equal(t, fmt.Sprintf("%d", len(body)), headers[0].Value())
			require.Equal(t, tc.requestModel, responseModel)

			inputTokens, ok := tokenUsage.InputTokens()
			require.True(t, ok)
			require.Equal(t, tc.wantInputTokens, inputTokens)
			_, ok = tokenUsage.OutputTokens()
			require.False(t, ok) // no output tokens for embeddings

			var actualResp openai.EmbeddingResponse
			require.NoError(t, json.Unmarshal(body, &actualResp))
			require.Equal(t, tc.wantResponseBody.Object, actualResp.Object)
			require.Equal(t, tc.wantResponseBody.Model, actualResp.Model)
			require.Equal(t, tc.wantResponseBody.Usage, actualResp.Usage)
			require.Len(t, actualResp.Data, len(tc.wantResponseBody.Data))
			for i := range tc.wantResponseBody.Data {
				require.Equal(t, tc.wantResponseBody.Data[i].Object, actualResp.Data[i].Object)
				require.Equal(t, tc.wantResponseBody.Data[i].Index, actualResp.Data[i].Index)
				wantVec := tc.wantResponseBody.Data[i].Embedding.Value.([]float64)
				actualVec := actualResp.Data[i].Embedding.Value.([]float64)
				require.Len(t, actualVec, len(wantVec))
				for j, wantVal := range wantVec {
					require.InDelta(t, wantVal, actualVec[j], 1e-6)
				}
			}
		})
	}
}

// mockEmbeddingsSpan is a test implementation of tracingapi.EmbeddingsSpan.
type mockEmbeddingsSpan struct {
	recordedResponse *openai.EmbeddingResponse
}

func (m *mockEmbeddingsSpan) RecordResponseChunk(_ *struct{}) {}
func (m *mockEmbeddingsSpan) RecordResponse(resp *openai.EmbeddingResponse) {
	m.recordedResponse = resp
}
func (m *mockEmbeddingsSpan) EndSpanOnError(_ int, _ []byte) {}
func (m *mockEmbeddingsSpan) EndSpan()                       {}

var _ tracingapi.EmbeddingsSpan = (*mockEmbeddingsSpan)(nil)

func TestEmbeddingOpenAIToAWSBedrockTranslator_ResponseBody_SpanRecorded(t *testing.T) {
	translator := NewEmbeddingOpenAIToAWSBedrockTranslator("").(*openAIToAWSBedrockTranslatorV1Embedding)
	translator.requestModel = "amazon.titan-embed-text-v2:0"

	span := &mockEmbeddingsSpan{}
	_, _, _, _, err := translator.ResponseBody(
		map[string]string{"content-type": "application/json"},
		strings.NewReader(`{"embedding":[0.1,0.2],"inputTextTokenCount":2}`),
		true,
		span,
	)
	require.NoError(t, err)
	require.NotNil(t, span.recordedResponse)
	require.Equal(t, "amazon.titan-embed-text-v2:0", span.recordedResponse.Model)
}

func TestEmbeddingOpenAIToAWSBedrockTranslator_ResponseError(t *testing.T) {
	tests := []struct {
		name           string
		headers        map[string]string
		body           string
		wantErr        bool
		wantNilHeaders bool
		wantError      openai.Error
	}{
		{
			name: "JSON Bedrock error with x-amzn-errortype",
			headers: map[string]string{
				statusHeaderName:       "400",
				contentTypeHeaderName:  "application/json",
				awsErrorTypeHeaderName: "ValidationException",
			},
			body: `{"message":"Malformed input request"}`,
			wantError: openai.Error{
				Type: "error",
				Error: openai.ErrorType{
					Type:    "ValidationException",
					Message: "Malformed input request",
					Code:    &[]string{"400"}[0],
				},
			},
		},
		{
			name: "non-JSON error body wrapped as backend error",
			headers: map[string]string{
				statusHeaderName: "503",
			},
			body: "Service Unavailable",
			wantError: openai.Error{
				Type: "error",
				Error: openai.ErrorType{
					Type:    awsBedrockBackendError,
					Message: "Service Unavailable",
					Code:    &[]string{"503"}[0],
				},
			},
		},
		{
			name: "JSON error with namespaced error type",
			headers: map[string]string{
				statusHeaderName:       "400",
				contentTypeHeaderName:  "application/json",
				awsErrorTypeHeaderName: "ValidationException:http://internal.amazon.com/coral/com.amazon.bedrock#ValidationException",
			},
			body: `{"message":"Malformed input request"}`,
			wantError: openai.Error{
				Type: "error",
				Error: openai.ErrorType{
					Type:    "ValidationException:http://internal.amazon.com/coral/com.amazon.bedrock#ValidationException",
					Message: "Malformed input request",
					Code:    &[]string{"400"}[0],
				},
			},
		},
		{
			name: "500 error with JSON content type",
			headers: map[string]string{
				statusHeaderName:       "500",
				contentTypeHeaderName:  "application/json; charset=utf-8",
				awsErrorTypeHeaderName: "InternalServerException",
			},
			body: `{"message":"Internal server error"}`,
			wantError: openai.Error{
				Type: "error",
				Error: openai.ErrorType{
					Type:    "InternalServerException",
					Message: "Internal server error",
					Code:    &[]string{"500"}[0],
				},
			},
		},
		{
			name: "malformed JSON body with AWS error type returns error",
			headers: map[string]string{
				statusHeaderName:       "400",
				contentTypeHeaderName:  "application/json",
				awsErrorTypeHeaderName: "ValidationException",
			},
			body:    `not valid json`,
			wantErr: true,
		},
		{
			name: "gateway-generated JSON error passed through unchanged",
			headers: map[string]string{
				statusHeaderName:      "422",
				contentTypeHeaderName: "application/json",
			},
			body:           `{"type":"error","error":{"type":"UnprocessableEntity","code":"422","message":"invalid request body: AWS Bedrock Titan does not support batch embeddings (got 2 inputs)"}}`,
			wantNilHeaders: true,
			wantError: openai.Error{
				Type: "error",
				Error: openai.ErrorType{
					Type:    "UnprocessableEntity",
					Message: "invalid request body: AWS Bedrock Titan does not support batch embeddings (got 2 inputs)",
					Code:    &[]string{"422"}[0],
				},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			translator := NewEmbeddingOpenAIToAWSBedrockTranslator("").(*openAIToAWSBedrockTranslatorV1Embedding)

			headers, body, err := translator.ResponseError(tc.headers, strings.NewReader(tc.body))

			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			if tc.wantNilHeaders {
				require.Nil(t, headers)
			} else {
				require.Len(t, headers, 2)
				require.Equal(t, contentTypeHeaderName, headers[0].Key())
				require.Equal(t, jsonContentType, headers[0].Value()) //nolint:testifylint
				require.Equal(t, contentLengthHeaderName, headers[1].Key())
				require.Equal(t, fmt.Sprintf("%d", len(body)), headers[1].Value())
			}

			var actualErr openai.Error
			require.NoError(t, json.Unmarshal(body, &actualErr))
			if diff := cmp.Diff(tc.wantError, actualErr); diff != "" {
				t.Errorf("Error response mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
