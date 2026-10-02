// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package testotel

import (
	"github.com/envoyproxy/ai-gateway/internal/apischema/openai"
)

// MockSpan is a mock implementation of tracingapi.ChatCompletionSpan for testing purposes.
type MockSpan struct {
	Resp          *openai.ChatCompletionResponse
	RespChunks    []*openai.ChatCompletionResponseChunk
	ErrorStatus   int
	ErrBody       string
	Cancelled     bool
	EndSpanCalled bool
}

// RecordResponseChunk implements tracingapi.ChatCompletionSpan.
func (s *MockSpan) RecordResponseChunk(resp *openai.ChatCompletionResponseChunk) {
	s.RespChunks = append(s.RespChunks, resp)
}

// RecordResponse implements tracingapi.ChatCompletionSpan.
func (s *MockSpan) RecordResponse(resp *openai.ChatCompletionResponse) {
	s.Resp = resp
}

// EndSpanOnError implements tracingapi.ChatCompletionSpan.
func (s *MockSpan) EndSpanOnError(statusCode int, body []byte) {
	s.ErrorStatus = statusCode
	s.ErrBody = string(body)
}

// EndSpanOnCancel implements tracingapi.ChatCompletionSpan.
func (s *MockSpan) EndSpanOnCancel() {
	s.Cancelled = true
}

// EndSpan implements tracingapi.ChatCompletionSpan.
func (s *MockSpan) EndSpan() {
	s.EndSpanCalled = true
}
