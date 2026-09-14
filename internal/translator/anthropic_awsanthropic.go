// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package translator

import (
	"bytes"
	"cmp"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream"
	"github.com/tidwall/sjson"

	anthropicschema "github.com/envoyproxy/ai-gateway/internal/apischema/anthropic"
	"github.com/envoyproxy/ai-gateway/internal/internalapi"
	"github.com/envoyproxy/ai-gateway/internal/json"
	"github.com/envoyproxy/ai-gateway/internal/metrics"
	"github.com/envoyproxy/ai-gateway/internal/tracing/tracingapi"
)

// NewAnthropicToAWSAnthropicTranslator creates a translator for Anthropic to AWS Bedrock Anthropic format.
// AWS Bedrock supports the native Anthropic Messages API, so this is essentially a passthrough
// translator with AWS-specific path modifications.
func NewAnthropicToAWSAnthropicTranslator(apiVersion string, modelNameOverride internalapi.ModelNameOverride) AnthropicMessagesTranslator {
	anthropicTranslator := NewAnthropicToAnthropicTranslator("", modelNameOverride).(*anthropicToAnthropicTranslator)
	return &anthropicToAWSAnthropicTranslator{
		apiVersion:                     apiVersion,
		anthropicToAnthropicTranslator: *anthropicTranslator,
	}
}

type anthropicToAWSAnthropicTranslator struct {
	anthropicToAnthropicTranslator
	apiVersion       string
	anthropicBetas   []string
	betaFilterMode   string
	betaFilterValues []string
}

// awsBedrockSupportedAnthropicBetas is the allowlist of anthropic-beta flags that AWS Bedrock
// accepts in the anthropic_beta request field. Bedrock rejects the entire request with a 400
// "invalid beta flag" error if any unrecognized flag is present, so unsupported flags (e.g.
// ones only the Anthropic API knows) must be stripped rather than forwarded.
//
// Every entry was confirmed accepted (HTTP 200) by live probes against Bedrock. Notably,
// prompt-caching-2024-07-31, extended-cache-ttl-2025-04-11, files-api-2025-04-14,
// code-execution-2025-05-22, memory-2025-08-18 and thinking-token-count-2026-05-13 were
// probed and rejected. Extend this set only with flags verified against Bedrock.
var awsBedrockSupportedAnthropicBetas = map[string]struct{}{
	"computer-use-2024-10-22":                  {},
	"computer-use-2025-01-24":                  {},
	"context-1m-2025-08-07":                    {},
	"context-management-2025-06-27":            {},
	"dev-full-thinking-2025-05-14":             {},
	"fine-grained-tool-streaming-2025-05-14":   {},
	"interleaved-thinking-2025-05-14":          {},
	"mcp-client-2025-04-04":                    {},
	"mcp-client-2025-11-20":                    {},
	"model-context-window-exceeded-2025-08-26": {},
	"output-128k-2025-02-19":                   {},
	"pdfs-2024-09-25":                          {},
	"search-results-2025-06-09":                {},
	"token-counting-2024-11-01":                {},
	"token-efficient-tools-2025-02-19":         {},
	"tool-search-tool-2025-10-19":              {},
	"web-search-2025-03-05":                    {},
}

// anthropicAPIToBedrockBetaAliases maps anthropic-beta flag names that only the Anthropic API
// accepts onto the equivalent Bedrock flag name. The same feature is often exposed under
// different flag names depending on the backend, and clients speaking the Anthropic API
// dialect (e.g. Claude Code) send the Anthropic API name, which would otherwise be stripped
// by the allowlist above, silently disabling the gated feature.
//
// advanced-tool-use-2025-11-20 is the Anthropic API umbrella flag for Tool Search,
// Programmatic Tool Calling and Tool Use Examples. Bedrock supports only Tool Search,
// exposed under the older-dated tool-search-tool-2025-10-19; the tool type strings
// (tool_search_tool_regex_20251119 / tool_search_tool_bm25_20251119) are identical on both.
// The umbrella flag's non-Bedrock-supported siblings are dropped by the allowlist as usual.
//
// Aliases are applied before the allowlist check in SetRequestHeaders.
var anthropicAPIToBedrockBetaAliases = map[string]string{
	"advanced-tool-use-2025-11-20": "tool-search-tool-2025-10-19",
}

// SetRequestHeaders implements [RequestHeadersSetter].
func (a *anthropicToAWSAnthropicTranslator) SetRequestHeaders(headers map[string]string) {
	var anthropicBetas []string
	seen := map[string]struct{}{}
	for _, beta := range parseCommaSeparatedHeader(headers, anthropicBetaHeaderName) {
		// Translate Anthropic-API-only flag names to their Bedrock equivalents
		// before the allowlist check.
		if alias, ok := anthropicAPIToBedrockBetaAliases[beta]; ok {
			beta = alias
		}
		if _, ok := awsBedrockSupportedAnthropicBetas[beta]; ok {
			// An alias can collide with an explicitly sent flag (e.g. both
			// advanced-tool-use-2025-11-20 and tool-search-tool-2025-10-19);
			// forward each Bedrock flag only once.
			if _, dup := seen[beta]; dup {
				continue
			}
			seen[beta] = struct{}{}
			anthropicBetas = append(anthropicBetas, beta)
		}
	}
	a.anthropicBetas = anthropicBetas
}

// SetHeaderValueFilter implements [HeaderValueFilterSetter]. Only anthropic-beta is handled here:
// Bedrock reads these values from the request body rather than the header, so the filtered set has
// to be mirrored into the body's anthropic_beta field by RequestBody below. Filters on any other
// header are applied by Envoy's header mutation instead.
//
// This filter runs on top of the awsBedrockSupportedAnthropicBetas allowlist applied in
// SetRequestHeaders: that allowlist is the static, code-level set of flags Bedrock is known to
// accept, while this is the operator's per-backend policy over what remains.
func (a *anthropicToAWSAnthropicTranslator) SetHeaderValueFilter(name, mode string, values []string) {
	if !strings.EqualFold(name, anthropicBetaHeaderName) {
		return
	}
	a.betaFilterMode = mode
	a.betaFilterValues = values
}

// ResponseHeaders implements [AnthropicMessagesTranslator.ResponseHeaders].
func (a *anthropicToAWSAnthropicTranslator) ResponseHeaders(headers map[string]string) (
	newHeaders []internalapi.Header, err error,
) {
	if a.stream {
		contentType := headers[contentTypeHeaderName]
		if contentType == "application/vnd.amazon.eventstream" {
			// We need to change the content-type to text/event-stream for streaming responses.
			newHeaders = []internalapi.Header{{contentTypeHeaderName, "text/event-stream"}}
		}
	}
	return
}

// RequestBody implements [AnthropicMessagesTranslator.RequestBody] for Anthropic to AWS Bedrock Anthropic translation.
// This handles the transformation from native Anthropic format to AWS Bedrock format.
// https://docs.aws.amazon.com/bedrock/latest/userguide/model-parameters-anthropic-claude-messages-request-response.html
func (a *anthropicToAWSAnthropicTranslator) RequestBody(rawBody []byte, body *anthropicschema.MessagesRequest, _ bool) (
	newHeaders []internalapi.Header, newBody []byte, err error,
) {
	a.stream = body.Stream
	a.requestModel = cmp.Or(a.modelNameOverride, body.Model)

	newBody, err = sjson.SetBytesOptions(rawBody, anthropicVersionKey, a.apiVersion, sjsonOptions)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to set anthropic_version field: %w", err)
	}
	// Remove the model field from the body as AWS Bedrock expects the model to be specified in the path.
	// Otherwise, AWS complains "extra inputs are not permitted".
	//
	// It is safe to use sjsonOptionsInPlace here since we have already created a new mutatedBody above.
	newBody, _ = sjson.DeleteBytesOptions(newBody, "model", sjsonOptionsInPlace)
	newBody, _ = sjson.DeleteBytesOptions(newBody, "stream", sjsonOptionsInPlace)

	betas, betasChanged := filterHeaderValues(a.anthropicBetas, a.betaFilterMode, a.betaFilterValues)
	if len(betas) > 0 {
		newBody, err = sjson.SetBytesOptions(newBody, "anthropic_beta", betas, sjsonOptionsInPlace)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to set anthropic_beta field: %w", err)
		}
	}

	// Determine the AWS Bedrock path based on whether streaming is requested.
	var pathTemplate string
	if body.Stream {
		pathTemplate = "/model/%s/invoke-with-response-stream"
	} else {
		pathTemplate = "/model/%s/invoke"
	}

	// URL encode the model ID for the path to handle ARNs with special characters.
	// AWS Bedrock model IDs can be simple names (e.g., "anthropic.claude-3-5-sonnet-20241022-v2:0")
	// or full ARNs which may contain special characters.
	encodedModelID := url.PathEscape(a.requestModel)
	path := fmt.Sprintf(pathTemplate, encodedModelID)

	newHeaders = []internalapi.Header{{pathHeaderName, path}, {contentLengthHeaderName, strconv.Itoa(len(newBody))}}
	// When the beta filter dropped a value, overwrite the forwarded anthropic-beta header to match the
	// filtered set (Bedrock reads the body anthropic_beta field, but keep the header consistent).
	if betasChanged {
		newHeaders = append(newHeaders, internalapi.Header{anthropicBetaHeaderName, strings.Join(betas, ",")})
	}
	return
}

// ResponseBody implements [AnthropicMessagesTranslator.ResponseBody].
func (a *anthropicToAWSAnthropicTranslator) ResponseBody(_ map[string]string, body io.Reader, endOfStream bool, span tracingapi.MessageSpan) (
	newHeaders []internalapi.Header, newBody []byte, tokenUsage metrics.TokenUsage, responseModel string, err error,
) {
	if !a.stream {
		return a.anthropicToAnthropicTranslator.ResponseBody(nil, body, endOfStream, span)
	}
	// For streaming responses, AWS somehow wraps each Anthropicschema.MessagesStreamChunk
	// in an Amazon EventStream message. We need to unwrap these messages and convert them
	// to SSE format.
	newBody = make([]byte, 0)
	var buf []byte
	buf, err = io.ReadAll(body)
	if err != nil {
		err = fmt.Errorf("failed to read body: %w", err)
		return
	}
	a.buffered = append(a.buffered, buf...)
	a.convertMessagesEventWrappedInAmazonEventStreamEvent(&newBody, span)
	if endOfStream {
		// Recalculate total tokens before returning
		a.updateTotalTokens()
	}
	return nil, newBody, a.streamingTokenUsage, cmp.Or(a.streamingResponseModel, a.requestModel), nil
}

func (a *anthropicToAWSAnthropicTranslator) convertMessagesEventWrappedInAmazonEventStreamEvent(out *[]byte, span tracingapi.MessageSpan) {
	// TODO: Maybe reuse the reader and decoder.
	r := bytes.NewReader(a.buffered)
	dec := eventstream.NewDecoder()
	var lastRead int64
	for {
		msg, err := dec.Decode(r, nil)
		if err != nil {
			a.buffered = a.buffered[lastRead:]
			return
		}
		// This is undocumented struct used to wrap the actual Anthropicschema.MessagesStreamChunk in AWS eventstream.
		var rawEvent struct {
			Bytes []byte `json:"bytes"`
		}
		if err := json.Unmarshal(msg.Payload, &rawEvent); err != nil {
			lastRead = r.Size() - int64(r.Len())
			continue
		}
		var event anthropicschema.MessagesStreamChunk
		if err := json.Unmarshal(rawEvent.Bytes, &event); err != nil {
			lastRead = r.Size() - int64(r.Len())
			continue
		}
		if span != nil {
			span.RecordResponseChunk(&event)
		}

		a.reflectStreamingEvent(&event)
		*out = append(*out, sseEventPrefixSpace...)
		*out = append(*out, event.Type...)
		*out = append(*out, '\n')
		*out = append(*out, sseDataPrefixSpace...)
		*out = append(*out, rawEvent.Bytes...)
		*out = append(*out, '\n', '\n')
		lastRead = r.Size() - int64(r.Len())
	}
}
