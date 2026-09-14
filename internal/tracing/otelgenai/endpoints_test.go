// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package otelgenai

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	oteltrace "go.opentelemetry.io/otel/trace"

	"github.com/envoyproxy/ai-gateway/internal/apischema/anthropic"
	"github.com/envoyproxy/ai-gateway/internal/apischema/cohere"
	"github.com/envoyproxy/ai-gateway/internal/apischema/openai"
	"github.com/envoyproxy/ai-gateway/internal/apischema/openai/tokenize"
	internaltesting "github.com/envoyproxy/ai-gateway/internal/testing"
	"github.com/envoyproxy/ai-gateway/internal/testing/testotel"
	"github.com/envoyproxy/ai-gateway/internal/tracing/tracingapi"
)

func TestChatCompletionRecorder_StartParams(t *testing.T) {
	tests := []struct {
		name         string
		model        string
		expectedName string
	}{
		{name: "names the span operation and model", model: "gpt-5-nano", expectedName: "chat gpt-5-nano"},
		// An empty model must not leave a trailing space in the span name.
		{name: "omits an unknown model", model: "", expectedName: "chat"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := NewChatCompletionRecorder(NewConfig())
			spanName, opts := r.StartParams(&openai.ChatCompletionRequest{Model: tc.model}, nil)
			require.Equal(t, tc.expectedName, spanName)

			span := testotel.RecordNewSpan(t, spanName, opts...)
			require.Equal(t, tc.expectedName, span.Name)
			// The conventions specify CLIENT for inference spans.
			require.Equal(t, oteltrace.SpanKindClient, span.SpanKind)
		})
	}
}

func TestChatCompletionRecorder_RecordRequest(t *testing.T) {
	r := NewChatCompletionRecorder(NewConfig())

	span := testotel.RecordWithSpan(t, func(span oteltrace.Span) bool {
		r.RecordRequest(span, &openai.ChatCompletionRequest{Model: "gpt-5-nano"}, []byte(`{"model":"gpt-5-nano"}`))
		return false
	})

	testotel.RequireAttributesEqual(t, []attribute.KeyValue{
		attribute.String(OperationName, "chat"),
		attribute.String(RequestModel, "gpt-5-nano"),
	}, span.Attributes)
}

// TestChatCompletionRecorder_RecordRequest_noContentByDefault pins that the raw
// request body is not copied onto the span, which is the whole point of the
// opt-in content default.
func TestChatCompletionRecorder_RecordRequest_noContentByDefault(t *testing.T) {
	const secret = "SENSITIVE-PROMPT-TEXT"
	body := []byte(`{"model":"gpt-5-nano","messages":[{"role":"user","content":"` + secret + `"}]}`)

	r := NewChatCompletionRecorder(NewConfig())
	span := testotel.RecordWithSpan(t, func(span oteltrace.Span) bool {
		r.RecordRequest(span, &openai.ChatCompletionRequest{Model: "gpt-5-nano"}, body)
		return false
	})

	for _, attr := range span.Attributes {
		require.NotContains(t, attr.Value.AsString(), secret, "attribute %s", attr.Key)
	}
}

func TestChatCompletionRecorder_RecordResponse(t *testing.T) {
	r := NewChatCompletionRecorder(NewConfig())

	resp := &openai.ChatCompletionResponse{
		ID:    "chatcmpl-123",
		Model: "gpt-5-nano-2025-08-07",
		Usage: openai.Usage{PromptTokens: 11, CompletionTokens: 7, TotalTokens: 18},
		Choices: []openai.ChatCompletionResponseChoice{
			{FinishReason: "stop"},
		},
	}

	span := testotel.RecordWithSpan(t, func(span oteltrace.Span) bool {
		r.RecordResponse(span, resp)
		return false
	})

	testotel.RequireAttributesEqual(t, []attribute.KeyValue{
		attribute.String(ResponseID, "chatcmpl-123"),
		attribute.String(ResponseModel, "gpt-5-nano-2025-08-07"),
		attribute.Int(UsageInputTokens, 11),
		attribute.Int(UsageOutputTokens, 7),
		attribute.StringSlice(ResponseFinishReasons, []string{"stop"}),
	}, span.Attributes)
	require.Equal(t, codes.Ok, span.Status.Code)
}

// TestChatCompletionRecorder_RecordResponse_omitsAbsent pins that absent values
// are omitted rather than emitted as zero, which would pollute backends with
// meaningless "0 tokens" datapoints.
func TestChatCompletionRecorder_RecordResponse_omitsAbsent(t *testing.T) {
	r := NewChatCompletionRecorder(NewConfig())

	span := testotel.RecordWithSpan(t, func(span oteltrace.Span) bool {
		r.RecordResponse(span, &openai.ChatCompletionResponse{})
		return false
	})

	require.Empty(t, span.Attributes)
	require.Equal(t, codes.Ok, span.Status.Code)
}

func TestChatCompletionRecorder_RecordResponseChunks(t *testing.T) {
	r := NewChatCompletionRecorder(NewConfig())

	finish := openai.ChatCompletionChoicesFinishReason("stop")
	chunks := []*openai.ChatCompletionResponseChunk{
		{ID: "chatcmpl-123", Model: "gpt-5-nano-2025-08-07"},
		{ID: "chatcmpl-123", Model: "gpt-5-nano-2025-08-07", Choices: []openai.ChatCompletionResponseChunkChoice{{FinishReason: finish}}},
		{ID: "chatcmpl-123", Model: "gpt-5-nano-2025-08-07", Usage: &openai.Usage{PromptTokens: 11, CompletionTokens: 7}},
	}

	span := testotel.RecordWithSpan(t, func(span oteltrace.Span) bool {
		r.RecordResponseChunks(span, chunks)
		return false
	})

	// Streaming must produce the same attributes as the equivalent unary response.
	testotel.RequireAttributesEqual(t, []attribute.KeyValue{
		attribute.String(ResponseID, "chatcmpl-123"),
		attribute.String(ResponseModel, "gpt-5-nano-2025-08-07"),
		attribute.Int(UsageInputTokens, 11),
		attribute.Int(UsageOutputTokens, 7),
		attribute.StringSlice(ResponseFinishReasons, []string{"stop"}),
	}, span.Attributes)
	require.Equal(t, codes.Ok, span.Status.Code)
}

func TestChatCompletionRecorder_RecordResponseChunks_boundaries(t *testing.T) {
	tests := []struct {
		name   string
		chunks []*openai.ChatCompletionResponseChunk
	}{
		{name: "zero chunks", chunks: nil},
		{name: "empty slice", chunks: []*openai.ChatCompletionResponseChunk{}},
		{name: "one empty chunk", chunks: []*openai.ChatCompletionResponseChunk{{}}},
		{name: "nil chunk is skipped", chunks: []*openai.ChatCompletionResponseChunk{nil}},
		{name: "nil among valid", chunks: []*openai.ChatCompletionResponseChunk{nil, {ID: "x"}, nil}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := NewChatCompletionRecorder(NewConfig())
			require.NotPanics(t, func() {
				testotel.RecordWithSpan(t, func(span oteltrace.Span) bool {
					r.RecordResponseChunks(span, tc.chunks)
					return false
				})
			})
		})
	}
}

// TestMessageRecorder_RecordResponseChunks_boundaries covers the same
// boundaries for the recorder that folds chunks rather than mapping them
// directly, since that path reaches a different implementation.
func TestMessageRecorder_RecordResponseChunks_boundaries(t *testing.T) {
	tests := []struct {
		name   string
		chunks []*anthropic.MessagesStreamChunk
	}{
		{name: "zero chunks", chunks: nil},
		{name: "empty slice", chunks: []*anthropic.MessagesStreamChunk{}},
		{name: "one empty chunk", chunks: []*anthropic.MessagesStreamChunk{{}}},
		{name: "nil chunk is skipped", chunks: []*anthropic.MessagesStreamChunk{nil}},
		{name: "nil among valid", chunks: []*anthropic.MessagesStreamChunk{nil, {}, nil}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := NewMessageRecorder(NewConfig())
			require.NotPanics(t, func() {
				testotel.RecordWithSpan(t, func(span oteltrace.Span) bool {
					r.RecordResponseChunks(span, tc.chunks)
					return false
				})
			})
		})
	}
}

// TestRecorders_operations pins the operation each endpoint reports, since these
// values are what users group and filter spans by.
func TestRecorders_operations(t *testing.T) {
	cfg := NewConfig()

	tests := []struct {
		name              string
		spanName          string
		expectedOperation string
	}{
		{name: "chat", spanName: mustStartName(t, NewChatCompletionRecorder(cfg), &openai.ChatCompletionRequest{Model: "m"}), expectedOperation: "chat m"},
		{name: "text_completion", spanName: mustStartName(t, NewCompletionRecorder(cfg), &openai.CompletionRequest{Model: "m"}), expectedOperation: "text_completion m"},
		{name: "embeddings", spanName: mustStartName(t, NewEmbeddingsRecorder(cfg), &openai.EmbeddingRequest{
			EmbeddingBaseRequest: openai.EmbeddingBaseRequest{Model: "m"},
		}), expectedOperation: "embeddings m"},
		{name: "image_generation", spanName: mustStartName(t, NewImageGenerationRecorder(cfg), &openai.ImageGenerationRequest{Model: "m"}), expectedOperation: "image_generation m"},
		{name: "speech", spanName: mustStartName(t, NewSpeechRecorder(cfg), &openai.SpeechRequest{Model: "m"}), expectedOperation: "speech m"},
		{name: "transcription", spanName: mustStartName(t, NewTranscriptionRecorder(cfg), &openai.TranscriptionRequest{Model: "m"}), expectedOperation: "transcription m"},
		{name: "translation", spanName: mustStartName(t, NewTranslationRecorder(cfg), &openai.TranslationRequest{Model: "m"}), expectedOperation: "translation m"},
		{name: "rerank", spanName: mustStartName(t, NewRerankRecorder(cfg), &cohere.RerankV2Request{Model: "m"}), expectedOperation: "rerank m"},
		// Anthropic messages are chat completions, so they share the chat
		// operation rather than minting an "anthropic" one.
		{name: "message", spanName: mustStartName(t, NewMessageRecorder(cfg), &anthropic.MessagesRequest{Model: "m"}), expectedOperation: "chat m"},
		{name: "responses", spanName: mustStartName(t, NewResponsesRecorder(cfg), &openai.ResponseRequest{Model: "m"}), expectedOperation: "chat m"},
		// The tokenize union carries the model on whichever request shape it
		// holds, and neither when it is empty.
		{name: "tokenize completion", spanName: mustStartName(t, NewTokenizeRecorder(cfg), &tokenize.RequestUnion{
			CompletionRequest: &tokenize.CompletionRequest{Model: "m"},
		}), expectedOperation: "tokenize m"},
		{name: "tokenize chat", spanName: mustStartName(t, NewTokenizeRecorder(cfg), &tokenize.RequestUnion{
			ChatRequest: &tokenize.ChatRequest{Model: "m"},
		}), expectedOperation: "tokenize m"},
		{name: "tokenize neither", spanName: mustStartName(t, NewTokenizeRecorder(cfg), &tokenize.RequestUnion{}), expectedOperation: "tokenize"},
		// Both token counting endpoints report tokenize: no inference runs, so
		// reporting chat would inflate chat span counts with requests that
		// never reached a model.
		{name: "responses input tokens", spanName: mustStartName(t, NewResponsesInputTokensRecorder(cfg), &openai.ResponseRequest{Model: "m"}), expectedOperation: "tokenize m"},
		{name: "count tokens", spanName: mustStartName(t, NewCountTokensRecorder(cfg), &anthropic.CountTokensRequest{Model: "m"}), expectedOperation: "tokenize m"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.expectedOperation, tc.spanName)
		})
	}
}

// TestCompletionRecorder_RecordResponse pins the legacy completions mapping.
// Usage is a pointer on this API, so a response that omits it must not emit
// zero token counts.
func TestCompletionRecorder_RecordResponse(t *testing.T) {
	r := NewCompletionRecorder(NewConfig())

	tests := []struct {
		name     string
		resp     *openai.CompletionResponse
		expected []attribute.KeyValue
	}{
		{
			name: "identity and usage",
			resp: &openai.CompletionResponse{
				ID:    "cmpl-1",
				Model: "gpt-3.5-turbo-instruct",
				Usage: &openai.Usage{PromptTokens: 5, CompletionTokens: 3},
			},
			expected: []attribute.KeyValue{
				attribute.String(ResponseID, "cmpl-1"),
				attribute.String(ResponseModel, "gpt-3.5-turbo-instruct"),
				attribute.Int(UsageInputTokens, 5),
				attribute.Int(UsageOutputTokens, 3),
			},
		},
		{
			name:     "absent usage",
			resp:     &openai.CompletionResponse{ID: "cmpl-2"},
			expected: []attribute.KeyValue{attribute.String(ResponseID, "cmpl-2")},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			span := testotel.RecordWithSpan(t, func(span oteltrace.Span) bool {
				r.RecordResponse(span, tc.resp)
				return false
			})
			testotel.RequireAttributesEqual(t, tc.expected, span.Attributes)
			require.Equal(t, codes.Ok, span.Status.Code)
		})
	}
}

// TestCompletionRecorder_RecordResponseChunks pins that streaming reuses the
// unary mapping. Completion chunks are whole responses rather than deltas, so
// the last one that arrived is the response.
func TestCompletionRecorder_RecordResponseChunks(t *testing.T) {
	r := NewCompletionRecorder(NewConfig())

	tests := []struct {
		name     string
		chunks   []*openai.CompletionResponse
		expected []attribute.KeyValue
	}{
		{
			name: "last chunk wins",
			chunks: []*openai.CompletionResponse{
				{ID: "cmpl-1", Model: "first"},
				{ID: "cmpl-1", Model: "last", Usage: &openai.Usage{PromptTokens: 5, CompletionTokens: 3}},
			},
			expected: []attribute.KeyValue{
				attribute.String(ResponseID, "cmpl-1"),
				attribute.String(ResponseModel, "last"),
				attribute.Int(UsageInputTokens, 5),
				attribute.Int(UsageOutputTokens, 3),
			},
		},
		{
			// A nil trailer must not erase what the stream already reported.
			name: "trailing nil is skipped",
			chunks: []*openai.CompletionResponse{
				{ID: "cmpl-2", Model: "real"},
				nil,
			},
			expected: []attribute.KeyValue{
				attribute.String(ResponseID, "cmpl-2"),
				attribute.String(ResponseModel, "real"),
			},
		},
		{name: "no chunks", chunks: nil},
		{name: "only nil chunks", chunks: []*openai.CompletionResponse{nil}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			span := testotel.RecordWithSpan(t, func(span oteltrace.Span) bool {
				r.RecordResponseChunks(span, tc.chunks)
				return false
			})
			testotel.RequireAttributesEqual(t, tc.expected, span.Attributes)
			// A stream that carried nothing usable is still a successful
			// response, not an error.
			require.Equal(t, codes.Ok, span.Status.Code)
		})
	}
}

func TestEmbeddingsRecorder_RecordRequest(t *testing.T) {
	r := NewEmbeddingsRecorder(NewConfig())

	tests := []struct {
		name           string
		encodingFormat *string
		expected       []attribute.KeyValue
	}{
		{
			name:           "encoding format",
			encodingFormat: ptr("base64"),
			expected: []attribute.KeyValue{
				attribute.String(OperationName, "embeddings"),
				attribute.String(RequestModel, "text-embedding-3-small"),
				attribute.StringSlice(RequestEncodingFormats, []string{"base64"}),
			},
		},
		{
			name: "absent encoding format",
			expected: []attribute.KeyValue{
				attribute.String(OperationName, "embeddings"),
				attribute.String(RequestModel, "text-embedding-3-small"),
			},
		},
		{
			name:           "empty encoding format is omitted",
			encodingFormat: ptr(""),
			expected: []attribute.KeyValue{
				attribute.String(OperationName, "embeddings"),
				attribute.String(RequestModel, "text-embedding-3-small"),
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := &openai.EmbeddingRequest{EmbeddingBaseRequest: openai.EmbeddingBaseRequest{
				Model:          "text-embedding-3-small",
				EncodingFormat: tc.encodingFormat,
			}}
			span := testotel.RecordWithSpan(t, func(span oteltrace.Span) bool {
				r.RecordRequest(span, req, nil)
				return false
			})
			testotel.RequireAttributesEqual(t, tc.expected, span.Attributes)
		})
	}
}

// TestEmbeddingsRecorder_RecordResponse pins that the dimension count is only
// reported when the vectors arrived as floats. Base64 vectors would need
// decoding to be measured, and a wrong dimension count is worse than none.
func TestEmbeddingsRecorder_RecordResponse(t *testing.T) {
	r := NewEmbeddingsRecorder(NewConfig())

	tests := []struct {
		name     string
		resp     *openai.EmbeddingResponse
		expected []attribute.KeyValue
	}{
		{
			name: "float vectors report dimensions",
			resp: &openai.EmbeddingResponse{
				Model: "text-embedding-3-small",
				Usage: openai.EmbeddingUsage{PromptTokens: 8, TotalTokens: 8},
				Data:  []openai.Embedding{{Embedding: openai.EmbeddingUnion{Value: []float64{0.1, 0.2, 0.3}}}},
			},
			expected: []attribute.KeyValue{
				attribute.String(ResponseModel, "text-embedding-3-small"),
				attribute.Int(UsageInputTokens, 8),
				attribute.Int(EmbeddingsDimensionCount, 3),
			},
		},
		{
			name: "base64 vectors omit dimensions",
			resp: &openai.EmbeddingResponse{
				Model: "text-embedding-3-small",
				Usage: openai.EmbeddingUsage{PromptTokens: 8},
				Data:  []openai.Embedding{{Embedding: openai.EmbeddingUnion{Value: "ZmFrZQ=="}}},
			},
			expected: []attribute.KeyValue{
				attribute.String(ResponseModel, "text-embedding-3-small"),
				attribute.Int(UsageInputTokens, 8),
			},
		},
		{
			name: "empty vector omits dimensions",
			resp: &openai.EmbeddingResponse{
				Model: "text-embedding-3-small",
				Data:  []openai.Embedding{{Embedding: openai.EmbeddingUnion{Value: []float64{}}}},
			},
			expected: []attribute.KeyValue{
				attribute.String(ResponseModel, "text-embedding-3-small"),
			},
		},
		{name: "empty response", resp: &openai.EmbeddingResponse{}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			span := testotel.RecordWithSpan(t, func(span oteltrace.Span) bool {
				r.RecordResponse(span, tc.resp)
				return false
			})
			testotel.RequireAttributesEqual(t, tc.expected, span.Attributes)
			require.Equal(t, codes.Ok, span.Status.Code)
		})
	}
}

// TestResponsesInputTokensRecorder_records pins that the endpoint reports the
// token count it returns and nothing about the request beyond the model: no
// inference runs, so sampling parameters and messages have no meaning here.
func TestResponsesInputTokensRecorder_records(t *testing.T) {
	r := NewResponsesInputTokensRecorder(&Config{CaptureMessageContent: true})

	req := &openai.ResponseRequest{Model: "gpt-5-nano", Instructions: "be brief", Temperature: ptr(0.3)}
	reqSpan := testotel.RecordWithSpan(t, func(span oteltrace.Span) bool {
		r.RecordRequest(span, req, nil)
		return false
	})
	testotel.RequireAttributesEqual(t, []attribute.KeyValue{
		attribute.String(OperationName, "tokenize"),
		attribute.String(RequestModel, "gpt-5-nano"),
	}, reqSpan.Attributes)

	respSpan := testotel.RecordWithSpan(t, func(span oteltrace.Span) bool {
		r.RecordResponse(span, &openai.ResponsesInputTokensResponse{InputTokens: 42})
		return false
	})
	testotel.RequireAttributesEqual(t, []attribute.KeyValue{
		attribute.Int(UsageInputTokens, 42),
	}, respSpan.Attributes)
	require.Equal(t, codes.Ok, respSpan.Status.Code)
}

// TestCountTokensRecorder_records pins that Anthropic's count_tokens describes
// the conversation it is counting exactly as the messages endpoint would, and
// that the content stays behind the same opt-in.
func TestCountTokensRecorder_records(t *testing.T) {
	const secret = "SENSITIVE-PROMPT-TEXT"
	req := &anthropic.CountTokensRequest{
		Model:    "claude-sonnet-5",
		System:   &anthropic.SystemPrompt{Text: secret},
		Messages: []anthropic.MessageParam{anthropicUserMessage(secret)},
		Tools: []anthropic.ToolUnion{
			{Tool: &anthropic.Tool{Type: "custom", Name: "get_weather"}},
		},
	}

	t.Run("content captured", func(t *testing.T) {
		r := NewCountTokensRecorder(&Config{CaptureMessageContent: true})
		span := testotel.RecordWithSpan(t, func(span oteltrace.Span) bool {
			r.RecordRequest(span, req, nil)
			return false
		})
		testotel.RequireAttributesEqual(t, []attribute.KeyValue{
			attribute.String(OperationName, "tokenize"),
			attribute.String(RequestModel, "claude-sonnet-5"),
			attribute.String(InputMessages, `[{"role":"user","parts":[{"type":"text","content":"`+secret+`"}]}]`),
			attribute.String(SystemInstructions, `[{"type":"text","content":"`+secret+`"}]`),
			attribute.String(ToolDefinitions, `[{"type":"custom","name":"get_weather"}]`),
		}, span.Attributes)
	})

	t.Run("content withheld by default", func(t *testing.T) {
		r := NewCountTokensRecorder(NewConfig())
		span := testotel.RecordWithSpan(t, func(span oteltrace.Span) bool {
			r.RecordRequest(span, req, nil)
			return false
		})
		testotel.RequireAttributesEqual(t, []attribute.KeyValue{
			attribute.String(OperationName, "tokenize"),
			attribute.String(RequestModel, "claude-sonnet-5"),
		}, span.Attributes)
	})

	t.Run("response is the token count", func(t *testing.T) {
		r := NewCountTokensRecorder(NewConfig())
		span := testotel.RecordWithSpan(t, func(span oteltrace.Span) bool {
			r.RecordResponse(span, &anthropic.CountTokensResponse{InputTokens: 42})
			return false
		})
		testotel.RequireAttributesEqual(t, []attribute.KeyValue{
			attribute.Int(UsageInputTokens, 42),
		}, span.Attributes)
		require.Equal(t, codes.Ok, span.Status.Code)
	})
}

// TestRecorder_RecordResponseOnError pins that the shared error path carries
// the recorder's own config, so an endpoint constructed without content capture
// cannot leak a provider error body that echoes the prompt.
func TestRecorder_RecordResponseOnError(t *testing.T) {
	const body = `{"error":{"message":"the prompt was: my secret"}}`

	for _, tc := range []struct {
		name    string
		capture bool
	}{
		{name: "capture off omits body", capture: false},
		{name: "capture on includes body", capture: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := NewChatCompletionRecorder(&Config{CaptureMessageContent: tc.capture})
			span := testotel.RecordWithSpan(t, func(span oteltrace.Span) bool {
				r.RecordResponseOnError(span, 429, []byte(body))
				return false
			})

			testotel.RequireAttributesEqual(t, []attribute.KeyValue{
				attribute.String(ErrorType, "429"),
			}, span.Attributes)
			require.Equal(t, codes.Error, span.Status.Code)
			require.Equal(t, tc.capture, strings.Contains(span.Status.Description, "my secret"))
		})
	}
}

// TestRecorders_nilConfigReadsEnv pins that a nil config falls back to the
// environment rather than to the compiled default. Passing nil is how the
// gateway's own wiring would inherit the operator's opt-in.
func TestRecorders_nilConfigReadsEnv(t *testing.T) {
	internaltesting.ClearTestEnv(t)
	t.Setenv(EnvCaptureMessageContent, "true")

	r := NewChatCompletionRecorder(nil)
	span := testotel.RecordWithSpan(t, func(span oteltrace.Span) bool {
		r.RecordRequest(span, &openai.ChatCompletionRequest{
			Model:    "gpt-5-nano",
			Messages: []openai.ChatCompletionMessageParamUnion{userMessage("hello")},
		}, nil)
		return false
	})

	require.True(t, hasAttr(span.Attributes, InputMessages))
}

func mustStartName[ReqT, RespT, ChunkT any](t *testing.T, r tracingapi.SpanRecorder[ReqT, RespT, ChunkT], req *ReqT) string {
	t.Helper()
	name, opts := r.StartParams(req, nil)
	require.NotEmpty(t, opts)
	return name
}
