// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package endpointspec

import (
	"fmt"

	"github.com/envoyproxy/ai-gateway/internal/apischema/fal"
	"github.com/envoyproxy/ai-gateway/internal/filterapi"
	"github.com/envoyproxy/ai-gateway/internal/internalapi"
	"github.com/envoyproxy/ai-gateway/internal/translator"
)

// FalEndpointSpec implements EndpointSpec for fal's <prefix>/run/ and <prefix>/queue/ endpoints.
// The model is the fal endpoint ID in the path; bodies are fal app inputs and are not read.
type FalEndpointSpec struct {
	// Prefix is the gateway path prefix fal is served under, such as /fal.
	Prefix string
	// URLScheme is the scheme of the gateway URLs that replace fal URLs in responses.
	URLScheme string
}

// ModelFromPath implements [PathModelExtractor.ModelFromPath].
func (s FalEndpointSpec) ModelFromPath(path string) (internalapi.OriginalModel, bool, bool) {
	p, ok := fal.ParsePath(s.Prefix, path)
	return p.Model, p.Stream, ok
}

// ParseBody implements [Spec.ParseBody].
func (FalEndpointSpec) ParseBody([]byte, bool) (internalapi.OriginalModel, *fal.Request, bool, []byte, error) {
	return "", &fal.Request{}, false, nil, nil
}

// ParseMultipartBody implements [Spec.ParseMultipartBody].
func (FalEndpointSpec) ParseMultipartBody([]byte, string, bool) (internalapi.OriginalModel, *fal.Request, bool, []byte, error) {
	return "", &fal.Request{}, false, nil, nil
}

// GetTranslator implements [Spec.GetTranslator].
func (s FalEndpointSpec) GetTranslator(schema filterapi.VersionedAPISchema, modelNameOverride string) (translator.FalTranslator, error) {
	if schema.Name != filterapi.APISchemaFal {
		return nil, fmt.Errorf("%w: unsupported API schema: backend=%s", internalapi.ErrInvalidRequestBody, schema)
	}
	if modelNameOverride != "" {
		return nil, fmt.Errorf("%w: model name override is not supported for fal: backend=%s", internalapi.ErrInvalidRequestBody, schema)
	}
	return translator.NewFalToFalTranslator(s.Prefix, s.URLScheme), nil
}

// RedactSensitiveInfoFromRequest implements [Spec.RedactSensitiveInfoFromRequest].
func (FalEndpointSpec) RedactSensitiveInfoFromRequest(req *fal.Request) (*fal.Request, error) {
	redacted := *req
	return &redacted, nil
}
