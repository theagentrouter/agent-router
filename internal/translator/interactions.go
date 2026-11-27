// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package translator

import (
	"bytes"
	"fmt"
	"io"
	"strconv"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	"github.com/envoyproxy/ai-gateway/internal/apischema/gcp"
	"github.com/envoyproxy/ai-gateway/internal/internalapi"
	"github.com/envoyproxy/ai-gateway/internal/metrics"
	"github.com/envoyproxy/ai-gateway/internal/tracing/tracingapi"
)

// InteractionsSpan is the tracing span type of the Interactions API. The endpoint is not traced
// yet, so the span is always nil.
type InteractionsSpan = tracingapi.Span[struct{}, struct{}]

// InteractionsTranslator translates a Gemini Interactions API create request to a provider.
type InteractionsTranslator = Translator[gcp.InteractionsRequest, InteractionsSpan]

// NewInteractionsGCPVertexAITranslator returns the [InteractionsTranslator] for the Vertex AI
// Interactions API
// (https://docs.cloud.google.com/gemini-enterprise-agent-platform/reference/models/interactions-api).
//
// The ":path" it sets is relative ("interactions"). The GCP backend auth handler prepends
// "/v1/projects/{project}/locations/{region}/", where the region is one Vertex AI serves the
// Interactions API in: "global", "us", or "eu".
func NewInteractionsGCPVertexAITranslator(modelNameOverride internalapi.ModelNameOverride) InteractionsTranslator {
	return &interactionsToGCPVertexAITranslator{modelNameOverride: modelNameOverride}
}

// interactionsToGCPVertexAITranslator forwards the request body unchanged apart from the model
// name override, and reads token usage from the response without changing it. Error responses
// are passed through, since Interactions API clients expect the Google error format.
type interactionsToGCPVertexAITranslator struct {
	modelNameOverride internalapi.ModelNameOverride
	stream            bool
	// buffered holds an incomplete server-sent event carried over from the previous chunk.
	buffered []byte
	// usage is the latest cumulative usage reported by the stream.
	usage         metrics.TokenUsage
	responseModel internalapi.ResponseModel
}

// RequestBody implements [Translator.RequestBody].
func (t *interactionsToGCPVertexAITranslator) RequestBody(raw []byte, req *gcp.InteractionsRequest, forceBodyMutation bool) (
	newHeaders []internalapi.Header, newBody []byte, err error,
) {
	t.stream = req.Stream
	t.responseModel = req.RoutingModel()
	if t.modelNameOverride != "" {
		// The backend serves the routed model or agent under a different name.
		field := "model"
		if req.Model == "" {
			field = "agent"
		}
		newBody, err = sjson.SetBytesOptions(raw, field, t.modelNameOverride, sjsonOptions)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to apply the model name override to the %q field: %w", field, err)
		}
		t.responseModel = t.modelNameOverride
	}
	newBody = forceOriginalBodyIfEmpty(forceBodyMutation, newBody, raw)

	newHeaders = []internalapi.Header{{pathHeaderName, "interactions"}}
	if len(newBody) > 0 {
		newHeaders = append(newHeaders, internalapi.Header{contentLengthHeaderName, strconv.Itoa(len(newBody))})
	}
	return newHeaders, newBody, nil
}

// ResponseHeaders implements [Translator.ResponseHeaders].
func (t *interactionsToGCPVertexAITranslator) ResponseHeaders(map[string]string) ([]internalapi.Header, error) {
	return nil, nil
}

// ResponseBody implements [Translator.ResponseBody]. The body is forwarded unchanged.
func (t *interactionsToGCPVertexAITranslator) ResponseBody(_ map[string]string, body io.Reader, endOfStream bool, _ InteractionsSpan) (
	newHeaders []internalapi.Header, newBody []byte, tokenUsage metrics.TokenUsage, responseModel internalapi.ResponseModel, err error,
) {
	raw, err := io.ReadAll(body)
	if err != nil {
		return nil, nil, tokenUsage, "", fmt.Errorf("failed to read the interactions response body: %w", err)
	}
	if !t.stream {
		interaction := gjson.ParseBytes(raw)
		usage := interaction.Get("usage")
		t.recordInteraction(&interaction)
		t.recordUsage(&usage)
		return nil, nil, t.usage, t.responseModel, nil
	}

	t.buffered = append(t.buffered, raw...)
	for {
		end, sepLen := nextSSEEventEnd(t.buffered)
		if end < 0 {
			break
		}
		t.recordEvent(t.buffered[:end])
		t.buffered = t.buffered[end+sepLen:]
	}
	if endOfStream && len(t.buffered) > 0 {
		// The stream ended without a final separator.
		t.recordEvent(t.buffered)
		t.buffered = nil
	}
	return nil, nil, t.usage, t.responseModel, nil
}

// ResponseError implements [Translator.ResponseError]. The error is forwarded unchanged.
func (t *interactionsToGCPVertexAITranslator) ResponseError(map[string]string, io.Reader) ([]internalapi.Header, []byte, error) {
	return nil, nil, nil
}

// recordEvent records the usage and model of one server-sent event. The interaction.created and
// interaction.completed events carry an "interaction" object. The interaction.status_update,
// step.stop, and interaction.cancelled events carry a top-level "usage". A step's own usage
// ("step_usage") is not cumulative and is ignored.
func (t *interactionsToGCPVertexAITranslator) recordEvent(event []byte) {
	for _, line := range bytes.Split(event, []byte("\n")) {
		data, ok := bytes.CutPrefix(bytes.TrimSuffix(line, []byte("\r")), []byte("data:"))
		if !ok {
			continue
		}
		data = bytes.TrimSpace(data)
		if !gjson.ValidBytes(data) {
			continue
		}
		parsed := gjson.ParseBytes(data)
		if interaction := parsed.Get("interaction"); interaction.IsObject() {
			usage := interaction.Get("usage")
			t.recordInteraction(&interaction)
			t.recordUsage(&usage)
		}
		usage := parsed.Get("usage")
		t.recordUsage(&usage)
	}
}

// recordInteraction records the model (or agent) an interaction object reports.
func (t *interactionsToGCPVertexAITranslator) recordInteraction(interaction *gjson.Result) {
	if m := interaction.Get("model").String(); m != "" {
		t.responseModel = m
	} else if a := interaction.Get("agent").String(); a != "" {
		t.responseModel = a
	}
}

// recordUsage replaces the accumulated usage with a cumulative usage report.
func (t *interactionsToGCPVertexAITranslator) recordUsage(usage *gjson.Result) {
	if u, ok := InteractionsUsage(usage); ok {
		t.usage.Override(u)
	}
}

// nextSSEEventEnd returns the end offset of the first complete server-sent event in buf and the
// length of the blank-line separator that ends it, or -1 when buf holds no complete event.
func nextSSEEventEnd(buf []byte) (end, sepLen int) {
	lf := bytes.Index(buf, []byte("\n\n"))
	crlf := bytes.Index(buf, []byte("\r\n\r\n"))
	switch {
	case lf < 0 && crlf < 0:
		return -1, 0
	case crlf >= 0 && (lf < 0 || crlf < lf):
		return crlf, 4
	default:
		return lf, 2
	}
}

// InteractionsUsage converts an Interactions API "usage" object into token usage.
//
// The Interactions API reports usage cumulatively for the whole interaction, so the result
// replaces earlier usage of the same interaction. The mapping follows the generateContent
// translation in this package:
//   - input tokens: total_input_tokens plus total_tool_use_tokens (the tool-use prompt, such as
//     tool results fed back to the model, is billed as input).
//   - output tokens: total_output_tokens plus total_thought_tokens.
//   - reasoning tokens: total_thought_tokens.
//   - cached input tokens: total_cached_tokens.
//   - total tokens: total_tokens.
//
// ok is false when usage carries no token counts.
func InteractionsUsage(usage *gjson.Result) (tokenUsage metrics.TokenUsage, ok bool) {
	if !usage.IsObject() {
		return tokenUsage, false
	}
	input, hasInput := usageCount(usage, "total_input_tokens")
	toolUse, hasToolUse := usageCount(usage, "total_tool_use_tokens")
	output, hasOutput := usageCount(usage, "total_output_tokens")
	thought, hasThought := usageCount(usage, "total_thought_tokens")
	cached, hasCached := usageCount(usage, "total_cached_tokens")
	total, hasTotal := usageCount(usage, "total_tokens")

	if hasInput || hasToolUse {
		tokenUsage.SetInputTokens(input + toolUse)
	}
	if hasOutput || hasThought {
		tokenUsage.SetOutputTokens(output + thought)
	}
	if hasThought {
		tokenUsage.SetReasoningTokens(thought)
	}
	if hasCached {
		tokenUsage.SetCachedInputTokens(cached)
	}
	if hasTotal {
		tokenUsage.SetTotalTokens(total)
	}
	return tokenUsage, hasInput || hasToolUse || hasOutput || hasThought || hasCached || hasTotal
}

// usageCount reads a non-negative integer token count field.
func usageCount(usage *gjson.Result, field string) (uint32, bool) {
	v := usage.Get(field)
	if !v.Exists() || v.Type != gjson.Number || v.Int() < 0 {
		return 0, false
	}
	return uint32(v.Uint()), true // #nosec G115 -- token counts fit in uint32.
}
