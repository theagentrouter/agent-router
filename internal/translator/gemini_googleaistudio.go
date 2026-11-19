// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package translator

import (
	"bytes"
	"cmp"
	"io"
	"strconv"
	"strings"

	"google.golang.org/genai"

	"github.com/envoyproxy/ai-gateway/internal/apischema/gcp"
	"github.com/envoyproxy/ai-gateway/internal/internalapi"
	"github.com/envoyproxy/ai-gateway/internal/json"
	"github.com/envoyproxy/ai-gateway/internal/metrics"
	"github.com/envoyproxy/ai-gateway/internal/tracing/tracingapi"
)

const geminiModelsPath = "/v1beta/models"

type geminiGenerateContentTranslator struct {
	path          gcp.GeminiGenerateContentPath
	requestModel  internalapi.RequestModel
	responseModel internalapi.ResponseModel
	usage         *genai.GenerateContentResponseUsageMetadata
	stream        geminiResponseStream
}

// NewGeminiGenerateContentTranslator forwards the Gemini Developer API protocol.
func NewGeminiGenerateContentTranslator(path gcp.GeminiGenerateContentPath, model string) Translator[gcp.GeminiGenerateContentRequest, tracingapi.GeminiGenerateContentSpan] {
	return &geminiGenerateContentTranslator{path: path, requestModel: model}
}

// RequestBody rewrites the model in the path and preserves the provider payload.
func (t *geminiGenerateContentTranslator) RequestBody(raw []byte, _ *gcp.GeminiGenerateContentRequest, forceBodyMutation bool) ([]internalapi.Header, []byte, error) {
	headers := []internalapi.Header{{pathHeaderName, t.path.Path(geminiModelsPath, t.requestModel)}}
	body := forceOriginalBodyIfEmpty(forceBodyMutation, nil, raw)
	if body != nil {
		headers = append(headers, internalapi.Header{contentLengthHeaderName, strconv.Itoa(len(body))})
	}
	return headers, body, nil
}

// RemovedRequestHeaders strips client credentials and system parameters before backend auth.
func (t *geminiGenerateContentTranslator) RemovedRequestHeaders() []string {
	return []string{
		"authorization",
		internalapi.GoogleAPIKeyHeader,
		"x-goog-user-project",
		"x-goog-quota-user",
		"x-goog-fieldmask",
		"x-http-method-override",
	}
}

func (t *geminiGenerateContentTranslator) ResponseHeaders(map[string]string) ([]internalapi.Header, error) {
	return nil, nil
}

func (t *geminiGenerateContentTranslator) ResponseBody(headers map[string]string, body io.Reader, endOfStream bool, span tracingapi.GeminiGenerateContentSpan) ([]internalapi.Header, []byte, metrics.TokenUsage, internalapi.ResponseModel, error) {
	data, err := io.ReadAll(body)
	if err != nil {
		return nil, nil, metrics.TokenUsage{}, "", err
	}
	if t.path.Streaming() {
		for _, chunk := range t.stream.split(headers[contentTypeHeaderName], data, endOfStream) {
			t.observe(chunk, span)
		}
	} else {
		t.observe(data, span)
	}
	var usage metrics.TokenUsage
	if t.usage != nil {
		input, output, total := gcp.GeminiUsageCounts(t.usage)
		usage.SetInputTokens(uint32(input))                                 //nolint:gosec // Token counts are nonnegative.
		usage.SetOutputTokens(uint32(output))                               //nolint:gosec // Token counts are nonnegative.
		usage.SetTotalTokens(uint32(total))                                 //nolint:gosec // Token counts are nonnegative.
		usage.SetCachedInputTokens(uint32(t.usage.CachedContentTokenCount)) //nolint:gosec // Token counts are nonnegative.
		usage.SetReasoningTokens(uint32(t.usage.ThoughtsTokenCount))        //nolint:gosec // Token counts are nonnegative.
	}
	return nil, nil, usage, cmp.Or(t.responseModel, t.requestModel), nil
}

func (t *geminiGenerateContentTranslator) observe(data []byte, span tracingapi.GeminiGenerateContentSpan) {
	var response gcp.GeminiGenerateContentResponse
	if json.Unmarshal(data, &response) != nil {
		return
	}
	t.responseModel = cmp.Or(response.ModelVersion, t.responseModel)
	t.usage = gcp.MergeGeminiUsage(t.usage, response.UsageMetadata)
	if span == nil {
		return
	}
	response.UsageMetadata = t.usage
	response.ModelVersion = t.responseModel
	if t.path.Streaming() {
		span.RecordResponseChunk(&response)
	} else {
		span.RecordResponse(&response)
	}
}

func (t *geminiGenerateContentTranslator) ResponseError(map[string]string, io.Reader) ([]internalapi.Header, []byte, error) {
	return nil, nil, nil
}

// geminiResponseStream observes complete messages across response chunks. With alt=sse
// Google sends SSE events. Without it, the stream is a JSON array of response objects.
type geminiResponseStream struct {
	buffer []byte

	cursor, start, depth int
	inString, escaped    bool

	sseData    []byte
	sseStarted bool
}

func (s *geminiResponseStream) split(contentType string, data []byte, endOfStream bool) [][]byte {
	s.buffer = append(s.buffer, data...)
	if strings.HasPrefix(contentType, eventStreamContentType) {
		return s.splitEvents(endOfStream)
	}
	return s.splitArray()
}

func (s *geminiResponseStream) splitEvents(endOfStream bool) (chunks [][]byte) {
	for {
		index := bytes.IndexAny(s.buffer[s.cursor:], "\r\n")
		if index < 0 {
			s.cursor = len(s.buffer)
			return chunks
		}
		index += s.cursor
		next := index + 1
		if s.buffer[index] == '\r' {
			// A CR at the chunk boundary may be the first byte of a CRLF.
			if next == len(s.buffer) && !endOfStream {
				s.cursor = index
				return chunks
			}
			if next < len(s.buffer) && s.buffer[next] == '\n' {
				next++
			}
		}
		line := s.buffer[:index]
		s.buffer = s.buffer[next:]
		s.cursor = 0
		if !s.sseStarted {
			s.sseStarted = true
			line = bytes.TrimPrefix(line, []byte("\xef\xbb\xbf"))
		}
		if len(line) == 0 {
			if len(s.sseData) != 0 {
				chunks = append(chunks, s.sseData[:len(s.sseData)-1])
				s.sseData = nil
			}
			continue
		}
		if field, value, _ := bytes.Cut(line, []byte(":")); bytes.Equal(field, []byte("data")) {
			s.sseData = append(s.sseData, bytes.TrimPrefix(value, []byte(" "))...)
			s.sseData = append(s.sseData, '\n')
		}
	}
}

func (s *geminiResponseStream) splitArray() (chunks [][]byte) {
	for ; s.cursor < len(s.buffer); s.cursor++ {
		c := s.buffer[s.cursor]
		if s.inString {
			switch {
			case s.escaped:
				s.escaped = false
			case c == '\\':
				s.escaped = true
			case c == '"':
				s.inString = false
			}
			continue
		}
		switch {
		case c == '"':
			s.inString = true
		case c == '{':
			if s.depth == 0 {
				s.start = s.cursor
			}
			s.depth++
		case c == '}' && s.depth > 0:
			s.depth--
			if s.depth == 0 {
				chunks = append(chunks, s.buffer[s.start:s.cursor+1])
			}
		}
	}
	keep := s.cursor
	if s.depth > 0 {
		keep = s.start
	}
	s.buffer = s.buffer[keep:]
	s.cursor -= keep
	s.start = 0
	return chunks
}
