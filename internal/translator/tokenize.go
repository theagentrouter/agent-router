// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package translator

import (
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/tidwall/sjson"

	"github.com/envoyproxy/ai-gateway/internal/apischema/openai"
	"github.com/envoyproxy/ai-gateway/internal/apischema/openai/tokenize"
	"github.com/envoyproxy/ai-gateway/internal/internalapi"
	"github.com/envoyproxy/ai-gateway/internal/json"
	"github.com/envoyproxy/ai-gateway/internal/metrics"
	"github.com/envoyproxy/ai-gateway/internal/tracing/tracingapi"
)

// NewTokenizeTranslator implements [Factory] for OpenAI to OpenAI tokenize translation.
func NewTokenizeTranslator(modelNameOverride internalapi.ModelNameOverride) TokenizeTranslator {
	return &ToOpenAITokenize{modelNameOverride: modelNameOverride, path: "/tokenize"}
}

// ToOpenAITokenize is a passthrough translator for OpenAI Tokenize API.
// It may apply model overrides but otherwise preserves the tokenization requests in OpenAI format.
// Supports both chat and completion tokenize requests as defined in the tokenize API spec.
type ToOpenAITokenize struct {
	modelNameOverride internalapi.ModelNameOverride
	// requestModel serves as fallback for non-compliant backends that
	// don't return model in responses, ensuring metrics/tracing always have a model.
	requestModel internalapi.RequestModel
	// path is the tokenize endpoint path used for routing the request.
	path string
}

// RequestBody implements [TokenizeTranslator.RequestBody].
// This method validates the tokenize request union, applies model overrides if specified,
// and sets the appropriate routing headers for the tokenize endpoint.
func (o *ToOpenAITokenize) RequestBody(original []byte, req *tokenize.RequestUnion, forceBodyMutation bool) (
	newHeaders []internalapi.Header, newBody []byte, err error,
) {
	// Validate that the union has exactly one request type set
	if err = req.Validate(); err != nil {
		return nil, nil, fmt.Errorf("invalid tokenize request: %w", err)
	}

	// Store the request model to use as fallback for response model
	var model string
	if req.CompletionRequest != nil {
		model = req.CompletionRequest.Model
	} else if req.ChatRequest != nil {
		model = req.ChatRequest.Model
	}

	o.requestModel = model
	if o.modelNameOverride != "" {
		// If modelNameOverride is set, we override the model to be used for the request.
		newBody, err = sjson.SetBytesOptions(original, "model", o.modelNameOverride, sjsonOptions)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to set model name: %w", err)
		}
		// Make everything coherent - update the stored model to match the override.
		o.requestModel = o.modelNameOverride
	}

	// Always set the path header to the tokenize endpoint so that the request is routed correctly.
	newHeaders = []internalapi.Header{{pathHeaderName, o.path}}

	newBody = forceOriginalBodyIfEmpty(forceBodyMutation, newBody, original)

	if len(newBody) > 0 {
		newHeaders = append(newHeaders, internalapi.Header{contentLengthHeaderName, strconv.Itoa(len(newBody))})
	}
	return
}

// ResponseError implements [TokenizeTranslator.ResponseError].
// For OpenAI-based backends we return the OpenAI error type as is.
// If connection fails the error body is translated to OpenAI error type for events such as HTTP 503 or 504.
func (o *ToOpenAITokenize) ResponseError(respHeaders map[string]string, body io.Reader) (
	newHeaders []internalapi.Header, newBody []byte, err error,
) {
	statusCode := respHeaders[statusHeaderName]
	if v, ok := respHeaders[contentTypeHeaderName]; !ok || !strings.Contains(v, jsonContentType) {
		var openaiError openai.Error
		buf, err := io.ReadAll(body)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to read error body: %w", err)
		}
		openaiError = openai.Error{
			Type: "error",
			Error: openai.ErrorType{
				Type:    openAIBackendError,
				Message: string(buf),
				Code:    &statusCode,
			},
		}
		newBody, err = json.Marshal(openaiError)
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

// ResponseHeaders implements [TokenizeTranslator.ResponseHeaders].
// For OpenAI tokenize responses, no header modifications are needed so this returns nil.
func (o *ToOpenAITokenize) ResponseHeaders(map[string]string) (newHeaders []internalapi.Header, err error) {
	return nil, nil
}

// ResponseBody implements [TokenizeTranslator.ResponseBody].
// OpenAI tokenize responses are passed through unchanged. The response does not contain
// a model field, so we fallback to the request model for metrics and tracing consistency.
func (o *ToOpenAITokenize) ResponseBody(_ map[string]string, body io.Reader, _ bool, span tracingapi.TokenizeSpan) (
	newHeaders []internalapi.Header, newBody []byte, tokenUsage metrics.TokenUsage, responseModel string, err error,
) {
	resp := &tokenize.Response{}
	if err := json.NewDecoder(body).Decode(resp); err != nil {
		return nil, nil, tokenUsage, responseModel, fmt.Errorf("failed to unmarshal body: %w", err)
	}

	// Fallback to request model for test or non-compliant OpenAI backends
	responseModel = o.requestModel
	if span != nil {
		span.RecordResponse(resp)
	}
	return
}
