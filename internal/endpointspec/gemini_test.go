// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package endpointspec

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/envoyproxy/ai-gateway/internal/apischema/gcp"
	"github.com/envoyproxy/ai-gateway/internal/filterapi"
	"github.com/envoyproxy/ai-gateway/internal/internalapi"
)

func TestGeminiGenerateContentEndpointSpec(t *testing.T) {
	const prefix = "/gemini/v1beta/models"
	path, err := gcp.ParseGeminiGenerateContentPath(prefix, prefix+"/alias:streamGenerateContent?alt=sse")
	require.NoError(t, err)
	spec := GeminiGenerateContentEndpointSpec{Path: path}
	raw := []byte("{ opaque provider input }")

	model, req, streaming, body, err := spec.ParseBody(raw, true)
	require.NoError(t, err)
	require.Equal(t, "alias", model)
	require.Equal(t, &gcp.GeminiGenerateContentRequest{Model: "alias"}, req)
	require.True(t, streaming)
	require.Nil(t, body)

	redacted, err := spec.RedactSensitiveInfoFromRequest(req)
	require.NoError(t, err)
	require.Equal(t, &gcp.GeminiGenerateContentRequest{Model: "alias"}, redacted)

	_, _, _, _, err = spec.ParseMultipartBody(raw, "multipart/form-data", true)
	require.ErrorIs(t, err, internalapi.ErrMalformedRequest)

	_, err = spec.GetTranslator(filterapi.VersionedAPISchema{Name: filterapi.APISchemaGCPVertexAI}, "")
	require.ErrorIs(t, err, internalapi.ErrInvalidRequestBody)
	schema := filterapi.VersionedAPISchema{Name: filterapi.APISchemaGoogleAIStudio}
	_, err = spec.GetTranslator(schema, "models/invalid")
	require.ErrorIs(t, err, internalapi.ErrInvalidRequestBody)

	tr, err := spec.GetTranslator(schema, "upstream-model")
	require.NoError(t, err)
	headers, body, err := tr.RequestBody(raw, req, true)
	require.NoError(t, err)
	require.Contains(t, headers, internalapi.Header{":path", "/v1beta/models/upstream-model:streamGenerateContent?alt=sse"})
	require.Equal(t, raw, body)
}
