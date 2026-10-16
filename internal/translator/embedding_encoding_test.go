// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package translator

import (
	"bytes"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"
	"k8s.io/utils/ptr"

	"github.com/envoyproxy/ai-gateway/internal/apischema/openai"
	"github.com/envoyproxy/ai-gateway/internal/internalapi"
	"github.com/envoyproxy/ai-gateway/internal/json"
)

func TestEncodeEmbedding(t *testing.T) {
	values := []float64{0.1, -0.1, 0}
	require.Equal(t, "zczMPc3MzL0AAAAA", encodeEmbedding(values, ptr.To("base64")).Value)
	require.Equal(t, values, encodeEmbedding(values, ptr.To("float")).Value)
	require.Equal(t, values, encodeEmbedding(values, nil).Value)
	empty := encodeEmbedding(nil, ptr.To("base64")).Value
	require.IsType(t, "", empty)
	require.Empty(t, empty)
}

func TestEmbeddingTranslationEncodingFormat(t *testing.T) {
	backends := []struct {
		name       string
		model      string
		translator OpenAIEmbeddingTranslator
		response   string
	}{
		{
			name:       "aws-titan",
			model:      "amazon.titan-embed-text-v2:0",
			translator: NewEmbeddingOpenAIToAWSBedrockTranslator(""),
			response:   `{"embedding":[1,-2.5,0.125],"inputTextTokenCount":3}`,
		},
		{
			name:       "gcp-predict",
			model:      "text-embedding-004",
			translator: NewEmbeddingOpenAIToGCPVertexAITranslator("", ""),
			response:   `{"predictions":[{"embeddings":{"values":[1,-2.5,0.125],"statistics":{"token_count":3}}}]}`,
		},
		{
			name:       "gcp-embed-content",
			model:      "gemini-embedding-2-preview",
			translator: NewEmbeddingOpenAIToGCPVertexAITranslator("", ""),
			response:   `{"embedding":{"values":[1,-2.5,0.125]},"usageMetadata":{"promptTokenCount":3}}`,
		},
	}
	for _, backend := range backends {
		for _, format := range []string{"base64", "float", ""} {
			t.Run(backend.name+"/"+format, func(t *testing.T) {
				request := []byte(`{"model":"` + backend.model + `","input":"hello"`)
				if format != "" {
					request = append(request, []byte(`,"encoding_format":"`+format+`"`)...)
				}
				request = append(request, '}')
				var req openai.EmbeddingRequest
				require.NoError(t, json.Unmarshal(request, &req))
				_, _, err := backend.translator.RequestBody(request, &req, false)
				require.NoError(t, err)
				headers, body, usage, model, err := backend.translator.ResponseBody(nil, bytes.NewBufferString(backend.response), true, nil)
				require.NoError(t, err)
				var response openai.EmbeddingResponse
				require.NoError(t, json.Unmarshal(body, &response))
				require.Len(t, response.Data, 1)
				if format == "base64" {
					// Three little-endian float32 values: 1, -2.5, and 0.125.
					require.Equal(t, "AACAPwAAIMAAAAA+", response.Data[0].Embedding.Value)
				} else {
					require.Equal(t, []float64{1, -2.5, 0.125}, response.Data[0].Embedding.Value)
				}
				require.Equal(t, 3, response.Usage.PromptTokens)
				require.Equal(t, 3, response.Usage.TotalTokens)
				tokens, set := usage.InputTokens()
				require.True(t, set)
				require.Equal(t, uint32(3), tokens)
				require.Equal(t, backend.model, model)
				require.Equal(t, []internalapi.Header{{contentLengthHeaderName, strconv.Itoa(len(body))}}, headers)
			})
		}
	}
}

func TestGCPEmbeddingTranslationBase64Batch(t *testing.T) {
	translator := NewEmbeddingOpenAIToGCPVertexAITranslator("", "")
	request := []byte(`{"model":"text-embedding-004","input":["first","second"],"encoding_format":"base64"}`)
	var req openai.EmbeddingRequest
	require.NoError(t, json.Unmarshal(request, &req))
	_, _, err := translator.RequestBody(request, &req, false)
	require.NoError(t, err)
	upstreamResponse := `{"predictions":[
		{"embeddings":{"values":[1,-2.5,0.125],"statistics":{"token_count":3,"truncated":true}}},
		{"embeddings":{"values":[0.25,0.5,0.75],"statistics":{"token_count":2}}}
	]}`
	_, body, _, _, err := translator.ResponseBody(nil, bytes.NewBufferString(upstreamResponse), true, nil)
	require.NoError(t, err)
	var response openai.EmbeddingResponse
	require.NoError(t, json.Unmarshal(body, &response))
	require.Len(t, response.Data, 2)
	require.Equal(t, "AACAPwAAIMAAAAA+", response.Data[0].Embedding.Value)
	require.Equal(t, "AACAPgAAAD8AAEA/", response.Data[1].Embedding.Value)
	require.Equal(t, 0, response.Data[0].Index)
	require.Equal(t, 1, response.Data[1].Index)
	require.True(t, response.Data[0].Truncated)
	require.False(t, response.Data[1].Truncated)
	require.Equal(t, 5, response.Usage.PromptTokens)
	require.Equal(t, 5, response.Usage.TotalTokens)
}
