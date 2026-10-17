// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package translator

import (
	"fmt"
	"io"
	"path"
	"strconv"
	"strings"

	"github.com/tidwall/sjson"

	cohereschema "github.com/envoyproxy/ai-gateway/internal/apischema/cohere"
	"github.com/envoyproxy/ai-gateway/internal/internalapi"
	"github.com/envoyproxy/ai-gateway/internal/json"
	"github.com/envoyproxy/ai-gateway/internal/metrics"
	"github.com/envoyproxy/ai-gateway/internal/tracing/tracingapi"
)

// NewRerankCohereToCohereTranslator implements [Factory] for Cohere Rerank v2 translation.
func NewRerankCohereToCohereTranslator(apiVersion string, modelNameOverride internalapi.ModelNameOverride) CohereRerankTranslator {
	return &cohereToCohereTranslatorV2Rerank{modelNameOverride: modelNameOverride, path: path.Join("/", apiVersion, "rerank")} // e.g., /v2/rerank
}

// cohereToCohereTranslatorV2Rerank is a passthrough translator for Cohere Rerank API v2.
// May apply model overrides but otherwise preserves the Cohere format:
// https://docs.cohere.com/reference/rerank
type cohereToCohereTranslatorV2Rerank struct {
	modelNameOverride internalapi.ModelNameOverride
	// requestModel stores the effective model for this request (override or provided)
	requestModel internalapi.RequestModel
	// The path of the rerank endpoint to be used for the request. It is prefixed with the API path prefix.
	path string
}

// RequestBody implements [CohereRerankTranslator.RequestBody].
func (t *cohereToCohereTranslatorV2Rerank) RequestBody(original []byte, req *cohereschema.RerankV2Request, onRetry bool) (
	newHeaders []internalapi.Header, newBody []byte, err error,
) {
	// Store the request model to use as fallback for response model
	t.requestModel = req.Model
	if t.modelNameOverride != "" {
		// Override the model if configured.
		newBody, err = sjson.SetBytesOptions(original, "model", t.modelNameOverride, sjsonOptions)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to set model name: %w", err)
		}
		// Make everything coherent.
		t.requestModel = t.modelNameOverride
	}

	// Always set the path header to the rerank endpoint so that the request is routed correctly.
	if onRetry && len(newBody) == 0 {
		newBody = original
	}

	newHeaders = []internalapi.Header{{pathHeaderName, t.path}}
	if len(newBody) > 0 {
		newHeaders = append(newHeaders, internalapi.Header{contentLengthHeaderName, strconv.Itoa(len(newBody))})
	}
	return
}

// ResponseHeaders implements [CohereRerankTranslator.ResponseHeaders].
func (t *cohereToCohereTranslatorV2Rerank) ResponseHeaders(map[string]string) (newHeaders []internalapi.Header, err error) {
	return nil, nil
}

// ResponseBody implements [CohereRerankTranslator.ResponseBody].
// Token usage comes from meta.tokens when the provider reports it. Cohere bills rerank in search
// units instead and reports them in meta.billed_units.search_units.
func (t *cohereToCohereTranslatorV2Rerank) ResponseBody(_ map[string]string, body io.Reader, _ bool, span tracingapi.RerankSpan) (
	newHeaders []internalapi.Header, newBody []byte, tokenUsage metrics.TokenUsage, responseModel internalapi.ResponseModel, err error,
) {
	var resp cohereschema.RerankV2Response
	if err := json.NewDecoder(body).Decode(&resp); err != nil {
		return nil, nil, tokenUsage, t.requestModel, fmt.Errorf("failed to unmarshal body: %w", err)
	}

	// Record the response in the span if successful.
	if span != nil {
		span.RecordResponse(&resp)
	}

	// Token accounting: rerank only has input tokens; output tokens do not apply.
	if resp.Meta != nil && resp.Meta.Tokens != nil {
		var totalTokens uint32
		if resp.Meta.Tokens.InputTokens != nil {
			// Cohere uses float; round down to uint32 like embeddings.
			input := uint32(*resp.Meta.Tokens.InputTokens) //nolint:gosec
			tokenUsage.SetInputTokens(input)
			totalTokens += input
		}
		if resp.Meta.Tokens.OutputTokens != nil {
			output := uint32(*resp.Meta.Tokens.OutputTokens) //nolint:gosec
			tokenUsage.SetOutputTokens(output)
			totalTokens += output
		}
		tokenUsage.SetTotalTokens(totalTokens)
	}
	if resp.Meta != nil && resp.Meta.BilledUnits != nil && resp.Meta.BilledUnits.SearchUnits != nil {
		tokenUsage.SetSearchUnits(uint32(*resp.Meta.BilledUnits.SearchUnits)) //nolint:gosec
	}

	// Cohere rerank responses do not echo model; report the effective request model if known.
	responseModel = t.requestModel
	return
}

// ResponseError implements [CohereRerankTranslator.ResponseError].
// If connection fails or a non-JSON error is returned, wrap it into a JSON error body.
func (t *cohereToCohereTranslatorV2Rerank) ResponseError(respHeaders map[string]string, body io.Reader) (
	newHeaders []internalapi.Header, newBody []byte, err error,
) {
	if v, ok := respHeaders[contentTypeHeaderName]; ok && !strings.Contains(v, jsonContentType) {
		buf, err := io.ReadAll(body)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to read error body: %w", err)
		}
		message := string(buf)
		// Wrap as a minimal Cohere v2 error JSON for consistency.
		cohereErr := cohereschema.RerankV2Error{
			Message: &message,
		}
		newBody, err = json.Marshal(cohereErr)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to marshal error body: %w", err)
		}
		newHeaders = append(newHeaders,
			internalapi.Header{contentTypeHeaderName, jsonContentType},
			internalapi.Header{contentLengthHeaderName, strconv.Itoa(len(newBody))},
		)
	}
	return
}
