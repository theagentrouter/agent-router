// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package translator

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/envoyproxy/ai-gateway/internal/apischema/openai"
	"github.com/envoyproxy/ai-gateway/internal/filterapi"
	"github.com/envoyproxy/ai-gateway/internal/metrics"
)

func TestOpenAIBatchesTranslators_Identity(t *testing.T) {
	translators := map[string]BatchesTranslator{
		"create":   &openAIBatchCreateTranslator{},
		"retrieve": &openAIBatchRetrieveTranslator{},
		"cancel":   &openAIBatchCancelTranslator{},
		"list":     &openAIBatchListTranslator{},
	}

	for op, tr := range translators {
		t.Run(op+"/RequestBody is a no-op", func(t *testing.T) {
			in := &BatchesRequest{NativeID: "batch_native", Path: "/v1/batches/batch_native", Method: "GET"}
			hdrs, body, err := tr.RequestBody([]byte("ignored"), in, false)
			require.NoError(t, err)
			require.Nil(t, hdrs)
			require.Nil(t, body)
		})

		t.Run(op+"/ResponseHeaders is a no-op", func(t *testing.T) {
			hdrs, err := tr.ResponseHeaders(map[string]string{"content-type": "application/json"})
			require.NoError(t, err)
			require.Nil(t, hdrs)
		})

		t.Run(op+"/ResponseBody passes through with zero usage", func(t *testing.T) {
			hdrs, body, usage, model, err := tr.ResponseBody(
				map[string]string{}, bytes.NewReader([]byte(`{"id":"batch_native"}`)), true, nil)
			require.NoError(t, err)
			require.Nil(t, hdrs)
			require.Nil(t, body)
			require.Equal(t, metrics.TokenUsage{}, usage)
			require.Empty(t, model)
		})

		t.Run(op+"/ResponseError is a no-op", func(t *testing.T) {
			hdrs, body, err := tr.ResponseError(map[string]string{}, bytes.NewReader([]byte(`{"error":{}}`)))
			require.NoError(t, err)
			require.Nil(t, hdrs)
			require.Nil(t, body)
		})
	}
}

func TestNewBatchTranslators(t *testing.T) {
	type opCase struct {
		newTranslator func(filterapi.VersionedAPISchema) (BatchesTranslator, error)
		wantType      BatchesTranslator
	}
	ops := map[string]opCase{
		"create":   {NewBatchCreateTranslator, &openAIBatchCreateTranslator{}},
		"retrieve": {NewBatchRetrieveTranslator, &openAIBatchRetrieveTranslator{}},
		"cancel":   {NewBatchCancelTranslator, &openAIBatchCancelTranslator{}},
		"list":     {NewBatchListTranslator, &openAIBatchListTranslator{}},
	}

	openAISchemas := []filterapi.VersionedAPISchema{
		{Name: filterapi.APISchemaOpenAI},
		{Name: filterapi.APISchemaOpenAI, Prefix: "v1"},
	}
	unsupportedSchemas := []filterapi.VersionedAPISchema{
		{Name: filterapi.APISchemaAWSBedrock},
		{Name: filterapi.APISchemaAzureOpenAI},
		{Name: filterapi.APISchemaGCPVertexAI},
		{Name: filterapi.APISchemaGCPAnthropic},
		{Name: filterapi.APISchemaAnthropic},
		{Name: filterapi.APISchemaAWSAnthropic},
		{Name: filterapi.APISchemaCohere},
	}

	for op, tc := range ops {
		for _, schema := range openAISchemas {
			t.Run(op+"/openai returns its own concrete identity", func(t *testing.T) {
				tr, err := tc.newTranslator(schema)
				require.NoError(t, err)
				require.IsType(t, tc.wantType, tr)
			})
		}
		for _, schema := range unsupportedSchemas {
			t.Run(op+"/"+string(schema.Name)+" fails closed", func(t *testing.T) {
				tr, err := tc.newTranslator(schema)
				require.Error(t, err)
				require.Nil(t, tr)
				require.Contains(t, err.Error(), "unsupported API schema for batches")
			})
		}
	}
}

func TestOpenAIBatchCreateTranslator_AcceptsAllEndpoints(t *testing.T) {
	tr, err := NewBatchCreateTranslator(filterapi.VersionedAPISchema{Name: filterapi.APISchemaOpenAI})
	require.NoError(t, err)
	for _, ep := range []openai.BatchNewParamsEndpoint{
		openai.BatchNewParamsEndpointV1ChatCompletions,
		openai.BatchNewParamsEndpointV1Embeddings,
		openai.BatchNewParamsEndpointV1Completions,
		openai.BatchNewParamsEndpointV1Responses,
		openai.BatchNewParamsEndpointV1Moderations,
		openai.BatchNewParamsEndpointV1ImagesGenerations,
		openai.BatchNewParamsEndpointV1ImagesEdits,
		openai.BatchNewParamsEndpointV1Videos,
		"/v1/not-yet-known",
	} {
		hdrs, body, err := tr.RequestBody([]byte(`{}`), &BatchesRequest{Endpoint: ep}, false)
		require.NoError(t, err, ep)
		require.Nil(t, hdrs)
		require.Nil(t, body)
	}
}
