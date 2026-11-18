// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package translator

import (
	"fmt"
	"io"
	"strconv"

	"github.com/tidwall/sjson"

	"github.com/envoyproxy/ai-gateway/internal/internalapi"
	"github.com/envoyproxy/ai-gateway/internal/json"
	"github.com/envoyproxy/ai-gateway/internal/metrics"
	"github.com/envoyproxy/ai-gateway/internal/tracing/tracingapi"
)

// PassthroughRequest is the part of a JSON request body the gateway needs to
// route an operation it forwards unchanged (vLLM rerank, score, tokenizer and
// batch chat endpoints): the model used for routing and authorization.
type PassthroughRequest struct {
	Model string `json:"model"`
}

// PassthroughResponse is the part of a forwarded JSON response the gateway
// reads for usage accounting.
type PassthroughResponse struct {
	Model string            `json:"model"`
	Usage *passthroughUsage `json:"usage"`
}

type passthroughUsage struct {
	PromptTokens     *int64 `json:"prompt_tokens"`
	InputTokens      *int64 `json:"input_tokens"`
	CompletionTokens *int64 `json:"completion_tokens"`
	OutputTokens     *int64 `json:"output_tokens"`
	TotalTokens      *int64 `json:"total_tokens"`
}

// PassthroughSpan is the tracing span type of passthrough operations.
type PassthroughSpan = tracingapi.Span[PassthroughResponse, struct{}]

// PassthroughTranslator forwards a JSON body unchanged except for the model
// name override, and leaves the request path as routed so endpoint-scoped
// routes keep their configured rewrite.
type PassthroughTranslator = Translator[PassthroughRequest, PassthroughSpan]

// NewPassthroughTranslator returns a translator for operations whose request
// and response formats are identical on the client and backend side.
func NewPassthroughTranslator(modelNameOverride internalapi.ModelNameOverride) PassthroughTranslator {
	return &passthroughTranslator{modelNameOverride: modelNameOverride}
}

type passthroughTranslator struct {
	modelNameOverride internalapi.ModelNameOverride
	requestModel      internalapi.RequestModel
}

func (t *passthroughTranslator) RequestBody(original []byte, req *PassthroughRequest, forceBodyMutation bool) (
	newHeaders []internalapi.Header, newBody []byte, err error,
) {
	t.requestModel = req.Model
	if t.modelNameOverride != "" {
		newBody, err = sjson.SetBytesOptions(original, "model", t.modelNameOverride, sjsonOptions)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to set model name: %w", err)
		}
		t.requestModel = t.modelNameOverride
	}
	if forceBodyMutation && len(newBody) == 0 {
		newBody = original
	}
	if len(newBody) > 0 {
		newHeaders = append(newHeaders, internalapi.Header{contentLengthHeaderName, strconv.Itoa(len(newBody))})
	}
	return
}

func (t *passthroughTranslator) ResponseHeaders(map[string]string) ([]internalapi.Header, error) {
	return nil, nil
}

// ResponseBody records token usage when the backend reports an OpenAI-style
// usage object. Bodies without one (tokenizer endpoints) carry no token usage.
func (t *passthroughTranslator) ResponseBody(_ map[string]string, body io.Reader, _ bool, span PassthroughSpan) (
	newHeaders []internalapi.Header, newBody []byte, tokenUsage metrics.TokenUsage, responseModel internalapi.ResponseModel, err error,
) {
	responseModel = t.requestModel
	raw, err := io.ReadAll(body)
	if err != nil {
		return nil, nil, tokenUsage, responseModel, fmt.Errorf("failed to read body: %w", err)
	}
	var resp PassthroughResponse
	if len(raw) == 0 || json.Unmarshal(raw, &resp) != nil {
		return nil, nil, tokenUsage, responseModel, nil
	}
	if span != nil {
		span.RecordResponse(&resp)
	}
	if resp.Model != "" {
		responseModel = resp.Model
	}
	if resp.Usage == nil {
		return nil, nil, tokenUsage, responseModel, nil
	}
	input := firstUsage(resp.Usage.PromptTokens, resp.Usage.InputTokens)
	output := firstUsage(resp.Usage.CompletionTokens, resp.Usage.OutputTokens)
	total := firstUsage(resp.Usage.TotalTokens)
	if input == nil && output == nil && total != nil {
		// Pooling endpoints (rerank, score) report only total_tokens, all of
		// which are input.
		input = total
	}
	if input != nil {
		tokenUsage.SetInputTokens(clampUint32(*input))
	}
	if output != nil {
		tokenUsage.SetOutputTokens(clampUint32(*output))
	}
	switch {
	case total != nil:
		tokenUsage.SetTotalTokens(clampUint32(*total))
	case input != nil || output != nil:
		var sum int64
		for _, v := range []*int64{input, output} {
			if v != nil {
				sum += *v
			}
		}
		tokenUsage.SetTotalTokens(clampUint32(sum))
	}
	return nil, nil, tokenUsage, responseModel, nil
}

func (t *passthroughTranslator) ResponseError(map[string]string, io.Reader) ([]internalapi.Header, []byte, error) {
	return nil, nil, nil
}

func firstUsage(values ...*int64) *int64 {
	for _, v := range values {
		if v != nil {
			return v
		}
	}
	return nil
}

func clampUint32(v int64) uint32 {
	if v <= 0 {
		return 0
	}
	if v > int64(^uint32(0)) {
		return ^uint32(0)
	}
	return uint32(v)
}
