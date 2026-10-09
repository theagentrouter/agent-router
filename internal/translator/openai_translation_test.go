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

func TestTranslationTranslator_RequestBody_NoOverride(t *testing.T) {
	tr := NewTranslationOpenAIToOpenAITranslator("v1", "")
	req := &openai.TranslationRequest{Model: "whisper-1", FileName: "test.mp3", FileSize: 2048}
	original := []byte("multipart-body-data")

	hm, bm, err := tr.RequestBody(original, req, false)
	require.NoError(t, err)
	require.Len(t, hm, 1)
	require.Equal(t, pathHeaderName, hm[0].Key())
	require.Equal(t, "/v1/audio/translations", hm[0].Value())
	require.Nil(t, bm)
}

func TestTranslationTranslator_RequestBody_ForceMutation(t *testing.T) {
	tr := NewTranslationOpenAIToOpenAITranslator("v1", "")
	req := &openai.TranslationRequest{Model: "whisper-1"}
	original := []byte("multipart-body-data")

	hm, bm, err := tr.RequestBody(original, req, true)
	require.NoError(t, err)
	require.NotNil(t, bm)
	require.Equal(t, original, bm)
	foundCL := false
	for _, h := range hm {
		if h.Key() == contentLengthHeaderName {
			foundCL = true
		}
	}
	require.True(t, foundCL)
}

func TestTranslationTranslator_RequestBody_ModelOverride(t *testing.T) {
	tr := NewTranslationOpenAIToOpenAITranslator("v1", "whisper-large-v3")

	body, contentType := buildMultipartBody(t, map[string]string{"model": "whisper-1"}, "file", "test.mp3", []byte("audio"))
	tr.(*openAIToOpenAITranslatorV1Translation).SetContentType(contentType)

	req := &openai.TranslationRequest{Model: "whisper-1", FileName: "test.mp3", FileSize: 5}

	hm, bm, err := tr.RequestBody(body, req, false)
	require.NoError(t, err)
	require.NotNil(t, bm)

	foundCT := false
	for _, h := range hm {
		if h.Key() == contentTypeHeaderName {
			foundCT = true
			require.Contains(t, h.Value(), "multipart/form-data")
		}
	}
	require.True(t, foundCT)

	// Verify the rewritten body has the new model.
	var newCT string
	for _, h := range hm {
		if h.Key() == contentTypeHeaderName {
			newCT = h.Value()
		}
	}
	fields := parseMultipartFields(t, bm, newCT)
	require.Equal(t, "whisper-large-v3", fields["model"])
}

func TestTranslationTranslator_ResponseHeaders_NoOp(t *testing.T) {
	tr := NewTranslationOpenAIToOpenAITranslator("v1", "")
	hm, err := tr.ResponseHeaders(map[string]string{"foo": "bar"})
	require.NoError(t, err)
	require.Nil(t, hm)
}

func TestTranslationTranslator_ResponseBody_NoSpan(t *testing.T) {
	tr := NewTranslationOpenAIToOpenAITranslator("v1", "")
	req := &openai.TranslationRequest{Model: "whisper-1"}
	_, _, _ = tr.RequestBody([]byte("body"), req, false)

	resp := openai.TranslationResponse{Text: "translated text"}
	respBytes, _ := json.Marshal(resp)

	hm, bm, usage, model, err := tr.ResponseBody(nil, bytes.NewReader(respBytes), true, nil)
	require.NoError(t, err)
	require.Nil(t, hm)
	require.Nil(t, bm)
	require.Equal(t, tokenUsageFrom(-1, -1, -1, -1, -1, -1), usage)
	require.Equal(t, "whisper-1", model)
}

func TestTranslationTranslator_ResponseBody_WithSpan(t *testing.T) {
	mockSpan := &mockTranslationSpan{}
	tr := NewTranslationOpenAIToOpenAITranslator("v1", "")
	req := &openai.TranslationRequest{Model: "whisper-1"}
	_, _, _ = tr.RequestBody([]byte("body"), req, false)

	resp := openai.TranslationResponse{Text: "translated text"}
	respBytes, _ := json.Marshal(resp)

	_, _, _, _, err := tr.ResponseBody(nil, bytes.NewReader(respBytes), true, mockSpan)
	require.NoError(t, err)
	require.NotNil(t, mockSpan.recordedResponse)
	require.Equal(t, "translated text", mockSpan.recordedResponse.Text)
}

func TestTranslationTranslator_ResponseBody_WithSpan_NonJSON(t *testing.T) {
	mockSpan := &mockTranslationSpan{}
	tr := NewTranslationOpenAIToOpenAITranslator("v1", "")
	req := &openai.TranslationRequest{Model: "whisper-1"}
	_, _, _ = tr.RequestBody([]byte("body"), req, false)

	rawResponse := "translated text\nwith new line and \"quotes\""
	_, _, _, _, err := tr.ResponseBody(nil, bytes.NewReader([]byte(rawResponse)), true, mockSpan)
	require.NoError(t, err)
	require.NotNil(t, mockSpan.recordedResponse)
	require.Equal(t, rawResponse, mockSpan.recordedResponse.Text)
}

func TestTranslationTranslator_ResponseError(t *testing.T) {
	tr := NewTranslationOpenAIToOpenAITranslator("v1", "")
	headers := map[string]string{contentTypeHeaderName: "text/plain", statusHeaderName: "400"}
	hm, bm, err := tr.ResponseError(headers, bytes.NewReader([]byte("error")))
	require.NoError(t, err)
	require.NotNil(t, hm)
	require.NotNil(t, bm)
}

type mockTranslationSpan struct {
	recordedResponse *openai.TranslationResponse
}

func (m *mockTranslationSpan) RecordResponse(resp *openai.TranslationResponse) {
	m.recordedResponse = resp
}

func (m *mockTranslationSpan) RecordResponseChunk(*struct{}) {}
func (m *mockTranslationSpan) EndSpanOnError(int, []byte)    {}
func (m *mockTranslationSpan) EndSpan()                      {}
func (m *mockTranslationSpan) EndSpanOnCancel()              {}
