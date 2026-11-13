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

	typesafeschema "github.com/envoyproxy/ai-gateway/internal/apischema/typesafe"
	"github.com/envoyproxy/ai-gateway/internal/internalapi"
	"github.com/envoyproxy/ai-gateway/internal/json"
	"github.com/envoyproxy/ai-gateway/internal/metrics"
	"github.com/envoyproxy/ai-gateway/internal/tracing/tracingapi"
)

// NewSystemOneTypeSafeToTypeSafeTranslator implements [Factory] for TypeSafe System One passthrough.
func NewSystemOneTypeSafeToTypeSafeTranslator(apiVersion string, modelNameOverride internalapi.ModelNameOverride) TypeSafeSystemOneTranslator {
	return &typeSafeToTypeSafeTranslatorSystemOne{modelNameOverride: modelNameOverride, path: path.Join("/", apiVersion, "systemone")} // e.g., /v1/systemone
}

// typeSafeToTypeSafeTranslatorSystemOne is a passthrough translator for the TypeSafe System One API.
// May apply model overrides but otherwise preserves the TypeSafe format:
// https://docs.typesafe.ai/api.md
type typeSafeToTypeSafeTranslatorSystemOne struct {
	modelNameOverride internalapi.ModelNameOverride
	// requestModel stores the effective model for this request (override or provided).
	requestModel internalapi.RequestModel
	// path is the upstream System One endpoint path, prefixed with the API version.
	path string
}

// RequestBody implements [TypeSafeSystemOneTranslator.RequestBody].
func (t *typeSafeToTypeSafeTranslatorSystemOne) RequestBody(original []byte, req *typesafeschema.SystemOneRequest, onRetry bool) (
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

	if onRetry && len(newBody) == 0 {
		newBody = original
	}

	newHeaders = []internalapi.Header{{pathHeaderName, t.path}}
	if len(newBody) > 0 {
		newHeaders = append(newHeaders, internalapi.Header{contentLengthHeaderName, strconv.Itoa(len(newBody))})
	}
	return
}

// ResponseHeaders implements [TypeSafeSystemOneTranslator.ResponseHeaders].
func (t *typeSafeToTypeSafeTranslatorSystemOne) ResponseHeaders(map[string]string) (newHeaders []internalapi.Header, err error) {
	return nil, nil
}

// ResponseBody implements [TypeSafeSystemOneTranslator.ResponseBody].
// The body is forwarded untouched; it is decoded only for token usage, the
// resolved model and tracing.
func (t *typeSafeToTypeSafeTranslatorSystemOne) ResponseBody(_ map[string]string, body io.Reader, _ bool, span tracingapi.SystemOneSpan) (
	newHeaders []internalapi.Header, newBody []byte, tokenUsage metrics.TokenUsage, responseModel internalapi.ResponseModel, err error,
) {
	var resp typesafeschema.SystemOneResponse
	if err := json.NewDecoder(body).Decode(&resp); err != nil {
		return nil, nil, tokenUsage, t.requestModel, fmt.Errorf("failed to unmarshal body: %w", err)
	}

	if span != nil {
		span.RecordResponse(&resp)
	}

	if resp.Usage != nil {
		var totalTokens uint32
		if resp.Usage.InputTokens != nil {
			input := uint32(*resp.Usage.InputTokens) //nolint:gosec
			tokenUsage.SetInputTokens(input)
			totalTokens += input
		}
		if resp.Usage.OutputTokens != nil {
			output := uint32(*resp.Usage.OutputTokens) //nolint:gosec
			tokenUsage.SetOutputTokens(output)
			totalTokens += output
		}
		tokenUsage.SetTotalTokens(totalTokens)
	}

	// TypeSafe resolves aliases such as jev-latest to the concrete version in the response.
	responseModel = resp.Model
	if responseModel == "" {
		responseModel = t.requestModel
	}
	return
}

// systemOneErrorType maps an HTTP status onto the error types TypeSafe uses.
func systemOneErrorType(status string) string {
	switch {
	case status == "401":
		return typesafeschema.ErrorTypeAuthentication
	case strings.HasPrefix(status, "4"):
		return typesafeschema.ErrorTypeAPIUsage
	default:
		return typesafeschema.ErrorTypeAPI
	}
}

// ResponseError implements [TypeSafeSystemOneTranslator.ResponseError].
// JSON error bodies are passed through untouched so the TypeSafe SDK error
// classes keep working. A non-JSON body is wrapped into the TypeSafe error shape.
func (t *typeSafeToTypeSafeTranslatorSystemOne) ResponseError(respHeaders map[string]string, body io.Reader) (
	newHeaders []internalapi.Header, newBody []byte, err error,
) {
	if v, ok := respHeaders[contentTypeHeaderName]; ok && !strings.Contains(v, jsonContentType) {
		buf, err := io.ReadAll(body)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to read error body: %w", err)
		}
		typesafeErr := typesafeschema.SystemOneError{
			Detail: typesafeschema.SystemOneErrorDetail{
				ErrorType: systemOneErrorType(respHeaders[statusHeaderName]),
				Message:   string(buf),
			},
		}
		newBody, err = json.Marshal(typesafeErr)
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
