// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package translator

import (
	"cmp"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/tidwall/sjson"

	anthropicschema "github.com/envoyproxy/ai-gateway/internal/apischema/anthropic"
	"github.com/envoyproxy/ai-gateway/internal/internalapi"
	"github.com/envoyproxy/ai-gateway/internal/json"
	"github.com/envoyproxy/ai-gateway/internal/metrics"
	"github.com/envoyproxy/ai-gateway/internal/tracing/tracingapi"
)

// NewCountTokensToGCPAnthropicTranslator creates a translator for count_tokens to GCP Anthropic.
func NewCountTokensToGCPAnthropicTranslator(apiVersion string, modelNameOverride internalapi.ModelNameOverride) AnthropicCountTokensTranslator {
	return &countTokensToGCPAnthropicTranslator{
		apiVersion:        apiVersion,
		modelNameOverride: modelNameOverride,
	}
}

type countTokensToGCPAnthropicTranslator struct {
	apiVersion        string
	modelNameOverride internalapi.ModelNameOverride
}

// RequestBody implements [AnthropicCountTokensTranslator.RequestBody].
func (t *countTokensToGCPAnthropicTranslator) RequestBody(raw []byte, body *anthropicschema.CountTokensRequest, _ bool) (
	newHeaders []internalapi.Header, newBody []byte, err error,
) {
	if t.apiVersion == "" {
		return nil, nil, fmt.Errorf("anthropic_version is required for GCP Vertex AI but not provided in backend configuration")
	}

	// Add anthropic_version field required by GCP.
	newBody, err = sjson.SetBytesOptions(raw, anthropicVersionKey, t.apiVersion, sjsonOptions)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to set anthropic_version: %w", err)
	}

	// GCP Vertex AI's count-tokens endpoint does not accept the "@default" or "@latest"
	// version aliases (e.g. "claude-sonnet-4-6@default" returns "not supported for token
	// counting"). Explicit version tags like "@20251001" are accepted. These aliases are
	// required for inference endpoints (messages, chat) which share the same modelNameOverride,
	// so we strip them here for count-tokens only. Strip from the resolved model (request body
	// or override) so the alias is removed regardless of where it came from.
	// See: https://docs.cloud.google.com/gemini-enterprise-agent-platform/models/partner-models/claude/count-tokens
	model := cmp.Or(t.modelNameOverride, body.Model)
	model = strings.TrimSuffix(model, "@default")
	model = strings.TrimSuffix(model, "@latest")
	newBody, err = sjson.SetBytesOptions(newBody, "model", model, sjsonOptions)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to set model: %w", err)
	}

	// GCP Vertex AI uses the special "count-tokens" model path with rawPredict.
	// The actual model name stays in the request body.
	// See: https://docs.cloud.google.com/vertex-ai/generative-ai/docs/partner-models/claude/count-tokens
	path := buildGCPModelPathSuffix(gcpModelPublisherAnthropic, gcpCountTokensModel, gcpMethodRawPredict)
	newHeaders = []internalapi.Header{{pathHeaderName, path}, {contentLengthHeaderName, strconv.Itoa(len(newBody))}}
	return
}

// ResponseHeaders implements [AnthropicCountTokensTranslator.ResponseHeaders].
func (t *countTokensToGCPAnthropicTranslator) ResponseHeaders(_ map[string]string) (
	newHeaders []internalapi.Header, err error,
) {
	return nil, nil
}

// ResponseBody implements [AnthropicCountTokensTranslator.ResponseBody].
func (t *countTokensToGCPAnthropicTranslator) ResponseBody(_ map[string]string, body io.Reader, _ bool, span tracingapi.CountTokensSpan) (
	newHeaders []internalapi.Header, newBody []byte, tokenUsage metrics.TokenUsage, responseModel internalapi.ResponseModel, err error,
) {
	resp := &anthropicschema.CountTokensResponse{}
	if err := json.NewDecoder(body).Decode(resp); err != nil {
		return nil, nil, tokenUsage, "", fmt.Errorf("failed to unmarshal body: %w", err)
	}
	if span != nil {
		span.RecordResponse(resp)
	}
	tokenUsage.SetInputTokens(uint32(resp.InputTokens)) //nolint:gosec
	return nil, nil, tokenUsage, "", nil
}

// ResponseError implements [AnthropicCountTokensTranslator.ResponseError].
func (t *countTokensToGCPAnthropicTranslator) ResponseError(_ map[string]string, _ io.Reader) (
	newHeaders []internalapi.Header, mutatedBody []byte, err error,
) {
	// GCP Vertex AI returns errors in Anthropic format when proxying to Anthropic models.
	return nil, nil, nil
}
