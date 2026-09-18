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

func TestOpenAIToOpenAISpeechTranslator_RequestBody_ModelOverrideAndPath(t *testing.T) {
	tr := NewSpeechOpenAIToOpenAITranslator("v1", "gpt-4o-audio-preview")
	req := &openai.SpeechRequest{
		Model: "tts-1",
		Input: "Hello world",
		Voice: "alloy",
	}
	original, _ := json.Marshal(req)

	hm, bm, err := tr.RequestBody(original, req, false)
	require.NoError(t, err)
	require.NotNil(t, hm)
	require.Len(t, hm, 2) // path and content-length headers
	require.Equal(t, pathHeaderName, hm[0].Key())
	require.Equal(t, "/v1/audio/speech", hm[0].Value())
	require.Equal(t, contentLengthHeaderName, hm[1].Key())

	require.NotNil(t, bm)
	var got openai.SpeechRequest
	require.NoError(t, json.Unmarshal(bm, &got))
	require.Equal(t, "gpt-4o-audio-preview", got.Model)
}

func TestOpenAIToOpenAISpeechTranslator_RequestBody_ForceMutation(t *testing.T) {
	tr := NewSpeechOpenAIToOpenAITranslator("v1", "")
	req := &openai.SpeechRequest{
		Model: "tts-1-hd",
		Input: "Test speech",
		Voice: "nova",
	}
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

func TestOpenAIToOpenAISpeechTranslator_RequestBody_NoOverrideNoForce(t *testing.T) {
	tr := NewSpeechOpenAIToOpenAITranslator("v1", "")
	req := &openai.SpeechRequest{
		Model: "tts-1",
		Input: "Hello",
		Voice: "alloy",
	}
	original, _ := json.Marshal(req)

	hm, bm, err := tr.RequestBody(original, req, false)
	require.NoError(t, err)
	require.NotNil(t, hm)
	// Only path header present; content-length should not be set when no mutation
	require.Len(t, hm, 1)
	require.Equal(t, pathHeaderName, hm[0].Key())
	require.Nil(t, bm)
}

func TestOpenAIToOpenAISpeechTranslator_RequestBody_StreamingDetection(t *testing.T) {
	tr := NewSpeechOpenAIToOpenAITranslator("v1", "").(*openAIToOpenAITranslatorV1Speech)

	sseFormat := "sse"
	req := &openai.SpeechRequest{
		Model:        "gpt-4o-mini-tts",
		Input:        "Streaming test",
		Voice:        "alloy",
		StreamFormat: &sseFormat,
	}
	original, _ := json.Marshal(req)

	_, _, err := tr.RequestBody(original, req, false)
	require.NoError(t, err)
	require.True(t, tr.stream, "Should detect SSE streaming mode")
}

func TestOpenAIToOpenAISpeechTranslator_RequestBody_NonStreamingDetection(t *testing.T) {
	tr := NewSpeechOpenAIToOpenAITranslator("v1", "").(*openAIToOpenAITranslatorV1Speech)

	req := &openai.SpeechRequest{
		Model: "tts-1",
		Input: "Binary test",
		Voice: "alloy",
	}
	original, _ := json.Marshal(req)

	_, _, err := tr.RequestBody(original, req, false)
	require.NoError(t, err)
	require.False(t, tr.stream, "Should detect binary mode")
}

func TestOpenAIToOpenAISpeechTranslator_ResponseHeaders_NoOp(t *testing.T) {
	tr := NewSpeechOpenAIToOpenAITranslator("v1", "")
	hm, err := tr.ResponseHeaders(map[string]string{"foo": "bar"})
	require.NoError(t, err)
	require.Nil(t, hm)
}

func TestOpenAIToOpenAISpeechTranslator_ResponseBody_BinaryAudio(t *testing.T) {
	tr := NewSpeechOpenAIToOpenAITranslator("v1", "tts-1").(*openAIToOpenAITranslatorV1Speech)

	// Set up translator state (simulate RequestBody call)
	req := &openai.SpeechRequest{Model: "tts-1", Input: "Test", Voice: "alloy"}
	original, _ := json.Marshal(req)
	_, _, _ = tr.RequestBody(original, req, false)

	// Simulate binary MP3 response
	audioData := []byte{0xFF, 0xFB, 0x90, 0x00} // MP3 header

	hm, bm, usage, respModel, err := tr.ResponseBody(map[string]string{}, bytes.NewReader(audioData), true, nil)
	require.NoError(t, err)
	require.Nil(t, hm)
	require.Nil(t, bm)
	require.Equal(t, tokenUsageFrom(-1, -1, -1, -1, -1, -1), usage)
	require.Equal(t, "tts-1", respModel)
}

func TestOpenAIToOpenAISpeechTranslator_ResponseBody_StreamingSSE(t *testing.T) {
	tr := NewSpeechOpenAIToOpenAITranslator("v1", "gpt-4o-mini-tts").(*openAIToOpenAITranslatorV1Speech)

	// Set up translator state for streaming
	sseFormat := "sse"
	req := &openai.SpeechRequest{
		Model:        "gpt-4o-mini-tts",
		Input:        "Test",
		Voice:        "alloy",
		StreamFormat: &sseFormat,
	}
	original, _ := json.Marshal(req)
	_, _, _ = tr.RequestBody(original, req, false)

	// Simulate SSE response
	sseData := `data: {"data":"dGVzdA=="}

data: [DONE]

`

	hm, bm, usage, respModel, err := tr.ResponseBody(map[string]string{}, bytes.NewReader([]byte(sseData)), true, nil)
	require.NoError(t, err)
	require.Nil(t, hm)
	require.Nil(t, bm)
	require.Equal(t, tokenUsageFrom(-1, -1, -1, -1, -1, -1), usage)
	require.Equal(t, "gpt-4o-mini-tts", respModel)
}

func TestOpenAIToOpenAISpeechTranslator_ResponseBody_RecordsSpan_Binary(t *testing.T) {
	mockSpan := &mockSpeechSpan{}
	tr := NewSpeechOpenAIToOpenAITranslator("v1", "tts-1").(*openAIToOpenAITranslatorV1Speech)

	// Set up translator state
	req := &openai.SpeechRequest{Model: "tts-1", Input: "Test", Voice: "alloy"}
	original, _ := json.Marshal(req)
	_, _, _ = tr.RequestBody(original, req, false)

	audioData := []byte{0xFF, 0xFB, 0x90, 0x00}

	_, _, _, _, err := tr.ResponseBody(map[string]string{}, bytes.NewReader(audioData), true, mockSpan)
	require.NoError(t, err)
	require.NotNil(t, mockSpan.recordedResponse)
	require.Equal(t, audioData, *mockSpan.recordedResponse)
}

func TestOpenAIToOpenAISpeechTranslator_ResponseBody_RecordsSpan_Streaming(t *testing.T) {
	mockSpan := &mockSpeechSpan{}
	tr := NewSpeechOpenAIToOpenAITranslator("v1", "gpt-4o-mini-tts").(*openAIToOpenAITranslatorV1Speech)

	// Set up translator state for streaming
	sseFormat := "sse"
	req := &openai.SpeechRequest{
		Model:        "gpt-4o-mini-tts",
		Input:        "Test",
		Voice:        "alloy",
		StreamFormat: &sseFormat,
	}
	original, _ := json.Marshal(req)
	_, _, _ = tr.RequestBody(original, req, false)

	sseData := `data: {"data":"dGVzdA=="}

`

	_, _, _, _, err := tr.ResponseBody(map[string]string{}, bytes.NewReader([]byte(sseData)), true, mockSpan)
	require.NoError(t, err)
	require.NotNil(t, mockSpan.recordedChunks)
	require.Len(t, mockSpan.recordedChunks, 1)
}

func TestOpenAIToOpenAISpeechTranslator_ResponseBody_RecordsSpan_StreamingCRLF(t *testing.T) {
	mockSpan := &mockSpeechSpan{}
	tr := NewSpeechOpenAIToOpenAITranslator("v1", "gpt-4o-mini-tts").(*openAIToOpenAITranslatorV1Speech)

	sseFormat := "sse"
	req := &openai.SpeechRequest{
		Model:        "gpt-4o-mini-tts",
		Input:        "Test",
		Voice:        "alloy",
		StreamFormat: &sseFormat,
	}
	original, _ := json.Marshal(req)
	_, _, _ = tr.RequestBody(original, req, false)

	// CRLF line endings and no space after "data:" are both valid SSE.
	sseData := "data:{\"data\":\"dGVzdA==\"}\r\n\r\ndata: {\"data\":\"dGVzdA==\"}\r\n\r\n"

	_, _, _, _, err := tr.ResponseBody(map[string]string{}, bytes.NewReader([]byte(sseData)), true, mockSpan)
	require.NoError(t, err)
	require.Len(t, mockSpan.recordedChunks, 2)
	require.Equal(t, []byte("test"), mockSpan.recordedChunks[0].Data)
	require.Empty(t, tr.buffered)
}

func TestOpenAIToOpenAISpeechTranslator_ResponseBody_RecordsSpan_StreamingSplitEvent(t *testing.T) {
	mockSpan := &mockSpeechSpan{}
	tr := NewSpeechOpenAIToOpenAITranslator("v1", "gpt-4o-mini-tts").(*openAIToOpenAITranslatorV1Speech)

	// Set up translator state for streaming
	sseFormat := "sse"
	req := &openai.SpeechRequest{
		Model:        "gpt-4o-mini-tts",
		Input:        "Test",
		Voice:        "alloy",
		StreamFormat: &sseFormat,
	}
	original, _ := json.Marshal(req)
	_, _, _ = tr.RequestBody(original, req, false)

	firstChunk := []byte(`data: {"data":"dGVz`)
	_, _, _, respModel, err := tr.ResponseBody(map[string]string{}, bytes.NewReader(firstChunk), false, mockSpan)
	require.NoError(t, err)
	require.Equal(t, "gpt-4o-mini-tts", respModel)
	require.Empty(t, mockSpan.recordedChunks)
	require.Equal(t, firstChunk, tr.buffered)

	secondChunk := []byte("dA==\"}\n\n")
	_, _, _, respModel, err = tr.ResponseBody(map[string]string{}, bytes.NewReader(secondChunk), true, mockSpan)
	require.NoError(t, err)
	require.Equal(t, "gpt-4o-mini-tts", respModel)
	require.Len(t, mockSpan.recordedChunks, 1)
	require.Equal(t, []byte("test"), mockSpan.recordedChunks[0].Data)
	require.Empty(t, tr.buffered)
}

func TestOpenAIToOpenAISpeechTranslator_ResponseError_NonJSON(t *testing.T) {
	tr := NewSpeechOpenAIToOpenAITranslator("v1", "")
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

func TestOpenAIToOpenAISpeechTranslator_ResponseError_JSONPassthrough(t *testing.T) {
	tr := NewSpeechOpenAIToOpenAITranslator("v1", "")
	headers := map[string]string{contentTypeHeaderName: jsonContentType, statusHeaderName: "500"}
	// Already JSON — should be passed through (no mutation)
	hm, bm, err := tr.ResponseError(headers, bytes.NewReader([]byte(`{"error":"msg"}`)))
	require.NoError(t, err)
	require.Nil(t, hm)
	require.Nil(t, bm)
}

func TestOpenAIToOpenAISpeechTranslator_ResponseBody_ModelPropagatesFromRequest(t *testing.T) {
	// Use override so effective model differs from original
	tr := NewSpeechOpenAIToOpenAITranslator("v1", "gpt-4o-audio-preview").(*openAIToOpenAITranslatorV1Speech)
	req := &openai.SpeechRequest{
		Model: "tts-1",
		Input: "Test",
		Voice: "alloy",
	}
	original, _ := json.Marshal(req)
	// Call RequestBody first to set requestModel inside translator
	_, _, err := tr.RequestBody(original, req, false)
	require.NoError(t, err)

	audioData := []byte{0xFF, 0xFB, 0x90, 0x00}
	_, _, _, respModel, err := tr.ResponseBody(map[string]string{}, bytes.NewReader(audioData), true, nil)
	require.NoError(t, err)
	require.Equal(t, "gpt-4o-audio-preview", respModel)
}

func TestOpenAIToOpenAISpeechTranslator_ResponseBody_ReadError(t *testing.T) {
	tr := NewSpeechOpenAIToOpenAITranslator("v1", "").(*openAIToOpenAITranslatorV1Speech)

	// Set up translator state
	req := &openai.SpeechRequest{Model: "tts-1", Input: "Test", Voice: "alloy"}
	original, _ := json.Marshal(req)
	_, _, _ = tr.RequestBody(original, req, false)

	// Use an erroring reader
	_, _, _, _, err := tr.ResponseBody(map[string]string{}, &errorReader{}, true, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "failed to read audio data")
}

// Mock implementations for testing

type mockSpeechSpan struct {
	recordedResponse *[]byte
	recordedChunks   []*openai.SpeechStreamChunk
}

func (m *mockSpeechSpan) RecordResponse(resp *[]byte) {
	m.recordedResponse = resp
}

func (m *mockSpeechSpan) RecordResponseChunk(chunk *openai.SpeechStreamChunk) {
	m.recordedChunks = append(m.recordedChunks, chunk)
}

func (m *mockSpeechSpan) EndSpanOnError(int, []byte) {}
func (m *mockSpeechSpan) EndSpan()                   {}

type errorReader struct{}

func (e *errorReader) Read(_ []byte) (n int, err error) {
	return 0, bytes.ErrTooLarge
}
