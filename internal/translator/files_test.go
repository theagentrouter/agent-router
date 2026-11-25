// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package translator

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/envoyproxy/ai-gateway/internal/filterapi"
	"github.com/envoyproxy/ai-gateway/internal/metrics"
)

func TestOpenAIFilesTranslators_Identity(t *testing.T) {
	// Every per-op type is its own concrete identity; assert each one's four methods are no-ops.
	translators := map[string]FilesTranslator{
		"upload":   &openAIFileUploadTranslator{},
		"retrieve": &openAIFileRetrieveTranslator{},
		"content":  &openAIFileContentTranslator{},
		"delete":   &openAIFileDeleteTranslator{},
		"list":     &openAIFileListTranslator{},
	}

	for op, tr := range translators {
		t.Run(op+"/RequestBody is a no-op", func(t *testing.T) {
			in := &FilesRequest{NativeID: "file-native", Path: "/v1/files/file-native", Method: "GET"}
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
				map[string]string{}, bytes.NewReader([]byte(`{"id":"file-native"}`)), true, nil)
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

func TestNewFileTranslators(t *testing.T) {
	// Each per-op constructor shares the same schema gate (identity for OpenAI, fail-closed
	// otherwise) but returns its OWN concrete per-op type.
	type opCase struct {
		newTranslator func(filterapi.VersionedAPISchema) (FilesTranslator, error)
		wantType      FilesTranslator
	}
	ops := map[string]opCase{
		"upload":   {NewFileUploadTranslator, &openAIFileUploadTranslator{}},
		"retrieve": {NewFileRetrieveTranslator, &openAIFileRetrieveTranslator{}},
		"content":  {NewFileContentTranslator, &openAIFileContentTranslator{}},
		"delete":   {NewFileDeleteTranslator, &openAIFileDeleteTranslator{}},
		"list":     {NewFileListTranslator, &openAIFileListTranslator{}},
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
				require.Contains(t, err.Error(), "unsupported API schema for files")
			})
		}
	}
}
