// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package openai

import (
	"encoding/base64"
	"encoding/binary"
	"math"

	"go.opentelemetry.io/otel/attribute"

	"github.com/envoyproxy/ai-gateway/internal/apischema/openai"
	"github.com/envoyproxy/ai-gateway/internal/json"
	"github.com/envoyproxy/ai-gateway/internal/tracing/openinference"
)

func buildResponseAttributes(resp *openai.ChatCompletionResponse, config *openinference.TraceConfig) []attribute.KeyValue {
	var attrs []attribute.KeyValue
	// Only set the model name when the response carries one, so an empty
	// value can never overwrite the request-time llm.model_name attribute
	// (matches buildResponsesResponseAttributes below).
	if resp.Model != "" {
		attrs = append(attrs, attribute.String(openinference.LLMModelName, resp.Model))
	}

	if !config.HideOutputs {
		attrs = append(attrs, attribute.String(openinference.OutputMimeType, openinference.MimeTypeJSON))
	}

	// Note: compound match here is from Python OpenInference OpenAI config.py.
	if !config.HideOutputs && !config.HideOutputMessages {
		for i := range resp.Choices {
			choice := &resp.Choices[i]
			attrs = append(attrs, attribute.String(openinference.OutputMessageAttribute(i, openinference.MessageRole), choice.Message.Role))

			if choice.Message.Content != nil && *choice.Message.Content != "" {
				content := *choice.Message.Content
				if config.HideOutputText {
					content = openinference.RedactedValue
				}
				attrs = append(attrs, attribute.String(openinference.OutputMessageAttribute(i, openinference.MessageContent), content))
			}

			for j, toolCall := range choice.Message.ToolCalls {
				if toolCall.ID != nil {
					attrs = append(attrs, attribute.String(openinference.OutputMessageToolCallAttribute(i, j, openinference.ToolCallID), *toolCall.ID))
				}
				attrs = append(attrs,
					attribute.String(openinference.OutputMessageToolCallAttribute(i, j, openinference.ToolCallFunctionName), toolCall.Function.Name),
					attribute.String(openinference.OutputMessageToolCallAttribute(i, j, openinference.ToolCallFunctionArguments), toolCall.Function.Arguments),
				)
			}
		}
	}

	// Token counts are considered metadata and are still included even when output content is hidden.
	u := resp.Usage
	if pt := u.PromptTokens; pt > 0 {
		attrs = append(attrs, attribute.Int(openinference.LLMTokenCountPrompt, pt))
		if td := resp.Usage.PromptTokensDetails; td != nil {
			attrs = append(attrs,
				attribute.Int(openinference.LLMTokenCountPromptAudio, td.AudioTokens),
				attribute.Int(openinference.LLMTokenCountPromptCacheHit, td.CachedTokens),
				attribute.Int(openinference.LLMTokenCountPromptCacheWrite, td.CacheWriteTokensValue()),
			)
		}
	}
	if ct := u.CompletionTokens; ct > 0 {
		attrs = append(attrs, attribute.Int(openinference.LLMTokenCountCompletion, ct))
		if td := resp.Usage.CompletionTokensDetails; td != nil {
			attrs = append(attrs,
				attribute.Int(openinference.LLMTokenCountCompletionAudio, td.AudioTokens),
				attribute.Int(openinference.LLMTokenCountCompletionReasoning, td.ReasoningTokens),
			)
		}
	}
	if tt := u.TotalTokens; tt > 0 {
		attrs = append(attrs, attribute.Int(openinference.LLMTokenCountTotal, tt))
	}
	return attrs
}

func buildEmbeddingsResponseAttributes(resp *openai.EmbeddingResponse, config *openinference.TraceConfig) []attribute.KeyValue {
	var attrs []attribute.KeyValue

	// Add the model name for successful responses.
	attrs = append(attrs, attribute.String(openinference.EmbeddingModelName, resp.Model))

	if !config.HideOutputs {
		attrs = append(attrs, attribute.String(openinference.OutputMimeType, openinference.MimeTypeJSON))

		// Record embedding vectors as float arrays.
		// Per OpenInference spec: base64-encoded vectors MUST be decoded to float arrays.
		if !config.HideEmbeddingsVectors {
			for i, data := range resp.Data {
				switch v := data.Embedding.Value.(type) {
				case []float64:
					if len(v) > 0 {
						attrs = append(attrs, attribute.Float64Slice(openinference.EmbeddingVectorAttribute(i), v))
					}
				case string:
					// Decode base64-encoded embeddings to float arrays.
					if floats, err := decodeBase64Embeddings(v); err == nil && len(floats) > 0 {
						attrs = append(attrs, attribute.Float64Slice(openinference.EmbeddingVectorAttribute(i), floats))
					}
				}
			}
		}
	}

	// Token counts are considered metadata and are still included even when output content is hidden.
	if pt := resp.Usage.PromptTokens; pt > 0 {
		attrs = append(attrs, attribute.Int(openinference.LLMTokenCountPrompt, pt))
	}
	if tt := resp.Usage.TotalTokens; tt > 0 {
		attrs = append(attrs, attribute.Int(openinference.LLMTokenCountTotal, tt))
	}
	return attrs
}

// decodeBase64Embeddings decodes a base64-encoded embedding vector to []float64.
// OpenAI returns base64-encoded little-endian float32 arrays when encoding_format="base64".
func decodeBase64Embeddings(encoded string) ([]float64, error) {
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, err
	}

	// Each float32 is 4 bytes
	numFloats := len(decoded) / 4
	result := make([]float64, numFloats)

	for i := 0; i < numFloats; i++ {
		bits := binary.LittleEndian.Uint32(decoded[i*4 : (i+1)*4])
		result[i] = float64(math.Float32frombits(bits))
	}

	return result, nil
}

// buildCompletionResponseAttributes builds OpenInference attributes from the completions response.
func buildCompletionResponseAttributes(resp *openai.CompletionResponse, config *openinference.TraceConfig) []attribute.KeyValue {
	attrs := []attribute.KeyValue{
		attribute.String(openinference.LLMModelName, resp.Model),
	}

	if !config.HideOutputs {
		attrs = append(attrs, attribute.String(openinference.OutputMimeType, openinference.MimeTypeJSON))
	}

	// Handle choices using indexed attribute format.
	// Per OpenInference spec, we record completion text for each choice.
	if !config.HideOutputs && !config.HideChoices {
		for i, choice := range resp.Choices {
			text := choice.Text
			attrs = append(attrs, attribute.String(openinference.ChoiceTextAttribute(i), text))
		}
	}

	// Token counts are considered metadata and are still included even when output content is hidden.
	u := resp.Usage
	if u != nil {
		if pt := u.PromptTokens; pt > 0 {
			attrs = append(attrs, attribute.Int(openinference.LLMTokenCountPrompt, pt))
		}
		if ct := u.CompletionTokens; ct > 0 {
			attrs = append(attrs, attribute.Int(openinference.LLMTokenCountCompletion, ct))
		}
		if tt := u.TotalTokens; tt > 0 {
			attrs = append(attrs, attribute.Int(openinference.LLMTokenCountTotal, tt))
		}
	}

	return attrs
}

// buildResponsesResponseAttributes builds OpenTelemetry attributes for responses responses.
func buildResponsesResponseAttributes(resp *openai.Response, config *openinference.TraceConfig) []attribute.KeyValue {
	var attrs []attribute.KeyValue
	attrs = append(attrs, attribute.String(openinference.OutputMimeType, openinference.MimeTypeJSON))
	if resp.Model != "" {
		attrs = append(attrs, attribute.String(openinference.LLMModelName, resp.Model))
	}
	if config.HideOutputs {
		attrs = append(attrs, attribute.String(openinference.OutputValue, openinference.RedactedValue))
	} else {
		if bytesJSON, err := json.Marshal(resp); err == nil {
			attrs = append(attrs, attribute.String(openinference.OutputValue, string(bytesJSON)))
		}
	}
	// Add token usage if available
	if resp.Usage != nil {
		if resp.Usage.InputTokens > 0 {
			attrs = append(attrs, attribute.Int(openinference.LLMTokenCountPrompt, int(resp.Usage.InputTokens)))
		}
		if resp.Usage.OutputTokens > 0 {
			attrs = append(attrs, attribute.Int(openinference.LLMTokenCountCompletion, int(resp.Usage.OutputTokens)))
		}
		if resp.Usage.TotalTokens > 0 {
			attrs = append(attrs, attribute.Int(openinference.LLMTokenCountTotal, int(resp.Usage.TotalTokens)))
		}
		if resp.Usage.InputTokensDetails.CachedTokens > 0 {
			attrs = append(attrs, attribute.Int(openinference.LLMTokenCountPromptCacheHit, int(resp.Usage.InputTokensDetails.CachedTokens)))
		}
		if cacheWriteTokens := resp.Usage.InputTokensDetails.CacheWriteTokensValue(); cacheWriteTokens > 0 {
			attrs = append(attrs, attribute.Int(openinference.LLMTokenCountPromptCacheWrite, int(cacheWriteTokens)))
		}
		if resp.Usage.OutputTokensDetails.ReasoningTokens > 0 {
			attrs = append(attrs, attribute.Int(openinference.LLMTokenCountCompletionReasoning, int(resp.Usage.OutputTokensDetails.ReasoningTokens)))
		}
	}
	if !config.HideOutputs && !config.HideOutputMessages {
		for i := range resp.Output {
			attrs = setResponseOutputAttrs(&resp.Output[i], attrs, config, i)
		}
	}
	return attrs
}

func setResponseOutputAttrs(output *openai.ResponseOutputItemUnion, attrs []attribute.KeyValue, config *openinference.TraceConfig, messageIndex int) []attribute.KeyValue {
	switch {
	case output.OfOutputMessage != nil:
		attrs = setResponseOutputMsgAttrs(output.OfOutputMessage, attrs, config, messageIndex)
	case output.OfFunctionCall != nil:
		attrs = setResponseFunctionCallAttrs(output.OfFunctionCall, attrs, config, messageIndex)
	case output.OfFileSearchCall != nil:
		attrs = setResponseFileSearchCallAttrs(output.OfFileSearchCall, attrs, config, messageIndex)
	case output.OfComputerCall != nil:
		attrs = setResponseComputerCallAttrs(output.OfComputerCall, attrs, config, messageIndex)
	case output.OfReasoning != nil:
		attrs = setResponseReasoningAttrs(output.OfReasoning, attrs, config, messageIndex)
	case output.OfWebSearchCall != nil:
		attrs = setResponseWebSearchCallAttrs(output.OfWebSearchCall, attrs, config, messageIndex)
	case output.OfCustomToolCall != nil:
		attrs = setResponseCustomToolCallAttrs(output.OfCustomToolCall, attrs, config, messageIndex)
	case output.OfImageGenerationCall != nil:
		// TODO: Handle image generation call
		// https://github.com/Arize-ai/openinference/blob/f6561ca5a169f13d5b40120311b782348550b5ac/python/instrumentation/openinference-instrumentation-openai/src/openinference/instrumentation/openai/_attributes/_responses_api.py#L573
	case output.OfCodeInterpreterCall != nil:
		// TODO: Handle code interpreter call
		// https://github.com/Arize-ai/openinference/blob/f6561ca5a169f13d5b40120311b782348550b5ac/python/instrumentation/openinference-instrumentation-openai/src/openinference/instrumentation/openai/_attributes/_responses_api.py#L576
	case output.OfLocalShellCall != nil:
		// TODO: Handle local shell call
		// https://github.com/Arize-ai/openinference/blob/f6561ca5a169f13d5b40120311b782348550b5ac/python/instrumentation/openinference-instrumentation-openai/src/openinference/instrumentation/openai/_attributes/_responses_api.py#L579
	case output.OfMcpCall != nil:
		// TODO: Handle mcp call
		// https://github.com/Arize-ai/openinference/blob/f6561ca5a169f13d5b40120311b782348550b5ac/python/instrumentation/openinference-instrumentation-openai/src/openinference/instrumentation/openai/_attributes/_responses_api.py#L582
	case output.OfMcpListTools != nil:
		// TODO: Handle mcp list tools
		// https://github.com/Arize-ai/openinference/blob/f6561ca5a169f13d5b40120311b782348550b5ac/python/instrumentation/openinference-instrumentation-openai/src/openinference/instrumentation/openai/_attributes/_responses_api.py#L585
	case output.OfMcpApprovalRequest != nil:
		// TODO: Handle mcp approval request
		// https://github.com/Arize-ai/openinference/blob/f6561ca5a169f13d5b40120311b782348550b5ac/python/instrumentation/openinference-instrumentation-openai/src/openinference/instrumentation/openai/_attributes/_responses_api.py#L588
	case output.OfShellCall != nil:
		// TODO: Handle shell call
		// https://github.com/Arize-ai/openinference/blob/f6561ca5a169f13d5b40120311b782348550b5ac/python/instrumentation/openinference-instrumentation-openai/src/openinference/instrumentation/openai/_attributes/_responses_api.py#L591
	case output.OfShellCallOutput != nil:
		// TODO: Handle shell call output
		// https://github.com/Arize-ai/openinference/blob/f6561ca5a169f13d5b40120311b782348550b5ac/python/instrumentation/openinference-instrumentation-openai/src/openinference/instrumentation/openai/_attributes/_responses_api.py#L594
	case output.OfApplyPatchCall != nil:
		// TODO: Handle patch call
		// https://github.com/Arize-ai/openinference/blob/f6561ca5a169f13d5b40120311b782348550b5ac/python/instrumentation/openinference-instrumentation-openai/src/openinference/instrumentation/openai/_attributes/_responses_api.py#L597
	case output.OfApplyPatchCallOutput != nil:
		// TODO: Handle patch call output
		// https://github.com/Arize-ai/openinference/blob/f6561ca5a169f13d5b40120311b782348550b5ac/python/instrumentation/openinference-instrumentation-openai/src/openinference/instrumentation/openai/_attributes/_responses_api.py#L600
	case output.OfCompaction != nil:
		// TODO: Handle compaction response
		// https://github.com/Arize-ai/openinference/blob/f6561ca5a169f13d5b40120311b782348550b5ac/python/instrumentation/openinference-instrumentation-openai/src/openinference/instrumentation/openai/_attributes/_responses_api.py#L603
	}
	return attrs
}

func setResponseOutputMsgAttrs(o *openai.ResponseOutputMessage, attrs []attribute.KeyValue, config *openinference.TraceConfig, messageIndex int) []attribute.KeyValue {
	attrs = append(attrs, attribute.String(openinference.OutputMessageAttribute(messageIndex, openinference.MessageRole), o.Role))
	switch {
	case o.Content.OfContentArray != nil:
		for i, content := range o.Content.OfContentArray {
			switch {
			case content.OfOutputText != nil:
				attrs = append(attrs, attribute.String(openinference.OutputMessageContentAttribute(messageIndex, i, "type"), "text"))
				if config.HideOutputText {
					attrs = append(attrs, attribute.String(openinference.OutputMessageContentAttribute(messageIndex, i, "text"), openinference.RedactedValue))
				} else {
					attrs = append(attrs, attribute.String(openinference.OutputMessageContentAttribute(messageIndex, i, "text"), content.OfOutputText.Text))
				}
			case content.OfRefusal != nil:
				attrs = append(attrs, attribute.String(openinference.OutputMessageContentAttribute(messageIndex, i, "type"), "text"))
				if config.HideOutputText {
					attrs = append(attrs, attribute.String(openinference.OutputMessageContentAttribute(messageIndex, i, "text"), openinference.RedactedValue))
				} else {
					attrs = append(attrs, attribute.String(openinference.OutputMessageContentAttribute(messageIndex, i, "text"), content.OfRefusal.Refusal))
				}
			}
		}
	case o.Content.OfString != nil:
		attrs = append(attrs, attribute.String(openinference.OutputMessageContentAttribute(messageIndex, 0, "type"), "text"))
		if config.HideOutputText {
			attrs = append(attrs, attribute.String(openinference.OutputMessageContentAttribute(messageIndex, 0, "text"), openinference.RedactedValue))
		} else {
			attrs = append(attrs, attribute.String(openinference.OutputMessageContentAttribute(messageIndex, 0, "text"), *o.Content.OfString))
		}
	}
	return attrs
}

func setResponseFunctionCallAttrs(f *openai.ResponseFunctionToolCall, attrs []attribute.KeyValue, config *openinference.TraceConfig, messageIndex int) []attribute.KeyValue {
	attrs = append(attrs, attribute.String(openinference.OutputMessageAttribute(messageIndex, openinference.MessageRole), "assistant"))
	if config.HideOutputText {
		attrs = append(attrs, attribute.String(openinference.OutputMessageToolCallAttribute(messageIndex, 0, openinference.ToolCallID), openinference.RedactedValue),
			attribute.String(openinference.OutputMessageToolCallAttribute(messageIndex, 0, openinference.ToolCallFunctionName), openinference.RedactedValue),
			attribute.String(openinference.OutputMessageToolCallAttribute(messageIndex, 0, openinference.ToolCallFunctionArguments), openinference.RedactedValue),
		)
	} else {
		attrs = append(attrs, attribute.String(openinference.OutputMessageToolCallAttribute(messageIndex, 0, openinference.ToolCallID), f.CallID),
			attribute.String(openinference.OutputMessageToolCallAttribute(messageIndex, 0, openinference.ToolCallFunctionName), f.Name),
			attribute.String(openinference.OutputMessageToolCallAttribute(messageIndex, 0, openinference.ToolCallFunctionArguments), f.Arguments),
		)
	}
	return attrs
}

func setResponseFileSearchCallAttrs(f *openai.ResponseFileSearchToolCall, attrs []attribute.KeyValue, config *openinference.TraceConfig, messageIndex int) []attribute.KeyValue {
	attrs = append(attrs, attribute.String(openinference.OutputMessageAttribute(messageIndex, openinference.MessageRole), "assistant"))
	if config.HideOutputText {
		attrs = append(attrs, attribute.String(openinference.OutputMessageToolCallAttribute(messageIndex, 0, openinference.ToolCallID), openinference.RedactedValue),
			attribute.String(openinference.OutputMessageToolCallAttribute(messageIndex, 0, openinference.ToolCallFunctionName), openinference.RedactedValue))
	} else {
		attrs = append(attrs, attribute.String(openinference.OutputMessageToolCallAttribute(messageIndex, 0, openinference.ToolCallID), f.ID),
			attribute.String(openinference.OutputMessageToolCallAttribute(messageIndex, 0, openinference.ToolCallFunctionName), f.Type))
	}
	return attrs
}

func setResponseComputerCallAttrs(c *openai.ResponseComputerToolCall, attrs []attribute.KeyValue, config *openinference.TraceConfig, messageIndex int) []attribute.KeyValue {
	attrs = append(attrs, attribute.String(openinference.OutputMessageAttribute(messageIndex, openinference.MessageRole), "assistant"),
		attribute.String(openinference.OutputMessageToolCallAttribute(messageIndex, 0, openinference.MessageRole), "tool"))
	if config.HideOutputText {
		attrs = append(attrs, attribute.String(openinference.OutputMessageToolCallAttribute(messageIndex, 0, openinference.ToolCallID), openinference.RedactedValue))
	} else {
		attrs = append(attrs, attribute.String(openinference.OutputMessageToolCallAttribute(messageIndex, 0, openinference.ToolCallID), c.CallID))
	}
	return attrs
}

func setResponseReasoningAttrs(r *openai.ResponseReasoningItem, attrs []attribute.KeyValue, config *openinference.TraceConfig, messageIndex int) []attribute.KeyValue {
	for i, summary := range r.Summary {
		if config.HideOutputText {
			attrs = append(attrs, attribute.String(openinference.OutputMessageContentAttribute(messageIndex, i, "type"), "text"),
				attribute.String(openinference.OutputMessageContentAttribute(messageIndex, i, "text"), openinference.RedactedValue))
		} else {
			attrs = append(attrs, attribute.String(openinference.OutputMessageContentAttribute(messageIndex, i, "type"), "text"),
				attribute.String(openinference.OutputMessageContentAttribute(messageIndex, i, "text"), summary.Text))
		}
	}
	return attrs
}

func setResponseWebSearchCallAttrs(w *openai.ResponseFunctionWebSearch, attrs []attribute.KeyValue, config *openinference.TraceConfig, messageIndex int) []attribute.KeyValue {
	attrs = append(attrs, attribute.String(openinference.OutputMessageAttribute(messageIndex, openinference.MessageRole), "assistant"))
	if config.HideOutputText {
		attrs = append(attrs, attribute.String(openinference.OutputMessageToolCallAttribute(messageIndex, 0, openinference.ToolCallID), openinference.RedactedValue),
			attribute.String(openinference.OutputMessageToolCallAttribute(messageIndex, 0, openinference.ToolCallFunctionName), openinference.RedactedValue))
	} else {
		attrs = append(attrs, attribute.String(openinference.OutputMessageToolCallAttribute(messageIndex, 0, openinference.ToolCallID), w.ID),
			attribute.String(openinference.OutputMessageToolCallAttribute(messageIndex, 0, openinference.ToolCallFunctionName), w.Type))
	}
	return attrs
}

func setResponseCustomToolCallAttrs(c *openai.ResponseCustomToolCall, attrs []attribute.KeyValue, config *openinference.TraceConfig, messageIndex int) []attribute.KeyValue {
	attrs = append(attrs, attribute.String(openinference.OutputMessageAttribute(messageIndex, openinference.MessageRole), "assistant"))
	if config.HideOutputText {
		attrs = append(attrs, attribute.String(openinference.OutputMessageToolCallAttribute(messageIndex, 0, openinference.ToolCallID), openinference.RedactedValue),
			attribute.String(openinference.OutputMessageToolCallAttribute(messageIndex, 0, openinference.ToolCallFunctionName), openinference.RedactedValue),
			attribute.String(openinference.OutputMessageToolCallAttribute(messageIndex, 0, openinference.ToolCallFunctionArguments), openinference.RedactedValue),
		)
	} else {
		attrs = append(attrs, attribute.String(openinference.OutputMessageToolCallAttribute(messageIndex, 0, openinference.ToolCallID), c.CallID),
			attribute.String(openinference.OutputMessageToolCallAttribute(messageIndex, 0, openinference.ToolCallFunctionName), c.Name),
		)
		if data, err := json.Marshal(map[string]string{"input": c.Input}); err == nil {
			attrs = append(attrs, attribute.String(openinference.OutputMessageToolCallAttribute(messageIndex, 0, openinference.ToolCallFunctionArguments), string(data)))
		}
	}
	return attrs
}
