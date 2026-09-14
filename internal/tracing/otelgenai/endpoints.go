// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package otelgenai

import (
	"go.opentelemetry.io/otel/attribute"

	anthropicschema "github.com/envoyproxy/ai-gateway/internal/apischema/anthropic"
	"github.com/envoyproxy/ai-gateway/internal/apischema/cohere"
	"github.com/envoyproxy/ai-gateway/internal/apischema/openai"
	"github.com/envoyproxy/ai-gateway/internal/apischema/openai/tokenize"
	"github.com/envoyproxy/ai-gateway/internal/tracing/tracingapi"
)

// This file wires each endpoint to the shared recorder. Every constructor
// supplies only the two things that differ between endpoints: how to read the
// requested model, and what the response contributes.

func configOrEnv(config *Config) *Config {
	if config == nil {
		return NewConfigFromEnv()
	}
	return config
}

// NewChatCompletionRecorder creates a tracingapi.ChatCompletionRecorder.
func NewChatCompletionRecorder(config *Config) tracingapi.ChatCompletionRecorder {
	return &recorder[openai.ChatCompletionRequest, openai.ChatCompletionResponse, openai.ChatCompletionResponseChunk]{
		operation:       OperationChat,
		config:          configOrEnv(config),
		requestModel:    func(r *openai.ChatCompletionRequest) string { return r.Model },
		requestAttrs:    chatRequestAttrs,
		responseAttrs:   chatCompletionResponseAttrs,
		chunkAttrs:      chatCompletionChunkAttrs,
		inputMessages:   chatInputMessages,
		outputMessages:  chatOutputMessages,
		toolDefinitions: chatToolDefinitions,
		chunkMessages:   chatCompletionChunkMessages,
	}
}

func chatCompletionResponseAttrs(resp *openai.ChatCompletionResponse) []attribute.KeyValue {
	attrs := responseIdentityAttrs(resp.ID, resp.Model)
	attrs = append(attrs, usageAttrs(resp.Usage.PromptTokens, resp.Usage.CompletionTokens)...)
	attrs = append(attrs, chatUsageDetailAttrs(&resp.Usage)...)
	if len(resp.Choices) > 0 {
		reasons := make([]string, 0, len(resp.Choices))
		for i := range resp.Choices {
			if reason := resp.Choices[i].FinishReason; reason != "" {
				reasons = append(reasons, string(reason))
			}
		}
		if len(reasons) > 0 {
			attrs = append(attrs, attribute.StringSlice(ResponseFinishReasons, reasons))
		}
	}
	return attrs
}

// chatCompletionChunkAttrs records what the final chunks reveal. Streaming
// responses carry the id and model on every chunk and usage only on the last
// one that reports it.
func chatCompletionChunkAttrs(chunks []*openai.ChatCompletionResponseChunk) []attribute.KeyValue {
	var id, model string
	var usage *openai.Usage
	var reasons []string
	for _, c := range chunks {
		if c == nil {
			continue
		}
		if c.ID != "" {
			id = c.ID
		}
		if c.Model != "" {
			model = c.Model
		}
		if c.Usage != nil {
			usage = c.Usage
		}
		for i := range c.Choices {
			if reason := c.Choices[i].FinishReason; reason != "" {
				reasons = append(reasons, string(reason))
			}
		}
	}
	attrs := responseIdentityAttrs(id, model)
	if usage != nil {
		// The same breakdown the unary path records, so streaming and
		// non-streaming responses report identical usage.
		attrs = append(attrs, usageAttrs(usage.PromptTokens, usage.CompletionTokens)...)
		attrs = append(attrs, chatUsageDetailAttrs(usage)...)
	}
	if len(reasons) > 0 {
		attrs = append(attrs, attribute.StringSlice(ResponseFinishReasons, reasons))
	}
	return attrs
}

// chatCompletionChunkMessages folds the streamed deltas into assistant
// messages. Content arrives as fragments, so text is accumulated per choice.
func chatCompletionChunkMessages(chunks []*openai.ChatCompletionResponseChunk) []message {
	type accumulator struct {
		role         string
		text         string
		finishReason string
		toolCalls    map[int64]*messagePart
		toolOrder    []int64
	}
	byChoice := map[int64]*accumulator{}
	var order []int64

	for _, c := range chunks {
		if c == nil {
			continue
		}
		for i := range c.Choices {
			choice := &c.Choices[i]
			acc, ok := byChoice[choice.Index]
			if !ok {
				acc = &accumulator{role: "assistant", toolCalls: map[int64]*messagePart{}}
				byChoice[choice.Index] = acc
				order = append(order, choice.Index)
			}
			if choice.Delta == nil {
				if choice.FinishReason != "" {
					acc.finishReason = string(choice.FinishReason)
				}
				continue
			}
			if choice.Delta.Role != "" {
				acc.role = choice.Delta.Role
			}
			if choice.Delta.Content != nil {
				acc.text += *choice.Delta.Content
			}
			for j := range choice.Delta.ToolCalls {
				call := &choice.Delta.ToolCalls[j]
				// Fragments carry the index of the tool call they belong to.
				// Keying on the position within this chunk would misattribute
				// arguments when a chunk repeats or reorders calls.
				part, ok := acc.toolCalls[call.Index]
				if !ok {
					part = &messagePart{Type: partTypeToolCall}
					acc.toolCalls[call.Index] = part
					acc.toolOrder = append(acc.toolOrder, call.Index)
				}
				if call.ID != nil && *call.ID != "" {
					part.ID = *call.ID
				}
				if call.Function.Name != "" {
					part.Name = call.Function.Name
				}
				part.Arguments += call.Function.Arguments
			}
			if choice.FinishReason != "" {
				acc.finishReason = string(choice.FinishReason)
			}
		}
	}

	msgs := make([]message, 0, len(order))
	for _, idx := range order {
		acc := byChoice[idx]
		m := message{Role: acc.role, FinishReason: acc.finishReason}
		m.Parts = append(m.Parts, textPart(acc.text)...)
		for _, j := range acc.toolOrder {
			m.Parts = append(m.Parts, *acc.toolCalls[j])
		}
		msgs = append(msgs, m)
	}
	return msgs
}

// NewCompletionRecorder creates a tracingapi.CompletionRecorder.
func NewCompletionRecorder(config *Config) tracingapi.CompletionRecorder {
	return &recorder[openai.CompletionRequest, openai.CompletionResponse, openai.CompletionResponse]{
		operation:      OperationTextCompletion,
		config:         configOrEnv(config),
		requestModel:   func(r *openai.CompletionRequest) string { return r.Model },
		requestAttrs:   completionRequestAttrs,
		responseAttrs:  completionResponseAttrs,
		inputMessages:  completionInputMessages,
		outputMessages: completionOutputMessages,
		// Completion streaming chunks are full responses, so the last one wins.
		foldChunks: func(chunks []*openai.CompletionResponse) *openai.CompletionResponse {
			for i := len(chunks) - 1; i >= 0; i-- {
				if chunks[i] != nil {
					return chunks[i]
				}
			}
			return &openai.CompletionResponse{}
		},
	}
}

func completionResponseAttrs(resp *openai.CompletionResponse) []attribute.KeyValue {
	attrs := responseIdentityAttrs(resp.ID, resp.Model)
	if resp.Usage != nil {
		attrs = append(attrs, usageAttrs(resp.Usage.PromptTokens, resp.Usage.CompletionTokens)...)
	}
	return attrs
}

// NewEmbeddingsRecorder creates a tracingapi.EmbeddingsRecorder.
func NewEmbeddingsRecorder(config *Config) tracingapi.EmbeddingsRecorder {
	return &recorder[openai.EmbeddingRequest, openai.EmbeddingResponse, struct{}]{
		operation:    OperationEmbeddings,
		config:       configOrEnv(config),
		requestModel: func(r *openai.EmbeddingRequest) string { return r.Model },
		requestAttrs: embeddingsRequestAttrs,
		responseAttrs: func(resp *openai.EmbeddingResponse) []attribute.KeyValue {
			attrs := responseIdentityAttrs("", resp.Model)
			// Embeddings report no output tokens.
			attrs = append(attrs, usageAttrs(resp.Usage.PromptTokens, 0)...)
			// Vectors are returned either as floats or as a base64 string. The
			// dimension count is only knowable in the former case.
			if len(resp.Data) > 0 {
				if floats, ok := resp.Data[0].Embedding.Value.([]float64); ok && len(floats) > 0 {
					attrs = append(attrs, attribute.Int(EmbeddingsDimensionCount, len(floats)))
				}
			}
			return attrs
		},
	}
}

// NewImageGenerationRecorder creates a tracingapi.ImageGenerationRecorder.
func NewImageGenerationRecorder(config *Config) tracingapi.ImageGenerationRecorder {
	return &recorder[openai.ImageGenerationRequest, openai.ImageGenerationResponse, struct{}]{
		operation:    OperationImageGeneration,
		config:       configOrEnv(config),
		requestModel: func(r *openai.ImageGenerationRequest) string { return r.Model },
	}
}

// NewResponsesRecorder creates a tracingapi.ResponsesRecorder.
//
// The Responses API is chat-shaped, so it reports the chat operation.
func NewResponsesRecorder(config *Config) tracingapi.ResponsesRecorder {
	return &recorder[openai.ResponseRequest, openai.Response, openai.ResponseStreamEventUnion]{
		operation:          OperationChat,
		config:             configOrEnv(config),
		requestModel:       func(r *openai.ResponseRequest) string { return r.Model },
		requestAttrs:       responsesRequestAttrs,
		responseAttrs:      responsesResponseAttrs,
		outputMessages:     responsesOutputMessages,
		systemInstructions: responsesSystemInstructions,
		conversationID:     responsesConversationID,
	}
}

// NewSpeechRecorder creates a tracingapi.SpeechRecorder.
func NewSpeechRecorder(config *Config) tracingapi.SpeechRecorder {
	return &recorder[openai.SpeechRequest, []byte, openai.SpeechStreamChunk]{
		operation:    OperationSpeech,
		config:       configOrEnv(config),
		requestModel: func(r *openai.SpeechRequest) string { return r.Model },
	}
}

// NewTranscriptionRecorder creates a tracingapi.TranscriptionRecorder.
func NewTranscriptionRecorder(config *Config) tracingapi.TranscriptionRecorder {
	return &recorder[openai.TranscriptionRequest, openai.TranscriptionResponse, openai.TranscriptionStreamEvent]{
		operation:    OperationTranscription,
		config:       configOrEnv(config),
		requestModel: func(r *openai.TranscriptionRequest) string { return r.Model },
	}
}

// NewTranslationRecorder creates a tracingapi.TranslationRecorder.
func NewTranslationRecorder(config *Config) tracingapi.TranslationRecorder {
	return &recorder[openai.TranslationRequest, openai.TranslationResponse, struct{}]{
		operation:    OperationTranslation,
		config:       configOrEnv(config),
		requestModel: func(r *openai.TranslationRequest) string { return r.Model },
	}
}

// NewRerankRecorder creates a tracingapi.RerankRecorder.
func NewRerankRecorder(config *Config) tracingapi.RerankRecorder {
	return &recorder[cohere.RerankV2Request, cohere.RerankV2Response, struct{}]{
		operation:    OperationRerank,
		config:       configOrEnv(config),
		requestModel: func(r *cohere.RerankV2Request) string { return r.Model },
	}
}

// NewMessageRecorder creates a tracingapi.MessageRecorder.
//
// Anthropic messages are chat completions, so they report the chat operation.
// Note that metrics report this endpoint as "messages".
func NewMessageRecorder(config *Config) tracingapi.MessageRecorder {
	return &recorder[anthropicschema.MessagesRequest, anthropicschema.MessagesResponse, anthropicschema.MessagesStreamChunk]{
		operation:          OperationChat,
		config:             configOrEnv(config),
		requestModel:       func(r *anthropicschema.MessagesRequest) string { return r.Model },
		requestAttrs:       anthropicRequestAttrs,
		responseAttrs:      anthropicResponseAttrs,
		inputMessages:      anthropicInputMessages,
		outputMessages:     anthropicOutputMessages,
		systemInstructions: anthropicSystemInstructions,
		toolDefinitions:    anthropicToolDefinitions,
		foldChunks:         anthropicschema.MessagesResponseFromStream,
	}
}

// NewTokenizeRecorder creates a tracingapi.TokenizeRecorder.
func NewTokenizeRecorder(config *Config) tracingapi.TokenizeRecorder {
	return &recorder[tokenize.RequestUnion, tokenize.Response, struct{}]{
		operation: OperationTokenize,
		config:    configOrEnv(config),
		requestModel: func(r *tokenize.RequestUnion) string {
			switch {
			case r.CompletionRequest != nil:
				return r.CompletionRequest.Model
			case r.ChatRequest != nil:
				return r.ChatRequest.Model
			default:
				return ""
			}
		},
	}
}

// NewResponsesInputTokensRecorder creates a tracingapi.ResponsesInputTokensRecorder.
//
// The endpoint counts the input tokens a Responses request would consume without
// running inference, so it reports the tokenize operation rather than chat: no
// model is invoked and the only result is a token count. Reusing the existing
// operation avoids inventing a gen_ai.operation.name value the conventions do
// not define.
func NewResponsesInputTokensRecorder(config *Config) tracingapi.ResponsesInputTokensRecorder {
	return &recorder[openai.ResponseRequest, openai.ResponsesInputTokensResponse, struct{}]{
		operation:    OperationTokenize,
		config:       configOrEnv(config),
		requestModel: func(r *openai.ResponseRequest) string { return r.Model },
		responseAttrs: func(r *openai.ResponsesInputTokensResponse) []attribute.KeyValue {
			return []attribute.KeyValue{
				attribute.Int(UsageInputTokens, int(r.InputTokens)),
			}
		},
	}
}

// NewCountTokensRecorder creates a tracingapi.CountTokensRecorder.
//
// Anthropic's count_tokens endpoint reports how many input tokens a
// conversation would consume without running inference, so it reports the
// tokenize operation for the same reason NewResponsesInputTokensRecorder does.
// The request carries a full conversation, so it records the same content the
// messages endpoint would, gated on the same content capture setting.
func NewCountTokensRecorder(config *Config) tracingapi.CountTokensRecorder {
	return &recorder[anthropicschema.CountTokensRequest, anthropicschema.CountTokensResponse, struct{}]{
		operation:    OperationTokenize,
		config:       configOrEnv(config),
		requestModel: func(r *anthropicschema.CountTokensRequest) string { return r.Model },
		inputMessages: func(r *anthropicschema.CountTokensRequest) []message {
			return anthropicMessages(r.Messages)
		},
		systemInstructions: func(r *anthropicschema.CountTokensRequest) []messagePart {
			return anthropicSystemParts(r.System)
		},
		toolDefinitions: func(r *anthropicschema.CountTokensRequest) []toolDefinition {
			return anthropicTools(r.Tools)
		},
		responseAttrs: func(r *anthropicschema.CountTokensResponse) []attribute.KeyValue {
			return []attribute.KeyValue{
				attribute.Int(UsageInputTokens, int(r.InputTokens)),
			}
		},
	}
}

// chatUsageDetailAttrs extracts the cache and reasoning breakdowns that OpenAI
// reports in the nested token details.
func chatUsageDetailAttrs(u *openai.Usage) []attribute.KeyValue {
	var cacheRead, cacheCreation, reasoning int
	if td := u.PromptTokensDetails; td != nil {
		cacheRead = td.CachedTokens
		cacheCreation = td.CacheCreationTokens
	}
	if td := u.CompletionTokensDetails; td != nil {
		reasoning = td.ReasoningTokens
	}
	return usageDetailAttrs(cacheRead, cacheCreation, reasoning)
}

// responseIdentityAttrs builds the response id and model attributes, omitting
// whichever the provider did not return.
func responseIdentityAttrs(id, model string) []attribute.KeyValue {
	var attrs []attribute.KeyValue
	if id != "" {
		attrs = append(attrs, attribute.String(ResponseID, id))
	}
	if model != "" {
		attrs = append(attrs, attribute.String(ResponseModel, model))
	}
	return attrs
}
