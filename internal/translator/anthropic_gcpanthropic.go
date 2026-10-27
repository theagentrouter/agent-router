// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package translator

import (
	"cmp"
	"fmt"
	"strconv"
	"strings"

	"github.com/tidwall/sjson"

	anthropicschema "github.com/envoyproxy/ai-gateway/internal/apischema/anthropic"
	"github.com/envoyproxy/ai-gateway/internal/internalapi"
)

// NewAnthropicToGCPAnthropicTranslator creates a translator for Anthropic to GCP Anthropic format.
// This is essentially a passthrough translator with GCP-specific modifications.
func NewAnthropicToGCPAnthropicTranslator(apiVersion string, modelNameOverride internalapi.ModelNameOverride) AnthropicMessagesTranslator {
	return &anthropicToGCPAnthropicTranslator{
		apiVersion:        apiVersion,
		modelNameOverride: modelNameOverride,
	}
}

type anthropicToGCPAnthropicTranslator struct {
	anthropicToAnthropicTranslator
	apiVersion        string
	modelNameOverride internalapi.ModelNameOverride
	requestModel      internalapi.RequestModel
	anthropicBetas    []string
	betaFilterMode    string
	betaFilterValues  []string
}

// SetRequestHeaders implements [RequestHeadersSetter].
func (a *anthropicToGCPAnthropicTranslator) SetRequestHeaders(headers map[string]string) {
	a.anthropicBetas = parseCommaSeparatedHeader(headers, anthropicBetaHeaderName)
}

// SetHeaderValueFilter implements [HeaderValueFilterSetter]. Only anthropic-beta is handled here,
// since it is the header this translator forwards upstream itself; filters on any other header are
// applied by Envoy's header mutation instead.
func (a *anthropicToGCPAnthropicTranslator) SetHeaderValueFilter(name, mode string, values []string) {
	if !strings.EqualFold(name, anthropicBetaHeaderName) {
		return
	}
	a.betaFilterMode = mode
	a.betaFilterValues = values
}

// RequestBody implements [AnthropicMessagesTranslator.RequestBody] for Anthropic to GCP Anthropic translation.
// This handles the transformation from native Anthropic format to GCP Anthropic format.
func (a *anthropicToGCPAnthropicTranslator) RequestBody(raw []byte, req *anthropicschema.MessagesRequest, _ bool) (
	newHeaders []internalapi.Header, newBody []byte, err error,
) {
	a.stream = req.Stream

	// Apply model name override if configured.
	a.requestModel = cmp.Or(a.modelNameOverride, req.Model)

	// Add GCP-specific anthropic_version field (required by GCP Vertex AI).
	// Uses backend config version (e.g., "vertex-2023-10-16" for GCP Vertex AI).
	if a.apiVersion == "" {
		return nil, nil, fmt.Errorf("anthropic_version is required for GCP Vertex AI but not provided in backend configuration")
	}

	mutatedBody, _ := sjson.SetBytesOptions(raw, anthropicVersionKey, a.apiVersion, sjsonOptions)

	// Remove the model field since GCP doesn't want it in the body.
	newBody, _ = sjson.DeleteBytesOptions(mutatedBody, "model",
		// It is safe to use sjsonOptionsInPlace here since we have already created a new mutatedBody above.
		sjsonOptionsInPlace)

	// Determine the GCP path based on whether streaming is requested.
	specifier := "rawPredict"
	if req.Stream {
		specifier = "streamRawPredict"
	}

	path := buildGCPModelPathSuffix(gcpModelPublisherAnthropic, a.requestModel, specifier)
	newHeaders = []internalapi.Header{{pathHeaderName, path}, {contentLengthHeaderName, strconv.Itoa(len(newBody))}}
	// Vertex forwards the anthropic-beta header verbatim and 400s on unsupported values. When the
	// filter drops a value, overwrite the forwarded header with the filtered set.
	if betas, changed := filterHeaderValues(a.anthropicBetas, a.betaFilterMode, a.betaFilterValues); changed {
		newHeaders = append(newHeaders, internalapi.Header{anthropicBetaHeaderName, strings.Join(betas, ",")})
	}
	return
}
