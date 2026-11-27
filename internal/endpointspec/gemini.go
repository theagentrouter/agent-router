// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package endpointspec

import (
	"cmp"
	"fmt"

	"github.com/envoyproxy/ai-gateway/internal/apischema/gcp"
	"github.com/envoyproxy/ai-gateway/internal/filterapi"
	"github.com/envoyproxy/ai-gateway/internal/internalapi"
	"github.com/envoyproxy/ai-gateway/internal/tracing/tracingapi"
	"github.com/envoyproxy/ai-gateway/internal/translator"
)

// GeminiGenerateContentEndpointSpec serves Gemini Developer API generation requests.
type GeminiGenerateContentEndpointSpec struct {
	Path gcp.GeminiGenerateContentPath
}

// ParseBody implements [Spec.ParseBody].
func (s GeminiGenerateContentEndpointSpec) ParseBody([]byte, bool) (internalapi.OriginalModel, *gcp.GeminiGenerateContentRequest, bool, []byte, error) {
	return s.Path.Model, &gcp.GeminiGenerateContentRequest{Model: s.Path.Model}, s.Path.Streaming(), nil, nil
}

// ParseMultipartBody implements [Spec.ParseMultipartBody].
func (GeminiGenerateContentEndpointSpec) ParseMultipartBody([]byte, string, bool) (internalapi.OriginalModel, *gcp.GeminiGenerateContentRequest, bool, []byte, error) {
	return "", nil, false, nil, errMultipartNotSupported
}

// GetTranslator implements [Spec.GetTranslator].
func (s GeminiGenerateContentEndpointSpec) GetTranslator(schema filterapi.VersionedAPISchema, override string) (translator.Translator[gcp.GeminiGenerateContentRequest, tracingapi.GeminiGenerateContentSpan], error) {
	if schema.Name != filterapi.APISchemaGoogleAIStudio {
		return nil, fmt.Errorf("%w: Gemini native generation requires a %s backend", internalapi.ErrInvalidRequestBody, filterapi.APISchemaGoogleAIStudio)
	}
	model := cmp.Or(override, s.Path.Model)
	if !gcp.IsGeminiModelID(model) {
		return nil, fmt.Errorf("%w: invalid Gemini backend model ID", internalapi.ErrInvalidRequestBody)
	}
	return translator.NewGeminiGenerateContentTranslator(s.Path, model), nil
}

// RedactSensitiveInfoFromRequest implements [Spec.RedactSensitiveInfoFromRequest].
func (GeminiGenerateContentEndpointSpec) RedactSensitiveInfoFromRequest(req *gcp.GeminiGenerateContentRequest) (*gcp.GeminiGenerateContentRequest, error) {
	return req, nil
}
