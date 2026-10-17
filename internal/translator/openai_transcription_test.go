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

func TestTranscriptionTranslator_RequestBody_NoOverride(t *testing.T) {
	tr := NewTranscriptionOpenAIToOpenAITranslator("v1", "")
	req := &openai.TranscriptionRequest{Model: "whisper-1", FileName: "test.mp3", FileSize: 1024}
	original := []byte("multipart-body-data")

	hm, bm, err := tr.RequestBody(original, req, false)
	require.NoError(t, err)
	require.Len(t, hm, 1)
	require.Equal(t, pathHeaderName, hm[0].Key())
	require.Equal(t, "/v1/audio/transcriptions", hm[0].Value())
	require.Nil(t, bm)
}

func TestTranscriptionTranslator_RequestBody_ForceMutation(t *testing.T) {
	tr := NewTranscriptionOpenAIToOpenAITranslator("v1", "")
	req := &openai.TranscriptionRequest{Model: "whisper-1"}
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

func TestTranscriptionTranslator_RequestBody_ModelOverride(t *testing.T) {
	tr := NewTranscriptionOpenAIToOpenAITranslator("v1", "whisper-large-v3")

	body, contentType := buildMultipartBody(t, map[string]string{"model": "whisper-1"}, "file", "test.mp3", []byte("audio"))
	// Set content-type on translator.
	tr.(*openAIToOpenAITranslatorV1Transcription).SetContentType(contentType)

	req := &openai.TranscriptionRequest{Model: "whisper-1", FileName: "test.mp3", FileSize: 5}

	hm, bm, err := tr.RequestBody(body, req, false)
	require.NoError(t, err)
	require.NotNil(t, bm)

	foundCT := false
	foundPath := false
	for _, h := range hm {
		if h.Key() == contentTypeHeaderName {
			foundCT = true
			require.Contains(t, h.Value(), "multipart/form-data")
		}
		if h.Key() == pathHeaderName {
			foundPath = true
		}
	}
	require.True(t, foundCT)
	require.True(t, foundPath)

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

func TestTranscriptionTranslator_ResponseHeaders_NoOp(t *testing.T) {
	tr := NewTranscriptionOpenAIToOpenAITranslator("v1", "")
	hm, err := tr.ResponseHeaders(map[string]string{"foo": "bar"})
	require.NoError(t, err)
	require.Nil(t, hm)
}

func TestTranscriptionTranslator_ResponseBody_NoSpan(t *testing.T) {
	tr := NewTranscriptionOpenAIToOpenAITranslator("v1", "")
	req := &openai.TranscriptionRequest{Model: "whisper-1"}
	_, _, _ = tr.RequestBody([]byte("body"), req, false)

	resp := openai.TranscriptionResponse{Text: "hello world"}
	respBytes, _ := json.Marshal(resp)

	hm, bm, usage, model, err := tr.ResponseBody(nil, bytes.NewReader(respBytes), true, nil)
	require.NoError(t, err)
	require.Nil(t, hm)
	require.Nil(t, bm)
	require.Equal(t, tokenUsageFrom(-1, -1, -1, -1, -1, -1), usage)
	require.Equal(t, "whisper-1", model)
}

func TestTranscriptionTranslator_ResponseBody_WithSpan(t *testing.T) {
	mockSpan := &mockTranscriptionSpan{}
	tr := NewTranscriptionOpenAIToOpenAITranslator("v1", "")
	req := &openai.TranscriptionRequest{Model: "whisper-1"}
	_, _, _ = tr.RequestBody([]byte("body"), req, false)

	resp := openai.TranscriptionResponse{Text: "hello world", Language: "en", Duration: 5.5}
	respBytes, _ := json.Marshal(resp)

	_, _, _, _, err := tr.ResponseBody(nil, bytes.NewReader(respBytes), true, mockSpan)
	require.NoError(t, err)
	require.NotNil(t, mockSpan.recordedResponse)
	require.Equal(t, "hello world", mockSpan.recordedResponse.Text)
}

func TestTranscriptionTranslator_ResponseBody_WithSpan_NonJSON(t *testing.T) {
	mockSpan := &mockTranscriptionSpan{}
	tr := NewTranscriptionOpenAIToOpenAITranslator("v1", "")
	req := &openai.TranscriptionRequest{Model: "whisper-1"}
	_, _, _ = tr.RequestBody([]byte("body"), req, false)

	rawResponse := "hello world\nwith new line and \"quotes\""
	_, _, _, _, err := tr.ResponseBody(nil, bytes.NewReader([]byte(rawResponse)), true, mockSpan)
	require.NoError(t, err)
	require.NotNil(t, mockSpan.recordedResponse)
	require.Equal(t, rawResponse, mockSpan.recordedResponse.Text)
}

// TestTranscriptionTranslator_ResponseBody_Streaming_FullSSE feeds a complete SSE stream
// from gpt-4o-transcribe in a single ResponseBody call and asserts that every parsed event
// reaches the span chunk recorder in order, and that response bytes are streamed through
// unchanged (newBody is nil).
func TestTranscriptionTranslator_ResponseBody_Streaming_FullSSE(t *testing.T) {
	mockSpan := &mockTranscriptionSpan{}
	tr := NewTranscriptionOpenAIToOpenAITranslator("v1", "").(*openAIToOpenAITranslatorV1Transcription)
	req := &openai.TranscriptionRequest{Model: "gpt-4o-transcribe", Stream: true}
	_, _, _ = tr.RequestBody([]byte("body"), req, false)

	sse := "" +
		`data: {"type":"transcript.text.delta","delta":"Imagine "}` + "\n" +
		`data: {"type":"transcript.text.delta","delta":"the wildest idea."}` + "\n" +
		`data: {"type":"transcript.text.done","text":"Imagine the wildest idea."}` + "\n"

	_, newBody, _, model, err := tr.ResponseBody(
		map[string]string{contentTypeHeaderName: eventStreamContentType},
		bytes.NewReader([]byte(sse)), true, mockSpan,
	)
	require.NoError(t, err)
	require.Nil(t, newBody, "SSE bytes must pass through to the client unchanged")
	require.Equal(t, "gpt-4o-transcribe", model)

	require.Len(t, mockSpan.recordedChunks, 3)
	require.Equal(t, openai.TranscriptionStreamEventTypeDelta, mockSpan.recordedChunks[0].Type)
	require.Equal(t, "Imagine ", mockSpan.recordedChunks[0].Delta)
	require.Equal(t, openai.TranscriptionStreamEventTypeDelta, mockSpan.recordedChunks[1].Type)
	require.Equal(t, "the wildest idea.", mockSpan.recordedChunks[1].Delta)
	require.Equal(t, openai.TranscriptionStreamEventTypeDone, mockSpan.recordedChunks[2].Type)
	require.Equal(t, "Imagine the wildest idea.", mockSpan.recordedChunks[2].Text)
}

// TestTranscriptionTranslator_ResponseBody_Streaming_SplitAcrossChunks proves that SSE
// state survives across multiple ResponseBody invocations — Envoy delivers SSE one chunk
// at a time, and a single `data:` line may straddle a chunk boundary. The translator must
// buffer partial lines and parse them as soon as a newline arrives.
func TestTranscriptionTranslator_ResponseBody_Streaming_SplitAcrossChunks(t *testing.T) {
	mockSpan := &mockTranscriptionSpan{}
	tr := NewTranscriptionOpenAIToOpenAITranslator("v1", "")
	req := &openai.TranscriptionRequest{Model: "gpt-4o-transcribe", Stream: true}
	_, _, _ = tr.RequestBody([]byte("body"), req, false)

	headers := map[string]string{contentTypeHeaderName: eventStreamContentType}

	// First chunk carries half a delta event with no terminating newline.
	chunk1 := `data: {"type":"transcript.text.delta","delta":"hel`
	_, _, _, _, err := tr.ResponseBody(headers, bytes.NewReader([]byte(chunk1)), false, mockSpan)
	require.NoError(t, err)
	require.Empty(t, mockSpan.recordedChunks, "no full line yet, no chunk recorded")

	// Second chunk completes the delta line and adds the terminal done event.
	chunk2 := `lo"}` + "\n" +
		`data: {"type":"transcript.text.done","text":"hello"}` + "\n"
	_, _, _, _, err = tr.ResponseBody(headers, bytes.NewReader([]byte(chunk2)), true, mockSpan)
	require.NoError(t, err)

	require.Len(t, mockSpan.recordedChunks, 2)
	require.Equal(t, "hello", mockSpan.recordedChunks[0].Delta)
	require.Equal(t, openai.TranscriptionStreamEventTypeDone, mockSpan.recordedChunks[1].Type)
}

// TestTranscriptionTranslator_ResponseBody_Streaming_NonDataLinesSkipped confirms that SSE
// `event:` preamble lines, comments, and blank lines are silently skipped without disturbing
// parsing of the actual `data:` payloads.
func TestTranscriptionTranslator_ResponseBody_Streaming_NonDataLinesSkipped(t *testing.T) {
	mockSpan := &mockTranscriptionSpan{}
	tr := NewTranscriptionOpenAIToOpenAITranslator("v1", "")
	req := &openai.TranscriptionRequest{Model: "gpt-4o-transcribe", Stream: true}
	_, _, _ = tr.RequestBody([]byte("body"), req, false)

	sse := "" +
		"event: transcript.text.delta\n" +
		`data: {"type":"transcript.text.delta","delta":"hi"}` + "\n" +
		"\n" + // blank line
		": this is an SSE comment\n" +
		"event: transcript.text.done\n" +
		`data: {"type":"transcript.text.done","text":"hi"}` + "\n"

	_, _, _, _, err := tr.ResponseBody(
		map[string]string{contentTypeHeaderName: eventStreamContentType},
		bytes.NewReader([]byte(sse)), true, mockSpan,
	)
	require.NoError(t, err)
	require.Len(t, mockSpan.recordedChunks, 2)
}

// TestTranscriptionTranslator_ResponseBody_Streaming_DoneSentinelSkipped proves that a
// `data: [DONE]` terminator (used by chat completions; not currently emitted by transcription
// but tolerated for forward-compat) does not raise an error and does not produce a spurious
// chunk recording.
func TestTranscriptionTranslator_ResponseBody_Streaming_DoneSentinelSkipped(t *testing.T) {
	mockSpan := &mockTranscriptionSpan{}
	tr := NewTranscriptionOpenAIToOpenAITranslator("v1", "")
	req := &openai.TranscriptionRequest{Model: "gpt-4o-transcribe", Stream: true}
	_, _, _ = tr.RequestBody([]byte("body"), req, false)

	sse := "" +
		`data: {"type":"transcript.text.delta","delta":"hi"}` + "\n" +
		`data: {"type":"transcript.text.done","text":"hi"}` + "\n" +
		"data: [DONE]\n"

	_, _, _, _, err := tr.ResponseBody(
		map[string]string{contentTypeHeaderName: eventStreamContentType},
		bytes.NewReader([]byte(sse)), true, mockSpan,
	)
	require.NoError(t, err)
	require.Len(t, mockSpan.recordedChunks, 2, "only the two real events should be recorded")
}

// TestTranscriptionTranslator_ResponseBody_Streaming_MalformedJSONSkipped proves that one
// bad `data:` line does not derail parsing of subsequent good lines — graceful degradation
// when a backend (or proxy) corrupts a single event.
func TestTranscriptionTranslator_ResponseBody_Streaming_MalformedJSONSkipped(t *testing.T) {
	mockSpan := &mockTranscriptionSpan{}
	tr := NewTranscriptionOpenAIToOpenAITranslator("v1", "")
	req := &openai.TranscriptionRequest{Model: "gpt-4o-transcribe", Stream: true}
	_, _, _ = tr.RequestBody([]byte("body"), req, false)

	sse := "" +
		"data: not-json\n" +
		`data: {"type":"transcript.text.done","text":"hi"}` + "\n"

	_, _, _, _, err := tr.ResponseBody(
		map[string]string{contentTypeHeaderName: eventStreamContentType},
		bytes.NewReader([]byte(sse)), true, mockSpan,
	)
	require.NoError(t, err)
	require.Len(t, mockSpan.recordedChunks, 1, "malformed line skipped, only the valid one recorded")
}

// TestTranscriptionTranslator_ResponseBody_Streaming_UnknownEventTypeForwarded proves that
// future or experimental SSE event types are forwarded to the span chunk recorder without
// error — forward-compat with any new OpenAI event types.
func TestTranscriptionTranslator_ResponseBody_Streaming_UnknownEventTypeForwarded(t *testing.T) {
	mockSpan := &mockTranscriptionSpan{}
	tr := NewTranscriptionOpenAIToOpenAITranslator("v1", "")
	req := &openai.TranscriptionRequest{Model: "gpt-4o-transcribe", Stream: true}
	_, _, _ = tr.RequestBody([]byte("body"), req, false)

	sse := `data: {"type":"transcript.experimental","delta":"hi"}` + "\n"

	_, _, _, _, err := tr.ResponseBody(
		map[string]string{contentTypeHeaderName: eventStreamContentType},
		bytes.NewReader([]byte(sse)), true, mockSpan,
	)
	require.NoError(t, err)
	require.Len(t, mockSpan.recordedChunks, 1)
	require.Equal(t, "transcript.experimental", mockSpan.recordedChunks[0].Type)
	require.Equal(t, "hi", mockSpan.recordedChunks[0].Delta)
}

// TestTranscriptionTranslator_ResponseBody_Streaming_CRLFLineEndings proves that SSE streams
// with \r\n line terminators (some servers/proxies use these) parse correctly — the trailing
// CR must be stripped before JSON decode.
func TestTranscriptionTranslator_ResponseBody_Streaming_CRLFLineEndings(t *testing.T) {
	mockSpan := &mockTranscriptionSpan{}
	tr := NewTranscriptionOpenAIToOpenAITranslator("v1", "")
	req := &openai.TranscriptionRequest{Model: "gpt-4o-transcribe", Stream: true}
	_, _, _ = tr.RequestBody([]byte("body"), req, false)

	sse := `data: {"type":"transcript.text.done","text":"hi"}` + "\r\n"

	_, _, _, _, err := tr.ResponseBody(
		map[string]string{contentTypeHeaderName: eventStreamContentType},
		bytes.NewReader([]byte(sse)), true, mockSpan,
	)
	require.NoError(t, err)
	require.Len(t, mockSpan.recordedChunks, 1)
	require.Equal(t, "hi", mockSpan.recordedChunks[0].Text)
}

// TestTranscriptionTranslator_ResponseBody_Whisper1WithStreamTrue is the key pass-through
// case: when the request has `stream=true` but the model is whisper-1 (which OpenAI silently
// treats as non-streaming), the backend returns Content-Type: application/json. The translator
// must take the JSON branch, NOT the SSE branch, despite stream=true on the request.
//
// This is why ResponseBody dispatches on the response Content-Type rather than on req.Stream.
func TestTranscriptionTranslator_ResponseBody_Whisper1WithStreamTrue(t *testing.T) {
	mockSpan := &mockTranscriptionSpan{}
	tr := NewTranscriptionOpenAIToOpenAITranslator("v1", "")
	req := &openai.TranscriptionRequest{Model: "whisper-1", Stream: true}
	_, _, _ = tr.RequestBody([]byte("body"), req, false)

	respJSON := `{"text": "hello"}`
	_, _, _, _, err := tr.ResponseBody(
		map[string]string{contentTypeHeaderName: jsonContentType},
		bytes.NewReader([]byte(respJSON)), true, mockSpan,
	)
	require.NoError(t, err)
	require.NotNil(t, mockSpan.recordedResponse)
	require.Equal(t, "hello", mockSpan.recordedResponse.Text)
	require.Empty(t, mockSpan.recordedChunks, "JSON branch must not invoke the chunk recorder")
}

func TestTranscriptionTranslator_ResponseError(t *testing.T) {
	tr := NewTranscriptionOpenAIToOpenAITranslator("v1", "")
	headers := map[string]string{contentTypeHeaderName: "text/plain", statusHeaderName: "400"}
	hm, bm, err := tr.ResponseError(headers, bytes.NewReader([]byte("error")))
	require.NoError(t, err)
	require.NotNil(t, hm)
	require.NotNil(t, bm)
}

type mockTranscriptionSpan struct {
	recordedResponse *openai.TranscriptionResponse
	recordedChunks   []*openai.TranscriptionStreamEvent
}

func (m *mockTranscriptionSpan) RecordResponse(resp *openai.TranscriptionResponse) {
	m.recordedResponse = resp
}

func (m *mockTranscriptionSpan) RecordResponseChunk(c *openai.TranscriptionStreamEvent) {
	m.recordedChunks = append(m.recordedChunks, c)
}
func (m *mockTranscriptionSpan) EndSpanOnError(int, []byte) {}
func (m *mockTranscriptionSpan) EndSpan()                   {}
func (m *mockTranscriptionSpan) EndSpanOnCancel()           {}
