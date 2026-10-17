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
	"github.com/envoyproxy/ai-gateway/internal/json"
)

func TestOpenAIToOpenAIImageTranslator_RequestBody_ModelOverrideAndPath(t *testing.T) {
	tr := NewImageGenerationOpenAIToOpenAITranslator("v1", openai.ModelGPTImage1Mini)
	req := &openai.ImageGenerationRequest{Model: "dall-e-3", Prompt: "a cat"}
	original, _ := json.Marshal(req)

	hm, bm, err := tr.RequestBody(original, req, false)
	require.NoError(t, err)
	require.NotNil(t, hm)
	require.Len(t, hm, 2) // path and content-length headers
	require.Equal(t, pathHeaderName, hm[0].Key())
	require.Equal(t, "/v1/images/generations", hm[0].Value())
	require.Equal(t, contentLengthHeaderName, hm[1].Key())

	require.NotNil(t, bm)
	var got openai.ImageGenerationRequest
	require.NoError(t, json.Unmarshal(bm, &got))
	require.Equal(t, "gpt-image-1-mini", got.Model)
}

func TestOpenAIToOpenAIImageTranslator_RequestBody_ForceMutation(t *testing.T) {
	tr := NewImageGenerationOpenAIToOpenAITranslator("v1", "")
	req := &openai.ImageGenerationRequest{Model: "dall-e-2", Prompt: "a cat"}
	original, _ := json.Marshal(req)

	hm, bm, err := tr.RequestBody(original, req, true)
	require.NoError(t, err)
	require.NotNil(t, hm)
	// Content-Length is set only when body mutated; with force it should be mutated to original.
	foundCL := false
	for _, h := range hm {
		if h.Key() == contentLengthHeaderName {
			foundCL = true
			break
		}
	}
	require.True(t, foundCL)
	require.NotNil(t, bm)
	require.Equal(t, original, bm)
}

func TestOpenAIToOpenAIImageTranslator_ResponseError_NonJSON(t *testing.T) {
	tr := NewImageGenerationOpenAIToOpenAITranslator("v1", "")
	headers := map[string]string{contentTypeHeaderName: "text/plain", statusHeaderName: "503"}
	hm, bm, err := tr.ResponseError(headers, bytes.NewReader([]byte("backend error")))
	require.NoError(t, err)
	require.NotNil(t, hm)
	require.NotNil(t, bm)

	// Body should be OpenAI error JSON
	var actual struct {
		Error openai.ErrorType `json:"error"`
	}
	require.NoError(t, json.Unmarshal(bm, &actual))
	require.Equal(t, openAIBackendError, actual.Error.Type)
}

func TestOpenAIToOpenAIImageTranslator_ResponseBody_OK(t *testing.T) {
	tr := NewImageGenerationOpenAIToOpenAITranslator("v1", "")
	resp := &openai.ImageGenerationResponse{}
	buf, _ := json.Marshal(resp)
	hm, bm, usage, responseModel, err := tr.ResponseBody(map[string]string{}, bytes.NewReader(buf), true, nil)
	require.NoError(t, err)
	require.Nil(t, hm)
	require.Nil(t, bm)
	require.Equal(t, tokenUsageFrom(-1, -1, -1, -1, -1, -1), usage)
	require.Empty(t, responseModel)
}

func TestOpenAIToOpenAIImageTranslator_RequestBody_NoOverrideNoForce(t *testing.T) {
	tr := NewImageGenerationOpenAIToOpenAITranslator("v1", "")
	req := &openai.ImageGenerationRequest{Model: "dall-e-2", Prompt: "a cat"}
	original, _ := json.Marshal(req)

	hm, bm, err := tr.RequestBody(original, req, false)
	require.NoError(t, err)
	require.NotNil(t, hm)
	// Only path header present; content-length should not be set when no mutation
	require.Len(t, hm, 1)
	require.Equal(t, pathHeaderName, hm[0].Key())
	require.Nil(t, bm)
}

func TestOpenAIToOpenAIImageTranslator_ResponseError_JSONPassthrough(t *testing.T) {
	tr := NewImageGenerationOpenAIToOpenAITranslator("v1", "")
	headers := map[string]string{contentTypeHeaderName: jsonContentType, statusHeaderName: "500"}
	// Already JSON — should be passed through (no mutation)
	hm, bm, err := tr.ResponseError(headers, bytes.NewReader([]byte(`{"error":"msg"}`)))
	require.NoError(t, err)
	require.Nil(t, hm)
	require.Nil(t, bm)
}

func TestOpenAIToOpenAIImageTranslator_ResponseBody_ModelPropagatesFromRequest(t *testing.T) {
	// Use override so effective model differs from original
	tr := NewImageGenerationOpenAIToOpenAITranslator("v1", openai.ModelGPTImage1Mini)
	req := &openai.ImageGenerationRequest{Model: "dall-e-3", Prompt: "a cat"}
	original, _ := json.Marshal(req)
	// Call RequestBody first to set requestModel inside translator
	_, _, err := tr.RequestBody(original, req, false)
	require.NoError(t, err)

	resp := &openai.ImageGenerationResponse{
		// Two images returned
		Data: make([]openai.ImageGenerationResponseData, 2),
	}
	buf, _ := json.Marshal(resp)
	_, _, _, respModel, err := tr.ResponseBody(map[string]string{}, bytes.NewReader(buf), true, nil)
	require.NoError(t, err)
	require.Equal(t, openai.ModelGPTImage1Mini, respModel)
}

func TestOpenAIToOpenAIImageTranslator_ResponseHeaders_NoOp(t *testing.T) {
	tr := NewImageGenerationOpenAIToOpenAITranslator("v1", "")
	hm, err := tr.ResponseHeaders(map[string]string{"foo": "bar"})
	require.NoError(t, err)
	require.Nil(t, hm)
}

func TestOpenAIToOpenAIImageTranslator_ResponseBody_DecodeError(t *testing.T) {
	tr := NewImageGenerationOpenAIToOpenAITranslator("v1", "")
	_, _, _, _, err := tr.ResponseBody(map[string]string{}, bytes.NewReader([]byte("not-json")), true, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "failed to decode response body")
}

func TestOpenAIToOpenAIImageTranslator_ResponseBody_RecordsSpan(t *testing.T) {
	mockSpan := &mockImageGenerationSpan{}
	tr := NewImageGenerationOpenAIToOpenAITranslator("v1", "")

	resp := &openai.ImageGenerationResponse{
		Data: []openai.ImageGenerationResponseData{{URL: "https://example.com/img.png"}},
	}
	buf, _ := json.Marshal(resp)
	_, _, _, _, err := tr.ResponseBody(map[string]string{}, bytes.NewReader(buf), true, mockSpan)
	require.NoError(t, err)
	require.NotNil(t, mockSpan.recordedResponse)
}

type mockImageGenerationSpan struct {
	recordedResponse *openai.ImageGenerationResponse
}

func (m *mockImageGenerationSpan) RecordResponse(resp *openai.ImageGenerationResponse) {
	m.recordedResponse = resp
}

func (m *mockImageGenerationSpan) EndSpanOnError(int, []byte)    {}
func (m *mockImageGenerationSpan) EndSpan()                      {}
func (m *mockImageGenerationSpan) EndSpanOnCancel()              {}
func (m *mockImageGenerationSpan) RecordResponseChunk(*struct{}) {}

func TestOpenAIToOpenAIImageTranslator_ResponseBody_Usage(t *testing.T) {
	tr := NewImageGenerationOpenAIToOpenAITranslator("v1", "")
	resp := &openai.ImageGenerationResponse{
		Usage: &openai.ImageGenerationUsage{
			TotalTokens:  100,
			InputTokens:  40,
			OutputTokens: 60,
		},
	}
	buf, _ := json.Marshal(resp)
	_, _, usage, _, err := tr.ResponseBody(map[string]string{}, bytes.NewReader(buf), true, nil)
	require.NoError(t, err)
	require.Equal(t, tokenUsageFrom(40, -1, -1, 60, 100, -1), usage)
}
