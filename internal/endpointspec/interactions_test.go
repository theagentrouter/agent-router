// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package endpointspec

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"k8s.io/utils/ptr"

	"github.com/envoyproxy/ai-gateway/internal/apischema/gcp"
	"github.com/envoyproxy/ai-gateway/internal/filterapi"
	"github.com/envoyproxy/ai-gateway/internal/internalapi"
)

func TestInteractionsEndpointSpec_ParseBody(t *testing.T) {
	spec := InteractionsEndpointSpec{}

	t.Run("agent request without store gets store=false", func(t *testing.T) {
		body := `{"agent":"antigravity-preview-09-2026","input":"fix the bug","environment":"remote"}`
		model, req, stream, mutated, err := spec.ParseBody([]byte(body), false)
		require.NoError(t, err)
		require.Equal(t, "antigravity-preview-09-2026", model)
		require.False(t, stream)
		require.Equal(t, ptr.To(false), req.Store)
		require.NotNil(t, mutated)
		require.Equal(t, gjson.False, gjson.GetBytes(mutated, "store").Type)
		require.Equal(t, "fix the bug", gjson.GetBytes(mutated, "input").String(), "other fields are kept")
		require.Equal(t, "remote", gjson.GetBytes(mutated, "environment").String())
	})

	t.Run("model request with store=false is forwarded unchanged", func(t *testing.T) {
		model, req, stream, mutated, err := spec.ParseBody([]byte(`{"model":"gemini-4-hydrogen","agent":"a","input":"hi","stream":true,"store":false,"background":false}`), true)
		require.NoError(t, err)
		require.Equal(t, "gemini-4-hydrogen", model, "model wins over agent")
		require.True(t, stream)
		require.Equal(t, &gcp.InteractionsRequest{Model: "gemini-4-hydrogen", Agent: "a", Stream: true, Store: ptr.To(false), Background: ptr.To(false)}, req)
		require.Nil(t, mutated)
	})

	for _, tc := range []struct {
		name    string
		body    string
		wantErr error
		wantMsg string
	}{
		{"store=true", `{"agent":"a","store":true}`, internalapi.ErrInvalidRequestBody, "'store' must be false"},
		{"background", `{"agent":"a","store":false,"background":true}`, internalapi.ErrInvalidRequestBody, "'background' is not supported"},
		{"background without store", `{"agent":"a","background":true}`, internalapi.ErrInvalidRequestBody, "'background' is not supported"},
		{"previous_interaction_id", `{"agent":"a","previous_interaction_id":"v1_prev"}`, internalapi.ErrInvalidRequestBody, "'previous_interaction_id' is not supported"},
		{"no model or agent", `{"input":"hi"}`, internalapi.ErrInvalidRequestBody, "'model' or 'agent' field is required"},
		{"malformed", `{"agent":`, internalapi.ErrMalformedRequest, "failed to parse JSON for /interactions"},
		{"wrong store type", `{"agent":"a","store":"no"}`, internalapi.ErrMalformedRequest, "failed to parse JSON for /interactions"},
	} {
		t.Run("rejects "+tc.name, func(t *testing.T) {
			_, _, _, _, err := spec.ParseBody([]byte(tc.body), false)
			require.ErrorIs(t, err, tc.wantErr)
			require.ErrorContains(t, err, tc.wantMsg)
			require.Error(t, internalapi.GetUserFacingError(err), "answered as a 400, not a 500")
		})
	}
}

func TestInteractionsEndpointSpec_ParseMultipartBody(t *testing.T) {
	_, _, _, _, err := InteractionsEndpointSpec{}.ParseMultipartBody(nil, "multipart/form-data", false)
	require.ErrorIs(t, err, internalapi.ErrMalformedRequest)
}

func TestInteractionsEndpointSpec_GetTranslator(t *testing.T) {
	spec := InteractionsEndpointSpec{}
	tr, err := spec.GetTranslator(filterapi.VersionedAPISchema{Name: filterapi.APISchemaGCPVertexAI}, "")
	require.NoError(t, err)
	require.NotNil(t, tr)

	for _, schema := range []filterapi.APISchemaName{filterapi.APISchemaOpenAI, filterapi.APISchemaGCPAnthropic, filterapi.APISchemaAWSBedrock, filterapi.APISchemaAnthropic} {
		t.Run(string(schema), func(t *testing.T) {
			_, err := spec.GetTranslator(filterapi.VersionedAPISchema{Name: schema}, "")
			require.ErrorIs(t, err, internalapi.ErrInvalidRequestBody, "answered as a 422, not a 500")
			require.ErrorContains(t, err, "only supports backends with the GCPVertexAI schema")
		})
	}
}

func TestInteractionsEndpointSpec_RedactSensitiveInfoFromRequest(t *testing.T) {
	req := &gcp.InteractionsRequest{Agent: "a", Store: ptr.To(false)}
	redacted, err := InteractionsEndpointSpec{}.RedactSensitiveInfoFromRequest(req)
	require.NoError(t, err)
	require.Equal(t, req, redacted)
	require.NotSame(t, req, redacted)
}
